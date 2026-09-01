package sqlite

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	contextcore "github.com/fdsprod/darkstar/runtime/src/core/contextmanifest"
	"github.com/fdsprod/darkstar/runtime/src/ports/artifactregistry"
	"github.com/fdsprod/darkstar/runtime/src/ports/artifactstore"
	"github.com/fdsprod/darkstar/runtime/src/ports/contentprocessor"
	manifestport "github.com/fdsprod/darkstar/runtime/src/ports/contextmanifest"
	"github.com/fdsprod/darkstar/runtime/src/ports/representationregistry"
)

func TestAttemptContextManifestSelectsDeterministicallyAndFreezes(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	database, err := Open(ctx, filepath.Join(t.TempDir(), "context.db"), Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	runID, nodeID, attemptID := "run_context", "design", "attempt_context"
	seedManifestAttempt(t, ctx, database, runID, nodeID, attemptID)
	when := time.Date(2026, 9, 1, 17, 0, 0, 0, time.UTC)
	required := registerManifestRepresentation(t, ctx, database, "artifact_CTXA", "representation_required", 18, when)
	optionalEarly := registerManifestRepresentation(t, ctx, database, "artifact_CTXB", "representation_optional_early", 8, when.Add(time.Second))
	optionalLate := registerManifestRepresentation(t, ctx, database, "artifact_CTXC", "representation_optional_late", 8, when.Add(2*time.Second))
	service, err := contextcore.New(database, database)
	if err != nil {
		t.Fatal(err)
	}
	request := manifestRequest(runID, nodeID, attemptID, []contextcore.Candidate{
		{RepresentationID: optionalLate.RepresentationID, Rank: 0, Arrival: 3, State: contextcore.CandidateEligible},
		{RepresentationID: required.RepresentationID, Required: true, Rank: 0, Arrival: 1, State: contextcore.CandidateEligible},
		{RepresentationID: optionalEarly.RepresentationID, Rank: 0, Arrival: 2, State: contextcore.CandidateEligible},
	})
	manifest, created, err := service.Prepare(ctx, request)
	if err != nil || !created {
		t.Fatalf("Prepare() = %#v, %v, %v", manifest, created, err)
	}
	if len(manifest.Entries) != 2 || manifest.Entries[0].RepresentationID != required.RepresentationID || manifest.Entries[1].RepresentationID != optionalEarly.RepresentationID || manifest.UsedTokens() != 26 || manifest.Reserved != 4 {
		t.Fatalf("selected entries = %#v", manifest.Entries)
	}
	if len(manifest.Omissions) != 1 || manifest.Omissions[0] != (manifestport.Omission{RepresentationID: optionalLate.RepresentationID, Reason: manifestport.OmissionBudget}) {
		t.Fatalf("omissions = %#v", manifest.Omissions)
	}
	if len(manifest.Instructions) != 1 || len(manifest.Schemas) != 1 || len(manifest.Capabilities) != 1 || !reflect.DeepEqual(manifest.Permissions, []string{"repository.read", "workspace.write"}) || manifest.Workspace.Access != "workspace_write" {
		t.Fatalf("frozen snapshots = %#v", manifest)
	}
	repeated, created, err := service.Prepare(ctx, request)
	if err != nil || created || !reflect.DeepEqual(repeated, manifest) {
		t.Fatalf("Prepare(repeat) = %#v, %v, %v", repeated, created, err)
	}
	changed := request
	changed.Permissions = append(append([]string(nil), request.Permissions...), "network.read")
	if _, _, err := service.Prepare(ctx, changed); !errors.Is(err, manifestport.ErrFrozen) {
		t.Fatalf("Prepare(changed frozen input) error = %v", err)
	}
	if _, err := database.SQL().ExecContext(ctx, `UPDATE context_manifests SET budget = 31 WHERE attempt_id = ?`, attemptID); err == nil {
		t.Fatal("context manifest update unexpectedly succeeded")
	}
}

func TestAttemptContextManifestFailsClosedForRequiredBudget(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	database, err := Open(ctx, filepath.Join(t.TempDir(), "required-context.db"), Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	seedManifestAttempt(t, ctx, database, "run_required", "node", "attempt_required")
	representation := registerManifestRepresentation(t, ctx, database, "artifact_REQUIRED", "representation_required_budget", 18, time.Now().UTC())
	service, _ := contextcore.New(database, database)
	request := manifestRequest("run_required", "node", "attempt_required", []contextcore.Candidate{{
		RepresentationID: representation.RepresentationID, Required: true, Rank: 0, Arrival: 1, State: contextcore.CandidateEligible,
	}})
	request.Budget = 17
	if _, _, err := service.Prepare(ctx, request); !errors.Is(err, contextcore.ErrRequiredExceedsBudget) {
		t.Fatalf("Prepare() error = %v", err)
	}
	if _, err := database.ManifestForAttempt(ctx, request.AttemptID); !errors.Is(err, manifestport.ErrNotFound) {
		t.Fatalf("ManifestForAttempt() error = %v", err)
	}
}

func manifestRequest(runID, nodeID, attemptID string, candidates []contextcore.Candidate) contextcore.Request {
	return contextcore.Request{
		RunID: runID, NodeID: nodeID, AttemptID: attemptID, IdempotencyKey: "freeze-" + attemptID,
		PolicyVersion: "artifact-context/v1alpha1", Budget: 30, Reserved: 4, Candidates: candidates,
		Instructions: []manifestport.DigestRef{{ID: "instructions/default", Digest: strings.Repeat("1", 64)}},
		Schemas:      []manifestport.DigestRef{{ID: "schema/output", Digest: strings.Repeat("2", 64)}},
		Permissions:  []string{"workspace.write", "repository.read"},
		Workspace:    manifestport.Workspace{ID: "workspace/repository", Digest: strings.Repeat("3", 64), Access: "workspace_write"},
		Capabilities: []manifestport.DigestRef{{ID: "provider/codex", Digest: strings.Repeat("4", 64)}},
	}
}

func seedManifestAttempt(t *testing.T, ctx context.Context, database *Database, runID, nodeID, attemptID string) {
	t.Helper()
	_, err := database.SQL().ExecContext(ctx, `INSERT INTO attempt_projection(
		attempt_id, run_id, visit_id, node_id, scenario, provider, status, resource_version,
		last_global_position, created_at, updated_at) VALUES (?, ?, '', ?, 'context-test', 'fake', 'created', 1, 0, ?, ?)`,
		attemptID, runID, nodeID, formatTime(time.Now().UTC()), formatTime(time.Now().UTC()))
	if err != nil {
		t.Fatal(err)
	}
}

func registerManifestRepresentation(t *testing.T, ctx context.Context, database *Database, artifactID, representationID string, tokens int64, when time.Time) representationregistry.Representation {
	t.Helper()
	digest := strings.Repeat(string(rune('a'+tokens%6)), 64)
	sourceRequest := artifactRequest(artifactID, "source-"+representationID, digest)
	sourceRequest.CreatedAt = when
	artifact, _, err := database.Register(ctx, sourceRequest)
	if err != nil {
		t.Fatal(err)
	}
	representationDigest := strings.Repeat(string(rune('1'+tokens%6)), 64)
	value, _, err := database.RegisterRepresentation(ctx, representationregistry.RegisterRequest{
		RepresentationID: representationID, IdempotencyKey: "derive-" + representationID,
		Artifact: artifactregistry.VersionRef{ArtifactID: artifact.ArtifactID, Version: artifact.Version},
		Kind:     contentprocessor.RepresentationText, Processor: contentprocessor.Descriptor{Name: "common", Version: "1.0.0"},
		MediaType: "text/plain", Locator: artifactstore.Locator("sha256:" + representationDigest), Digest: representationDigest,
		Size: tokens * 4, TokenEstimate: tokens, Disclosure: representationregistry.DisclosureRaw,
		Diagnostics: []string{}, Metadata: map[string]string{}, CreatedAt: when,
	})
	if err != nil {
		t.Fatal(err)
	}
	return value
}
