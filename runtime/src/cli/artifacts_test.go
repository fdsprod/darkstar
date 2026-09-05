package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"darkstar/src/core/artifactops"
	"darkstar/src/ports/artifactbinding"
	"darkstar/src/ports/artifactregistry"
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

func TestArtifactDiffOptionsAndJSONUseAPITypes(t *testing.T) {
	t.Parallel()
	input, err := parseArtifactDiff([]string{"artifact_one", "--to", "9", "--from", "3", "--from-representation", "representation_a", "--to-representation", "representation_b", "--limit", "17", "--cursor", "cursor"})
	if err != nil || input.ArtifactID != "artifact_one" || input.From != 3 || input.To != 9 || input.FromRepresentationID != "representation_a" || input.ToRepresentationID != "representation_b" || input.Limit != 17 || input.Cursor != "cursor" {
		t.Fatalf("diff input = %#v, %v", input, err)
	}
	for _, invalid := range [][]string{{"artifact_one", "--from", "1"}, {"artifact_one", "--from", "1", "--to", "2", "--limit", "201"}, {"artifact_one", "--from", "1", "--to", "2", "--from", "3"}} {
		if _, err := parseArtifactDiff(invalid); err == nil {
			t.Fatalf("invalid options accepted: %#v", invalid)
		}
	}
	value := artifactops.VersionDiff{ArtifactID: "artifact_one", From: 3, To: 9, Changed: []string{}, FromDigest: strings.Repeat("a", 64), ToDigest: strings.Repeat("b", 64), Representations: map[string][]string{"from": {}, "to": {}}, TextDiff: artifactops.TextDiff{Status: "unavailable", Reason: "too_large"}}
	var output bytes.Buffer
	if code := writeArtifactResult(value, "ignored", false, true, &output, &bytes.Buffer{}, "darkstar artifact diff"); code != int(ExitSuccess) {
		t.Fatalf("exit = %d", code)
	}
	var envelope struct {
		SchemaVersion int                     `json:"schemaVersion"`
		Result        artifactops.VersionDiff `json:"result"`
	}
	if err := json.Unmarshal(output.Bytes(), &envelope); err != nil || envelope.Result.TextDiff.Reason != "too_large" {
		t.Fatalf("JSON parity = %#v, %v", envelope, err)
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
