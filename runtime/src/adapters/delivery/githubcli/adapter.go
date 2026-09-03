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
	Executable string
	Runner     CommandRunner
}

// Adapter owns GitHub-specific command construction and response translation.
// Operation implementations are added behind the stable delivery capabilities.
type Adapter struct {
	executable string
	runner     CommandRunner
}

var _ delivery.Connector = (*Adapter)(nil)

// New resolves and pins gh once. An empty executable discovers gh from PATH.
func New(options Options) (*Adapter, error) {
	runner := options.Runner
	if runner == nil {
		runner = osCommandRunner{}
	}
	target := strings.TrimSpace(options.Executable)
	if target == "" {
		target = "gh"
	}
	resolved, err := runner.LookPath(target)
	if err != nil {
		return nil, failure(ports.FailureUnavailable, "GitHub CLI is not executable", true)
	}
	if strings.TrimSpace(resolved) == "" {
		return nil, failure(ports.FailureUnavailable, "GitHub CLI is not executable", true)
	}
	resolved, err = filepath.Abs(resolved)
	if err != nil {
		return nil, failure(ports.FailureInvalidRequest, "GitHub CLI executable path is invalid", false)
	}
	return &Adapter{executable: filepath.Clean(resolved), runner: runner}, nil
}

func (adapter *Adapter) ProbeHealth(context.Context, delivery.HealthRequest) (delivery.HealthObservation, error) {
	return delivery.HealthObservation{}, adapter.unsupported("GitHub health probing")
}

func (adapter *Adapter) ObserveBranch(context.Context, delivery.ObserveBranchRequest) (delivery.BranchObservation, error) {
	return delivery.BranchObservation{}, adapter.unsupported("GitHub branch observation")
}

func (adapter *Adapter) PublishBranch(context.Context, delivery.PublishBranchRequest) (delivery.BranchPublication, error) {
	return delivery.BranchPublication{}, adapter.unsupported("GitHub branch publication")
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
	if adapter == nil || adapter.runner == nil || adapter.executable == "" {
		return nil, failure(ports.FailureInternal, "GitHub CLI adapter is not configured", false)
	}
	stdout, _, err := adapter.runner.Run(ctx, adapter.executable, append([]string(nil), arguments...), append([]byte(nil), input...))
	if err == nil {
		if len(stdout) > maxOutputBytes {
			return nil, failure(ports.FailureResourceExhausted, "GitHub CLI output exceeded the adapter limit", false)
		}
		return append([]byte(nil), stdout...), nil
	}
	switch {
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		return nil, failure(ports.FailureTimeout, "GitHub CLI operation timed out", true)
	case errors.Is(ctx.Err(), context.Canceled):
		return nil, failure(ports.FailureCancelled, "GitHub CLI operation was cancelled", false)
	case errors.Is(err, exec.ErrNotFound), errors.Is(err, os.ErrNotExist):
		return nil, failure(ports.FailureUnavailable, "GitHub CLI is not executable", true)
	default:
		return nil, failure(ports.FailureUncertain, "GitHub CLI operation did not produce a proven result", false)
	}
}

func failure(code ports.FailureCode, message string, retryable bool) *ports.Failure {
	return &ports.Failure{Code: code, Message: message, Retryable: retryable}
}
