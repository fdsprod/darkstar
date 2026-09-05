//go:build !windows

package githubcli

import "os/exec"

func configureCommand(_ *exec.Cmd) {}
