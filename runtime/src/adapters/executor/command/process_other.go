//go:build !windows

package command

import (
	"errors"
	"os/exec"
	"syscall"
)

type processOwner struct{ command *exec.Cmd }

func configureOwnedProcess(command *exec.Cmd) {
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

func newProcessOwner(command *exec.Cmd) (*processOwner, error) {
	return &processOwner{command: command}, nil
}

func (owner *processOwner) Wait() error { return owner.command.Wait() }

func (owner *processOwner) Terminate() (bool, error) {
	err := syscall.Kill(-owner.command.Process.Pid, syscall.SIGTERM)
	if errors.Is(err, syscall.ESRCH) {
		return true, nil
	}
	return true, err
}

func (owner *processOwner) Kill() error {
	err := syscall.Kill(-owner.command.Process.Pid, syscall.SIGKILL)
	if errors.Is(err, syscall.ESRCH) {
		return nil
	}
	return err
}

func (owner *processOwner) PID() int { return owner.command.Process.Pid }
