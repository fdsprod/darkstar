package codex

import (
	"context"
	"fmt"
	"io"
	"os/exec"
	"path/filepath"
)

// StartAppServer pins one canonical executable, starts its stdio App Server,
// and completes protocol negotiation before returning it to the adapter.
func StartAppServer(ctx context.Context, executable string, options AppServerOptions) (*AppServerClient, InitializeResult, error) {
	canonical, err := canonicalExecutable(executable)
	if err != nil {
		return nil, InitializeResult{}, err
	}
	command := exec.Command(canonical, "app-server")
	configureAppServerProcess(command)
	stdin, err := command.StdinPipe()
	if err != nil {
		return nil, InitializeResult{}, fmt.Errorf("open Codex App Server stdin: %w", err)
	}
	stdout, err := command.StdoutPipe()
	if err != nil {
		_ = stdin.Close()
		return nil, InitializeResult{}, fmt.Errorf("open Codex App Server stdout: %w", err)
	}
	stderr, err := command.StderrPipe()
	if err != nil {
		_ = stdin.Close()
		_ = stdout.Close()
		return nil, InitializeResult{}, fmt.Errorf("open Codex App Server stderr: %w", err)
	}
	if err := command.Start(); err != nil {
		_ = stdin.Close()
		_ = stdout.Close()
		_ = stderr.Close()
		return nil, InitializeResult{}, fmt.Errorf("start Codex App Server: %w", err)
	}
	owner, err := newCommandOwner(command)
	if err != nil {
		_ = command.Process.Kill()
		_ = command.Wait()
		_ = stdin.Close()
		_ = stdout.Close()
		_ = stderr.Close()
		return nil, InitializeResult{}, fmt.Errorf("own Codex App Server process tree: %w", err)
	}
	go func() { _, _ = io.Copy(io.Discard, stderr) }()

	client, err := newAppServerClient(stdin, stdout, owner, options)
	if err != nil {
		_ = owner.Kill()
		return nil, InitializeResult{}, err
	}
	result, err := client.Initialize(ctx)
	if err != nil {
		_ = client.KillOwnedProcess()
		return nil, InitializeResult{}, err
	}
	return client, result, nil
}

func canonicalExecutable(executable string) (string, error) {
	if executable == "" {
		return "", fmt.Errorf("codex executable path is required")
	}
	resolved, err := exec.LookPath(executable)
	if err != nil {
		return "", fmt.Errorf("resolve Codex executable %q: %w", executable, err)
	}
	absolute, err := filepath.Abs(resolved)
	if err != nil {
		return "", fmt.Errorf("canonicalize Codex executable %q: %w", resolved, err)
	}
	if evaluated, evaluateErr := filepath.EvalSymlinks(absolute); evaluateErr == nil {
		absolute = evaluated
	}
	return filepath.Clean(absolute), nil
}
