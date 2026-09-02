package sqlite

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fdsprod/darkstar/runtime/src/ports/artifactlineage"
	"github.com/fdsprod/darkstar/runtime/src/ports/artifactregistry"
)

func TestArtifactRevisionInvalidatesOnlyReachableDescendants(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	database, err := Open(ctx, filepath.Join(t.TempDir(), "artifact-lineage.db"), Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })

	createdAt := time.Date(2026, time.September, 1, 13, 0, 0, 0, time.UTC)
	upstream := registerLineageArtifact(t, ctx, database, testID("artifact", 'G'), "upstream-1", "a", createdAt)
	stale := registerLineageArtifact(t, ctx, database, testID("artifact", 'H'), "stale-1", "b", createdAt)
	invalid := registerLineageArtifact(t, ctx, database, testID("artifact", 'I'), "invalid-1", "c", createdAt)
	directInvalid := registerLineageArtifact(t, ctx, database, testID("artifact", 'J'), "direct-1", "d", createdAt)
	unrelated := registerLineageArtifact(t, ctx, database, testID("artifact", 'K'), "unrelated-1", "e", createdAt)

	edges := []artifactlineage.AddRequest{
		{Source: upstream, Dependent: stale, Impact: artifactlineage.ImpactPotentiallyStale, CreatedAt: createdAt.Add(time.Second)},
		{Source: stale, Dependent: invalid, Impact: artifactlineage.ImpactInvalidated, CreatedAt: createdAt.Add(2 * time.Second)},
		{Source: upstream, Dependent: directInvalid, Impact: artifactlineage.ImpactInvalidated, CreatedAt: createdAt.Add(3 * time.Second)},
	}
	for _, edge := range edges {
		dependency, created, err := database.AddDependency(ctx, edge)
		if err != nil || !created || dependency.Source != edge.Source || dependency.Dependent != edge.Dependent {
			t.Fatalf("AddDependency(%#v) = %#v, created %v, error %v", edge, dependency, created, err)
		}
		repeated, created, err := database.AddDependency(ctx, edge)
		if err != nil || created || repeated != dependency {
			t.Fatalf("AddDependency(repeat) = %#v, created %v, error %v", repeated, created, err)
		}
	}

	conflict := edges[0]
	conflict.Impact = artifactlineage.ImpactInvalidated
	if _, _, err := database.AddDependency(ctx, conflict); !errors.Is(err, artifactlineage.ErrDependencyConflict) {
		t.Fatalf("AddDependency(conflict) error = %v, want dependency conflict", err)
	}
	cycle := artifactlineage.AddRequest{Source: invalid, Dependent: upstream, Impact: artifactlineage.ImpactInvalidated, CreatedAt: createdAt.Add(4 * time.Second)}
	if _, _, err := database.AddDependency(ctx, cycle); !errors.Is(err, artifactlineage.ErrDependencyCycle) {
		t.Fatalf("AddDependency(cycle) error = %v, want dependency cycle", err)
	}

	revisionRequest := artifactRequest(upstream.ArtifactID, "upstream-2", strings.Repeat("f", 64))
	revisionRequest.CreatedAt = createdAt.Add(time.Minute)
	revision, created, err := database.Register(ctx, revisionRequest)
	if err != nil || !created || revision.Version != 2 {
		t.Fatalf("Register(revision) = %#v, created %v, error %v", revision, created, err)
	}

	wants := map[artifactregistry.VersionRef]artifactlineage.Freshness{
		upstream:      artifactlineage.FreshnessCurrent,
		stale:         artifactlineage.FreshnessPotentiallyStale,
		invalid:       artifactlineage.FreshnessInvalidated,
		directInvalid: artifactlineage.FreshnessInvalidated,
		unrelated:     artifactlineage.FreshnessCurrent,
	}
	for reference, want := range wants {
		got, err := database.Freshness(ctx, reference)
		if err != nil || got != want {
			t.Errorf("Freshness(%#v) = %q, %v, want %q", reference, got, err, want)
		}
	}
	invalidations, err := database.Invalidations(ctx, invalid)
	if err != nil || len(invalidations) != 1 || invalidations[0].Trigger != (artifactregistry.VersionRef{ArtifactID: upstream.ArtifactID, Version: 2}) || invalidations[0].Freshness != artifactlineage.FreshnessInvalidated {
		t.Fatalf("Invalidations(invalid) = %#v, %v", invalidations, err)
	}
	affected, err := database.AffectedBy(ctx, artifactregistry.VersionRef{ArtifactID: upstream.ArtifactID, Version: 2})
	if err != nil || len(affected) != 3 || affected[0].Freshness != artifactlineage.FreshnessInvalidated || affected[2].Freshness != artifactlineage.FreshnessPotentiallyStale {
		t.Fatalf("AffectedBy(revision) = %#v, %v", affected, err)
	}
	inputs, err := database.Dependencies(ctx, invalid)
	if err != nil || len(inputs) != 1 || inputs[0].Source != stale {
		t.Fatalf("Dependencies(invalid) = %#v, %v", inputs, err)
	}
	outputs, err := database.Dependents(ctx, upstream)
	if err != nil || len(outputs) != 2 {
		t.Fatalf("Dependents(upstream) = %#v, %v", outputs, err)
	}

	var artifactCount int
	if err := database.SQL().QueryRowContext(ctx, `SELECT count(*) FROM artifact_versions`).Scan(&artifactCount); err != nil || artifactCount != 6 {
		t.Fatalf("artifact version count = %d, %v, want 6", artifactCount, err)
	}
	if _, err := database.ArtifactVersion(ctx, stale); err != nil {
		t.Fatalf("invalidated descendant was not retained: %v", err)
	}
	if _, err := database.SQL().ExecContext(ctx, `DELETE FROM artifact_dependencies`); err == nil {
		t.Fatal("artifact dependency deletion unexpectedly succeeded")
	}
	if _, err := database.SQL().ExecContext(ctx, `UPDATE artifact_invalidations SET freshness = 'potentially_stale'`); err == nil {
		t.Fatal("artifact invalidation update unexpectedly succeeded")
	}
}

func registerLineageArtifact(t *testing.T, ctx context.Context, database *Database, artifactID, key, digestCharacter string, createdAt time.Time) artifactregistry.VersionRef {
	t.Helper()
	request := artifactRequest(artifactID, key, strings.Repeat(digestCharacter, 64))
	request.CreatedAt = createdAt
	version, created, err := database.Register(ctx, request)
	if err != nil || !created {
		t.Fatalf("Register(%s) = %#v, created %v, error %v", artifactID, version, created, err)
	}
	return artifactregistry.VersionRef{ArtifactID: version.ArtifactID, Version: version.Version}
}
