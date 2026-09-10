// Package nodeprocess adapts bounded process execution for deterministic nodes.
package nodeprocess

import (
	"context"
	"os/exec"
	"time"

	"darkstar/src/platform/process"
)

type Runner struct {
	Environment []string
	OutputLimit int
}

func (r Runner) Run(ctx context.Context, workspace string, argv []string, timeout time.Duration) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	command := exec.CommandContext(ctx, argv[0], argv[1:]...)
	process.HideConsole(command)
	command.Dir = workspace
	command.Env = r.Environment
	output := &boundedOutput{limit: r.OutputLimit}
	command.Stdout = output
	command.Stderr = output
	err := command.Run()
	return output.text, err
}

type boundedOutput struct {
	text  string
	limit int
}

func (b *boundedOutput) Write(p []byte) (int, error) {
	n := len(p)
	if b.limit <= 0 {
		b.text += string(p)
		return n, nil
	}
	if remaining := b.limit - len(b.text); remaining > 0 {
		if len(p) > remaining {
			p = p[:remaining]
		}
		b.text += string(p)
	}
	return n, nil
}
