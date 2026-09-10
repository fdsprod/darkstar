package codex

import (
	"darkstar/src/platform/process"
	"os/exec"
)

// Legacy Codex composition shares OS process ownership with plugin providers.
type commandOwner = process.Owner

func configureAppServerProcess(command *exec.Cmd) {
	process.PrepareOwned(command)
}
func configureProbeProcess(command *exec.Cmd) {
	process.PrepareProbe(command)
}
func newCommandOwner(command *exec.Cmd) (*commandOwner, error) {
	return process.Own(command)
}
