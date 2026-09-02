package sqlite

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	registryport "darkstar/src/ports/capabilityregistry"
)

func TestCapabilityRegistryPersistsImmutableNamespacedRecords(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	database, err := Open(ctx, filepath.Join(t.TempDir(), "capabilities.db"), Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	record := registryport.Record{
		SchemaVersion: 1, ID: "cap_project_review_v1", Name: "project:review", Kind: registryport.KindSkill,
		Class: registryport.ClassRegistered, DeclaredVersion: "1.0.0", Fingerprint: strings.Repeat("a", 64),
		Source:       registryport.Source{Type: "skill_path", Locator: ".agents/skills/review/SKILL.md"},
		Interfaces:   registryport.Interfaces{Inputs: strings.Repeat("b", 64), Outputs: strings.Repeat("c", 64)},
		Dependencies: []string{"darkstar:artifact.read"}, Risk: registryport.Risk{Reads: true},
		Availability: registryport.AvailabilityAvailable, ObservedAt: time.Date(2026, 9, 2, 1, 2, 3, 0, time.UTC),
	}
	created, added, err := database.RegisterCapability(ctx, record, "register-review-v1")
	if err != nil || !added || !reflect.DeepEqual(created, record) {
		t.Fatalf("Register() = %#v, %v, %v", created, added, err)
	}
	repeated, added, err := database.RegisterCapability(ctx, record, "register-review-v1")
	if err != nil || added || !reflect.DeepEqual(repeated, record) {
		t.Fatalf("Register(repeat) = %#v, %v, %v", repeated, added, err)
	}
	loaded, err := database.Capability(ctx, record.ID)
	if err != nil || !reflect.DeepEqual(loaded, record) {
		t.Fatalf("Capability() = %#v, %v", loaded, err)
	}
	snapshot, err := database.Snapshot(ctx)
	if err != nil || len(snapshot) != 1 || !reflect.DeepEqual(snapshot[0], record) {
		t.Fatalf("Snapshot() = %#v, %v", snapshot, err)
	}
	if _, err := database.SQL().ExecContext(ctx, `UPDATE capability_records SET availability = 'unhealthy' WHERE capability_id = ?`, record.ID); err == nil {
		t.Fatal("capability record update unexpectedly succeeded")
	}
}

func TestCapabilityRegistryRejectsSameClassShadowing(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	database, err := Open(ctx, filepath.Join(t.TempDir(), "shadow.db"), Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	base := registryport.Record{SchemaVersion: 1, ID: "cap_a", Name: "project:review", Kind: registryport.KindSkill, Class: registryport.ClassRegistered, Fingerprint: strings.Repeat("a", 64), Source: registryport.Source{Type: "test", Locator: "a"}, Availability: registryport.AvailabilityAvailable, ObservedAt: time.Now().UTC()}
	if _, _, err := database.RegisterCapability(ctx, base, "a"); err != nil {
		t.Fatal(err)
	}
	base.ID, base.Fingerprint, base.Source.Locator = "cap_b", strings.Repeat("b", 64), "b"
	if _, _, err := database.RegisterCapability(ctx, base, "b"); !errors.Is(err, registryport.ErrConflict) {
		t.Fatalf("error = %v", err)
	}
}
