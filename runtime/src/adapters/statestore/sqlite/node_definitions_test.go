package sqlite_test

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"darkstar/src/adapters/statestore/sqlite"
	"darkstar/src/ports/workflowstore"
)

func TestNodeDefinitionsPersistImmutableVersionsAndArchiveDiscoveryState(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "definitions.db")
	db, err := sqlite.Open(ctx, path, sqlite.Options{})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 9, 7, 1, 2, 3, 0, time.UTC)
	record := workflowstore.NodeDefinitionRecord{Scope: workflowstore.DraftScopeProject, Owner: "project-1", Name: "review/check", Version: "1.0.0", Digest: strings.Repeat("a", 64), Document: json.RawMessage(`{"name":"check"}`), CreatedAt: now}
	stored, created, err := db.InstallNodeDefinition(ctx, record)
	if err != nil || !created || stored.Digest != record.Digest {
		t.Fatalf("install = %#v %v %v", stored, created, err)
	}
	_, created, err = db.InstallNodeDefinition(ctx, record)
	if err != nil || created {
		t.Fatalf("idempotent = %v %v", created, err)
	}
	conflict := record
	conflict.Digest = strings.Repeat("b", 64)
	if _, _, err = db.InstallNodeDefinition(ctx, conflict); !errors.Is(err, workflowstore.ErrNodeDefinitionConflict) {
		t.Fatalf("conflict = %v", err)
	}
	archived, changed, err := db.ArchiveNodeDefinition(ctx, record.Scope, record.Owner, record.Name, record.Version, now.Add(time.Hour))
	if err != nil || !changed || archived.ArchivedAt == nil {
		t.Fatalf("archive = %#v %v %v", archived, changed, err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := sqlite.Open(ctx, path, sqlite.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	values, err := reopened.NodeDefinitions(ctx)
	if err != nil || len(values) != 1 || values[0].ArchivedAt == nil {
		t.Fatalf("reopened = %#v %v", values, err)
	}
}
