package command

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"darkstar/src/core/attemptexecution"
	"darkstar/src/ports"
	executorport "darkstar/src/ports/executor"
)

func TestAdapterRunsExactArgumentArrayWithAllowlistedContext(t *testing.T) {
	t.Parallel()
	workspace := t.TempDir()
	if err := os.Mkdir(filepath.Join(workspace, "nested"), 0o700); err != nil {
		t.Fatal(err)
	}
	recorder := &memoryRecorder{}
	adapter := newHelperAdapter(t, Definition{
		Argv: helperArguments("echo", "literal; Write-Output injected", "$(not-expanded)", "two words"),
		CWD:  "nested", Environment: []string{"DARKSTAR_COMMAND_HELPER"},
	}, recorder, Policy{})

	execution, err := adapter.Start(context.Background(), executorport.Request{
		AttemptID: "attempt-1", Workspace: workspace, Timeout: 5 * time.Second,
	})
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	events := receiveAll(t, execution.Events())
	result, err := execution.Wait(context.Background())
	if err != nil {
		t.Fatalf("Wait() error = %v", err)
	}
	var output helperOutput
	if err := json.Unmarshal(result.CandidateOutput, &output); err != nil {
		t.Fatalf("candidate output is not JSON: %v", err)
	}
	wantArguments := []string{"literal; Write-Output injected", "$(not-expanded)", "two words"}
	if !reflect.DeepEqual(output.Arguments, wantArguments) {
		t.Fatalf("arguments = %#v, want %#v", output.Arguments, wantArguments)
	}
	if filepath.Base(output.Directory) != "nested" || output.HelperEnvironment != "1" {
		t.Fatalf("helper context = %#v", output)
	}
	if output.InheritedSecret != "" {
		t.Fatalf("inherited environment leaked: %q", output.InheritedSecret)
	}
	if output.InheritedPath != "" {
		t.Fatalf("PATH was inherited instead of allowlisted: %q", output.InheritedPath)
	}
	if len(events) != 2 || events[0].Kind != "command.started" || events[1].Kind != "command.completed" || events[1].EvidenceRef == "" {
		t.Fatalf("events = %#v", events)
	}
	if len(result.Evidence) != 1 || len(recorder.records) != 1 || result.Evidence[0].Digest == "" {
		t.Fatalf("result evidence = %#v, records = %d", result.Evidence, len(recorder.records))
	}
	var evidenceDocument map[string]any
	if err := json.Unmarshal(recorder.records[0].Data, &evidenceDocument); err != nil {
		t.Fatalf("decode evidence: %v", err)
	}
	if _, exists := evidenceDocument["arguments"]; exists || evidenceDocument["argumentDigest"] == "" {
		t.Fatal("evidence stored raw command metadata instead of an argument digest")
	}
}

func TestPolicyFailsClosedForExecutableDirectoryAndEnvironment(t *testing.T) {
	t.Parallel()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	base := Policy{
		Executables: map[string]string{"helper": executable}, WorkingDirectories: []string{"."},
		Environment: map[string]string{"ALLOWED": "value"},
	}
	tests := []struct {
		name       string
		definition Definition
		policy     Policy
	}{
		{name: "unknown executable", definition: Definition{Argv: []string{"other"}}, policy: base},
		{name: "escaping directory", definition: Definition{Argv: []string{"helper"}, CWD: ".."}, policy: base},
		{name: "unlisted directory", definition: Definition{Argv: []string{"helper"}, CWD: "nested"}, policy: base},
		{name: "unlisted environment", definition: Definition{Argv: []string{"helper"}, Environment: []string{"SECRET"}}, policy: base},
		{name: "relative executable", definition: Definition{Argv: []string{"helper"}}, policy: Policy{Executables: map[string]string{"helper": "go"}, WorkingDirectories: []string{"."}}},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if _, err := New(test.definition, test.policy, &memoryRecorder{}); err == nil {
				t.Fatal("New() error = nil")
			}
		})
	}
}

func TestAdapterClassifiesNonzeroExitTimeoutAndOutputLimit(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		mode    string
		value   string
		timeout time.Duration
		limit   int
		class   Classification
		code    ports.FailureCode
	}{
		{name: "nonzero", mode: "fail", value: "23", timeout: 5 * time.Second, class: ClassificationFailed, code: ports.FailureInvalidRequest},
		{name: "timeout", mode: "sleep", value: "5s", timeout: 50 * time.Millisecond, class: ClassificationTimedOut, code: ports.FailureTimeout},
		{name: "output limit", mode: "output", value: "4096", timeout: 5 * time.Second, limit: 128, class: ClassificationOutputLimited, code: ports.FailureResourceExhausted},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			recorder := &memoryRecorder{}
			adapter := newHelperAdapter(t, Definition{
				Argv: helperArguments(test.mode, test.value), Environment: []string{"DARKSTAR_COMMAND_HELPER"},
			}, recorder, Policy{OutputLimitBytes: test.limit})
			execution, err := adapter.Start(context.Background(), executorport.Request{
				AttemptID: "attempt-classification", Workspace: t.TempDir(), Timeout: test.timeout,
			})
			if err != nil {
				t.Fatal(err)
			}
			_ = receiveAll(t, execution.Events())
			_, err = execution.Wait(context.Background())
			var executionErr *ExecutionError
			if !errors.As(err, &executionErr) || executionErr.Completion.Classification() != test.class {
				t.Fatalf("Wait() error = %#v, want class %q", err, test.class)
			}
			var portFailure *ports.Failure
			if !errors.As(err, &portFailure) || portFailure.Code != test.code {
				t.Fatalf("port failure = %#v, want %q", portFailure, test.code)
			}
			if executionErr.Evidence.Ref == "" || len(recorder.records) != 1 {
				t.Fatalf("failure evidence = %#v, records = %d", executionErr.Evidence, len(recorder.records))
			}
			if test.class == ClassificationFailed {
				failed, ok := executionErr.Completion.(Failed)
				if !ok || failed.ExitCode != 23 {
					t.Fatalf("completion = %#v", executionErr.Completion)
				}
			}
		})
	}
}

func TestAdapterCancelsOwnedProcessIdempotently(t *testing.T) {
	t.Parallel()
	adapter := newHelperAdapter(t, Definition{
		Argv: helperArguments("sleep", "5s"), Environment: []string{"DARKSTAR_COMMAND_HELPER"},
	}, &memoryRecorder{}, Policy{})
	execution, err := adapter.Start(context.Background(), executorport.Request{
		AttemptID: "attempt-cancel", Workspace: t.TempDir(), Timeout: 10 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	request := executorport.CancelRequest{IdempotencyKey: "cancel-1", GracePeriod: 100 * time.Millisecond}
	first, err := execution.Cancel(context.Background(), request)
	if err != nil {
		t.Fatalf("Cancel() error = %v", err)
	}
	second, err := execution.Cancel(context.Background(), request)
	if err != nil || second != first {
		t.Fatalf("repeat Cancel() = %#v, %v; want %#v", second, err, first)
	}
	if first.Disposition != executorport.CancelForced && first.Disposition != executorport.CancelGraceful {
		t.Fatalf("cancel disposition = %q", first.Disposition)
	}
	_ = receiveAll(t, execution.Events())
	_, err = execution.Wait(context.Background())
	var executionErr *ExecutionError
	if !errors.As(err, &executionErr) || executionErr.Completion.Classification() != ClassificationCancelled {
		t.Fatalf("Wait() error = %#v", err)
	}
}

func TestTimeoutTerminatesOwnedProcessTree(t *testing.T) {
	t.Parallel()
	workspace := t.TempDir()
	marker := filepath.Join(workspace, "escaped-child.txt")
	adapter := newHelperAdapter(t, Definition{
		Argv: helperArguments("spawn", marker), Environment: []string{"DARKSTAR_COMMAND_HELPER"},
	}, &memoryRecorder{}, Policy{})
	execution, err := adapter.Start(context.Background(), executorport.Request{
		AttemptID: "attempt-tree-timeout", Workspace: workspace, Timeout: 100 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	_ = receiveAll(t, execution.Events())
	_, err = execution.Wait(context.Background())
	var executionErr *ExecutionError
	if !errors.As(err, &executionErr) || executionErr.Completion.Classification() != ClassificationTimedOut {
		t.Fatalf("Wait() error = %#v", err)
	}
	time.Sleep(750 * time.Millisecond)
	if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("timed-out child escaped the owned process tree: %v", err)
	}
}

func TestValidatorRunsCommandsInOrderAndReturnsEvidence(t *testing.T) {
	t.Parallel()
	recorder := &memoryRecorder{}
	definitions := []Definition{
		{Argv: helperArguments("validate", "ok"), Environment: []string{"DARKSTAR_COMMAND_HELPER"}},
		{Argv: helperArguments("validate", "ok"), Environment: []string{"DARKSTAR_COMMAND_HELPER"}},
	}
	policy := helperPolicy(t, Policy{})
	validator, err := NewValidator(definitions, policy, recorder, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	result, err := validator.Validate(context.Background(), attemptexecution.ValidationRequest{
		AttemptID: "attempt-validation", Workspace: t.TempDir(),
		Result: executorport.Result{CandidateOutput: json.RawMessage(`{"ok":true}`)},
	})
	if err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
	if len(result.Evidence) != 2 || len(recorder.records) != 2 {
		t.Fatalf("validation evidence = %d, records = %d", len(result.Evidence), len(recorder.records))
	}
}

func TestDirectoryEvidenceRecorderIsContentAddressedAndIdempotent(t *testing.T) {
	t.Parallel()
	recorder, err := NewDirectoryEvidenceRecorder(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	record := EvidenceRecord{AttemptID: "attempt/1", Kind: "command-execution", MediaType: "application/json", Data: []byte(`{"ok":true}`)}
	first, err := recorder.Record(context.Background(), record)
	if err != nil {
		t.Fatal(err)
	}
	second, err := recorder.Record(context.Background(), record)
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Fatalf("repeat evidence = %#v, want %#v", second, first)
	}
	stored, err := os.ReadFile(first.Ref)
	if err != nil || !reflect.DeepEqual(stored, record.Data) {
		t.Fatalf("stored evidence = %q, %v", stored, err)
	}
}

func newHelperAdapter(t *testing.T, definition Definition, recorder EvidenceRecorder, override Policy) *Adapter {
	t.Helper()
	policy := helperPolicy(t, override)
	adapter, err := New(definition, policy, recorder)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	return adapter
}

func helperPolicy(t *testing.T, override Policy) Policy {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	policy := Policy{
		Executables: map[string]string{"helper": executable}, WorkingDirectories: []string{".", "nested"},
		Environment: map[string]string{"DARKSTAR_COMMAND_HELPER": "1"}, OutputLimitBytes: override.OutputLimitBytes,
	}
	return policy
}

func helperArguments(mode string, arguments ...string) []string {
	result := []string{"helper", "-test.run=^TestCommandHelper$", "--", mode}
	return append(result, arguments...)
}

func receiveAll(t *testing.T, stream executorport.Events) []executorport.Event {
	t.Helper()
	var result []executorport.Event
	for {
		event, err := stream.Receive()
		if errors.Is(err, io.EOF) {
			return result
		}
		if err != nil {
			t.Fatalf("Receive() error = %v", err)
		}
		result = append(result, event)
	}
}

type memoryRecorder struct {
	mu      sync.Mutex
	records []EvidenceRecord
}

func (recorder *memoryRecorder) Record(_ context.Context, record EvidenceRecord) (executorport.Evidence, error) {
	recorder.mu.Lock()
	defer recorder.mu.Unlock()
	record.Data = append([]byte(nil), record.Data...)
	recorder.records = append(recorder.records, record)
	digestBytes := sha256.Sum256(record.Data)
	digest := hex.EncodeToString(digestBytes[:])
	return executorport.Evidence{Kind: record.Kind, Ref: fmt.Sprintf("memory://%d", len(recorder.records)), Digest: digest}, nil
}

type helperOutput struct {
	Arguments         []string `json:"arguments"`
	Directory         string   `json:"directory"`
	HelperEnvironment string   `json:"helperEnvironment"`
	InheritedSecret   string   `json:"inheritedSecret"`
	InheritedPath     string   `json:"inheritedPath"`
}

func TestCommandHelper(t *testing.T) {
	if os.Getenv("DARKSTAR_COMMAND_HELPER") != "1" {
		return
	}
	separator := -1
	for index, argument := range os.Args {
		if argument == "--" {
			separator = index
			break
		}
	}
	if separator < 0 || separator+1 >= len(os.Args) {
		os.Exit(97)
	}
	arguments := os.Args[separator+1:]
	switch arguments[0] {
	case "echo":
		directory, _ := os.Getwd()
		_ = json.NewEncoder(os.Stdout).Encode(helperOutput{
			Arguments: arguments[1:], Directory: directory, HelperEnvironment: os.Getenv("DARKSTAR_COMMAND_HELPER"),
			InheritedSecret: os.Getenv("DARKSTAR_UNLISTED_SECRET"), InheritedPath: os.Getenv("PATH"),
		})
	case "fail":
		var code int
		_, _ = fmt.Sscan(arguments[1], &code)
		os.Exit(code)
	case "sleep":
		duration, _ := time.ParseDuration(arguments[1])
		time.Sleep(duration)
		_, _ = os.Stdout.WriteString(`{"done":true}`)
	case "output":
		var count int
		_, _ = fmt.Sscan(arguments[1], &count)
		_, _ = os.Stdout.WriteString(strings.Repeat("x", count))
	case "spawn":
		child := exec.Command(os.Args[0], "-test.run=^TestCommandHelper$", "--", "mark", arguments[1])
		child.Env = os.Environ()
		if child.Start() != nil {
			os.Exit(96)
		}
		time.Sleep(5 * time.Second)
	case "mark":
		time.Sleep(400 * time.Millisecond)
		if os.WriteFile(arguments[1], []byte("escaped"), 0o600) != nil {
			os.Exit(95)
		}
	case "validate":
		var candidate map[string]any
		if json.NewDecoder(os.Stdin).Decode(&candidate) != nil || candidate[arguments[1]] != true {
			os.Exit(9)
		}
		_, _ = os.Stdout.WriteString(`{"valid":true}`)
	default:
		os.Exit(98)
	}
	os.Exit(0)
}
