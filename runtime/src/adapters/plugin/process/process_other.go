//go:build !windows

package pluginprocess

import "os/exec"

func hideWindow(command *exec.Cmd) {}
