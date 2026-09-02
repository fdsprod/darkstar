package codex

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"darkstar/src/ports/provider"
)

type fixtureFrame struct {
	Sequence  uint64      `json:"sequence"`
	Direction string      `json:"direction"`
	Message   wireMessage `json:"message"`
}

func TestEventNormalizerReplaysVersionedAppServerFixtures(t *testing.T) {
	t.Parallel()

	fixed := time.Date(2026, time.September, 1, 12, 0, 0, 0, time.UTC)
	normalizer, err := NewEventNormalizer(NormalizerOptions{
		AttemptID:       "attempt-fixture",
		ProviderVersion: "0.151.0-alpha.7.2",
		Clock:           func() time.Time { return fixed },
		EvidenceRef: func(sequence uint64, _ string) string {
			return "evidence/codex/frame-" + strconv.FormatUint(sequence, 10) + ".json"
		},
	})
	if err != nil {
		t.Fatalf("NewEventNormalizer() error = %v", err)
	}

	wantKinds := map[provider.EventKind]bool{
		provider.EventAttemptStarted:             false,
		provider.EventTurnStarted:                false,
		provider.EventTurnCompleted:              false,
		provider.EventTurnInterrupted:            false,
		provider.EventMessageDelta:               false,
		provider.EventMessageCompleted:           false,
		provider.EventCommandStarted:             false,
		provider.EventCommandOutput:              false,
		provider.EventCommandCompleted:           false,
		provider.EventPermissionRequested:        false,
		provider.EventPermissionResponseRecorded: false,
		provider.EventUserInputRequested:         false,
		provider.EventUserInputResponseRecorded:  false,
		provider.EventUsageUpdated:               false,
		provider.EventWarning:                    false,
		provider.EventUnknownProvider:            false,
	}

	var sequence uint64
	for _, path := range fixturePaths(t) {
		file, err := os.Open(path)
		if err != nil {
			t.Fatalf("open fixture %s: %v", path, err)
		}
		scanner := bufio.NewScanner(file)
		scanner.Buffer(make([]byte, 64*1024), defaultMaxMessageBytes)
		for scanner.Scan() {
			var frame fixtureFrame
			if err := json.Unmarshal(scanner.Bytes(), &frame); err != nil {
				t.Fatalf("decode fixture %s: %v", path, err)
			}
			if frame.Direction != "server-to-client" || frame.Message.Method == "" {
				continue
			}
			var incoming IncomingMessage
			if len(frame.Message.ID) > 0 {
				incoming = ServerRequest{ID: frame.Message.ID, Method: frame.Message.Method, Params: frame.Message.Params}
			} else {
				incoming = ServerNotification{Method: frame.Message.Method, Params: frame.Message.Params, EmittedAtMS: frame.Message.EmittedAtMS}
			}
			event, err := normalizer.Normalize(incoming)
			if err != nil {
				t.Fatalf("normalize %s from %s: %v", frame.Message.Method, path, err)
			}
			sequence++
			if event.Sequence != sequence || event.AttemptID != "attempt-fixture" || event.RawEvidenceRef == "" {
				t.Fatalf("event envelope = %#v", event)
			}
			if err := event.Validate(); err != nil {
				t.Fatalf("event Validate() error = %v", err)
			}
			if _, tracked := wantKinds[event.Kind]; tracked {
				wantKinds[event.Kind] = true
			}
		}
		if err := scanner.Err(); err != nil {
			t.Fatalf("scan fixture %s: %v", path, err)
		}
		_ = file.Close()
	}
	for kind, observed := range wantKinds {
		if !observed {
			t.Errorf("fixture replay did not produce %q", kind)
		}
	}
}

func TestEventNormalizerContinuesPersistedSequence(t *testing.T) {
	t.Parallel()
	normalizer, err := NewEventNormalizer(NormalizerOptions{
		AttemptID: "attempt-resumed", ProviderVersion: "0.151.0-alpha.7.2", InitialSequence: 41,
	})
	if err != nil {
		t.Fatalf("NewEventNormalizer() error = %v", err)
	}
	event, err := normalizer.Normalize(ServerNotification{
		Method: "turn/started", Params: json.RawMessage(`{"threadId":"thread-1","turn":{"id":"turn-1","status":"inProgress"}}`),
	})
	if err != nil || event.Sequence != 42 {
		t.Fatalf("Normalize() = (sequence %d, %v), want (42, nil)", event.Sequence, err)
	}
}

func TestEventNormalizerPreservesKnownUnknownAndRequestPayloads(t *testing.T) {
	t.Parallel()

	normalizer, err := NewEventNormalizer(NormalizerOptions{
		AttemptID:       "attempt-1",
		ProviderVersion: "0.151.0-alpha.7.2",
		Clock:           func() time.Time { return time.Unix(10, 0).UTC() },
	})
	if err != nil {
		t.Fatalf("NewEventNormalizer() error = %v", err)
	}
	knownRaw := json.RawMessage(`{"threadId":"thread-1","turnId":"turn-1","itemId":"message-1","delta":"hello"}`)
	known, err := normalizer.Normalize(ServerNotification{Method: "item/agentMessage/delta", Params: knownRaw, EmittedAtMS: 2000})
	if err != nil {
		t.Fatalf("Normalize(known) error = %v", err)
	}
	if known.Kind != provider.EventMessageDelta || known.ProviderItemID != "message-1" || !known.OccurredAt.Equal(time.Unix(2, 0)) {
		t.Fatalf("known event = %#v", known)
	}
	assertNativePayload(t, known.Payload, "item/agentMessage/delta", "", knownRaw)

	unknownRaw := json.RawMessage(`{"future":true}`)
	unknown, err := normalizer.Normalize(ServerNotification{Method: "future/event", Params: unknownRaw})
	if err != nil {
		t.Fatalf("Normalize(unknown) error = %v", err)
	}
	if unknown.Kind != provider.EventUnknownProvider {
		t.Fatalf("unknown event = %#v", unknown)
	}
	assertNativePayload(t, unknown.Payload, "future/event", "", unknownRaw)

	requested, err := normalizer.Normalize(ServerRequest{ID: json.RawMessage(`"request-1"`), Method: "item/fileChange/requestApproval", Params: json.RawMessage(`{"threadId":"thread-1","turnId":"turn-1","itemId":"change-1"}`)})
	if err != nil {
		t.Fatalf("Normalize(request) error = %v", err)
	}
	if requested.Kind != provider.EventPermissionRequested || requested.ProviderItemID != "change-1" {
		t.Fatalf("request event = %#v", requested)
	}
	var payload struct {
		RequestID  string                         `json:"requestId"`
		Checkpoint provider.InteractionCheckpoint `json:"checkpoint"`
		Params     json.RawMessage                `json:"params"`
	}
	if err := json.Unmarshal(requested.Payload, &payload); err != nil || payload.RequestID != "request-1" ||
		payload.Checkpoint.Kind != provider.InteractionFile || payload.Checkpoint.ProviderRequestID != `"request-1"` ||
		len(payload.Checkpoint.ScopeDigest) != 64 || !json.Valid(payload.Params) {
		t.Fatalf("request payload = %s, error = %v", requested.Payload, err)
	}
	resolved, err := normalizer.Normalize(ServerNotification{Method: "serverRequest/resolved", Params: json.RawMessage(`{"requestId":"request-1"}`)})
	if err != nil || resolved.Kind != provider.EventPermissionResponseRecorded {
		t.Fatalf("resolved event = %#v, error = %v", resolved, err)
	}
	var resolvedPayload struct {
		Checkpoint provider.InteractionCheckpoint `json:"checkpoint"`
	}
	if err := json.Unmarshal(resolved.Payload, &resolvedPayload); err != nil || resolvedPayload.Checkpoint != payload.Checkpoint {
		t.Fatalf("resolved checkpoint payload = %s, error = %v", resolved.Payload, err)
	}
}

func TestEventNormalizerEmitsDistinctInteractionCheckpoints(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		method    string
		params    string
		wantKind  provider.InteractionKind
		wantEvent provider.EventKind
	}{
		{"command", "item/commandExecution/requestApproval", `{"threadId":"thread-1","turnId":"turn-1","itemId":"command-1","command":"go test"}`, provider.InteractionCommand, provider.EventPermissionRequested},
		{"network", "item/commandExecution/requestApproval", `{"threadId":"thread-1","turnId":"turn-1","itemId":"command-2","networkApprovalContext":{"host":"example.com","protocol":"https"}}`, provider.InteractionNetwork, provider.EventPermissionRequested},
		{"file", "item/fileChange/requestApproval", `{"threadId":"thread-1","turnId":"turn-1","itemId":"file-1"}`, provider.InteractionFile, provider.EventPermissionRequested},
		{"permission", "item/permissions/requestApproval", `{"threadId":"thread-1","turnId":"turn-1","itemId":"permission-1","permissions":{"network":null,"fileSystem":{"read":["C:\\\\repo"]}}}`, provider.InteractionPermission, provider.EventPermissionRequested},
		{"tool", "item/tool/call", `{"threadId":"thread-1","turnId":"turn-1","callId":"tool-1","tool":"lookup","arguments":{}}`, provider.InteractionTool, provider.EventToolStarted},
		{"user", "item/tool/requestUserInput", `{"threadId":"thread-1","turnId":"turn-1","itemId":"question-1","questions":[],"isBlocking":true}`, provider.InteractionUser, provider.EventUserInputRequested},
	}
	digests := map[string]string{}
	for index, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			normalizer, err := NewEventNormalizer(NormalizerOptions{AttemptID: "attempt-1", ProviderVersion: "version-1"})
			if err != nil {
				t.Fatal(err)
			}
			event, err := normalizer.Normalize(ServerRequest{
				ID: json.RawMessage(strconv.Itoa(index + 1)), Method: test.method, Params: json.RawMessage(test.params),
			})
			if err != nil {
				t.Fatalf("Normalize() error = %v", err)
			}
			var payload struct {
				Checkpoint provider.InteractionCheckpoint `json:"checkpoint"`
			}
			if err := json.Unmarshal(event.Payload, &payload); err != nil || event.Kind != test.wantEvent ||
				payload.Checkpoint.Kind != test.wantKind || payload.Checkpoint.ProviderRequestID != strconv.Itoa(index+1) ||
				len(payload.Checkpoint.ScopeDigest) != 64 {
				t.Fatalf("checkpoint event = %#v payload=%s error=%v", event, event.Payload, err)
			}
			digests[test.name] = payload.Checkpoint.ScopeDigest
		})
	}
	if digests["command"] == digests["network"] {
		t.Fatal("command and network checkpoints shared a scope digest")
	}
}

func TestEventNormalizerMapsExtendedItemAndDiagnosticKinds(t *testing.T) {
	t.Parallel()

	normalizer, err := NewEventNormalizer(NormalizerOptions{AttemptID: "attempt-1", ProviderVersion: "version-1"})
	if err != nil {
		t.Fatalf("NewEventNormalizer() error = %v", err)
	}
	tests := []struct {
		method   string
		params   string
		wantKind provider.EventKind
	}{
		{"item/started", `{"item":{"type":"fileChange","id":"change-1"},"threadId":"thread-1","turnId":"turn-1"}`, provider.EventFileChangeStarted},
		{"item/completed", `{"item":{"type":"fileChange","id":"change-1"},"threadId":"thread-1","turnId":"turn-1"}`, provider.EventFileChangeCompleted},
		{"item/started", `{"item":{"type":"mcpToolCall","id":"tool-1"},"threadId":"thread-1","turnId":"turn-1"}`, provider.EventToolStarted},
		{"item/completed", `{"item":{"type":"mcpToolCall","id":"tool-1"},"threadId":"thread-1","turnId":"turn-1"}`, provider.EventToolCompleted},
		{"turn/plan/updated", `{"threadId":"thread-1","turnId":"turn-1","plan":[]}`, provider.EventPlanUpdated},
		{"error", `{"message":"provider failed"}`, provider.EventError},
		{"warning", `{"message":"provider warning"}`, provider.EventWarning},
	}
	for _, test := range tests {
		test := test
		t.Run(test.method+"/"+string(test.wantKind), func(t *testing.T) {
			event, err := normalizer.Normalize(ServerNotification{Method: test.method, Params: json.RawMessage(test.params)})
			if err != nil {
				t.Fatalf("Normalize() error = %v", err)
			}
			if event.Kind != test.wantKind {
				t.Fatalf("event kind = %q, want %q", event.Kind, test.wantKind)
			}
		})
	}
}

func TestEventNormalizerRejectsMalformedProviderParams(t *testing.T) {
	t.Parallel()

	normalizer, err := NewEventNormalizer(NormalizerOptions{AttemptID: "attempt-1", ProviderVersion: "version-1"})
	if err != nil {
		t.Fatalf("NewEventNormalizer() error = %v", err)
	}
	if _, err := normalizer.Normalize(ServerNotification{Method: "future/event", Params: json.RawMessage(`[]`)}); err == nil {
		t.Fatal("Normalize() accepted non-object params")
	}
}

func fixturePaths(t *testing.T) []string {
	t.Helper()
	workingDirectory, err := os.Getwd()
	if err != nil {
		t.Fatalf("get working directory: %v", err)
	}
	root := workingDirectory
	for {
		if _, err := os.Stat(filepath.Join(root, "go.mod")); err == nil {
			break
		}
		parent := filepath.Dir(root)
		if parent == root {
			t.Fatalf("could not locate runtime root from %s", workingDirectory)
		}
		root = parent
	}
	paths, err := filepath.Glob(filepath.Join(filepath.Dir(root), "probes", "codex-host", "fixtures", "0.151.0-alpha.7.2", "app-server-*.jsonl"))
	if err != nil || len(paths) == 0 {
		t.Fatalf("find App Server fixtures: paths=%v error=%v", paths, err)
	}
	return paths
}

func assertNativePayload(t *testing.T, raw json.RawMessage, method, requestID string, params json.RawMessage) {
	t.Helper()
	var payload struct {
		ProviderMethod string          `json:"providerMethod"`
		RequestID      string          `json:"requestId"`
		Params         json.RawMessage `json:"params"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatalf("decode native payload: %v", err)
	}
	if payload.ProviderMethod != method || payload.RequestID != requestID || string(payload.Params) != string(params) {
		t.Fatalf("native payload = %#v, params = %s", payload, payload.Params)
	}
}
