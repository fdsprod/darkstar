package artifactops_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

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
	"darkstar/src/ports/statestore"
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

func TestArtifactMutationsAppendReplaySafeGlobalAuditEvents(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	service, database := newIntegrationService(t, ctx)
	runID := "run_01K3Z1D0000000000000000000"
	if _, err := database.Append(ctx, statestore.PendingEvent{
		SchemaVersion: 1, ID: "event_01K3Z1D0000000000000000001", AggregateType: statestore.AggregateRun,
		AggregateID: runID, ExpectedRevision: 0, Kind: "run.created", OccurredAt: testTime(),
		CorrelationID: runID, CommandID: "seed-run", Actor: statestore.Actor{Type: statestore.ActorSystem, ID: "test"},
		Data: json.RawMessage(`{"workItemId":"work_01K3Z1C1AAAAAAAAAAAAAAAAAA","workflowId":"delivery","workflowVersion":"1"}`), Metadata: json.RawMessage(`{}`),
	}); err != nil {
		t.Fatal(err)
	}

	input := artifactops.IngestInput{
		SourceKind: artifactregistry.SourcePaste, SourceName: "private-evidence.txt", MediaType: "text/plain",
		Content: []byte("secret source bytes must not enter the event log"), Roles: []string{"evidence"},
	}
	first, err := service.Ingest(ctx, input, "audit-ingest")
	if err != nil {
		t.Fatal(err)
	}
	reference := artifactregistry.VersionRef{ArtifactID: first.Artifact.ArtifactID, Version: first.Artifact.Version}
	if _, err := service.Extract(ctx, reference, "audit-extract"); err != nil {
		t.Fatal(err)
	}
	target := artifactbinding.Target{Kind: artifactbinding.TargetRun, ID: runID}
	attached, err := service.Attach(ctx, artifactops.AttachInput{Artifact: reference, Target: target}, "audit-attach")
	if err != nil {
		t.Fatal(err)
	}
	revisionInput := input
	revisionInput.Content = []byte("revised secret source bytes")
	second, err := service.Revise(ctx, reference.ArtifactID, reference.Version, revisionInput, "audit-revise")
	if err != nil {
		t.Fatal(err)
	}

	events, err := database.EventsAfter(ctx, 1, 20)
	if err != nil {
		t.Fatal(err)
	}
	wantKinds := []string{"artifact.ingested", "artifact.extracted", "artifact.attached", "artifact.revised"}
	if len(events) != len(wantKinds) {
		t.Fatalf("artifact audit event count = %d, want %d: %#v", len(events), len(wantKinds), events)
	}
	for index, event := range events {
		if event.Kind != wantKinds[index] || event.AggregateType != statestore.AggregateOperation || event.AggregateRevision != 1 {
			t.Errorf("event %d = %s/%s@%d, want %s/operation@1", index, event.Kind, event.AggregateType, event.AggregateRevision, wantKinds[index])
		}
		if event.Actor != (statestore.Actor{Type: statestore.ActorUser, ID: "local-user"}) {
			t.Errorf("event %d actor = %#v", index, event.Actor)
		}
		if len(event.AggregateID) <= len("operation_") || event.AggregateID[:len("operation_")] != "operation_" {
			t.Errorf("event %d aggregate ID = %q", index, event.AggregateID)
		}
		encoded := string(event.Data)
		for _, forbidden := range []string{"secret source bytes", "revised secret", "locator", "private-evidence.txt"} {
			if strings.Contains(encoded, forbidden) {
				t.Errorf("event %d leaked %q in %s", index, forbidden, encoded)
			}
		}
	}
	if events[2].CorrelationID != runID {
		t.Errorf("attach correlation = %q, want owning run %q", events[2].CorrelationID, runID)
	}
	var revisedData struct {
		Artifact    artifactregistry.VersionRef `json:"artifact"`
		BaseVersion uint64                      `json:"baseVersion"`
	}
	if err := json.Unmarshal(events[3].Data, &revisedData); err != nil || revisedData.BaseVersion != reference.Version || revisedData.Artifact.Version != second.Artifact.Version {
		t.Fatalf("revised event data = %#v, %v", revisedData, err)
	}
	evidence, err := database.RunEvidence(ctx, runID)
	if err != nil {
		t.Fatal(err)
	}
	if len(evidence.Events) != 2 || evidence.Events[1].Kind != "artifact.attached" {
		t.Fatalf("run evidence events = %#v", evidence.Events)
	}

	if _, err := service.Ingest(ctx, input, "audit-ingest"); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Extract(ctx, reference, "audit-extract"); err != nil {
		t.Fatal(err)
	}
	if repeated, err := service.Attach(ctx, artifactops.AttachInput{BindingID: attached.BindingID, Artifact: reference, Target: target}, "audit-attach"); err != nil || repeated != attached {
		t.Fatalf("replayed attach = %#v, %v", repeated, err)
	}
	if _, err := service.Revise(ctx, reference.ArtifactID, reference.Version, revisionInput, "audit-revise"); err != nil {
		t.Fatal(err)
	}
	afterReplay, err := database.EventsAfter(ctx, 1, 20)
	if err != nil || len(afterReplay) != len(events) {
		t.Fatalf("events after exact replay = %d, %v; want %d", len(afterReplay), err, len(events))
	}
}

func testTime() time.Time {
	return time.Date(2026, time.September, 4, 12, 0, 0, 0, time.UTC)
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
	service, err := artifactops.New(store, database, database, database, database, database, ingestion, derivation, impact)
	if err != nil {
		t.Fatal(err)
	}
	return service, database
}
