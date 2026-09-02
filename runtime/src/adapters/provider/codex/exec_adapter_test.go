package codex

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"darkstar/src/ports"
	providerport "darkstar/src/ports/provider"
)

type fixtureExecProcess struct {
	stdout io.Reader
	stderr io.Reader
	wait   error
	pid    int
	mu     sync.Mutex
	killed bool
}

func (process *fixtureExecProcess) Stdout() io.Reader { return process.stdout }
func (process *fixtureExecProcess) Stderr() io.Reader { return process.stderr }
func (process *fixtureExecProcess) PID() int          { return process.pid }
func (process *fixtureExecProcess) Wait() error       { return process.wait }
func (process *fixtureExecProcess) Kill() error {
	process.mu.Lock()
	process.killed = true
	process.mu.Unlock()
	return nil
}

func TestExecAdapterStreamsValidatesAndRecordsSelection(t *testing.T) {
	request := testAttemptRequest(t.TempDir())
	request.Timeout = time.Second
	var captured ExecCommand
	adapter := newExecTestAdapter(t, NewMemoryExecRecoveryStore(), func(command ExecCommand) (ExecProcess, error) {
		captured = command
		schemaIndex := argumentIndex(command.Arguments, "--output-schema")
		if schemaIndex < 0 || schemaIndex+1 == len(command.Arguments) {
			t.Fatal("exec command omitted --output-schema")
		}
		if _, err := os.Stat(command.Arguments[schemaIndex+1]); err != nil {
			t.Fatalf("schema was not readable when process started: %v", err)
		}
		return execFixtureProcess(execSuccessFixture("session-1", `{"answer":"done"}`)), nil
	})

	handle, err := adapter.StartAttempt(context.Background(), request)
	if err != nil {
		t.Fatalf("StartAttempt() error = %v", err)
	}
	if handle.ProviderThreadID != "session-1" || handle.ProviderTurnID != execSyntheticTurnID || handle.ProcessOwnerID != "4242" {
		t.Fatalf("handle = %#v", handle)
	}
	wantPrefix := []string{"exec", "--json", "--skip-git-repo-check", "--sandbox", "read-only", "--config", `approval_policy="never"`, "--output-schema"}
	if len(captured.Arguments) < len(wantPrefix) || !reflect.DeepEqual(captured.Arguments[:len(wantPrefix)], wantPrefix) {
		t.Fatalf("arguments = %#v, want prefix %#v", captured.Arguments, wantPrefix)
	}
	if captured.Directory != request.Workspace || !strings.Contains(captured.Arguments[len(captured.Arguments)-1], "Prepared immutable context") {
		t.Fatalf("command did not retain bounded workspace and prepared input: %#v", captured)
	}

	events := collectExecEvents(t, adapter, handle)
	var kinds []providerport.EventKind
	for _, event := range events {
		kinds = append(kinds, event.Kind)
		if event.ProviderThreadID != "session-1" || event.ProviderTurnID != execSyntheticTurnID {
			t.Fatalf("event lost exec identity: %#v", event)
		}
	}
	wantKinds := []providerport.EventKind{
		providerport.EventAttemptStarted, providerport.EventTurnStarted, providerport.EventMessageCompleted,
		providerport.EventTurnCompleted, providerport.EventStructuredOutputCompleted,
	}
	if !reflect.DeepEqual(kinds, wantKinds) {
		t.Fatalf("event kinds = %v, want %v", kinds, wantKinds)
	}
	result, err := adapter.GetResult(context.Background(), providerport.ResultRequest{Handle: handle})
	if err != nil {
		t.Fatalf("GetResult() error = %v", err)
	}
	succeeded, ok := result.(providerport.SucceededResult)
	if !ok || string(succeeded.StructuredOutput) != `{"answer":"done"}` {
		t.Fatalf("result = %#v", result)
	}
	if succeeded.Usage.InputTokens != 12 || succeeded.Usage.CachedTokens != 3 || succeeded.Usage.OutputTokens != 4 || !succeeded.Recovery.Resumable {
		t.Fatalf("metadata = %#v", succeeded.AttemptResultMetadata)
	}
	foundSelection := false
	for _, evidence := range succeeded.WorkspaceEvidence {
		if strings.Contains(evidence.Ref, "transport-selection") {
			foundSelection = true
		}
	}
	if !foundSelection {
		t.Fatal("result omitted durable fallback transport-selection evidence")
	}
}

func TestExecAdapterReplaysReviewedWindowsFixture(t *testing.T) {
	fixture := loadExecFixture(t, "exec-read-only.jsonl")
	request := testAttemptRequest(t.TempDir())
	request.Timeout = time.Second
	request.OutputSchema = json.RawMessage(`{
		"type":"object","additionalProperties":false,
		"properties":{"probe":{"const":"exec-read-only"},"success":{"const":true},"detail":{"type":"string"}},
		"required":["probe","success","detail"]
	}`)
	adapter := newExecTestAdapter(t, NewMemoryExecRecoveryStore(), func(ExecCommand) (ExecProcess, error) {
		return execFixtureProcess(fixture), nil
	})
	handle, err := adapter.StartAttempt(context.Background(), request)
	if err != nil {
		t.Fatalf("StartAttempt() error = %v", err)
	}
	result, err := adapter.GetResult(context.Background(), providerport.ResultRequest{Handle: handle})
	if err != nil {
		t.Fatalf("GetResult() error = %v", err)
	}
	succeeded, ok := result.(providerport.SucceededResult)
	if !ok || succeeded.Recovery.ProviderThreadID != "01a053c7-a9c5-7032-bb84-d9051d72076d" || succeeded.Usage.InputTokens != 31539 {
		t.Fatalf("reviewed fixture result = %#v", result)
	}
}

func TestExecAdapterRejectsUnboundedInteractiveAndUnreviewedAttempts(t *testing.T) {
	request := testAttemptRequest(t.TempDir())
	adapter := newExecTestAdapter(t, nil, func(ExecCommand) (ExecProcess, error) {
		t.Fatal("ineligible request started a process")
		return nil, nil
	})

	for name, mutate := range map[string]func(*providerport.AttemptRequest){
		"unbounded": func(value *providerport.AttemptRequest) { value.Timeout = 0 },
		"interactive": func(value *providerport.AttemptRequest) {
			value.Timeout = time.Second
			value.CommandPolicy = providerport.InteractionAsk
		},
		"write": func(value *providerport.AttemptRequest) {
			value.Timeout = time.Second
			value.Access = providerport.AccessWorkspaceWrite
		},
	} {
		t.Run(name, func(t *testing.T) {
			candidate := request
			mutate(&candidate)
			if _, err := adapter.StartAttempt(context.Background(), candidate); err == nil {
				t.Fatal("StartAttempt() accepted an ineligible exec fallback")
			}
		})
	}

	recorder, err := NewDirectoryEvidenceRecorder(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	unreviewed, err := NewExecAdapter(ExecAdapterOptions{
		ProviderVersion: "9.9.9", Factory: func(ExecCommand) (ExecProcess, error) { return execFixtureProcess(""), nil }, EvidenceRecorder: recorder,
		RecoveryStore: NewMemoryExecRecoveryStore(),
	})
	if err != nil {
		t.Fatal(err)
	}
	request.Timeout = time.Second
	_, err = unreviewed.StartAttempt(context.Background(), request)
	var failure *ports.Failure
	if !errors.As(err, &failure) || failure.Code != ports.FailureProtocolDrift {
		t.Fatalf("unreviewed version error = %#v", err)
	}
}

func TestExecAdapterCoalescesConcurrentIdempotentStart(t *testing.T) {
	request := testAttemptRequest(t.TempDir())
	request.Timeout = time.Second
	reader, writer := io.Pipe()
	factoryCalled := make(chan struct{})
	var factoryMu sync.Mutex
	factoryCalls := 0
	adapter := newExecTestAdapter(t, NewMemoryExecRecoveryStore(), func(ExecCommand) (ExecProcess, error) {
		factoryMu.Lock()
		factoryCalls++
		factoryMu.Unlock()
		close(factoryCalled)
		return &fixtureExecProcess{stdout: reader, stderr: bytes.NewReader(nil), pid: 4242}, nil
	})
	type outcome struct {
		handle providerport.AttemptHandle
		err    error
	}
	first := make(chan outcome, 1)
	second := make(chan outcome, 1)
	go func() {
		handle, err := adapter.StartAttempt(context.Background(), request)
		first <- outcome{handle: handle, err: err}
	}()
	<-factoryCalled
	go func() {
		handle, err := adapter.StartAttempt(context.Background(), request)
		second <- outcome{handle: handle, err: err}
	}()
	_, _ = io.WriteString(writer, execSuccessFixture("session-coalesced", `{"answer":"done"}`))
	_ = writer.Close()
	left, right := <-first, <-second
	if left.err != nil || right.err != nil || !reflect.DeepEqual(left.handle, right.handle) {
		t.Fatalf("coalesced starts = (%#v, %v), (%#v, %v)", left.handle, left.err, right.handle, right.err)
	}
	factoryMu.Lock()
	defer factoryMu.Unlock()
	if factoryCalls != 1 {
		t.Fatalf("factory calls = %d, want 1", factoryCalls)
	}
}

func TestExecAdapterResumesFromDurableRequestMaterial(t *testing.T) {
	store := NewMemoryExecRecoveryStore()
	request := testAttemptRequest(t.TempDir())
	request.Timeout = time.Second
	first := newExecTestAdapter(t, store, func(ExecCommand) (ExecProcess, error) {
		return execFixtureProcess(execSuccessFixture("session-resume", `{"answer":"first"}`)), nil
	})
	handle, err := first.StartAttempt(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	result, err := first.GetResult(context.Background(), providerport.ResultRequest{Handle: handle})
	if err != nil {
		t.Fatal(err)
	}
	last := result.(providerport.SucceededResult).Recovery.LastSequence

	var resumedCommand ExecCommand
	second := newExecTestAdapter(t, store, func(command ExecCommand) (ExecProcess, error) {
		resumedCommand = command
		return execFixtureProcess(execSuccessFixture("session-resume", `{"answer":"resumed"}`)), nil
	})
	resumed, err := second.ResumeAttempt(context.Background(), providerport.ResumeRequest{
		AttemptID: request.AttemptID, IdempotencyKey: "resume-1", ProviderThreadID: "session-resume",
		ProviderTurnID: execSyntheticTurnID, LastSequence: last,
	})
	if err != nil {
		t.Fatalf("ResumeAttempt() error = %v", err)
	}
	wantPrefix := []string{"exec", "resume", "--json", "--skip-git-repo-check", "--output-schema"}
	if len(resumedCommand.Arguments) < len(wantPrefix) || !reflect.DeepEqual(resumedCommand.Arguments[:len(wantPrefix)], wantPrefix) {
		t.Fatalf("resume arguments = %#v", resumedCommand.Arguments)
	}
	if resumedCommand.Arguments[len(resumedCommand.Arguments)-2] != "session-resume" {
		t.Fatalf("resume changed session selection: %#v", resumedCommand.Arguments)
	}
	events := collectExecEvents(t, second, resumed)
	if len(events) == 0 || events[0].Sequence <= last {
		t.Fatalf("resumed events did not continue durable sequence %d: %#v", last, events)
	}
	resumedResult, err := second.GetResult(context.Background(), providerport.ResultRequest{Handle: resumed})
	if err != nil {
		t.Fatal(err)
	}
	if got := string(resumedResult.(providerport.SucceededResult).StructuredOutput); got != `{"answer":"resumed"}` {
		t.Fatalf("resumed output = %s", got)
	}
}

func TestExecExitClassification(t *testing.T) {
	tests := []struct {
		stderr string
		want   ports.FailureCode
	}{
		{"error: unexpected argument '--ask-for-approval' found\nUsage: codex exec", ports.FailureProtocolDrift},
		{"not logged in; authentication required", ports.FailureUnauthenticated},
		{"rate limit exceeded", ports.FailureResourceExhausted},
		{"Access is denied", ports.FailurePermissionDenied},
		{"provider connection closed", ports.FailureUnavailable},
	}
	for _, test := range tests {
		if got := classifyExecExit(errors.New("exit"), test.stderr); got.Code != test.want {
			t.Errorf("classifyExecExit(%q) = %s, want %s", test.stderr, got.Code, test.want)
		}
	}
}

func TestDirectoryExecRecoveryStorePersistsImmutableRecord(t *testing.T) {
	store, err := NewDirectoryExecRecoveryStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	record := ExecRecoveryRecord{
		AttemptID: "attempt/with:path", Workspace: t.TempDir(), Prompt: "prepared",
		OutputSchema: json.RawMessage(`{"type":"object"}`),
	}
	if err := store.SaveExecRecovery(context.Background(), record); err != nil {
		t.Fatalf("SaveExecRecovery() error = %v", err)
	}
	if err := store.SaveExecRecovery(context.Background(), record); err != nil {
		t.Fatalf("idempotent SaveExecRecovery() error = %v", err)
	}
	loaded, err := store.LoadExecRecovery(context.Background(), record.AttemptID)
	if err != nil {
		t.Fatalf("LoadExecRecovery() error = %v", err)
	}
	if loaded.AttemptID != record.AttemptID || loaded.Workspace != record.Workspace || loaded.Prompt != record.Prompt || string(loaded.OutputSchema) != string(record.OutputSchema) {
		t.Fatalf("loaded record = %#v, want %#v", loaded, record)
	}
	record.Prompt = "changed"
	if err := store.SaveExecRecovery(context.Background(), record); err == nil {
		t.Fatal("SaveExecRecovery() replaced immutable recovery material")
	}
}

func newExecTestAdapter(t *testing.T, store ExecRecoveryStore, factory ExecProcessFactory) *ExecAdapter {
	t.Helper()
	if store == nil {
		store = NewMemoryExecRecoveryStore()
	}
	recorder, err := NewDirectoryEvidenceRecorder(t.TempDir())
	if err != nil {
		t.Fatalf("NewDirectoryEvidenceRecorder() error = %v", err)
	}
	adapter, err := NewExecAdapter(ExecAdapterOptions{
		Executable: "codex.exe", ProviderVersion: "0.151.0-alpha.7.2", Factory: factory,
		EvidenceRecorder: recorder, RecoveryStore: store,
		Clock: func() time.Time { return time.Date(2026, time.September, 1, 12, 0, 0, 0, time.UTC) },
	})
	if err != nil {
		t.Fatalf("NewExecAdapter() error = %v", err)
	}
	return adapter
}

func execFixtureProcess(output string) *fixtureExecProcess {
	return &fixtureExecProcess{stdout: strings.NewReader(output), stderr: bytes.NewReader(nil), pid: 4242}
}

func execSuccessFixture(sessionID, output string) string {
	frames := []any{
		map[string]any{"type": "thread.started", "thread_id": sessionID},
		map[string]any{"type": "turn.started"},
		map[string]any{"type": "item.completed", "item": map[string]any{"id": "item-1", "type": "agent_message", "text": output}},
		map[string]any{"type": "turn.completed", "usage": map[string]any{"input_tokens": 12, "cached_input_tokens": 3, "output_tokens": 4}},
	}
	var builder strings.Builder
	encoder := json.NewEncoder(&builder)
	for _, frame := range frames {
		_ = encoder.Encode(frame)
	}
	return builder.String()
}

func collectExecEvents(t *testing.T, adapter *ExecAdapter, handle providerport.AttemptHandle) []providerport.Event {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
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
		events = append(events, event)
	}
}

func argumentIndex(arguments []string, value string) int {
	for index, argument := range arguments {
		if argument == value {
			return index
		}
	}
	return -1
}

func loadExecFixture(t *testing.T, name string) string {
	t.Helper()
	workingDirectory, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
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
	file, err := os.Open(filepath.Join(filepath.Dir(root), "probes", "codex-host", "fixtures", "0.151.0-alpha.7.2", name))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = file.Close() }()
	var output strings.Builder
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		var envelope struct {
			Message json.RawMessage `json:"message"`
		}
		if err := json.Unmarshal(scanner.Bytes(), &envelope); err != nil {
			t.Fatalf("decode fixture envelope: %v", err)
		}
		output.Write(envelope.Message)
		output.WriteByte('\n')
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	return output.String()
}
