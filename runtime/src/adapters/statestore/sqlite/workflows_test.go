package sqlite

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"darkstar/src/core/config"
	"darkstar/src/core/workflow"
	"darkstar/src/ports/statestore"
	"darkstar/src/ports/workflowstore"
)

func TestWorkflowInstallationAndRunSnapshotsAreImmutable(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	database, err := Open(ctx, filepath.Join(t.TempDir(), "workflows.db"), Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })

	source := &workflowCandidates{values: []workflowstore.Candidate{
		{Scope: workflowstore.ScopeDefault, Reference: "built-in/delivery.json", Content: json.RawMessage(testWorkflow("default"))},
		{Scope: workflowstore.ScopeProject, Reference: ".darkstar/workflows/delivery.json", Content: json.RawMessage(testWorkflow("project"))},
	}}
	catalog, err := workflow.NewCatalog(source, database)
	if err != nil {
		t.Fatal(err)
	}
	results, err := catalog.InstallConfigured(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || results[0].Disposition != workflow.InstallCreated || results[0].Version.SourceScope != workflowstore.ScopeProject {
		t.Fatalf("installation results = %#v", results)
	}
	repeated, err := catalog.InstallConfigured(ctx)
	if err != nil || len(repeated) != 1 || repeated[0].Disposition != workflow.InstallAlreadyInstalled {
		t.Fatalf("repeated installation = %#v, %v", repeated, err)
	}

	source.values[1].Content = json.RawMessage(testWorkflow("changed-project"))
	if _, err := catalog.InstallConfigured(ctx); !errors.Is(err, workflowstore.ErrVersionConflict) {
		t.Fatalf("changed reinstall error = %v, want version conflict", err)
	}
	source.values[1].Content = json.RawMessage(testWorkflow("project"))

	runID := testID("run", 'W')
	if _, err := database.Append(ctx, pendingEvent(testID("event", 'W'), statestore.AggregateRun, runID, 0, "run.created",
		`{"workItemId":"`+testID("work", 'W')+`","workflowId":"delivery","workflowVersion":"1.0.0"}`)); err != nil {
		t.Fatal(err)
	}
	defaults, _ := config.Defaults(map[string]any{"provider": "fake", "timeout": 10})
	project, _ := config.ProjectFile(filepath.Join(t.TempDir(), "config.yaml"), map[string]any{"timeout": 30})
	effective, err := config.Resolve(defaults, project)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, created, err := catalog.SnapshotRun(ctx, runID, "delivery", "1.0.0", effective)
	if err != nil || !created {
		t.Fatalf("SnapshotRun() = created %v, error %v", created, err)
	}
	if snapshot.WorkflowDigest != results[0].Version.Digest || !strings.Contains(string(snapshot.ConfigSnapshot), `"scope":"project"`) {
		t.Fatalf("snapshot = %#v", snapshot)
	}
	if _, created, err := catalog.SnapshotRun(ctx, runID, "delivery", "1.0.0", effective); err != nil || created {
		t.Fatalf("repeat SnapshotRun() = created %v, error %v", created, err)
	}

	changed, _ := config.CLIOverride(map[string]any{"timeout": 5})
	changedEffective, _ := config.Resolve(defaults, project, changed)
	if _, _, err := catalog.SnapshotRun(ctx, runID, "delivery", "1.0.0", changedEffective); !errors.Is(err, workflowstore.ErrRunSnapshotConflict) {
		t.Fatalf("changed snapshot error = %v, want run snapshot conflict", err)
	}
	if _, err := database.SQL().ExecContext(ctx, `UPDATE workflow_versions SET source_reference = 'changed'`); err == nil {
		t.Fatal("workflow version update unexpectedly succeeded")
	}
	if _, err := database.SQL().ExecContext(ctx, `DELETE FROM run_workflow_snapshots WHERE run_id = ?`, runID); err == nil {
		t.Fatal("run snapshot delete unexpectedly succeeded")
	}
}

func TestRunSnapshotRequiresExistingRunAggregate(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	database, err := Open(ctx, filepath.Join(t.TempDir(), "missing-run.db"), Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	document, canonical, digest, err := workflow.Canonicalize([]byte(testWorkflow("installed")))
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = database.Install(ctx, workflowstore.InstallRequest{
		Name: document.Metadata.Name, Version: document.Metadata.Version, Digest: digest, Document: canonical,
		SourceScope: workflowstore.ScopeUser, SourceRef: "user/workflow.json", InstalledAt: time.Now().UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}
	configSnapshot := json.RawMessage(`{"values":{},"sources":{}}`)
	configHash := sha256.Sum256(configSnapshot)
	_, _, err = database.CreateRunSnapshot(ctx, workflowstore.RunSnapshotRequest{
		RunID: testID("run", 'M'), WorkflowName: document.Metadata.Name, WorkflowVersion: document.Metadata.Version,
		WorkflowDigest: digest, WorkflowDocument: canonical, ConfigDigest: hex.EncodeToString(configHash[:]),
		ConfigSnapshot: configSnapshot, CreatedAt: time.Now().UTC(),
	})
	if !errors.Is(err, workflowstore.ErrNotFound) {
		t.Fatalf("CreateRunSnapshot() error = %v, want not found", err)
	}
}

type workflowCandidates struct {
	values []workflowstore.Candidate
}

func (s *workflowCandidates) Load(context.Context) ([]workflowstore.Candidate, error) {
	return append([]workflowstore.Candidate(nil), s.values...), nil
}

func testWorkflow(description string) string {
	return `{"apiVersion":"darkstar.local/v1alpha1","kind":"Workflow","metadata":{"name":"delivery","version":"1.0.0","description":"` + description + `"},"spec":{"routeDefaults":{"entry":"finish","terminals":["finish"]},"nodes":{"finish":{"type":"reasoning","entry":true,"terminal":true,"inputs":{},"outputs":{},"reasoning":{"agent":"fake"},"checkpoint":{"mode":"none"},"transitions":[]}}}}`
}
