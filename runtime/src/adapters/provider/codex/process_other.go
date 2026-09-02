//go:build !windows

package codex

import "os/exec"

type commandOwner struct{ command *exec.Cmd }

func configureAppServerProcess(_ *exec.Cmd) {}
func configureProbeProcess(_ *exec.Cmd)     {}

func newCommandOwner(command *exec.Cmd) (*commandOwner, error) {
	return &commandOwner{command: command}, nil
}

func (owner *commandOwner) Wait() error { return owner.command.Wait() }
func (owner *commandOwner) Kill() error {
	err := owner.command.Process.Kill()
	if err == nil {
		_ = owner.command.Wait()
	}
	return err
}
func (owner *commandOwner) PID() int { return owner.command.Process.Pid }
