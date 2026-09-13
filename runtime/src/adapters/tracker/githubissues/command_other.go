//go:build !windows

package githubissues

import "os/exec"

func configureCommand(_ *exec.Cmd) {}
