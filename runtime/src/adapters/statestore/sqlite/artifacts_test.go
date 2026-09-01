package sqlite

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/fdsprod/darkstar/runtime/src/ports/artifactregistry"
	"github.com/fdsprod/darkstar/runtime/src/ports/artifactstore"
)

func TestArtifactRegistryAllocatesImmutableVersionsWithExactProvenance(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	database, err := Open(ctx, filepath.Join(t.TempDir(), "artifacts.db"), Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })

	artifactID := testID("artifact", 'A')
	firstRequest := artifactRequest(artifactID, "ingest-1", strings.Repeat("a", 64))
	first, created, err := database.Register(ctx, firstRequest)
	if err != nil || !created {
		t.Fatalf("Register(first) = created %v, error %v", created, err)
	}
	if first.Version != 1 || first.Trust != "untrusted" || !reflect.DeepEqual(first.Roles, []string{"note", "transcript"}) || !reflect.DeepEqual(first.Tags, []string{"customer", "planning"}) {
		t.Fatalf("first artifact version = %#v", first)
	}

	repeated, created, err := database.Register(ctx, firstRequest)
	if err != nil || created || !reflect.DeepEqual(repeated, first) {
		t.Fatalf("Register(repeat) = %#v, created %v, error %v", repeated, created, err)
	}
	conflict := firstRequest
	conflict.BlobDigest = strings.Repeat("b", 64)
	if _, _, err := database.Register(ctx, conflict); !errors.Is(err, artifactregistry.ErrVersionConflict) {
		t.Fatalf("Register(conflict) error = %v, want version conflict", err)
	}

	secondRequest := artifactRequest(artifactID, "derive-2", strings.Repeat("b", 64))
	secondRequest.SourceKind = artifactregistry.SourceGenerated
	secondRequest.SourceName = "summary.md"
	secondRequest.Producer = artifactregistry.Producer{Name: "summary-processor", Version: "2.1.0"}
	secondRequest.Roles = []string{"summary"}
	secondRequest.Tags = nil
	secondRequest.Provenance = artifactregistry.AttemptProvenance{
		RunID: testID("run", 'B'), NodeID: "summarize", AttemptID: testID("attempt", 'B'),
		OperationID: testID("operation", 'B'), Source: &artifactregistry.VersionRef{ArtifactID: artifactID, Version: 1},
	}
	secondRequest.CreatedAt = firstRequest.CreatedAt.Add(time.Second)
	second, created, err := database.Register(ctx, secondRequest)
	if err != nil || !created || second.Version != 2 {
		t.Fatalf("Register(second) = %#v, created %v, error %v", second, created, err)
	}
	wantProvenance := secondRequest.Provenance
	if !reflect.DeepEqual(second.Provenance, wantProvenance) {
		t.Fatalf("second provenance = %#v, want %#v", second.Provenance, wantProvenance)
	}

	versions, err := database.Versions(ctx, artifactID)
	if err != nil || len(versions) != 2 || versions[0].Version != 1 || versions[1].Version != 2 {
		t.Fatalf("Versions() = %#v, %v", versions, err)
	}
	latest, err := database.LatestVersion(ctx, artifactID)
	if err != nil || !reflect.DeepEqual(latest, second) {
		t.Fatalf("LatestVersion() = %#v, %v", latest, err)
	}
	exact, err := database.ArtifactVersion(ctx, artifactregistry.VersionRef{ArtifactID: artifactID, Version: 1})
	if err != nil || !reflect.DeepEqual(exact, first) {
		t.Fatalf("Version(1) = %#v, %v", exact, err)
	}

	if _, err := database.SQL().ExecContext(ctx, `UPDATE artifact_versions SET source_name = 'changed' WHERE artifact_id = ?`, artifactID); err == nil {
		t.Fatal("artifact version update unexpectedly succeeded")
	}
	if _, err := database.SQL().ExecContext(ctx, `DELETE FROM artifact_versions WHERE artifact_id = ?`, artifactID); err == nil {
		t.Fatal("artifact version delete unexpectedly succeeded")
	}
}

func TestArtifactRegistryRejectsInvalidOrDanglingMetadata(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	database, err := Open(ctx, filepath.Join(t.TempDir(), "artifact-validation.db"), Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })

	request := artifactRequest(testID("artifact", 'C'), "invalid-attempt", strings.Repeat("c", 64))
	request.Provenance = artifactregistry.AttemptProvenance{OperationID: testID("operation", 'C')}
	if _, _, err := database.Register(ctx, request); err == nil {
		t.Fatal("incomplete attempt provenance unexpectedly succeeded")
	}

	request = artifactRequest(testID("artifact", 'C'), "dangling-source", strings.Repeat("c", 64))
	request.Provenance = artifactregistry.OperationProvenance{
		OperationID: testID("operation", 'C'),
		Source:      &artifactregistry.VersionRef{ArtifactID: testID("artifact", 'D'), Version: 1},
	}
	if _, _, err := database.Register(ctx, request); err == nil {
		t.Fatal("dangling source artifact version unexpectedly succeeded")
	}

	if _, err := database.ArtifactVersion(ctx, artifactregistry.VersionRef{ArtifactID: request.ArtifactID, Version: 99}); !errors.Is(err, artifactregistry.ErrNotFound) {
		t.Fatalf("Version(missing) error = %v, want not found", err)
	}
}

func TestArtifactDigestDeduplicatesStorageWithoutCollapsingIdentity(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	database, err := Open(ctx, filepath.Join(t.TempDir(), "artifact-identity.db"), Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })

	digest := strings.Repeat("d", 64)
	left, _, err := database.Register(ctx, artifactRequest(testID("artifact", 'E'), "left", digest))
	if err != nil {
		t.Fatal(err)
	}
	right, _, err := database.Register(ctx, artifactRequest(testID("artifact", 'F'), "right", digest))
	if err != nil {
		t.Fatal(err)
	}
	if left.BlobDigest != right.BlobDigest || left.Locator != right.Locator || left.ArtifactID == right.ArtifactID {
		t.Fatalf("equal bytes did not retain distinct identities: left %#v right %#v", left, right)
	}
}

func artifactRequest(artifactID, key, digest string) artifactregistry.RegisterRequest {
	return artifactregistry.RegisterRequest{
		ArtifactID: artifactID, IdempotencyKey: key, SourceKind: artifactregistry.SourcePaste,
		SourceName: "planning-notes.md", BlobDigest: digest, Size: 128,
		DeclaredMediaType: "text/markdown", DetectedMediaType: "text/plain; charset=utf-8",
		Locator: artifactstore.Locator("sha256:" + digest), Sensitivity: artifactregistry.SensitivityInternal,
		Creator: "user:local", Status: artifactregistry.StatusStored,
		Producer: artifactregistry.Producer{Name: "darkstar-ingest", Version: "1.0.0"},
		Roles:    []string{"transcript", "note"}, Tags: []string{"planning", "customer"},
		Metadata:   map[string]string{"language": "en", "encoding": "utf-8"},
		Provenance: artifactregistry.OperationProvenance{OperationID: testID("operation", 'A')},
		CreatedAt:  time.Date(2026, time.September, 1, 12, 0, 0, 0, time.UTC),
	}
}
