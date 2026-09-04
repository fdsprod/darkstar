package artifactops_test

import (
	"context"
	"errors"
	"io"
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
	"darkstar/src/ports/contentprocessor"
	"darkstar/src/ports/impactassessment"
	"darkstar/src/ports/representationregistry"
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
	original, err := service.OriginalContent(ctx, reference)
	if err != nil {
		t.Fatal(err)
	}
	originalBytes, readErr := io.ReadAll(original.Reader)
	_ = original.Reader.Close()
	if readErr != nil || string(originalBytes) != `{"answer":41}` || original.Digest != first.Artifact.BlobDigest {
		t.Fatalf("OriginalContent() = %q %#v, %v", originalBytes, original, readErr)
	}
	derived, err := service.RepresentationContent(ctx, extracted.Representations[0].RepresentationID)
	if err != nil {
		t.Fatal(err)
	}
	derivedBytes, readErr := io.ReadAll(derived.Reader)
	_ = derived.Reader.Close()
	if readErr != nil || len(derivedBytes) == 0 || derived.Digest != extracted.Representations[0].Digest {
		t.Fatalf("RepresentationContent() = %q %#v, %v", derivedBytes, derived, readErr)
	}
	withheld, _, err := database.RegisterRepresentation(ctx, representationregistry.RegisterRequest{
		RepresentationID: "representation_withheld", IdempotencyKey: "withheld-view", Artifact: reference,
		Kind: contentprocessor.RepresentationDescriptor, Processor: contentprocessor.Descriptor{Name: "test", Version: "1", MediaTypes: []string{"application/json"}},
		MediaType: "application/json", Locator: extracted.Representations[0].Locator, Digest: extracted.Representations[0].Digest,
		Size: extracted.Representations[0].Size, Disclosure: representationregistry.DisclosureWithheld, Diagnostics: []string{}, Metadata: map[string]string{}, CreatedAt: first.Artifact.CreatedAt,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.RepresentationContent(ctx, withheld.RepresentationID); !errors.Is(err, artifactops.ErrContentWithheld) {
		t.Fatalf("RepresentationContent(withheld) error = %v", err)
	}
	quarantined, err := service.Ingest(ctx, artifactops.IngestInput{
		SourceKind: artifactregistry.SourcePaste, SourceName: "active.html", MediaType: "text/html",
		Content: []byte(`<script>alert("unsafe")</script>`), Roles: []string{"evidence"},
	}, "ingest-quarantined")
	if err != nil || quarantined.Artifact.Status != artifactregistry.StatusQuarantined {
		t.Fatalf("Ingest(quarantined) = %#v, %v", quarantined, err)
	}
	if _, err := service.OriginalContent(ctx, artifactregistry.VersionRef{ArtifactID: quarantined.Artifact.ArtifactID, Version: quarantined.Artifact.Version}); !errors.Is(err, artifactops.ErrContentWithheld) {
		t.Fatalf("OriginalContent(quarantined) error = %v", err)
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

	second, err := service.Revise(ctx, reference.ArtifactID, reference.Version, artifactops.IngestInput{
		SourceKind: artifactregistry.SourcePaste, SourceName: "evidence.json", MediaType: "application/json",
		Content: []byte(`{"answer":42}`), Roles: []string{"note"},
	}, "ingest-second")
	if err != nil || second.Artifact.Version != 2 {
		t.Fatalf("Revise() = %#v, %v", second, err)
	}
	if _, err := service.Revise(ctx, reference.ArtifactID, reference.Version, artifactops.IngestInput{
		SourceKind: artifactregistry.SourcePaste, SourceName: "stale.json", MediaType: "application/json",
		Content: []byte(`{"stale":true}`),
	}, "stale-revision"); !errors.Is(err, artifactregistry.ErrVersionConflict) {
		t.Fatalf("stale revision error = %v, want version conflict", err)
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
	service, err := artifactops.New(store, database, database, database, database, ingestion, derivation, impact)
	if err != nil {
		t.Fatal(err)
	}
	return service, database
}
