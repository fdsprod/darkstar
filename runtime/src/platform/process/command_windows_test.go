//go:build windows

package process

import (
	"os/exec"
	"syscall"
	"testing"

	"golang.org/x/sys/windows"
)

func TestHideConsolePreservesOwnershipFlags(t *testing.T) {
	command := exec.Command("unused")
	command.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windows.CREATE_SUSPENDED | windows.CREATE_NEW_PROCESS_GROUP}
	HideConsole(command)
	want := uint32(windows.CREATE_SUSPENDED | windows.CREATE_NEW_PROCESS_GROUP | windows.CREATE_NO_WINDOW)
	if !command.SysProcAttr.HideWindow || command.SysProcAttr.CreationFlags != want {
		t.Fatalf("background process flags: %+v", command.SysProcAttr)
	}
}
