package sqlite

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fdsprod/darkstar/runtime/src/ports/statestore"
)

var eventTestTime = time.Date(2026, time.August, 31, 20, 0, 0, 123000000, time.UTC)

func TestAppendAtomicallyAdvancesEventStreamAndRunProjection(t *testing.T) {
	t.Parallel()

	database := openEventTestDatabase(t)
	ctx := context.Background()
	runID := testID("run", 'A')
	created := pendingEvent(testID("event", 'A'), statestore.AggregateRun, runID, 0, "run.created",
		`{"workItemId":"work_01K3Z1C1AAAAAAAAAAAAAAAAAA","workflowId":"software-delivery","workflowVersion":"1.0.0"}`)

	committed, err := database.Append(ctx, created)
	if err != nil {
		t.Fatalf("append run.created: %v", err)
	}
	if len(committed) != 1 || committed[0].GlobalPosition != 1 || committed[0].AggregateRevision != 1 {
		t.Fatalf("committed events = %#v, want position 1 and revision 1", committed)
	}
	if !committed[0].RecordedAt.Equal(eventTestTime) {
		t.Fatalf("recorded at = %v, want %v", committed[0].RecordedAt, eventTestTime)
	}

	next, err := database.Append(ctx,
		pendingEvent(testID("event", 'B'), statestore.AggregateRun, runID, 1, "context.frozen", `{}`),
		pendingEvent(testID("event", 'C'), statestore.AggregateRun, runID, 2, "run.started", `{}`),
	)
	if err != nil {
		t.Fatalf("append run batch: %v", err)
	}
	if len(next) != 2 || next[0].GlobalPosition != 2 || next[1].GlobalPosition != 3 || next[1].AggregateRevision != 3 {
		t.Fatalf("batch positions/revisions = %#v", next)
	}

	run, err := database.Run(ctx, runID)
	if err != nil {
		t.Fatalf("read run projection: %v", err)
	}
	if run.Status != statestore.RunRunning || run.ResourceVersion != 3 || run.LastGlobalPosition != 3 {
		t.Fatalf("run projection = %#v, want running at revision/position 3", run)
	}
	events, err := database.EventsAfter(ctx, 1, 10)
	if err != nil {
		t.Fatalf("read event suffix: %v", err)
	}
	if len(events) != 2 || events[0].ID != testID("event", 'B') || events[1].ID != testID("event", 'C') {
		t.Fatalf("event suffix = %#v", events)
	}
	var checkpoint uint64
	if err := database.SQL().QueryRowContext(ctx,
		`SELECT last_global_position FROM projection_checkpoints WHERE projection_name = ?`, currentStateProjection).Scan(&checkpoint); err != nil {
		t.Fatalf("read projection checkpoint: %v", err)
	}
	if checkpoint != 3 {
		t.Fatalf("projection checkpoint = %d, want 3", checkpoint)
	}
}

func TestAppendRollsBackEventAggregateAndProjectionTogether(t *testing.T) {
	t.Parallel()

	database := openEventTestDatabase(t)
	ctx := context.Background()
	runID := testID("run", 'D')
	invalidProjection := pendingEvent(testID("event", 'D'), statestore.AggregateRun, runID, 0, "run.created", `{}`)

	if _, err := database.Append(ctx, invalidProjection); err == nil {
		t.Fatal("run.created without required projection data unexpectedly committed")
	}
	for _, table := range []string{"aggregates", "events", "run_projection", "projection_checkpoints"} {
		var count int
		if err := database.SQL().QueryRowContext(ctx, `SELECT count(*) FROM `+table).Scan(&count); err != nil {
			t.Fatalf("count %s: %v", table, err)
		}
		if count != 0 {
			t.Errorf("%s count = %d after failed append, want 0", table, count)
		}
	}
	var position uint64
	if err := database.SQL().QueryRowContext(ctx, `SELECT last_position FROM global_positions WHERE singleton = 1`).Scan(&position); err != nil {
		t.Fatalf("read global position: %v", err)
	}
	if position != 0 {
		t.Fatalf("global position = %d after failed append, want 0", position)
	}
}

func TestAppendRejectsStaleRevisionWithoutPartialBatch(t *testing.T) {
	t.Parallel()

	database := openEventTestDatabase(t)
	ctx := context.Background()
	runID := testID("run", 'E')
	if _, err := database.Append(ctx, pendingEvent(testID("event", 'E'), statestore.AggregateRun, runID, 0, "run.created",
		`{"workItemId":"work_01K3Z1C1AAAAAAAAAAAAAAAAAA","workflowId":"delivery","workflowVersion":"1"}`)); err != nil {
		t.Fatalf("append first event: %v", err)
	}

	_, err := database.Append(ctx,
		pendingEvent(testID("event", 'F'), statestore.AggregateRun, runID, 1, "context.frozen", `{}`),
		pendingEvent(testID("event", 'G'), statestore.AggregateRun, runID, 1, "run.started", `{}`),
	)
	var conflict *RevisionConflictError
	if !errors.As(err, &conflict) {
		t.Fatalf("append error = %v, want RevisionConflictError", err)
	}
	if conflict.Expected != 1 || conflict.Actual != 2 {
		t.Fatalf("revision conflict = %#v, want expected 1 actual 2", conflict)
	}
	var eventCount, revision, position uint64
	if err := database.SQL().QueryRowContext(ctx, `SELECT count(*) FROM events`).Scan(&eventCount); err != nil {
		t.Fatalf("count events: %v", err)
	}
	if err := database.SQL().QueryRowContext(ctx, `SELECT revision FROM aggregates WHERE aggregate_id = ?`, runID).Scan(&revision); err != nil {
		t.Fatalf("read aggregate revision: %v", err)
	}
	if err := database.SQL().QueryRowContext(ctx, `SELECT last_position FROM global_positions WHERE singleton = 1`).Scan(&position); err != nil {
		t.Fatalf("read global position: %v", err)
	}
	if eventCount != 1 || revision != 1 || position != 1 {
		t.Fatalf("after partial batch rollback: events=%d revision=%d position=%d, want 1/1/1", eventCount, revision, position)
	}
}

func TestProjectionRebuildMatchesLiveCurrentState(t *testing.T) {
	t.Parallel()

	database := openEventTestDatabase(t)
	ctx := context.Background()
	runID := testID("run", 'N')
	runCreated := pendingEvent(testID("event", 'N'), statestore.AggregateRun, runID, 0, "run.created",
		`{"workItemId":"work_01K3Z1C1AAAAAAAAAAAAAAAAAA","workflowId":"delivery","workflowVersion":"1"}`)
	runStarted := pendingEvent(testID("event", 'P'), statestore.AggregateRun, runID, 1, "run.started", `{}`)
	approvalID := testID("approval", 'H')
	request := pendingEvent(testID("event", 'Q'), statestore.AggregateApproval, approvalID, 0, "approval.requested",
		`{"runId":"`+runID+`","class":"workflow_checkpoint","scopeDigest":"sha256:scope","policyDigest":"sha256:policy"}`)
	decision := pendingEvent(testID("event", 'R'), statestore.AggregateApproval, approvalID, 1, "approval.decided", `{"action":"approve"}`)
	if _, err := database.Append(ctx, runCreated, runStarted, request, decision); err != nil {
		t.Fatalf("append projection events: %v", err)
	}
	wantRun, err := database.Run(ctx, runID)
	if err != nil {
		t.Fatalf("read live run: %v", err)
	}
	wantApproval, err := database.Approval(ctx, approvalID)
	if err != nil {
		t.Fatalf("read live approval: %v", err)
	}
	if wantRun.Status != statestore.RunRunning || wantRun.ResourceVersion != 2 || wantRun.LastGlobalPosition != 2 {
		t.Fatalf("live run = %#v", wantRun)
	}
	if wantApproval.Status != statestore.ApprovalApproved || wantApproval.ResourceVersion != 2 || wantApproval.LastGlobalPosition != 4 {
		t.Fatalf("live approval = %#v", wantApproval)
	}

	if _, err := database.SQL().ExecContext(ctx, `UPDATE run_projection SET status = 'failed', resource_version = 1, last_global_position = 1`); err != nil {
		t.Fatalf("corrupt derived run projection for replay test: %v", err)
	}
	if _, err := database.SQL().ExecContext(ctx, `UPDATE approval_projection SET status = 'denied', resource_version = 1, last_global_position = 1`); err != nil {
		t.Fatalf("corrupt derived approval projection for replay test: %v", err)
	}
	if err := database.RebuildProjections(ctx); err != nil {
		t.Fatalf("rebuild projections: %v", err)
	}
	gotRun, err := database.Run(ctx, runID)
	if err != nil {
		t.Fatalf("read rebuilt run: %v", err)
	}
	gotApproval, err := database.Approval(ctx, approvalID)
	if err != nil {
		t.Fatalf("read rebuilt approval: %v", err)
	}
	if gotRun != wantRun {
		t.Fatalf("rebuilt run = %#v, want %#v", gotRun, wantRun)
	}
	if gotApproval != wantApproval {
		t.Fatalf("rebuilt approval = %#v, want %#v", gotApproval, wantApproval)
	}
}

func TestCommittedEventsAreAppendOnlyAcrossRestart(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "events.db")
	database, err := Open(ctx, path, Options{})
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	database.now = func() time.Time { return eventTestTime }
	runID := testID("run", 'K')
	if _, err := database.Append(ctx, pendingEvent(testID("event", 'K'), statestore.AggregateRun, runID, 0, "run.created",
		`{"workItemId":"work_01K3Z1C1AAAAAAAAAAAAAAAAAA","workflowId":"delivery","workflowVersion":"1"}`)); err != nil {
		t.Fatalf("append event: %v", err)
	}
	if _, err := database.SQL().ExecContext(ctx, `UPDATE events SET kind = 'run.failed'`); err == nil || !strings.Contains(err.Error(), "append-only") {
		t.Fatalf("event update error = %v, want append-only rejection", err)
	}
	if _, err := database.SQL().ExecContext(ctx, `DELETE FROM events`); err == nil || !strings.Contains(err.Error(), "append-only") {
		t.Fatalf("event delete error = %v, want append-only rejection", err)
	}
	if err := database.Close(); err != nil {
		t.Fatalf("close database: %v", err)
	}

	reopened, err := Open(ctx, path, Options{})
	if err != nil {
		t.Fatalf("reopen database: %v", err)
	}
	defer reopened.Close()
	events, err := reopened.EventsAfter(ctx, 0, 10)
	if err != nil {
		t.Fatalf("read events after restart: %v", err)
	}
	run, err := reopened.Run(ctx, runID)
	if err != nil {
		t.Fatalf("read projection after restart: %v", err)
	}
	if len(events) != 1 || events[0].Kind != "run.created" || run.Status != statestore.RunPending {
		t.Fatalf("restart state: events=%#v run=%#v", events, run)
	}
}

func openEventTestDatabase(t *testing.T) *Database {
	t.Helper()
	database, err := Open(context.Background(), filepath.Join(t.TempDir(), "events.db"), Options{})
	if err != nil {
		t.Fatalf("open event test database: %v", err)
	}
	database.now = func() time.Time { return eventTestTime }
	t.Cleanup(func() {
		if err := database.Close(); err != nil {
			t.Errorf("close event test database: %v", err)
		}
	})
	return database
}

func pendingEvent(eventID string, aggregateType statestore.AggregateType, aggregateID string, expectedRevision uint64, kind, data string) statestore.PendingEvent {
	return statestore.PendingEvent{
		SchemaVersion: 1, ID: eventID, AggregateType: aggregateType, AggregateID: aggregateID,
		ExpectedRevision: expectedRevision, Kind: kind, OccurredAt: eventTestTime.Add(-time.Second),
		CorrelationID: aggregateID, CommandID: "command-" + eventID,
		Actor: statestore.Actor{Type: statestore.ActorSystem, ID: "test"},
		Data:  json.RawMessage(data), Metadata: json.RawMessage(`{}`),
	}
}

func testID(prefix string, value rune) string {
	return prefix + "_" + strings.Repeat(string(value), 26)
}
