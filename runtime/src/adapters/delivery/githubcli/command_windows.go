//go:build windows

package githubcli

import (
	"os/exec"
	"syscall"

	"golang.org/x/sys/windows"
)

func configureCommand(command *exec.Cmd) {
	command.SysProcAttr = &syscall.SysProcAttr{
		HideWindow:    true,
		CreationFlags: windows.CREATE_NO_WINDOW,
	}
}
