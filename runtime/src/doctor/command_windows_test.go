//go:build windows

package doctor

import (
	"os"
	"os/exec"
	"testing"

	"golang.org/x/sys/windows"
)

func TestDiagnosticCommandDoesNotCreateAWindow(t *testing.T) {
	command := exec.Command(os.Args[0], "-test.run=^$")
	configureCommand(command)

	if command.SysProcAttr == nil || !command.SysProcAttr.HideWindow {
		t.Fatal("diagnostic command was not configured as hidden")
	}
	if command.SysProcAttr.CreationFlags&windows.CREATE_NO_WINDOW == 0 {
		t.Fatal("diagnostic command may create a console window")
	}
}
