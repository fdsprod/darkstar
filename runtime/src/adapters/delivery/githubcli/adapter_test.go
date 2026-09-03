package githubcli

import (
	"context"
	"errors"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"darkstar/src/ports"
	"darkstar/src/ports/delivery"
)

func TestNewPinsResolvedGitHubCLIExecutable(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	runner := &fakeRunner{resolvedByName: map[string]string{
		"configured-gh":  filepath.Join(root, "tools", "gh.exe"),
		"configured-git": filepath.Join(root, "tools", "git.exe"),
	}}
	adapter, err := New(Options{Executable: "configured-gh", GitExecutable: "configured-git", Runner: runner})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(runner.lookedUp, []string{"configured-gh", "configured-git"}) {
		t.Fatalf("looked up %#v", runner.lookedUp)
	}
	if adapter.executable != filepath.Clean(runner.resolvedByName["configured-gh"]) || adapter.gitExecutable != filepath.Clean(runner.resolvedByName["configured-git"]) {
		t.Fatalf("executables = %q, %q", adapter.executable, adapter.gitExecutable)
	}
}

func TestNewRejectsMissingGitHubCLI(t *testing.T) {
	t.Parallel()
	for _, runner := range []*fakeRunner{
		{lookPathErr: exec.ErrNotFound},
		{resolved: ""},
	} {
		_, err := New(Options{Runner: runner})
		assertFailureCode(t, err, ports.FailureUnavailable)
	}
}

func TestRunUsesExactArgumentsAndDefensiveCopies(t *testing.T) {
	t.Parallel()
	runner := &fakeRunner{resolved: filepath.Join(t.TempDir(), "gh.exe"), stdout: []byte(`{"name":"repo"}`)}
	adapter, err := New(Options{Runner: runner})
	if err != nil {
		t.Fatal(err)
	}
	arguments := []string{"api", "repos/example/repo", "--method", "GET"}
	input := []byte("request")
	output, err := adapter.run(context.Background(), arguments, input)
	if err != nil {
		t.Fatal(err)
	}
	arguments[0] = "mutated"
	input[0] = 'X'
	output[0] = 'X'
	if !reflect.DeepEqual(runner.arguments, []string{"api", "repos/example/repo", "--method", "GET"}) {
		t.Fatalf("arguments = %#v", runner.arguments)
	}
	if string(runner.input) != "request" {
		t.Fatalf("input = %q", runner.input)
	}
	if string(runner.stdout) != `{"name":"repo"}` {
		t.Fatalf("runner output was aliased: %q", runner.stdout)
	}
}

func TestRunNormalizesProcessFailures(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		ctx  func() context.Context
		err  error
		code ports.FailureCode
	}{
		{name: "missing executable", ctx: context.Background, err: exec.ErrNotFound, code: ports.FailureUnavailable},
		{name: "uncertain exit", ctx: context.Background, err: errors.New("provider detail that must not cross the port"), code: ports.FailureUncertain},
		{name: "cancelled", ctx: cancelledContext, err: context.Canceled, code: ports.FailureCancelled},
		{name: "deadline", ctx: expiredContext, err: context.DeadlineExceeded, code: ports.FailureTimeout},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			runner := &fakeRunner{resolved: filepath.Join(t.TempDir(), "gh.exe"), runErr: test.err}
			adapter, err := New(Options{Runner: runner})
			if err != nil {
				t.Fatal(err)
			}
			_, err = adapter.run(test.ctx(), []string{"api"}, nil)
			assertFailureCode(t, err, test.code)
			if test.code == ports.FailureUncertain && err.Error() == test.err.Error() {
				t.Fatal("raw provider error crossed the adapter boundary")
			}
		})
	}
}

func TestRunRejectsOversizedOutput(t *testing.T) {
	t.Parallel()
	runner := &fakeRunner{resolved: filepath.Join(t.TempDir(), "gh.exe"), stdout: make([]byte, maxOutputBytes+1)}
	adapter, err := New(Options{Runner: runner})
	if err != nil {
		t.Fatal(err)
	}
	_, err = adapter.run(context.Background(), []string{"api"}, nil)
	assertFailureCode(t, err, ports.FailureResourceExhausted)
}

func TestChangeRequestCapabilitiesRejectEmptyRequests(t *testing.T) {
	t.Parallel()
	adapter, err := New(Options{Runner: &fakeRunner{resolved: filepath.Join(t.TempDir(), "gh.exe")}})
	if err != nil {
		t.Fatal(err)
	}
	operations := []func() error{
		func() error {
			_, err := adapter.UpdateChangeRequest(context.Background(), delivery.UpdateChangeRequestRequest{})
			return err
		},
		func() error {
			_, err := adapter.ObserveChangeRequest(context.Background(), delivery.ObserveChangeRequestRequest{})
			return err
		},
	}
	for index, operation := range operations {
		if err := operation(); err == nil {
			t.Fatalf("operation %d unexpectedly succeeded", index)
		} else {
			assertFailureCode(t, err, ports.FailureInvalidRequest)
		}
	}
}

type fakeRunner struct {
	resolved       string
	resolvedByName map[string]string
	lookedUp       []string
	arguments      []string
	input          []byte
	stdout         []byte
	stderr         []byte
	lookPathErr    error
	runErr         error
	calls          []commandCall
	responses      []commandResponse
}

func (runner *fakeRunner) LookPath(name string) (string, error) {
	runner.lookedUp = append(runner.lookedUp, name)
	if resolved, ok := runner.resolvedByName[name]; ok {
		return resolved, runner.lookPathErr
	}
	return runner.resolved, runner.lookPathErr
}

func (runner *fakeRunner) Run(_ context.Context, executable string, arguments []string, input []byte) ([]byte, []byte, error) {
	runner.arguments = append([]string(nil), arguments...)
	runner.input = append([]byte(nil), input...)
	runner.calls = append(runner.calls, commandCall{executable: executable, arguments: append([]string(nil), arguments...), input: append([]byte(nil), input...)})
	if len(runner.responses) > 0 {
		response := runner.responses[0]
		runner.responses = runner.responses[1:]
		return append([]byte(nil), response.stdout...), append([]byte(nil), response.stderr...), response.err
	}
	return runner.stdout, runner.stderr, runner.runErr
}

type commandCall struct {
	executable string
	arguments  []string
	input      []byte
}

type commandResponse struct {
	stdout []byte
	stderr []byte
	err    error
}

func cancelledContext() context.Context {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	return ctx
}

func expiredContext() context.Context {
	ctx, cancel := context.WithDeadline(context.Background(), time.Unix(1, 0))
	cancel()
	return ctx
}

func assertFailureCode(t *testing.T, err error, want ports.FailureCode) {
	t.Helper()
	var failure *ports.Failure
	if !errors.As(err, &failure) {
		t.Fatalf("error = %v, want ports.Failure", err)
	}
	if failure.Code != want {
		t.Fatalf("failure code = %q, want %q", failure.Code, want)
	}
}
