package artifactregistry

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestArtifactVersionJSONRoundTripPreservesClosedProvenance(t *testing.T) {
	for _, provenance := range []Provenance{
		OperationProvenance{OperationID: "operation_one", Source: &VersionRef{ArtifactID: "artifact_source", Version: 2}},
		AttemptProvenance{RunID: "run_one", NodeID: "design", AttemptID: "attempt_one", OperationID: "operation_two"},
	} {
		value := ArtifactVersion{
			ArtifactID: "artifact_one", Version: 3, SourceKind: SourcePaste, SourceName: "note.txt",
			BlobDigest: strings.Repeat("a", 64), Size: 4, DeclaredMediaType: "text/plain", DetectedMediaType: "text/plain",
			Locator: "sha256/aa", Sensitivity: SensitivityInternal, Trust: "untrusted", Creator: "user:local", Status: StatusStored,
			Producer: Producer{Name: "darkstar-ingest", Version: "1"}, Roles: []string{"note"}, Tags: []string{}, Metadata: map[string]string{},
			Provenance: provenance, CreatedAt: time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC),
		}
		content, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(content), `"origin":"`) {
			t.Fatalf("provenance discriminator missing: %s", content)
		}
		var decoded ArtifactVersion
		if err := json.Unmarshal(content, &decoded); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(decoded, value) {
			t.Fatalf("round trip = %#v, want %#v", decoded, value)
		}
	}
}

func TestArtifactVersionJSONRejectsUnknownProvenanceFields(t *testing.T) {
	content := []byte(`{"artifactId":"artifact_one","version":1,"sourceKind":"paste","sourceName":"note.txt","blobDigest":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","size":1,"declaredMediaType":"text/plain","detectedMediaType":"text/plain","locator":"sha256/aa","sensitivity":"internal","trust":"untrusted","creator":"user:local","status":"stored","producer":{"name":"ingest","version":"1"},"roles":[],"tags":[],"metadata":{},"provenance":{"origin":"operation","operationId":"operation_one","secret":"leak"},"createdAt":"2026-09-04T12:00:00Z"}`)
	var value ArtifactVersion
	if err := json.Unmarshal(content, &value); err == nil {
		t.Fatal("unknown provenance field was accepted")
	}
}
