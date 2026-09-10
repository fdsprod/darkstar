//go:build windows

// Package process configures noninteractive background subprocesses.
package process

import (
	"os/exec"
	"syscall"

	"golang.org/x/sys/windows"
)

// HideConsole preserves ownership flags while preventing a new console window.
func HideConsole(command *exec.Cmd) {
	if command.SysProcAttr == nil {
		command.SysProcAttr = &syscall.SysProcAttr{}
	}
	command.SysProcAttr.HideWindow = true
	command.SysProcAttr.CreationFlags |= windows.CREATE_NO_WINDOW
}
