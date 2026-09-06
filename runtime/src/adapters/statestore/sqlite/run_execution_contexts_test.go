package sqlite

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"darkstar/src/ports/statestore"
)

func TestRunExecutionContextRoundTripAndCAS(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	database := openEventTestDatabase(t)
	runID := testID("run", 'R')
	createRunAggregate(t, database, runID)

	original := executionContextFixture(runID)
	created, err := database.SaveRunExecutionContext(ctx, original, 0)
	if err != nil {
		t.Fatalf("create execution context: %v", err)
	}
	if created.Revision != 1 || len(created.Digest) != 64 {
		t.Fatalf("created metadata = revision %d digest %q", created.Revision, created.Digest)
	}
	if string(created.FrameSnapshot) == "" || !strings.Contains(string(created.FrameSnapshot), `"tokens"`) {
		t.Fatalf("frame snapshot did not preserve tokens: %s", created.FrameSnapshot)
	}

	// Returned and caller-owned JSON are copies of the durable bytes.
	original.RunInputs["repository"][1] = 'X'
	created.AcceptedOutputs["readiness"]["status"][1] = 'X'
	loaded, err := database.RunExecutionContext(ctx, runID)
	if err != nil {
		t.Fatalf("read execution context: %v", err)
	}
	if string(loaded.RunInputs["repository"]) != `"C:/repo"` || string(loaded.AcceptedOutputs["readiness"]["status"]) != `"ready"` {
		t.Fatalf("durable context was mutated through caller bytes: %#v", loaded)
	}

	loaded.AcceptedOutputs["implementation"] = map[string]json.RawMessage{"result": json.RawMessage(`{"changed":true}`)}
	updated, err := database.SaveRunExecutionContext(ctx, loaded, 1)
	if err != nil {
		t.Fatalf("advance execution context: %v", err)
	}
	if updated.Revision != 2 || updated.Digest == loaded.Digest {
		t.Fatalf("updated metadata = revision %d digest %q", updated.Revision, updated.Digest)
	}
	if _, err := database.SaveRunExecutionContext(ctx, loaded, 1); !errors.Is(err, statestore.ErrRunExecutionContextRevisionConflict) {
		t.Fatalf("stale save error = %v, want revision conflict", err)
	}
	updated.RunInputs["repository"] = json.RawMessage(`"C:/another"`)
	if _, err := database.SaveRunExecutionContext(ctx, updated, 2); err == nil || !strings.Contains(err.Error(), "immutable") {
		t.Fatalf("changed run inputs error = %v, want immutable rejection", err)
	}
}

func TestRunExecutionContextRejectsInvalidAndCorruptSnapshots(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	database := openEventTestDatabase(t)
	runID := testID("run", 'S')
	createRunAggregate(t, database, runID)

	invalid := executionContextFixture(runID)
	invalid.SchemaVersion = 2
	if _, err := database.SaveRunExecutionContext(ctx, invalid, 0); err == nil || !strings.Contains(err.Error(), "unsupported") {
		t.Fatalf("future schema error = %v", err)
	}
	invalid = executionContextFixture(runID)
	invalid.FrameSnapshot = json.RawMessage(`[]`)
	if _, err := database.SaveRunExecutionContext(ctx, invalid, 0); err == nil || !strings.Contains(err.Error(), "object") {
		t.Fatalf("non-object frame error = %v", err)
	}
	invalid = executionContextFixture(runID)
	invalid.RunInputs["repository"] = json.RawMessage(`{"broken"`)
	if _, err := database.SaveRunExecutionContext(ctx, invalid, 0); err == nil || !strings.Contains(err.Error(), "invalid JSON") {
		t.Fatalf("invalid input error = %v", err)
	}

	created, err := database.SaveRunExecutionContext(ctx, executionContextFixture(runID), 0)
	if err != nil {
		t.Fatalf("create valid execution context: %v", err)
	}
	if _, err := database.SQL().ExecContext(ctx, `UPDATE run_execution_contexts SET accepted_outputs_json = '{"readiness":{"status":"corrupt"}}' WHERE run_id = ?`, runID); err != nil {
		t.Fatalf("tamper execution context: %v", err)
	}
	if _, err := database.RunExecutionContext(ctx, runID); err == nil || !strings.Contains(err.Error(), "integrity") {
		t.Fatalf("tampered read error = %v", err)
	}
	if _, err := database.SQL().ExecContext(ctx, `DELETE FROM run_execution_contexts WHERE run_id = ?`, runID); err == nil {
		t.Fatal("durable context delete unexpectedly succeeded")
	}
	if created.Revision != 1 {
		t.Fatalf("created revision = %d", created.Revision)
	}
}

func TestRunExecutionContextMigrationAndProjectionRebuildCompatibility(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "upgrade.db")
	db, err := openSQLite(path, Options{})
	if err != nil {
		t.Fatalf("open legacy database: %v", err)
	}
	migrations, err := embeddedMigrationSet()
	if err != nil {
		t.Fatalf("load migrations: %v", err)
	}
	if err := migrate(ctx, db, migrations[:19], fixedNow); err != nil {
		t.Fatalf("migrate through v19: %v", err)
	}
	assertTableCount(t, db, "run_execution_contexts", 0)
	if err := migrate(ctx, db, migrations, fixedNow); err != nil {
		t.Fatalf("apply execution context migration: %v", err)
	}
	assertTableCount(t, db, "run_execution_contexts", 1)
	if err := db.Close(); err != nil {
		t.Fatalf("close upgraded database: %v", err)
	}

	database, err := Open(ctx, path, Options{})
	if err != nil {
		t.Fatalf("open upgraded database: %v", err)
	}
	database.now = func() time.Time { return eventTestTime }
	t.Cleanup(func() { _ = database.Close() })
	runID := testID("run", 'T')
	createRunAggregate(t, database, runID)
	want, err := database.SaveRunExecutionContext(ctx, executionContextFixture(runID), 0)
	if err != nil {
		t.Fatalf("save context before rebuild: %v", err)
	}
	if err := database.RebuildProjections(ctx); err != nil {
		t.Fatalf("rebuild projections: %v", err)
	}
	got, err := database.RunExecutionContext(ctx, runID)
	if err != nil {
		t.Fatalf("read context after rebuild: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("context after rebuild = %#v, want %#v", got, want)
	}
}

func createRunAggregate(t *testing.T, database *Database, runID string) {
	t.Helper()
	_, err := database.Append(context.Background(), pendingEvent(testID("event", rune(runID[len(runID)-1])), statestore.AggregateRun, runID, 0, "run.created",
		`{"workItemId":"work_01K3Z1C1AAAAAAAAAAAAAAAAAA","workflowId":"story-execution","workflowVersion":"1.4.0"}`))
	if err != nil {
		t.Fatalf("create run aggregate: %v", err)
	}
}

func executionContextFixture(runID string) statestore.RunExecutionContext {
	return statestore.RunExecutionContext{
		SchemaVersion: statestore.RunExecutionContextSchemaVersion,
		RunID:         runID,
		RunInputs: map[string]json.RawMessage{
			"repository": json.RawMessage(` "C:/repo" `),
			"story":      json.RawMessage(`{"title":"Add README"}`),
		},
		AcceptedOutputs: map[string]map[string]json.RawMessage{
			"readiness": {"status": json.RawMessage(`"ready"`)},
		},
		FrameSnapshot: json.RawMessage(`{
			"id":"frame-1",
			"workflow":{"name":"story-execution","version":"1.4.0","digest":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},
			"origin":"root",
			"runId":"` + runID + `",
			"route":{"entry":"readiness","terminals":["implementation"],"nodes":[{"id":"readiness"},{"id":"implementation"}],"transitions":[{"id":"ready-to-implementation","from":"readiness","to":"implementation"}],"excludedNodes":[],"inputRequirements":[]},
			"inputs":{"repository":"C:/repo"},
			"tokens":[{"key":{"sourceVisitId":"visit-1","transitionId":"ready-to-implementation","joinEpoch":0},"source":"readiness","target":"implementation","kind":"normal"}]
		}`),
	}
}
