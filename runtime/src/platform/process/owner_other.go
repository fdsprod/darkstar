//go:build !windows

package process

import "os/exec"

type Owner struct{ command *exec.Cmd }

func PrepareOwned(_ *exec.Cmd) {}
func PrepareProbe(_ *exec.Cmd) {}

func Own(command *exec.Cmd) (*Owner, error) {
	return &Owner{command: command}, nil
}

func (owner *Owner) Wait() error {
	return owner.command.Wait()
}
func (owner *Owner) Kill() error {
	err := owner.command.Process.Kill()
	if err == nil {
		_ = owner.command.Wait()
	}
	return err
}
func (owner *Owner) PID() int {
	return owner.command.Process.Pid
}
