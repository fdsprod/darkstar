package codex

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"darkstar/src/ports"
	providerport "darkstar/src/ports/provider"
)

type appServerScript struct {
	output       string
	write        bool
	threadParams chan json.RawMessage
	turnParams   chan json.RawMessage
	done         chan error
}

type resumeAppServerScript struct {
	turnStatus   string
	resumeParams chan json.RawMessage
	done         chan error
}

type stopAppServerScript struct {
	graceful        bool
	interruptParams chan json.RawMessage
	turnReady       chan struct{}
	done            chan error
	owner           *scriptedProcessOwner
}

type scriptedProcessOwner struct {
	killed   chan struct{}
	exited   chan struct{}
	killOnce atomic.Bool
	exitOnce atomic.Bool
}

func newStopAppServerScript(graceful bool) *stopAppServerScript {
	owner := &scriptedProcessOwner{killed: make(chan struct{}), exited: make(chan struct{})}
	return &stopAppServerScript{
		graceful: graceful, interruptParams: make(chan json.RawMessage, 1), turnReady: make(chan struct{}), done: make(chan error, 1), owner: owner,
	}
}

func (owner *scriptedProcessOwner) Wait() error {
	<-owner.exited
	return nil
}

func (owner *scriptedProcessOwner) Kill() error {
	if owner.killOnce.CompareAndSwap(false, true) {
		close(owner.killed)
	}
	owner.exit()
	return nil
}

func (owner *scriptedProcessOwner) PID() int { return 4242 }

func (owner *scriptedProcessOwner) exit() {
	if owner.exitOnce.CompareAndSwap(false, true) {
		close(owner.exited)
	}
}

func newResumeAppServerScript(turnStatus string) *resumeAppServerScript {
	return &resumeAppServerScript{
		turnStatus: turnStatus, resumeParams: make(chan json.RawMessage, 1), done: make(chan error, 1),
	}
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

func (script *resumeAppServerScript) factory(ctx context.Context) (*AppServerClient, InitializeResult, error) {
	serverReads, clientWrites := io.Pipe()
	clientReads, serverWrites := io.Pipe()
	client, err := NewAppServerClient(clientWrites, clientReads, AppServerOptions{
		ClientInfo: ClientInfo{Name: "darkstar-test", Version: "1.0.0"}, SupportedVersions: []string{"0.151.0-alpha.7.2"},
	})
	if err != nil {
		return nil, InitializeResult{}, err
	}
	go func() { script.done <- script.run(serverReads, serverWrites) }()
	initialized, err := client.Initialize(ctx)
	if err != nil {
		return nil, InitializeResult{}, err
	}
	return client, initialized, nil
}

func (script *resumeAppServerScript) run(reader *io.PipeReader, writer *io.PipeWriter) error {
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
		return errors.New("resume script expected initialize")
	}
	if err := send(map[string]any{"id": initialize.ID, "result": map[string]any{
		"userAgent": "Codex Desktop/0.151.0-alpha.7.2 (Windows 10; x86_64)", "codexHome": `C:\Users\test\.codex`,
		"platformFamily": "windows", "platformOs": "windows",
	}}); err != nil {
		return err
	}
	initialized, err := receive()
	if err != nil || initialized.Method != "initialized" {
		return errors.New("resume script expected initialized notification")
	}
	resume, err := receive()
	if err != nil || resume.Method != "thread/resume" {
		return errors.New("resume script expected thread/resume")
	}
	script.resumeParams <- cloneRaw(resume.Params)
	if err := send(map[string]any{"id": resume.ID, "result": map[string]any{"thread": map[string]any{
		"id": "thread-1", "turns": []map[string]string{{"id": "turn-1", "status": script.turnStatus}},
	}}}); err != nil {
		return err
	}
	if script.turnStatus == "inProgress" {
		if err := send(map[string]any{"method": "item/completed", "params": map[string]any{
			"threadId": "thread-1", "turnId": "turn-1",
			"item": map[string]any{"type": "agentMessage", "id": "message-2", "phase": "final_answer", "text": `{"answer":"resumed"}`},
		}, "emittedAtMs": 7000}); err != nil {
			return err
		}
		if err := send(map[string]any{"method": "turn/completed", "params": map[string]any{
			"threadId": "thread-1", "turn": map[string]any{"id": "turn-1", "status": "completed"},
		}, "emittedAtMs": 8000}); err != nil {
			return err
		}
	}
	unsubscribe, err := receive()
	if err != nil || unsubscribe.Method != "thread/unsubscribe" {
		return errors.New("resume script expected thread/unsubscribe")
	}
	return send(map[string]any{"id": unsubscribe.ID, "result": map[string]string{"status": "unsubscribed"}})
}

func (script *stopAppServerScript) factory(ctx context.Context) (*AppServerClient, InitializeResult, error) {
	serverReads, clientWrites := io.Pipe()
	clientReads, serverWrites := io.Pipe()
	client, err := newAppServerClient(clientWrites, clientReads, script.owner, AppServerOptions{
		ClientInfo: ClientInfo{Name: "darkstar-test", Version: "1.0.0"}, SupportedVersions: []string{"0.151.0-alpha.7.2"},
	})
	if err != nil {
		return nil, InitializeResult{}, err
	}
	go func() { script.done <- script.run(serverReads, serverWrites) }()
	initialized, err := client.Initialize(ctx)
	if err != nil {
		return nil, InitializeResult{}, err
	}
	return client, initialized, nil
}

func (script *stopAppServerScript) run(reader *io.PipeReader, writer *io.PipeWriter) error {
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
	require := func(method string) (wireMessage, error) {
		message, err := receive()
		if err != nil {
			return wireMessage{}, err
		}
		if message.Method != method {
			return wireMessage{}, fmt.Errorf("stop script expected %s, got %s", method, message.Method)
		}
		return message, nil
	}

	initialize, err := require("initialize")
	if err != nil {
		return err
	}
	if err := send(map[string]any{"id": initialize.ID, "result": map[string]any{
		"userAgent": "Codex Desktop/0.151.0-alpha.7.2 (Windows 10; x86_64)", "platformFamily": "windows", "platformOs": "windows",
	}}); err != nil {
		return err
	}
	if _, err := require("initialized"); err != nil {
		return err
	}
	thread, err := require("thread/start")
	if err != nil {
		return err
	}
	if err := send(map[string]any{"id": thread.ID, "result": map[string]any{"thread": map[string]string{"id": "thread-stop"}}}); err != nil {
		return err
	}
	turn, err := require("turn/start")
	if err != nil {
		return err
	}
	if err := send(map[string]any{"id": turn.ID, "result": map[string]any{"turn": map[string]string{"id": "turn-stop"}}}); err != nil {
		return err
	}
	if err := send(map[string]any{"method": "thread/started", "params": map[string]any{"thread": map[string]string{"id": "thread-stop"}}}); err != nil {
		return err
	}
	if err := send(map[string]any{"method": "turn/started", "params": map[string]any{"threadId": "thread-stop", "turn": map[string]string{"id": "turn-stop", "status": "inProgress"}}}); err != nil {
		return err
	}
	close(script.turnReady)
	interrupt, err := require("turn/interrupt")
	if err != nil {
		return err
	}
	script.interruptParams <- cloneRaw(interrupt.Params)
	if err := send(map[string]any{"id": interrupt.ID, "result": map[string]any{}}); err != nil {
		return err
	}
	if !script.graceful {
		<-script.owner.killed
		return nil
	}
	if err := send(map[string]any{
		"method": "turn/completed", "params": map[string]any{"threadId": "thread-stop", "turn": map[string]string{"id": "turn-stop", "status": "interrupted"}},
	}); err != nil {
		return err
	}
	unsubscribe, err := require("thread/unsubscribe")
	if err != nil {
		return err
	}
	if err := send(map[string]any{"id": unsubscribe.ID, "result": map[string]string{"status": "unsubscribed"}}); err != nil {
		return err
	}
	script.owner.exit()
	return nil
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
		checkpoint, present, err := providerport.InteractionCheckpointFromEvent(event)
		if err != nil || !present {
			t.Fatalf("decode interaction checkpoint: present=%t error=%v", present, err)
		}
		response := providerport.PermissionResponse{
			InteractionContext: providerport.InteractionContext{
				AttemptID:         handle.AttemptID,
				ProviderThreadID:  handle.ProviderThreadID,
				ProviderRequestID: checkpoint.ProviderRequestID,
				IdempotencyKey:    "approval-1",
				ScopeDigest:       checkpoint.ScopeDigest,
			},
			Decision: providerport.PermissionAllowOnce,
		}
		unknown := response
		unknown.ProviderRequestID = "99"
		_, err = adapter.Respond(context.Background(), unknown)
		var notFound *ports.Failure
		if !errors.As(err, &notFound) || notFound.Code != ports.FailureNotFound {
			t.Fatalf("unknown response error = %#v", err)
		}
		wrongScope := response
		wrongScope.ScopeDigest = strings.Repeat("b", 64)
		_, err = adapter.Respond(context.Background(), wrongScope)
		var invalid *ports.Failure
		if !errors.As(err, &invalid) || invalid.Code != ports.FailureInvalidRequest {
			t.Fatalf("wrong-scope response error = %#v", err)
		}
		type responseResult struct {
			receipt providerport.InteractionReceipt
			err     error
		}
		results := make(chan responseResult, 8)
		for range 8 {
			go func() {
				receipt, err := adapter.Respond(context.Background(), response)
				results <- responseResult{receipt: receipt, err: err}
			}()
		}
		var want providerport.InteractionReceipt
		for range 8 {
			result := <-results
			if result.err != nil {
				t.Fatalf("Respond() error = %v", result.err)
			}
			if want == (providerport.InteractionReceipt{}) {
				want = result.receipt
			} else if result.receipt != want {
				t.Fatalf("concurrent response receipt = %#v, want %#v", result.receipt, want)
			}
		}
		changed := response
		changed.Decision = providerport.PermissionDenied
		_, err = adapter.Respond(context.Background(), changed)
		var conflict *ports.Failure
		if !errors.As(err, &conflict) || conflict.Code != ports.FailureConflict {
			t.Fatalf("changed response error = %#v", err)
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

func TestAdapterResumesRecordedActiveTurnFromPersistedSequence(t *testing.T) {
	t.Parallel()
	script := newResumeAppServerScript("inProgress")
	adapter := newResumeTestAdapter(t, script)
	request := providerport.ResumeRequest{
		AttemptID: "attempt-resume", IdempotencyKey: "resume-1", ProviderThreadID: "thread-1", ProviderTurnID: "turn-1", LastSequence: 7,
	}
	handle, err := adapter.ResumeAttempt(context.Background(), request)
	if err != nil {
		t.Fatalf("ResumeAttempt() error = %v", err)
	}
	params := <-script.resumeParams
	if !strings.Contains(string(params), `"threadId":"thread-1"`) || handle.ProviderTurnID != "turn-1" {
		t.Fatalf("resume params = %s, handle = %#v", params, handle)
	}
	replayed, err := adapter.ResumeAttempt(context.Background(), request)
	if err != nil || replayed != handle {
		t.Fatalf("idempotent ResumeAttempt() = (%#v, %v)", replayed, err)
	}
	changed := request
	changed.LastSequence++
	_, err = adapter.ResumeAttempt(context.Background(), changed)
	var conflict *ports.Failure
	if !errors.As(err, &conflict) || conflict.Code != ports.FailureConflict {
		t.Fatalf("changed ResumeAttempt() error = %#v", err)
	}
	events := collectEvents(t, adapter, handle, nil)
	if len(events) != 3 || events[0].Sequence != 8 || events[2].Sequence != 10 ||
		!hasEventKind(events, providerport.EventStructuredOutputCompleted) {
		t.Fatalf("resumed events = %#v", events)
	}
	result := getResult(t, adapter, handle)
	succeeded, ok := result.(providerport.SucceededResult)
	if !ok || string(succeeded.StructuredOutput) != `{"answer":"resumed"}` || succeeded.Recovery.LastSequence != 10 {
		t.Fatalf("resumed result = %#v", result)
	}
	if err := <-script.done; err != nil {
		t.Fatalf("resume App Server script error = %v", err)
	}
}

func TestAdapterFailsClosedWhenRecordedTurnIsNotActive(t *testing.T) {
	t.Parallel()
	script := newResumeAppServerScript("completed")
	adapter := newResumeTestAdapter(t, script)
	_, err := adapter.ResumeAttempt(context.Background(), providerport.ResumeRequest{
		AttemptID: "attempt-resume", IdempotencyKey: "resume-1", ProviderThreadID: "thread-1", ProviderTurnID: "turn-1", LastSequence: 7,
	})
	var failure *ports.Failure
	if !errors.As(err, &failure) || failure.Code != ports.FailureUncertain {
		t.Fatalf("ResumeAttempt() error = %#v", err)
	}
	if err := <-script.done; err != nil {
		t.Fatalf("resume App Server script error = %v", err)
	}
}

func TestAdapterCancelsGracefullyAndDeduplicatesTheRequest(t *testing.T) {
	script := newStopAppServerScript(true)
	adapter := newStopTestAdapter(t, script)
	request := testAttemptRequest(t.TempDir())
	request.CancellationGrace = 3 * time.Second
	handle, err := adapter.StartAttempt(context.Background(), request)
	if err != nil {
		t.Fatalf("StartAttempt() error = %v", err)
	}
	<-script.turnReady
	cancelRequest := providerport.CancelRequest{Handle: handle, IdempotencyKey: "cancel-1", GracePeriod: 3 * time.Second}
	first, err := adapter.CancelAttempt(context.Background(), cancelRequest)
	if err != nil || first.Disposition != providerport.CancelGraceful || first.EvidenceRef == "" {
		t.Fatalf("CancelAttempt() = (%#v, %v)", first, err)
	}
	if params := <-script.interruptParams; !strings.Contains(string(params), `"threadId":"thread-stop"`) || !strings.Contains(string(params), `"turnId":"turn-stop"`) {
		t.Fatalf("turn/interrupt params = %s", params)
	}
	repeated, err := adapter.CancelAttempt(context.Background(), cancelRequest)
	if err != nil || repeated != first {
		t.Fatalf("repeated CancelAttempt() = (%#v, %v), want %#v", repeated, err, first)
	}
	changed := cancelRequest
	changed.IdempotencyKey = "cancel-2"
	_, err = adapter.CancelAttempt(context.Background(), changed)
	var conflict *ports.Failure
	if !errors.As(err, &conflict) || conflict.Code != ports.FailureConflict {
		t.Fatalf("conflicting CancelAttempt() error = %#v", err)
	}
	events := collectEvents(t, adapter, handle, nil)
	if !hasEventKind(events, providerport.EventTurnInterrupted) {
		t.Fatalf("events = %#v, want turn interruption", events)
	}
	result := getResult(t, adapter, handle)
	cancelled, ok := result.(providerport.CancelledResult)
	if !ok || cancelled.Recovery.ProviderThreadID != "thread-stop" || cancelled.Recovery.ProviderTurnID != "turn-stop" || cancelled.Recovery.EvidenceRef == "" {
		t.Fatalf("result = %#v", result)
	}
	if err := <-script.done; err != nil {
		t.Fatalf("stop App Server script error = %v", err)
	}
}

func TestAdapterForcesOwnedProcessTerminationAfterCancellationGrace(t *testing.T) {
	script := newStopAppServerScript(false)
	adapter := newStopTestAdapter(t, script)
	handle, err := adapter.StartAttempt(context.Background(), testAttemptRequest(t.TempDir()))
	if err != nil {
		t.Fatalf("StartAttempt() error = %v", err)
	}
	<-script.turnReady
	result, err := adapter.CancelAttempt(context.Background(), providerport.CancelRequest{
		Handle: handle, IdempotencyKey: "cancel-force", GracePeriod: 20 * time.Millisecond,
	})
	if err != nil || result.Disposition != providerport.CancelForced || result.EvidenceRef == "" {
		t.Fatalf("CancelAttempt() = (%#v, %v)", result, err)
	}
	select {
	case <-script.owner.killed:
	default:
		t.Fatal("owned process was not terminated")
	}
	events := collectEvents(t, adapter, handle, nil)
	if !hasEventKind(events, providerport.EventAttemptCancelled) {
		t.Fatalf("events = %#v, want forced cancellation event", events)
	}
	if _, ok := getResult(t, adapter, handle).(providerport.CancelledResult); !ok {
		t.Fatal("forced cancellation did not produce CancelledResult")
	}
	if err := <-script.done; err != nil {
		t.Fatalf("stop App Server script error = %v", err)
	}
}

func TestAdapterTimeoutInterruptsThenClassifiesAForcedStop(t *testing.T) {
	script := newStopAppServerScript(false)
	adapter := newStopTestAdapter(t, script)
	request := testAttemptRequest(t.TempDir())
	request.Timeout = 20 * time.Millisecond
	request.CancellationGrace = 20 * time.Millisecond
	handle, err := adapter.StartAttempt(context.Background(), request)
	if err != nil {
		t.Fatalf("StartAttempt() error = %v", err)
	}
	<-script.turnReady
	result := getResult(t, adapter, handle)
	interrupted, ok := result.(providerport.InterruptedResult)
	if !ok || interrupted.Failure.Code != ports.FailureTimeout || !interrupted.Failure.Retryable {
		t.Fatalf("result = %#v", result)
	}
	if interrupted.Recovery.ProviderThreadID != "thread-stop" || interrupted.Recovery.ProviderTurnID != "turn-stop" ||
		interrupted.Recovery.ProcessOwnerID != "4242" || interrupted.Recovery.EvidenceRef == "" || !interrupted.Recovery.Resumable {
		t.Fatalf("recovery = %#v", interrupted.Recovery)
	}
	select {
	case params := <-script.interruptParams:
		if !strings.Contains(string(params), `"turnId":"turn-stop"`) {
			t.Fatalf("turn/interrupt params = %s", params)
		}
	case <-time.After(time.Second):
		t.Fatal("timeout did not request turn/interrupt")
	}
	select {
	case <-script.owner.killed:
	default:
		t.Fatal("timeout did not terminate the owned process")
	}
	events := collectEvents(t, adapter, handle, nil)
	if !hasEventKind(events, providerport.EventAttemptFailed) {
		t.Fatalf("events = %#v, want timeout failure event", events)
	}
	if err := <-script.done; err != nil {
		t.Fatalf("stop App Server script error = %v", err)
	}
}

func TestInteractionPayloadsMatchDistinctCodexRequestTypes(t *testing.T) {
	t.Parallel()
	context := providerport.InteractionContext{
		AttemptID: "attempt-1", ProviderThreadID: "thread-1", ProviderRequestID: "1",
		IdempotencyKey: "response-1", ScopeDigest: strings.Repeat("a", 64),
	}
	tests := []struct {
		name     string
		response providerport.InteractionResponse
		request  interactionRequest
		wantJSON string
	}{
		{
			name: "command once", response: providerport.PermissionResponse{InteractionContext: context, Decision: providerport.PermissionAllowOnce},
			request:  interactionRequest{checkpoint: providerport.InteractionCheckpoint{Kind: providerport.InteractionCommand}, providerMethod: "item/commandExecution/requestApproval"},
			wantJSON: `{"decision":"accept"}`,
		},
		{
			name: "file session", response: providerport.PermissionResponse{InteractionContext: context, Decision: providerport.PermissionAllowForSession},
			request:  interactionRequest{checkpoint: providerport.InteractionCheckpoint{Kind: providerport.InteractionFile}, providerMethod: "item/fileChange/requestApproval"},
			wantJSON: `{"decision":"acceptForSession"}`,
		},
		{
			name: "legacy command deny", response: providerport.PermissionResponse{InteractionContext: context, Decision: providerport.PermissionDenied},
			request:  interactionRequest{checkpoint: providerport.InteractionCheckpoint{Kind: providerport.InteractionCommand}, providerMethod: "execCommandApproval"},
			wantJSON: `{"decision":{"denied":{"rejection":"rejected by user"}}}`,
		},
		{
			name: "permission once", response: providerport.PermissionResponse{InteractionContext: context, Decision: providerport.PermissionAllowOnce},
			request:  interactionRequest{checkpoint: providerport.InteractionCheckpoint{Kind: providerport.InteractionPermission}, providerMethod: "item/permissions/requestApproval", params: json.RawMessage(`{"permissions":{"network":{"enabled":true},"fileSystem":null}}`)},
			wantJSON: `{"permissions":{"network":{"enabled":true}},"scope":"turn"}`,
		},
		{
			name: "permission deny", response: providerport.PermissionResponse{InteractionContext: context, Decision: providerport.PermissionDenied},
			request:  interactionRequest{checkpoint: providerport.InteractionCheckpoint{Kind: providerport.InteractionPermission}, providerMethod: "item/permissions/requestApproval", params: json.RawMessage(`{"permissions":{"network":{"enabled":true},"fileSystem":null}}`)},
			wantJSON: `{"permissions":{},"scope":"turn"}`,
		},
		{
			name: "user answer", response: providerport.AnswerResponse{InteractionContext: context, Answer: json.RawMessage(`{"answers":{"choice":{"answers":["yes"]}}}`)},
			request:  interactionRequest{checkpoint: providerport.InteractionCheckpoint{Kind: providerport.InteractionUser}, providerMethod: "item/tool/requestUserInput"},
			wantJSON: `{"answers":{"choice":{"answers":["yes"]}}}`,
		},
		{
			name: "tool result", response: providerport.AnswerResponse{InteractionContext: context, Answer: json.RawMessage(`{"contentItems":[],"success":true}`)},
			request:  interactionRequest{checkpoint: providerport.InteractionCheckpoint{Kind: providerport.InteractionTool}, providerMethod: "item/tool/call"},
			wantJSON: `{"contentItems":[],"success":true}`,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			payload, err := interactionPayload(test.response, test.request)
			if err != nil {
				t.Fatalf("interactionPayload() error = %v", err)
			}
			encoded, err := json.Marshal(payload)
			if err != nil {
				t.Fatalf("marshal payload: %v", err)
			}
			var gotValue, wantValue any
			if json.Unmarshal(encoded, &gotValue) != nil || json.Unmarshal([]byte(test.wantJSON), &wantValue) != nil ||
				!reflect.DeepEqual(gotValue, wantValue) {
				t.Fatalf("payload = %s, want %s", encoded, test.wantJSON)
			}
		})
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

func newResumeTestAdapter(t *testing.T, script *resumeAppServerScript) *Adapter {
	t.Helper()
	recorder, err := NewDirectoryEvidenceRecorder(t.TempDir())
	if err != nil {
		t.Fatalf("NewDirectoryEvidenceRecorder() error = %v", err)
	}
	adapter, err := NewAdapter(AdapterOptions{
		Factory: script.factory, EvidenceRecorder: recorder,
		Clock: func() time.Time { return time.Date(2026, time.September, 1, 12, 0, 0, 0, time.UTC) },
	})
	if err != nil {
		t.Fatalf("NewAdapter() error = %v", err)
	}
	return adapter
}

func newStopTestAdapter(t *testing.T, script *stopAppServerScript) *Adapter {
	t.Helper()
	recorder, err := NewDirectoryEvidenceRecorder(t.TempDir())
	if err != nil {
		t.Fatalf("NewDirectoryEvidenceRecorder() error = %v", err)
	}
	adapter, err := NewAdapter(AdapterOptions{
		Factory: script.factory, EvidenceRecorder: recorder,
		Clock: func() time.Time { return time.Date(2026, time.September, 1, 12, 0, 0, 0, time.UTC) },
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
