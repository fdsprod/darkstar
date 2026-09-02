package cli_test

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"darkstar/src/cli"
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
	if !strings.Contains(stdout.String(), "daemon status --json") {
		t.Fatalf("Run(help) stdout = %q, want daemon commands", stdout.String())
	}
	if stderr.Len() != 0 {
		t.Fatalf("Run(help) stderr = %q, want empty", stderr.String())
	}
}

func TestRunDaemonRequiresValidSubcommand(t *testing.T) {
	t.Parallel()

	for _, args := range [][]string{{"daemon"}, {"daemon", "unknown"}, {"daemon", "status", "--yaml"}} {
		var stdout bytes.Buffer
		var stderr bytes.Buffer
		if code := cli.Run(args, &stdout, &stderr); code != 2 {
			t.Errorf("Run(%q) code = %d, want 2", args, code)
		}
		if stderr.Len() == 0 {
			t.Errorf("Run(%q) stderr is empty", args)
		}
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

func TestRunVersionJSONIsVersioned(t *testing.T) {
	t.Parallel()

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	if code := cli.Run([]string{"version", "--json"}, &stdout, &stderr); code != int(cli.ExitSuccess) {
		t.Fatalf("Run(version --json) code = %d, want %d", code, cli.ExitSuccess)
	}
	var output struct {
		SchemaVersion int    `json:"schemaVersion"`
		Version       string `json:"version"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &output); err != nil {
		t.Fatalf("Run(version --json) output is not JSON: %v", err)
	}
	if output.SchemaVersion != 1 || output.Version != "dev" {
		t.Fatalf("Run(version --json) output = %#v", output)
	}
	if stderr.Len() != 0 {
		t.Fatalf("Run(version --json) stderr = %q, want empty", stderr.String())
	}
}

func TestRunJSONFailureUsesStableEnvelope(t *testing.T) {
	t.Parallel()

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	if code := cli.Run([]string{"unknown", "--json"}, &stdout, &stderr); code != int(cli.ExitInvalidInput) {
		t.Fatalf("Run(unknown --json) code = %d, want %d", code, cli.ExitInvalidInput)
	}
	var output struct {
		SchemaVersion int `json:"schemaVersion"`
		Error         struct {
			Code      string `json:"code"`
			Message   string `json:"message"`
			Retryable bool   `json:"retryable"`
		} `json:"error"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &output); err != nil {
		t.Fatalf("Run(unknown --json) output is not JSON: %v", err)
	}
	if output.SchemaVersion != 1 || output.Error.Code != "ARGUMENT_INVALID" || output.Error.Message == "" || output.Error.Retryable {
		t.Fatalf("Run(unknown --json) output = %#v", output)
	}
	if stderr.Len() != 0 {
		t.Fatalf("Run(unknown --json) stderr = %q, want empty", stderr.String())
	}
}

func TestRunRejectsMisplacedJSONFlagWithJSONError(t *testing.T) {
	t.Parallel()

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	if code := cli.Run([]string{"--json", "version"}, &stdout, &stderr); code != int(cli.ExitInvalidInput) {
		t.Fatalf("Run(--json version) code = %d, want %d", code, cli.ExitInvalidInput)
	}
	if !json.Valid(stdout.Bytes()) {
		t.Fatalf("Run(--json version) stdout = %q, want JSON", stdout.String())
	}
	if stderr.Len() != 0 {
		t.Fatalf("Run(--json version) stderr = %q, want empty", stderr.String())
	}
}
