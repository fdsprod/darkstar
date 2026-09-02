package common_test

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"darkstar/src/adapters/artifactstore/folder"
	"darkstar/src/adapters/contentprocessor/common"
	"darkstar/src/adapters/statestore/sqlite"
	"darkstar/src/core/artifactderive"
	"darkstar/src/core/artifactingest"
	"darkstar/src/core/artifactsafety"
	"darkstar/src/ports/artifactregistry"
	"darkstar/src/ports/contentprocessor"
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

func TestDerivationEnforcesProcessorTimeout(t *testing.T) {
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
	processor := slowProcessor{}
	ingestion, err := artifactingest.New(store, database, processor)
	if err != nil {
		t.Fatal(err)
	}
	ingested, err := ingestion.IngestPaste(ctx, "wait", artifactingest.Request{
		ArtifactID: "artifact_timeout", OperationID: "operation_ingest_timeout", IdempotencyKey: "ingest-timeout",
		SourceName: "wait.txt", Creator: "user:local", Sensitivity: artifactregistry.SensitivityInternal,
	})
	if err != nil {
		t.Fatal(err)
	}
	policy := artifactsafety.DefaultPolicy()
	policy.ProcessorWallTime = 10 * time.Millisecond
	derivation, err := artifactderive.NewWithPolicy(store, database, database, policy, processor)
	if err != nil {
		t.Fatal(err)
	}
	_, err = derivation.Derive(ctx, artifactderive.Request{
		Artifact:    artifactregistry.VersionRef{ArtifactID: ingested.Artifact.ArtifactID, Version: ingested.Artifact.Version},
		OperationID: "operation_derive_timeout", IdempotencyKey: "derive-timeout", PolicyVersion: "artifact-context/v1alpha1",
	})
	if !errors.Is(err, artifactderive.ErrProcessorTimeout) {
		t.Fatalf("Derive() error = %v", err)
	}
}

type slowProcessor struct{}

func (slowProcessor) Descriptor() contentprocessor.Descriptor {
	return contentprocessor.Descriptor{Name: "slow", Version: "1.0.0", MediaTypes: []string{"text/plain"}}
}

func (slowProcessor) Supports(_ context.Context, source contentprocessor.SourceDescriptor) (contentprocessor.Support, error) {
	return contentprocessor.Support{State: contentprocessor.SupportSupported, MediaType: source.DetectedMediaType}, nil
}

func (slowProcessor) Process(ctx context.Context, _ contentprocessor.ProcessRequest, _ contentprocessor.Sink) (contentprocessor.ProcessResult, error) {
	<-ctx.Done()
	return contentprocessor.ProcessResult{}, ctx.Err()
}
