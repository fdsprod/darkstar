package sqlite

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"darkstar/src/ports/artifactregistry"
	"darkstar/src/ports/artifactstore"
	"darkstar/src/ports/contentprocessor"
	"darkstar/src/ports/representationregistry"
)

func TestRepresentationRegistryIsImmutableAndIdempotent(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	database, err := Open(ctx, filepath.Join(t.TempDir(), "representations.db"), Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	artifactID := testID("artifact", 'R')
	artifact, _, err := database.Register(ctx, artifactRequest(artifactID, "source", strings.Repeat("a", 64)))
	if err != nil {
		t.Fatal(err)
	}
	request := representationregistry.RegisterRequest{
		RepresentationID: "representation_0123456789abcdef", IdempotencyKey: "derive-1",
		Artifact:  artifactregistry.VersionRef{ArtifactID: artifact.ArtifactID, Version: artifact.Version},
		Kind:      contentprocessor.RepresentationStructured,
		Processor: contentprocessor.Descriptor{Name: "common", Version: "1.0.0", MediaTypes: []string{"application/json"}},
		MediaType: "application/json", Locator: artifactstore.Locator("sha256:" + strings.Repeat("b", 64)),
		Digest: strings.Repeat("b", 64), Size: 12, TokenEstimate: 3,
		Disclosure: representationregistry.DisclosureRaw, Diagnostics: []string{}, Metadata: map[string]string{"sourceFormat": "json"},
		CreatedAt: time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC),
	}
	created, inserted, err := database.RegisterRepresentation(ctx, request)
	if err != nil || !inserted {
		t.Fatalf("Register() = %#v, %v, %v", created, inserted, err)
	}
	repeated, inserted, err := database.RegisterRepresentation(ctx, request)
	if err != nil || inserted || !reflect.DeepEqual(repeated, created) {
		t.Fatalf("Register(repeat) = %#v, %v, %v", repeated, inserted, err)
	}
	conflict := request
	conflict.Digest = strings.Repeat("c", 64)
	if _, _, err := database.RegisterRepresentation(ctx, conflict); !errors.Is(err, representationregistry.ErrConflict) {
		t.Fatalf("conflict error = %v", err)
	}
	values, err := database.ForArtifact(ctx, request.Artifact)
	if err != nil || len(values) != 1 || !reflect.DeepEqual(values[0], created) {
		t.Fatalf("ForArtifact() = %#v, %v", values, err)
	}
	if _, err := database.SQL().ExecContext(ctx, `UPDATE artifact_representations SET media_type = 'text/plain'`); err == nil {
		t.Fatal("representation update unexpectedly succeeded")
	}
}
