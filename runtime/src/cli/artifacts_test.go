package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fdsprod/darkstar/runtime/src/ports/artifactbinding"
	"github.com/fdsprod/darkstar/runtime/src/ports/artifactregistry"
)

func TestParseArtifactContentRequiresOneSourceAndPreservesMetadata(t *testing.T) {
	t.Parallel()
	input, err := parseArtifactContent([]string{"--paste", "hello", "--role", "evidence", "--tag", "reviewed", "--sensitivity", "sensitive"})
	if err != nil {
		t.Fatal(err)
	}
	if input.SourceKind != artifactregistry.SourcePaste || input.SourceName != "pasted-note.txt" || input.MediaType != "text/plain" || string(input.Content) != "hello" {
		t.Fatalf("content input = %#v", input)
	}
	if len(input.Roles) != 1 || input.Roles[0] != "evidence" || len(input.Tags) != 1 || input.Tags[0] != "reviewed" || input.Sensitivity != artifactregistry.SensitivitySensitive {
		t.Fatalf("metadata input = %#v", input)
	}
	if _, err := parseArtifactContent([]string{"--paste", "one", "--paste", "two"}); err == nil {
		t.Fatal("multiple sources were accepted")
	}
	if _, err := parseArtifactContent([]string{"--paste", "one", "--sensitivity", "confidential"}); err == nil {
		t.Fatal("unknown sensitivity was accepted")
	}
}

func TestParseArtifactFileDetectsMediaType(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "evidence.json")
	if err := os.WriteFile(path, []byte(`{"answer":42}`), 0o600); err != nil {
		t.Fatal(err)
	}
	input, err := parseArtifactContent([]string{"--file", path})
	if err != nil {
		t.Fatal(err)
	}
	if input.SourceKind != artifactregistry.SourceFile || input.SourceName != "evidence.json" || input.MediaType != "application/json" {
		t.Fatalf("file input = %#v", input)
	}
}

func TestParseArtifactReferenceAndTargetUseClosedBoundaries(t *testing.T) {
	t.Parallel()
	reference, err := parseArtifactReference("artifact_one@7")
	if err != nil || reference.ArtifactID != "artifact_one" || reference.Version != 7 {
		t.Fatalf("reference = %#v, %v", reference, err)
	}
	target, err := parseArtifactTarget("implementation_point:src/main.go:42")
	if err != nil || target.Kind != artifactbinding.TargetImplementationPoint || target.ID != "src/main.go:42" {
		t.Fatalf("target = %#v, %v", target, err)
	}
	if _, err := parseArtifactTarget("unknown:item"); err == nil {
		t.Fatal("unknown target kind was accepted")
	}
}

func TestWriteArtifactMachineResultUsesStableEnvelopeAndLintExit(t *testing.T) {
	t.Parallel()
	var output bytes.Buffer
	code := writeArtifactResult(map[string]string{"artifactId": "artifact_one"}, "ignored", true, true, &output, &bytes.Buffer{}, "darkstar artifact lint")
	if code != int(ExitValidationFailed) {
		t.Fatalf("exit = %d, want %d", code, ExitValidationFailed)
	}
	for _, fragment := range []string{`"schemaVersion":1`, `"result":`, `"artifactId":"artifact_one"`} {
		if !strings.Contains(output.String(), fragment) {
			t.Fatalf("output %q does not contain %q", output.String(), fragment)
		}
	}
}
