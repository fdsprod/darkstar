package typescript

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"darkstar/src/ports/extension"
	"darkstar/src/ports/provider"
)

func fixtureAdapter(t *testing.T, mode string) (*Adapter, provider.AttemptRequest, context.Context) {
	t.Helper()
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("Node runtime unavailable")
	}
	node, err = filepath.Abs(node)
	if err != nil {
		t.Fatal(err)
	}
	root, err := filepath.Abs("../../../../..")
	if err != nil {
		t.Fatal(err)
	}
	bundle := filepath.Join(root, "runtime", "src", "adapters", "provider", "typescript", "builtin.mjs")
	data, err := os.ReadFile(bundle)
	if err != nil {
		t.Fatal(err)
	}
	var evidenceMu sync.Mutex
	lastSequence := uint64(0)
	adapter, err := New(Config{NodeExecutable: node, Entrypoint: bundle, ProviderExecutable: node, Provider: "codex",
		Arguments:   []string{filepath.Join(root, "plugins", "provider-codex", "tests", "app-server-fixture.mjs"), mode},
		Environment: os.Environ(), ProjectRoot: t.TempDir(),
		Ref: extension.Ref{ID: "darkstar/provider-codex", Version: "1.0.0", Digest: fmt.Sprintf("%x", sha256.Sum256(data))},
		RecordEvidence: func(_ context.Context, record EvidenceRecord) (provider.Evidence, error) {
			evidenceMu.Lock()
			defer evidenceMu.Unlock()
			if record.AttemptID != "attempt-1" || record.Sequence != lastSequence+1 || !json.Valid(record.Data) {
				return provider.Evidence{}, fmt.Errorf("invalid evidence identity/sequence")
			}
			lastSequence = record.Sequence
			return provider.Evidence{Kind: record.Kind, Ref: fmt.Sprintf("evidence:%d", record.Sequence), Digest: fmt.Sprintf("%x", sha256.Sum256(record.Data))}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = adapter.Close()
	})
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	t.Cleanup(cancel)
	request := provider.AttemptRequest{AttemptID: "attempt-1", RunID: "run-1", NodeID: "node-1", IdempotencyKey: "start-1", Workspace: t.TempDir(),
		Access: provider.AccessReadOnly, Network: provider.NetworkDenied, CommandPolicy: provider.InteractionAsk, FilePolicy: provider.InteractionAsk, ToolPolicy: provider.InteractionAsk,
		Prompt: "Scoped task only", OutputSchema: json.RawMessage(`{"type":"object","required":["answer"],"properties":{"answer":{"type":"integer"}}}`), CancellationGrace: time.Second}
	return adapter, request, ctx
}

func TestBuiltinProviderLifecycleThroughGenericBridge(t *testing.T) {
	adapter, request, ctx := fixtureAdapter(t, "success")
	health, err := adapter.ProbeHealth(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if health.State != provider.HealthAvailable || health.Authentication != provider.AuthenticationAuthenticated {
		t.Fatalf("health %#v", health)
	}
	healthJSON, _ := json.Marshal(health)
	if strings.Contains(string(healthJSON), "secret@example") {
		t.Fatal("health leaked identity")
	}
	capabilities, err := adapter.Capabilities(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := capabilities.Features["resume"].(provider.AvailableCapability); !ok {
		t.Fatal("missing resume")
	}
	handle, err := adapter.StartAttempt(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	stream, err := adapter.StreamEvents(ctx, provider.EventRequest{Handle: handle})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = stream.Close()
	}()
	previous := uint64(0)
	unknown := false
	structured := false
	for {
		event, err := stream.Receive()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if event.Sequence != previous+1 || event.RawEvidenceRef == "" {
			t.Fatalf("invalid event %#v", event)
		}
		previous = event.Sequence
		unknown = unknown || event.Kind == provider.EventUnknownProvider
		structured = structured || event.Kind == provider.EventStructuredOutputCompleted
	}
	if !unknown || !structured {
		t.Fatal("missing normalized events")
	}
	result, err := adapter.GetResult(ctx, provider.ResultRequest{Handle: handle})
	if err != nil {
		t.Fatal(err)
	}
	success, ok := result.(provider.SucceededResult)
	if !ok || string(success.StructuredOutput) != `{"answer":42}` || success.Usage.InputTokens != 11 {
		t.Fatalf("result %#v", result)
	}
}

type fixtureTools struct{ calls int }

func (f *fixtureTools) Call(_ context.Context, id, name string, args json.RawMessage) (json.RawMessage, error) {
	if id != "call-1" || name != "journal_items" || string(args) != `{"operation":"read"}` {
		return nil, fmt.Errorf("unexpected tool call")
	}
	f.calls++
	return json.RawMessage(`{"recorded":true}`), nil
}
func (*fixtureTools) ResolveSubmittedOutputs(context.Context) (json.RawMessage, error) {
	return json.RawMessage(`{"answer":42}`), nil
}

func TestBuiltinProviderToolOutputUsesHostAuthority(t *testing.T) {
	adapter, request, ctx := fixtureAdapter(t, "tool")
	handler := &fixtureTools{}
	request.ToolHandler = handler
	request.DynamicTools = []provider.ToolDefinition{{Type: "function", Name: "journal_items", Description: "Read", InputSchema: json.RawMessage(`{"type":"object"}`)}}
	handle, err := adapter.StartAttempt(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	result, err := adapter.GetResult(ctx, provider.ResultRequest{Handle: handle})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := result.(provider.SucceededResult); !ok || handler.calls != 1 {
		t.Fatalf("result=%#v calls=%d", result, handler.calls)
	}
}

func TestBuiltinProviderPermissionAndCancellation(t *testing.T) {
	t.Run("permission", func(t *testing.T) {
		adapter, request, ctx := fixtureAdapter(t, "approval")
		handle, err := adapter.StartAttempt(ctx, request)
		if err != nil {
			t.Fatal(err)
		}
		stream, err := adapter.StreamEvents(ctx, provider.EventRequest{Handle: handle})
		if err != nil {
			t.Fatal(err)
		}
		defer func() {
			_ = stream.Close()
		}()
		for {
			event, err := stream.Receive()
			if err != nil {
				t.Fatal(err)
			}
			if event.Kind != provider.EventPermissionRequested {
				continue
			}
			checkpoint, present, err := provider.InteractionCheckpointFromEvent(event)
			if err != nil || !present {
				t.Fatalf("checkpoint %v %v", present, err)
			}
			response := provider.PermissionResponse{InteractionContext: provider.InteractionContext{AttemptID: request.AttemptID, ProviderThreadID: handle.ProviderThreadID, ProviderRequestID: checkpoint.ProviderRequestID, IdempotencyKey: "response-1", ScopeDigest: checkpoint.ScopeDigest}, Decision: provider.PermissionAllowOnce}
			if _, err := adapter.Respond(ctx, response); err != nil {
				t.Fatal(err)
			}
			break
		}
		result, err := adapter.GetResult(ctx, provider.ResultRequest{Handle: handle})
		if err != nil {
			t.Fatal(err)
		}
		if _, ok := result.(provider.SucceededResult); !ok {
			t.Fatalf("result %#v", result)
		}
	})
	t.Run("cancel", func(t *testing.T) {
		adapter, request, ctx := fixtureAdapter(t, "wait")
		handle, err := adapter.StartAttempt(ctx, request)
		if err != nil {
			t.Fatal(err)
		}
		cancelled, err := adapter.CancelAttempt(ctx, provider.CancelRequest{Handle: handle, IdempotencyKey: "cancel-1", GracePeriod: time.Second})
		if err != nil {
			t.Fatal(err)
		}
		if cancelled.Disposition != provider.CancelGraceful {
			t.Fatalf("cancel %#v", cancelled)
		}
		result, err := adapter.GetResult(ctx, provider.ResultRequest{Handle: handle})
		if err != nil {
			t.Fatal(err)
		}
		if _, ok := result.(provider.CancelledResult); !ok {
			t.Fatalf("result %#v", result)
		}
	})
}

func TestBuiltinProviderRefusesStaleResume(t *testing.T) {
	adapter, _, ctx := fixtureAdapter(t, "stale-resume")
	_, err := adapter.ResumeAttempt(ctx, provider.ResumeRequest{AttemptID: "attempt-1", IdempotencyKey: "resume-1", ProviderThreadID: "thread-1", ProviderTurnID: "turn-1", ContextDigest: strings.Repeat("a", 64), WorkspaceDigest: strings.Repeat("b", 64)})
	if err == nil || !strings.Contains(err.Error(), "not active") {
		t.Fatalf("resume error %v", err)
	}
}

func TestBuiltinProviderResumedOutputHasNoInventedAdapterSchema(t *testing.T) {
	adapter, _, ctx := fixtureAdapter(t, "resume")
	handle, err := adapter.ResumeAttempt(ctx, provider.ResumeRequest{AttemptID: "attempt-1", IdempotencyKey: "resume-1", ProviderThreadID: "thread-1", ProviderTurnID: "turn-1", ContextDigest: strings.Repeat("a", 64), WorkspaceDigest: strings.Repeat("b", 64)})
	if err != nil {
		t.Fatal(err)
	}
	result, err := adapter.GetResult(ctx, provider.ResultRequest{Handle: handle})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := result.(provider.SucceededResult); !ok {
		t.Fatalf("resumed result %#v", result)
	}
}

func TestInvalidStructuredOutputCannotBeForwardedAsCompleted(t *testing.T) {
	adapter, request, ctx := fixtureAdapter(t, "success")
	request.OutputSchema = json.RawMessage(`{"type":"object","required":["answer"],"properties":{"answer":{"type":"string"}}}`)
	handle, err := adapter.StartAttempt(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	stream, err := adapter.StreamEvents(ctx, provider.EventRequest{Handle: handle})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = stream.Close()
	}()
	for {
		event, err := stream.Receive()
		if err != nil {
			if err == io.EOF {
				t.Fatal("invalid structured output was silently accepted")
			}
			break
		}
		if event.Kind == provider.EventStructuredOutputCompleted {
			t.Fatal("unvalidated structured output escaped the provider boundary")
		}
	}
	if _, err := adapter.GetResult(ctx, provider.ResultRequest{Handle: handle}); err == nil {
		t.Fatal("invalid structured result was accepted")
	}
}

func TestHandleRequiresProviderTurnIdentity(t *testing.T) {
	adapter := &Adapter{config: Config{Provider: "codex"}}
	if err := adapter.validateHandle(provider.AttemptHandle{AttemptID: "attempt-1", Provider: "codex", ProviderThreadID: "thread-1", ProcessOwnerID: "123"}, "attempt-1"); err == nil {
		t.Fatal("provider handle without turn identity was accepted")
	}
}
