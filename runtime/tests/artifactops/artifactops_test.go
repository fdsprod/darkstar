package artifactops_test

import (
	"context"
	"path/filepath"
	"reflect"
	"testing"

	"darkstar/src/adapters/artifactstore/folder"
	"darkstar/src/adapters/contentprocessor/common"
	"darkstar/src/adapters/contentprocessor/commonimage"
	"darkstar/src/adapters/statestore/sqlite"
	"darkstar/src/core/artifactderive"
	"darkstar/src/core/artifactingest"
	"darkstar/src/core/artifactops"
	"darkstar/src/core/lateevidence"
	"darkstar/src/ports/artifactbinding"
	"darkstar/src/ports/artifactregistry"
	"darkstar/src/ports/impactassessment"
)

func TestArtifactOperationsCoverIngestBindingInspectionRevisionAndImpact(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	service, database := newIntegrationService(t, ctx)

	first, err := service.Ingest(ctx, artifactops.IngestInput{
		SourceKind: artifactregistry.SourcePaste, SourceName: "evidence.json", MediaType: "application/json",
		Content: []byte(`{"answer":41}`), Roles: []string{"note"},
	}, "ingest-first")
	if err != nil || first.Artifact.Version != 1 {
		t.Fatalf("Ingest() = %#v, %v", first, err)
	}
	reference := artifactregistry.VersionRef{ArtifactID: first.Artifact.ArtifactID, Version: 1}
	extracted, err := service.Extract(ctx, reference, "extract-first")
	if err != nil || len(extracted.Representations) != 2 {
		t.Fatalf("Extract() = %#v, %v", extracted, err)
	}
	target := artifactbinding.Target{Kind: artifactbinding.TargetRun, ID: "run_scope"}
	bound, err := service.Attach(ctx, artifactops.AttachInput{Artifact: reference, Target: target}, "attach-first")
	if err != nil || bound.State != artifactbinding.StateBound {
		t.Fatalf("Attach() = %#v, %v", bound, err)
	}
	repeated, err := service.Attach(ctx, artifactops.AttachInput{Artifact: reference, Target: target}, "attach-first")
	if err != nil || repeated != bound {
		t.Fatalf("Attach(repeat) = %#v, %v", repeated, err)
	}

	listed, err := service.List(ctx, artifactops.ListInput{Target: &target})
	if err != nil || len(listed) != 1 || listed[0].Artifact.ArtifactID != reference.ArtifactID {
		t.Fatalf("List() = %#v, %v", listed, err)
	}
	lint, err := service.Lint(ctx, reference)
	if err != nil || !lint.Valid || len(lint.Issues) != 0 {
		t.Fatalf("Lint() = %#v, %v", lint, err)
	}
	assessment, err := service.Impact(ctx, lateevidence.Request{Evidence: reference, Target: target, RunID: target.ID})
	if err != nil || len(assessment.Proposals) != 1 || assessment.Proposals[0].Action() != impactassessment.ActionContinue {
		t.Fatalf("Impact() = %#v, %v", assessment, err)
	}

	second, err := service.Revise(ctx, reference.ArtifactID, artifactops.IngestInput{
		SourceKind: artifactregistry.SourcePaste, SourceName: "evidence.json", MediaType: "application/json",
		Content: []byte(`{"answer":42}`), Roles: []string{"note"},
	}, "ingest-second")
	if err != nil || second.Artifact.Version != 2 {
		t.Fatalf("Revise() = %#v, %v", second, err)
	}
	diff, err := service.Diff(ctx, reference.ArtifactID, 1, 2)
	if err != nil || !reflect.DeepEqual(diff.Changed, []string{"content"}) {
		t.Fatalf("Diff() = %#v, %v", diff, err)
	}
	if _, err := service.Detach(ctx, bound.BindingID, "detach-first"); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Detach(ctx, bound.BindingID, "detach-first"); err != nil {
		t.Fatalf("Detach(repeat) = %v", err)
	}
	if _, err := database.LatestBinding(ctx, bound.BindingID); err != nil {
		t.Fatal(err)
	}
}

func newIntegrationService(t *testing.T, ctx context.Context) (*artifactops.Service, *sqlite.Database) {
	t.Helper()
	store, err := folder.New(filepath.Join(t.TempDir(), "artifacts"))
	if err != nil {
		t.Fatal(err)
	}
	database, err := sqlite.Open(ctx, filepath.Join(t.TempDir(), "state.db"), sqlite.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	derivation, err := artifactderive.New(store, database, database, common.New(), commonimage.New())
	if err != nil {
		t.Fatal(err)
	}
	ingestion, err := artifactingest.New(store, database, derivation)
	if err != nil {
		t.Fatal(err)
	}
	impact, err := lateevidence.New(database, database, database, database, database)
	if err != nil {
		t.Fatal(err)
	}
	service, err := artifactops.New(database, database, database, database, ingestion, derivation, impact)
	if err != nil {
		t.Fatal(err)
	}
	return service, database
}
