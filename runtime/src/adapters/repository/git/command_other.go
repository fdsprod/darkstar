//go:build !windows

package git

import "os/exec"

func configureCommand(_ *exec.Cmd) {}
