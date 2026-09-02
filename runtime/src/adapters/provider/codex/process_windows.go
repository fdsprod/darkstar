//go:build windows

package codex

import (
	"os/exec"
	"syscall"
)

func configureAppServerProcess(command *exec.Cmd) {
	command.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP}
}
