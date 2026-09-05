//go:build windows

package doctor

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
