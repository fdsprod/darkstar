package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestRunExportRejectsInvalidArgumentsBeforeConnecting(t *testing.T) {
	t.Parallel()
	for _, args := range [][]string{{"run"}, {"run", "export"}, {"run", "export", "bad", "--output", "bundle.zip"}} {
		var stdout, stderr bytes.Buffer
		if code := Run(args, &stdout, &stderr); code != int(ExitInvalidInput) {
			t.Errorf("Run(%q) code = %d, want %d", args, code, ExitInvalidInput)
		}
	}
}

func TestWriteNewFileAtomicallyDoesNotOverwrite(t *testing.T) {
	t.Parallel()
	destination := filepath.Join(t.TempDir(), "bundle.zip")
	if err := writeNewFileAtomically(destination, []byte("first")); err != nil {
		t.Fatal(err)
	}
	if err := writeNewFileAtomically(destination, []byte("second")); err == nil {
		t.Fatal("second write unexpectedly overwrote export")
	}
	content, err := os.ReadFile(destination)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "first" {
		t.Fatalf("output = %q, want first", content)
	}
}
