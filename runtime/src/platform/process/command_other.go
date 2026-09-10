//go:build !windows

package process

import "os/exec"

func HideConsole(command *exec.Cmd) {}
