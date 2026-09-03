// Package githubcli implements the delivery port with the GitHub CLI.
package githubcli

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"darkstar/src/ports"
	"darkstar/src/ports/delivery"
)

const (
	Provider       = "github"
	maxOutputBytes = 4 << 20
)

// CommandRunner is the argument-array process boundary used by the adapter.
// Implementations must not invoke a shell.
type CommandRunner interface {
	LookPath(string) (string, error)
	Run(context.Context, string, []string, []byte) ([]byte, []byte, error)
}

type osCommandRunner struct{}

func (osCommandRunner) LookPath(name string) (string, error) { return exec.LookPath(name) }

func (osCommandRunner) Run(ctx context.Context, executable string, arguments []string, input []byte) ([]byte, []byte, error) {
	command := exec.CommandContext(ctx, executable, arguments...)
	command.Env = append(os.Environ(), "GH_PROMPT_DISABLED=1", "GIT_TERMINAL_PROMPT=0", "GCM_INTERACTIVE=Never")
	command.Stdin = bytes.NewReader(input)
	var stdout, stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	err := command.Run()
	return stdout.Bytes(), stderr.Bytes(), err
}

// Options supplies the pinned executable and testable process boundary.
type Options struct {
	Executable    string
	GitExecutable string
	Runner        CommandRunner
	Now           func() time.Time
}

// Adapter owns GitHub-specific command construction and response translation.
// Operation implementations are added behind the stable delivery capabilities.
type Adapter struct {
	executable    string
	gitExecutable string
	runner        CommandRunner
	now           func() time.Time
}

var _ delivery.Connector = (*Adapter)(nil)

// New resolves and pins gh once. An empty executable discovers gh from PATH.
func New(options Options) (*Adapter, error) {
	runner := options.Runner
	if runner == nil {
		runner = osCommandRunner{}
	}
	github, err := resolveExecutable(runner, options.Executable, "gh", "GitHub CLI")
	if err != nil {
		return nil, err
	}
	git, err := resolveExecutable(runner, options.GitExecutable, "git", "Git")
	if err != nil {
		return nil, err
	}
	now := options.Now
	if now == nil {
		now = time.Now
	}
	return &Adapter{executable: github, gitExecutable: git, runner: runner, now: now}, nil
}

func (adapter *Adapter) FindChangeRequests(context.Context, delivery.FindChangeRequestsRequest) (delivery.ChangeRequestSearch, error) {
	return delivery.ChangeRequestSearch{}, adapter.unsupported("GitHub change-request search")
}

func (adapter *Adapter) CreateChangeRequest(context.Context, delivery.CreateChangeRequestRequest) (delivery.ChangeRequestCreation, error) {
	return delivery.ChangeRequestCreation{}, adapter.unsupported("GitHub change-request creation")
}

func (adapter *Adapter) UpdateChangeRequest(context.Context, delivery.UpdateChangeRequestRequest) (delivery.ChangeRequestUpdate, error) {
	return delivery.ChangeRequestUpdate{}, adapter.unsupported("GitHub change-request update")
}

func (adapter *Adapter) ObserveChangeRequest(context.Context, delivery.ObserveChangeRequestRequest) (delivery.ChangeRequestObservation, error) {
	return delivery.ChangeRequestObservation{}, adapter.unsupported("GitHub change-request observation")
}

func (adapter *Adapter) unsupported(operation string) error {
	if adapter == nil || adapter.runner == nil || adapter.executable == "" {
		return failure(ports.FailureInternal, "GitHub CLI adapter is not configured", false)
	}
	return failure(ports.FailureUnsupported, operation+" is not implemented", false)
}

func (adapter *Adapter) run(ctx context.Context, arguments []string, input []byte) ([]byte, error) {
	if adapter == nil {
		return nil, failure(ports.FailureInternal, "GitHub CLI adapter is not configured", false)
	}
	return adapter.runExecutable(ctx, adapter.executable, arguments, input)
}

func (adapter *Adapter) runGit(ctx context.Context, arguments []string) ([]byte, error) {
	if adapter == nil {
		return nil, failure(ports.FailureInternal, "GitHub CLI adapter is not configured", false)
	}
	return adapter.runExecutable(ctx, adapter.gitExecutable, arguments, nil)
}

type commandResult struct {
	stdout []byte
	stderr []byte
	err    error
}

func (adapter *Adapter) execute(ctx context.Context, executable string, arguments []string, input []byte) commandResult {
	if adapter == nil || adapter.runner == nil || strings.TrimSpace(executable) == "" {
		return commandResult{err: failure(ports.FailureInternal, "GitHub CLI adapter is not configured", false)}
	}
	stdout, stderr, err := adapter.runner.Run(ctx, executable, append([]string(nil), arguments...), append([]byte(nil), input...))
	if len(stdout)+len(stderr) > maxOutputBytes {
		return commandResult{err: failure(ports.FailureResourceExhausted, "command output exceeded the adapter limit", false)}
	}
	return commandResult{stdout: append([]byte(nil), stdout...), stderr: append([]byte(nil), stderr...), err: err}
}

func (adapter *Adapter) runExecutable(ctx context.Context, executable string, arguments []string, input []byte) ([]byte, error) {
	result := adapter.execute(ctx, executable, arguments, input)
	if result.err == nil {
		return result.stdout, nil
	}
	return nil, normalizeCommandFailure(ctx, result.err)
}

func normalizeCommandFailure(ctx context.Context, err error) error {
	var classified *ports.Failure
	if errors.As(err, &classified) {
		return classified
	}
	switch {
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		return failure(ports.FailureTimeout, "GitHub CLI operation timed out", true)
	case errors.Is(ctx.Err(), context.Canceled):
		return failure(ports.FailureCancelled, "GitHub CLI operation was cancelled", false)
	case errors.Is(err, exec.ErrNotFound), errors.Is(err, os.ErrNotExist):
		return failure(ports.FailureUnavailable, "GitHub CLI is not executable", true)
	default:
		return failure(ports.FailureUncertain, "GitHub CLI operation did not produce a proven result", false)
	}
}

func resolveExecutable(runner CommandRunner, configured, fallback, label string) (string, error) {
	target := strings.TrimSpace(configured)
	if target == "" {
		target = fallback
	}
	resolved, err := runner.LookPath(target)
	if err != nil || strings.TrimSpace(resolved) == "" {
		return "", failure(ports.FailureUnavailable, label+" is not executable", true)
	}
	resolved, err = filepath.Abs(resolved)
	if err != nil {
		return "", failure(ports.FailureInvalidRequest, label+" executable path is invalid", false)
	}
	return filepath.Clean(resolved), nil
}

func failure(code ports.FailureCode, message string, retryable bool) *ports.Failure {
	return &ports.Failure{Code: code, Message: message, Retryable: retryable}
}
