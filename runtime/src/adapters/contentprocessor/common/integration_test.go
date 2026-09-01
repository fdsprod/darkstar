package common_test

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/fdsprod/darkstar/runtime/src/adapters/artifactstore/folder"
	"github.com/fdsprod/darkstar/runtime/src/adapters/contentprocessor/common"
	"github.com/fdsprod/darkstar/runtime/src/adapters/statestore/sqlite"
	"github.com/fdsprod/darkstar/runtime/src/core/artifactderive"
	"github.com/fdsprod/darkstar/runtime/src/core/artifactingest"
	"github.com/fdsprod/darkstar/runtime/src/ports/artifactregistry"
	"github.com/fdsprod/darkstar/runtime/src/ports/contentprocessor"
)

func TestIngestAndDerivePersistsVersionedRepresentations(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, err := folder.New(filepath.Join(t.TempDir(), "artifacts"))
	if err != nil {
		t.Fatal(err)
	}
	database, err := sqlite.Open(ctx, filepath.Join(t.TempDir(), "state.db"), sqlite.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	processor := common.New()
	ingestion, err := artifactingest.New(store, database, processor)
	if err != nil {
		t.Fatal(err)
	}
	ingested, err := ingestion.IngestPaste(ctx, `{"z":1,"a":2}`, artifactingest.Request{
		ArtifactID: "artifact_integration", OperationID: "operation_ingest", IdempotencyKey: "ingest-json",
		SourceName: "note.json", DeclaredMediaType: "application/json", Creator: "user:local",
		Sensitivity: artifactregistry.SensitivityInternal,
	})
	if err != nil {
		t.Fatal(err)
	}
	derivation, err := artifactderive.New(store, database, database, processor)
	if err != nil {
		t.Fatal(err)
	}
	request := artifactderive.Request{
		Artifact:    artifactregistry.VersionRef{ArtifactID: ingested.Artifact.ArtifactID, Version: ingested.Artifact.Version},
		OperationID: "operation_derive", IdempotencyKey: "derive-json", PolicyVersion: "artifact-context/v1alpha1",
		Limits: contentprocessor.Limits{SourceBytes: 1024, OutputBytes: 1024, Representations: 8},
	}
	first, err := derivation.Derive(ctx, request)
	if err != nil || len(first.Representations) != 2 {
		t.Fatalf("Derive() = %#v, %v", first, err)
	}
	second, err := derivation.Derive(ctx, request)
	if err != nil || len(second.Representations) != 2 || second.Representations[0].RepresentationID != first.Representations[0].RepresentationID {
		t.Fatalf("Derive(retry) = %#v, %v", second, err)
	}
	stored, err := database.ForArtifact(ctx, request.Artifact)
	if err != nil || len(stored) != 2 || stored[0].Processor.Version != common.Version {
		t.Fatalf("ForArtifact() = %#v, %v", stored, err)
	}
}
