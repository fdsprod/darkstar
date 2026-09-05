//go:build windows

package githubcli

import (
	"os"
	"os/exec"
	"testing"

	"golang.org/x/sys/windows"
)

func TestGitHubCLICommandDoesNotCreateAWindow(t *testing.T) {
	command := exec.Command(os.Args[0], "-test.run=^$")
	configureCommand(command)

	if command.SysProcAttr == nil || !command.SysProcAttr.HideWindow {
		t.Fatal("GitHub CLI command was not configured as hidden")
	}
	if command.SysProcAttr.CreationFlags&windows.CREATE_NO_WINDOW == 0 {
		t.Fatal("GitHub CLI command may create a console window")
	}
}
