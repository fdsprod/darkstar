package codex

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/fdsprod/darkstar/runtime/src/ports"
	providerport "github.com/fdsprod/darkstar/runtime/src/ports/provider"
)

type appServerScript struct {
	output       string
	write        bool
	threadParams chan json.RawMessage
	turnParams   chan json.RawMessage
	done         chan error
}

func newAppServerScript(output string, write bool) *appServerScript {
	return &appServerScript{
		output:       output,
		write:        write,
		threadParams: make(chan json.RawMessage, 1),
		turnParams:   make(chan json.RawMessage, 1),
		done:         make(chan error, 1),
	}
}

func (script *appServerScript) factory(ctx context.Context) (*AppServerClient, InitializeResult, error) {
	serverReads, clientWrites := io.Pipe()
	clientReads, serverWrites := io.Pipe()
	client, err := NewAppServerClient(clientWrites, clientReads, AppServerOptions{
		ClientInfo:        ClientInfo{Name: "darkstar-test", Version: "1.0.0"},
		SupportedVersions: []string{"0.151.0-alpha.7.2"},
	})
	if err != nil {
		return nil, InitializeResult{}, err
	}
	go func() {
		script.done <- script.run(serverReads, serverWrites)
	}()
	initialized, err := client.Initialize(ctx)
	if err != nil {
		return nil, InitializeResult{}, err
	}
	return client, initialized, nil
}

func (script *appServerScript) run(reader *io.PipeReader, writer *io.PipeWriter) error {
	defer func() { _ = reader.Close() }()
	defer func() { _ = writer.Close() }()
	scanner := bufio.NewScanner(reader)
	receive := func() (wireMessage, error) {
		if !scanner.Scan() {
			if err := scanner.Err(); err != nil {
				return wireMessage{}, err
			}
			return wireMessage{}, io.EOF
		}
		var message wireMessage
		if err := json.Unmarshal(scanner.Bytes(), &message); err != nil {
			return wireMessage{}, err
		}
		return message, nil
	}
	send := func(value any) error {
		payload, err := json.Marshal(value)
		if err != nil {
			return err
		}
		_, err = writer.Write(append(payload, '\n'))
		return err
	}

	initialize, err := receive()
	if err != nil || initialize.Method != "initialize" {
		return errors.New("script expected initialize")
	}
	if err := send(map[string]any{
		"id": initialize.ID,
		"result": map[string]any{
			"userAgent":      "Codex Desktop/0.151.0-alpha.7.2 (Windows 10; x86_64)",
			"codexHome":      `C:\Users\test\.codex`,
			"platformFamily": "windows",
			"platformOs":     "windows",
		},
	}); err != nil {
		return err
	}
	initialized, err := receive()
	if err != nil || initialized.Method != "initialized" {
		return errors.New("script expected initialized notification")
	}
	thread, err := receive()
	if err != nil || thread.Method != "thread/start" {
		return errors.New("script expected thread/start")
	}
	script.threadParams <- cloneRaw(thread.Params)
	if err := send(map[string]any{"id": thread.ID, "result": map[string]any{"thread": map[string]string{"id": "thread-1"}}}); err != nil {
		return err
	}
	turn, err := receive()
	if err != nil || turn.Method != "turn/start" {
		return errors.New("script expected turn/start")
	}
	script.turnParams <- cloneRaw(turn.Params)
	if err := send(map[string]any{"id": turn.ID, "result": map[string]any{"turn": map[string]string{"id": "turn-1"}}}); err != nil {
		return err
	}
	if err := send(map[string]any{"method": "thread/started", "params": map[string]any{"thread": map[string]string{"id": "thread-1"}}, "emittedAtMs": 1000}); err != nil {
		return err
	}
	if err := send(map[string]any{"method": "turn/started", "params": map[string]any{"threadId": "thread-1", "turn": map[string]string{"id": "turn-1", "status": "inProgress"}}, "emittedAtMs": 2000}); err != nil {
		return err
	}
	if script.write {
		if err := send(map[string]any{
			"id":     0,
			"method": "item/commandExecution/requestApproval",
			"params": map[string]any{"threadId": "thread-1", "turnId": "turn-1", "itemId": "command-1", "command": "write output.txt"},
		}); err != nil {
			return err
		}
		approval, err := receive()
		if err != nil || string(approval.ID) != "0" || !strings.Contains(string(approval.Result), `"decision":"accept"`) {
			return errors.New("script expected accepted command approval")
		}
		if err := send(map[string]any{"method": "serverRequest/resolved", "params": map[string]any{"threadId": "thread-1", "requestId": 0}, "emittedAtMs": 2500}); err != nil {
			return err
		}
		if err := send(map[string]any{"method": "item/started", "params": map[string]any{"threadId": "thread-1", "turnId": "turn-1", "item": map[string]any{"type": "commandExecution", "id": "command-1", "status": "inProgress"}}, "emittedAtMs": 3000}); err != nil {
			return err
		}
		if err := send(map[string]any{"method": "item/completed", "params": map[string]any{"threadId": "thread-1", "turnId": "turn-1", "item": map[string]any{"type": "commandExecution", "id": "command-1", "status": "completed", "exitCode": 0}}, "emittedAtMs": 3500}); err != nil {
			return err
		}
		if err := send(map[string]any{"method": "item/started", "params": map[string]any{"threadId": "thread-1", "turnId": "turn-1", "item": map[string]any{"type": "fileChange", "id": "change-1", "status": "inProgress"}}, "emittedAtMs": 4000}); err != nil {
			return err
		}
		if err := send(map[string]any{"method": "item/completed", "params": map[string]any{"threadId": "thread-1", "turnId": "turn-1", "item": map[string]any{"type": "fileChange", "id": "change-1", "status": "completed", "changes": []string{"output.txt"}}}, "emittedAtMs": 4500}); err != nil {
			return err
		}
	}
	if err := send(map[string]any{
		"method": "item/completed",
		"params": map[string]any{
			"threadId": "thread-1",
			"turnId":   "turn-1",
			"item":     map[string]any{"type": "agentMessage", "id": "message-1", "phase": "final_answer", "text": script.output},
		},
		"emittedAtMs": 5000,
	}); err != nil {
		return err
	}
	if err := send(map[string]any{
		"method": "thread/tokenUsage/updated",
		"params": map[string]any{
			"threadId":   "thread-1",
			"turnId":     "turn-1",
			"tokenUsage": map[string]any{"total": map[string]int64{"inputTokens": 10, "cachedInputTokens": 4, "outputTokens": 6}},
		},
		"emittedAtMs": 5500,
	}); err != nil {
		return err
	}
	if err := send(map[string]any{
		"method":      "turn/completed",
		"params":      map[string]any{"threadId": "thread-1", "turn": map[string]any{"id": "turn-1", "status": "completed", "durationMs": 100}},
		"emittedAtMs": 6000,
	}); err != nil {
		return err
	}
	unsubscribe, err := receive()
	if err != nil || unsubscribe.Method != "thread/unsubscribe" {
		return errors.New("script expected thread/unsubscribe")
	}
	return send(map[string]any{"id": unsubscribe.ID, "result": map[string]string{"status": "unsubscribed"}})
}

func TestAdapterExecutesStructuredReadOnlyNode(t *testing.T) {
	t.Parallel()

	workspace := t.TempDir()
	script := newAppServerScript(`{"answer":"ok"}`, false)
	adapter := newTestAdapter(t, script)
	request := testAttemptRequest(workspace)
	handle, err := adapter.StartAttempt(context.Background(), request)
	if err != nil {
		t.Fatalf("StartAttempt() error = %v", err)
	}
	threadParams := <-script.threadParams
	var thread struct {
		CWD            string `json:"cwd"`
		Sandbox        string `json:"sandbox"`
		ApprovalPolicy string `json:"approvalPolicy"`
	}
	if err := json.Unmarshal(threadParams, &thread); err != nil {
		t.Fatalf("decode thread params: %v", err)
	}
	if thread.CWD != workspace || thread.Sandbox != "read-only" || thread.ApprovalPolicy != "never" {
		t.Fatalf("thread params = %#v", thread)
	}
	turnParams := <-script.turnParams
	var turn struct {
		Input        []turnInput     `json:"input"`
		OutputSchema json.RawMessage `json:"outputSchema"`
	}
	if err := json.Unmarshal(turnParams, &turn); err != nil || len(turn.Input) != 2 || !json.Valid(turn.OutputSchema) {
		t.Fatalf("turn params = %#v, error = %v", turn, err)
	}
	events := collectEvents(t, adapter, handle, nil)
	if !hasEventKind(events, providerport.EventStructuredOutputCompleted) {
		t.Fatal("event stream omitted validated structured output")
	}
	result := getResult(t, adapter, handle)
	succeeded, ok := result.(providerport.SucceededResult)
	if !ok || string(succeeded.StructuredOutput) != `{"answer":"ok"}` {
		t.Fatalf("result = %#v", result)
	}
	if succeeded.Usage.InputTokens != 10 || succeeded.Usage.CachedTokens != 4 || succeeded.Usage.OutputTokens != 6 {
		t.Fatalf("usage = %#v", succeeded.Usage)
	}
	if len(succeeded.WorkspaceEvidence) != 1 || succeeded.Recovery.LastSequence != events[len(events)-1].Sequence {
		t.Fatalf("metadata = %#v", succeeded.AttemptResultMetadata)
	}
	assertEvidenceFiles(t, succeeded.WorkspaceEvidence)
	if err := <-script.done; err != nil {
		t.Fatalf("App Server script error = %v", err)
	}
}

func TestAdapterExecutesWorkspaceWriteNodeWithExplicitApprovalEvidence(t *testing.T) {
	t.Parallel()

	workspace := t.TempDir()
	script := newAppServerScript(`{"wrote":true}`, true)
	adapter := newTestAdapter(t, script)
	request := testAttemptRequest(workspace)
	request.Access = providerport.AccessWorkspaceWrite
	request.CommandPolicy = providerport.InteractionAsk
	request.FilePolicy = providerport.InteractionAsk
	request.OutputSchema = json.RawMessage(`{"type":"object","additionalProperties":false,"properties":{"wrote":{"type":"boolean"}},"required":["wrote"]}`)
	handle, err := adapter.StartAttempt(context.Background(), request)
	if err != nil {
		t.Fatalf("StartAttempt() error = %v", err)
	}
	threadParams := <-script.threadParams
	var thread struct {
		Sandbox           string `json:"sandbox"`
		ApprovalPolicy    string `json:"approvalPolicy"`
		ApprovalsReviewer string `json:"approvalsReviewer"`
	}
	if err := json.Unmarshal(threadParams, &thread); err != nil {
		t.Fatalf("decode thread params: %v", err)
	}
	if thread.Sandbox != "workspace-write" || thread.ApprovalPolicy != "on-request" || thread.ApprovalsReviewer != "user" {
		t.Fatalf("thread params = %#v", thread)
	}
	<-script.turnParams
	events := collectEvents(t, adapter, handle, func(event providerport.Event) {
		if event.Kind != providerport.EventPermissionRequested {
			return
		}
		_, err := adapter.Respond(context.Background(), providerport.PermissionResponse{
			InteractionContext: providerport.InteractionContext{
				AttemptID:         handle.AttemptID,
				ProviderThreadID:  handle.ProviderThreadID,
				ProviderRequestID: "0",
				IdempotencyKey:    "approval-1",
			},
			Decision: providerport.PermissionAllowOnce,
		})
		if err != nil {
			t.Fatalf("Respond() error = %v", err)
		}
	})
	for _, kind := range []providerport.EventKind{
		providerport.EventPermissionRequested,
		providerport.EventPermissionResponseRecorded,
		providerport.EventCommandCompleted,
		providerport.EventFileChangeCompleted,
		providerport.EventStructuredOutputCompleted,
	} {
		if !hasEventKind(events, kind) {
			t.Errorf("event stream omitted %q", kind)
		}
	}
	result := getResult(t, adapter, handle)
	succeeded, ok := result.(providerport.SucceededResult)
	if !ok || len(succeeded.WorkspaceEvidence) < 4 {
		t.Fatalf("result = %#v", result)
	}
	assertEvidenceFiles(t, succeeded.WorkspaceEvidence)
	if err := <-script.done; err != nil {
		t.Fatalf("App Server script error = %v", err)
	}
}

func TestAdapterRejectsInvalidSchemaAndFailsMismatchedOutput(t *testing.T) {
	t.Parallel()

	recorder, err := NewDirectoryEvidenceRecorder(t.TempDir())
	if err != nil {
		t.Fatalf("NewDirectoryEvidenceRecorder() error = %v", err)
	}
	var starts atomic.Int64
	adapter, err := NewAdapter(AdapterOptions{
		Factory: func(context.Context) (*AppServerClient, InitializeResult, error) {
			starts.Add(1)
			return nil, InitializeResult{}, errors.New("should not start")
		},
		EvidenceRecorder: recorder,
	})
	if err != nil {
		t.Fatalf("NewAdapter() error = %v", err)
	}
	invalid := testAttemptRequest(t.TempDir())
	invalid.OutputSchema = json.RawMessage(`{`)
	_, err = adapter.StartAttempt(context.Background(), invalid)
	var failure *ports.Failure
	if !errors.As(err, &failure) || failure.Code != ports.FailureInvalidRequest || starts.Load() != 0 {
		t.Fatalf("StartAttempt() error = %#v, starts = %d", err, starts.Load())
	}

	script := newAppServerScript(`{"answer":7}`, false)
	mismatch := newTestAdapter(t, script)
	request := testAttemptRequest(t.TempDir())
	handle, err := mismatch.StartAttempt(context.Background(), request)
	if err != nil {
		t.Fatalf("StartAttempt() error = %v", err)
	}
	<-script.threadParams
	<-script.turnParams
	_ = collectEvents(t, mismatch, handle, nil)
	result := getResult(t, mismatch, handle)
	failed, ok := result.(providerport.FailedResult)
	if !ok || failed.Failure.Code != ports.FailureInvalidRequest || failed.Failure.Details["phase"] != "output_validation" {
		t.Fatalf("result = %#v", result)
	}
	if err := <-script.done; err != nil {
		t.Fatalf("App Server script error = %v", err)
	}
}

func newTestAdapter(t *testing.T, script *appServerScript) *Adapter {
	t.Helper()
	recorder, err := NewDirectoryEvidenceRecorder(t.TempDir())
	if err != nil {
		t.Fatalf("NewDirectoryEvidenceRecorder() error = %v", err)
	}
	adapter, err := NewAdapter(AdapterOptions{
		Factory:          script.factory,
		EvidenceRecorder: recorder,
		Clock: func() time.Time {
			return time.Date(2026, time.September, 1, 12, 0, 0, 0, time.UTC)
		},
	})
	if err != nil {
		t.Fatalf("NewAdapter() error = %v", err)
	}
	return adapter
}

func testAttemptRequest(workspace string) providerport.AttemptRequest {
	return providerport.AttemptRequest{
		AttemptID:      "attempt-1",
		RunID:          "run-1",
		NodeID:         "node-1",
		IdempotencyKey: "start-1",
		Workspace:      workspace,
		Access:         providerport.AccessReadOnly,
		Network:        providerport.NetworkDenied,
		CommandPolicy:  providerport.InteractionDeny,
		FilePolicy:     providerport.InteractionDeny,
		ToolPolicy:     providerport.InteractionDeny,
		Prompt:         "Return the structured answer.",
		Inputs: []providerport.Input{{
			Kind: providerport.InputText,
			Name: "context",
			Text: "Prepared immutable context.",
		}},
		OutputSchema: json.RawMessage(`{"type":"object","additionalProperties":false,"properties":{"answer":{"type":"string"}},"required":["answer"]}`),
	}
}

func collectEvents(t *testing.T, adapter *Adapter, handle providerport.AttemptHandle, observe func(providerport.Event)) []providerport.Event {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	stream, err := adapter.StreamEvents(ctx, providerport.EventRequest{Handle: handle})
	if err != nil {
		t.Fatalf("StreamEvents() error = %v", err)
	}
	defer func() { _ = stream.Close() }()
	var events []providerport.Event
	for {
		event, err := stream.Receive()
		if errors.Is(err, io.EOF) {
			return events
		}
		if err != nil {
			t.Fatalf("Receive() error = %v", err)
		}
		if observe != nil {
			observe(event)
		}
		events = append(events, event)
	}
}

func getResult(t *testing.T, adapter *Adapter, handle providerport.AttemptHandle) providerport.AttemptResult {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	result, err := adapter.GetResult(ctx, providerport.ResultRequest{Handle: handle})
	if err != nil {
		t.Fatalf("GetResult() error = %v", err)
	}
	return result
}

func hasEventKind(events []providerport.Event, kind providerport.EventKind) bool {
	for _, event := range events {
		if event.Kind == kind {
			return true
		}
	}
	return false
}

func assertEvidenceFiles(t *testing.T, evidence []providerport.Evidence) {
	t.Helper()
	for _, item := range evidence {
		if !filepath.IsAbs(item.Ref) || item.Digest == "" {
			t.Errorf("evidence = %#v", item)
			continue
		}
		payload, err := os.ReadFile(item.Ref)
		if err != nil || len(payload) == 0 {
			t.Errorf("read evidence %s: bytes=%d error=%v", item.Ref, len(payload), err)
		}
	}
}
