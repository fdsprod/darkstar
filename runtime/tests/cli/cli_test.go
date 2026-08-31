package cli_test

import (
	"bytes"
	"strings"
	"testing"

	"github.com/fdsprod/darkstar/runtime/src/cli"
)

func TestRunHelp(t *testing.T) {
	t.Parallel()

	var stdout bytes.Buffer
	var stderr bytes.Buffer

	if code := cli.Run([]string{"help"}, &stdout, &stderr); code != 0 {
		t.Fatalf("Run(help) code = %d, want 0", code)
	}
	if !strings.Contains(stdout.String(), "Usage:") {
		t.Fatalf("Run(help) stdout = %q, want usage", stdout.String())
	}
	if stderr.Len() != 0 {
		t.Fatalf("Run(help) stderr = %q, want empty", stderr.String())
	}
}

func TestRunVersion(t *testing.T) {
	t.Parallel()

	var stdout bytes.Buffer
	var stderr bytes.Buffer

	if code := cli.Run([]string{"version"}, &stdout, &stderr); code != 0 {
		t.Fatalf("Run(version) code = %d, want 0", code)
	}
	if got, want := stdout.String(), "darkstar dev\n"; got != want {
		t.Fatalf("Run(version) stdout = %q, want %q", got, want)
	}
}

func TestRunUnknownCommand(t *testing.T) {
	t.Parallel()

	var stdout bytes.Buffer
	var stderr bytes.Buffer

	if code := cli.Run([]string{"unknown"}, &stdout, &stderr); code != 2 {
		t.Fatalf("Run(unknown) code = %d, want 2", code)
	}
	if !strings.Contains(stderr.String(), "unknown command") {
		t.Fatalf("Run(unknown) stderr = %q, want error", stderr.String())
	}
}
