package sqlite

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"darkstar/src/ports/artifactbinding"
	"darkstar/src/ports/artifactregistry"
)

func TestArtifactBindingsRetainVersionedHistoryForEveryTarget(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	database, err := Open(ctx, filepath.Join(t.TempDir(), "artifact-bindings.db"), Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })

	createdAt := time.Date(2026, time.September, 1, 14, 0, 0, 0, time.UTC)
	artifactID := testID("artifact", 'L')
	first := registerLineageArtifact(t, ctx, database, artifactID, "binding-artifact-1", "a", createdAt)
	revisionRequest := artifactRequest(artifactID, "binding-artifact-2", strings.Repeat("b", 64))
	revisionRequest.CreatedAt = createdAt.Add(time.Second)
	secondVersion, _, err := database.Register(ctx, revisionRequest)
	if err != nil {
		t.Fatal(err)
	}
	second := artifactregistry.VersionRef{ArtifactID: artifactID, Version: secondVersion.Version}

	kinds := []artifactbinding.TargetKind{
		artifactbinding.TargetProject,
		artifactbinding.TargetWork,
		artifactbinding.TargetRun,
		artifactbinding.TargetNode,
		artifactbinding.TargetCheckpoint,
		artifactbinding.TargetDecision,
		artifactbinding.TargetStory,
		artifactbinding.TargetImplementationPoint,
	}
	for index, kind := range kinds {
		bindingID := testID("binding", rune('A'+index))
		target := artifactbinding.Target{Kind: kind, ID: string(kind) + "-target"}
		boundAt := createdAt.Add(time.Duration(index+1) * time.Minute)
		bind := artifactbinding.BindRequest{
			BindingID: bindingID, IdempotencyKey: "bind-1", Artifact: first, Target: target, CreatedAt: boundAt,
		}
		firstBinding, created, err := database.Bind(ctx, bind)
		if err != nil || !created || firstBinding.Version != 1 || firstBinding.State != artifactbinding.StateBound {
			t.Fatalf("Bind(%s) = %#v, created %v, error %v", kind, firstBinding, created, err)
		}
		repeated, created, err := database.Bind(ctx, bind)
		if err != nil || created || repeated != firstBinding {
			t.Fatalf("Bind(%s repeat) = %#v, created %v, error %v", kind, repeated, created, err)
		}
		if _, _, err := database.Bind(ctx, artifactbinding.BindRequest{
			BindingID: bindingID, IdempotencyKey: "bind-while-bound", Artifact: second, Target: target, CreatedAt: boundAt.Add(time.Second),
		}); !errors.Is(err, artifactbinding.ErrStateConflict) {
			t.Fatalf("Bind(%s while bound) error = %v, want state conflict", kind, err)
		}

		unbind := artifactbinding.UnbindRequest{BindingID: bindingID, IdempotencyKey: "unbind-2", CreatedAt: boundAt.Add(time.Minute)}
		unbound, created, err := database.Unbind(ctx, unbind)
		if err != nil || !created || unbound.Version != 2 || unbound.State != artifactbinding.StateUnbound {
			t.Fatalf("Unbind(%s) = %#v, created %v, error %v", kind, unbound, created, err)
		}
		repeatedUnbind, created, err := database.Unbind(ctx, unbind)
		if err != nil || created || repeatedUnbind != unbound {
			t.Fatalf("Unbind(%s repeat) = %#v, created %v, error %v", kind, repeatedUnbind, created, err)
		}

		rebound, created, err := database.Bind(ctx, artifactbinding.BindRequest{
			BindingID: bindingID, IdempotencyKey: "rebind-3", Artifact: second, Target: target, CreatedAt: boundAt.Add(2 * time.Minute),
		})
		if err != nil || !created || rebound.Version != 3 || rebound.State != artifactbinding.StateBound || rebound.Artifact != second {
			t.Fatalf("Bind(%s revision) = %#v, created %v, error %v", kind, rebound, created, err)
		}
		history, err := database.BindingVersions(ctx, bindingID)
		if err != nil || len(history) != 3 || history[0].State != artifactbinding.StateBound || history[1].State != artifactbinding.StateUnbound || history[2].State != artifactbinding.StateBound {
			t.Fatalf("BindingVersions(%s) = %#v, %v", kind, history, err)
		}
		exact, err := database.BindingVersion(ctx, bindingID, 1)
		if err != nil || exact != firstBinding {
			t.Fatalf("BindingVersion(%s, 1) = %#v, %v", kind, exact, err)
		}
		latest, err := database.LatestBinding(ctx, bindingID)
		if err != nil || latest != rebound {
			t.Fatalf("LatestBinding(%s) = %#v, %v", kind, latest, err)
		}
		active, err := database.ActiveBindings(ctx, target)
		if err != nil || len(active) != 1 || active[0] != rebound {
			t.Fatalf("ActiveBindings(%s) = %#v, %v", kind, active, err)
		}
	}

	if _, err := database.SQL().ExecContext(ctx, `UPDATE artifact_binding_versions SET state = 'unbound'`); err == nil {
		t.Fatal("artifact binding update unexpectedly succeeded")
	}
	if _, err := database.SQL().ExecContext(ctx, `DELETE FROM artifact_binding_versions`); err == nil {
		t.Fatal("artifact binding deletion unexpectedly succeeded")
	}
}

func TestArtifactBindingRejectsIdentityAndTransitionConflicts(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	database, err := Open(ctx, filepath.Join(t.TempDir(), "artifact-binding-conflicts.db"), Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	createdAt := time.Date(2026, time.September, 1, 15, 0, 0, 0, time.UTC)
	first := registerLineageArtifact(t, ctx, database, testID("artifact", 'M'), "first", "c", createdAt)
	other := registerLineageArtifact(t, ctx, database, testID("artifact", 'N'), "other", "d", createdAt)
	bindingID := testID("binding", 'Z')
	target := artifactbinding.Target{Kind: artifactbinding.TargetStory, ID: "story-1"}
	if _, _, err := database.Unbind(ctx, artifactbinding.UnbindRequest{BindingID: bindingID, IdempotencyKey: "missing", CreatedAt: createdAt}); !errors.Is(err, artifactbinding.ErrNotFound) {
		t.Fatalf("Unbind(missing) error = %v, want not found", err)
	}
	if _, _, err := database.Bind(ctx, artifactbinding.BindRequest{BindingID: bindingID, IdempotencyKey: "bind", Artifact: first, Target: target, CreatedAt: createdAt}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := database.Unbind(ctx, artifactbinding.UnbindRequest{BindingID: bindingID, IdempotencyKey: "unbind", CreatedAt: createdAt.Add(time.Second)}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := database.Unbind(ctx, artifactbinding.UnbindRequest{BindingID: bindingID, IdempotencyKey: "unbind-again", CreatedAt: createdAt.Add(2 * time.Second)}); !errors.Is(err, artifactbinding.ErrStateConflict) {
		t.Fatalf("Unbind(again) error = %v, want state conflict", err)
	}
	if _, _, err := database.Bind(ctx, artifactbinding.BindRequest{BindingID: bindingID, IdempotencyKey: "other-artifact", Artifact: other, Target: target, CreatedAt: createdAt.Add(3 * time.Second)}); !errors.Is(err, artifactbinding.ErrConflict) {
		t.Fatalf("Bind(other artifact) error = %v, want conflict", err)
	}
	if _, err := database.LatestBinding(ctx, testID("binding", 'Y')); !errors.Is(err, artifactbinding.ErrNotFound) {
		t.Fatalf("LatestBinding(missing) error = %v, want not found", err)
	}
}
