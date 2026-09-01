package sqlite

import (
	"context"
	"errors"
	"testing"

	"github.com/fdsprod/darkstar/runtime/src/core/projection"
	"github.com/fdsprod/darkstar/runtime/src/ports/statestore"
)

func TestWorkflowStateMachinesCommitThroughAllThreeProjections(t *testing.T) {
	t.Parallel()
	database := openEventTestDatabase(t)
	ctx := context.Background()
	runID, visitID, attemptID := testID("run", 'S'), testID("visit", 'S'), testID("attempt", 'S')

	_, err := database.Append(ctx,
		pendingEvent(testID("event", '1'), statestore.AggregateRun, runID, 0, "run.created", `{"workItemId":"work_01K3Z1C1AAAAAAAAAAAAAAAAAA","workflowId":"delivery","workflowVersion":"1"}`),
		pendingEvent(testID("event", '2'), statestore.AggregateRun, runID, 1, "run.route_frozen", `{}`),
		pendingEvent(testID("event", '3'), statestore.AggregateRun, runID, 2, "run.started", `{}`),
		pendingEvent(testID("event", '4'), statestore.AggregateVisit, visitID, 0, "visit.created", `{"runId":"`+runID+`","nodeId":"design"}`),
		pendingEvent(testID("event", '5'), statestore.AggregateVisit, visitID, 1, "visit.ready", `{}`),
		pendingEvent(testID("event", '6'), statestore.AggregateVisit, visitID, 2, "visit.started", `{}`),
		pendingEvent(testID("event", '7'), statestore.AggregateAttempt, attemptID, 0, "attempt.created", `{"runId":"`+runID+`","visitId":"`+visitID+`","nodeId":"design","scenario":"fake-success","provider":"fake","logReference":"attempt.log"}`),
		pendingEvent(testID("event", '8'), statestore.AggregateAttempt, attemptID, 1, "attempt.resources_acquired", `{}`),
		pendingEvent(testID("event", '9'), statestore.AggregateAttempt, attemptID, 2, "attempt.started", `{"providerThreadId":"thread","providerTurnId":"turn","processOwnerId":"process"}`),
		pendingEvent(testID("event", 'A'), statestore.AggregateRun, runID, 3, "run.visit_ready", `{}`),
	)
	if err != nil {
		t.Fatalf("append active lifecycle: %v", err)
	}

	_, err = database.Append(ctx,
		pendingEvent(testID("event", 'B'), statestore.AggregateAttempt, attemptID, 3, "attempt.result_received", `{}`),
		pendingEvent(testID("event", 'C'), statestore.AggregateAttempt, attemptID, 4, "attempt.succeeded", `{}`),
		pendingEvent(testID("event", 'D'), statestore.AggregateVisit, visitID, 3, "visit.result_received", `{}`),
		pendingEvent(testID("event", 'E'), statestore.AggregateVisit, visitID, 4, "visit.succeeded", `{}`),
		pendingEvent(testID("event", 'F'), statestore.AggregateRun, runID, 4, "run.completed", `{}`),
	)
	if err != nil {
		t.Fatalf("append terminal lifecycle: %v", err)
	}
	run, _ := database.Run(ctx, runID)
	node, _ := database.Node(ctx, visitID)
	attempt, _ := database.Attempt(ctx, attemptID)
	if run.Status != statestore.RunCompleted || node.Status != statestore.NodeSucceeded || attempt.Status != statestore.AttemptSucceeded {
		t.Fatalf("terminal projections: run=%s node=%s attempt=%s", run.Status, node.Status, attempt.Status)
	}
	if attempt.VisitID != visitID {
		t.Fatalf("attempt visit = %s, want %s", attempt.VisitID, visitID)
	}
	nodes, err := database.NodesForRun(ctx, runID)
	if err != nil || len(nodes) != 1 || nodes[0].VisitID != visitID {
		t.Fatalf("nodes for run = %#v, %v", nodes, err)
	}
}

func TestInvalidTransitionRollsBackEventAggregateAndProjection(t *testing.T) {
	t.Parallel()
	database := openEventTestDatabase(t)
	ctx := context.Background()
	runID := testID("run", 'T')
	if _, err := database.Append(ctx,
		pendingEvent(testID("event", 'G'), statestore.AggregateRun, runID, 0, "run.created", `{"workItemId":"work_01K3Z1C1AAAAAAAAAAAAAAAAAA","workflowId":"delivery","workflowVersion":"1"}`),
		pendingEvent(testID("event", 'H'), statestore.AggregateRun, runID, 1, "run.route_frozen", `{}`),
	); err != nil {
		t.Fatal(err)
	}
	_, err := database.Append(ctx, pendingEvent(testID("event", 'J'), statestore.AggregateRun, runID, 2, "run.completed", `{}`))
	var invalid *projection.InvalidTransitionError
	if !errors.As(err, &invalid) {
		t.Fatalf("error = %v, want InvalidTransitionError", err)
	}
	run, err := database.Run(ctx, runID)
	if err != nil || run.Status != statestore.RunReady || run.ResourceVersion != 2 {
		t.Fatalf("projection after rollback = %#v, %v", run, err)
	}
	var events, revision, position uint64
	_ = database.SQL().QueryRowContext(ctx, `SELECT count(*) FROM events`).Scan(&events)
	_ = database.SQL().QueryRowContext(ctx, `SELECT revision FROM aggregates WHERE aggregate_id = ?`, runID).Scan(&revision)
	_ = database.SQL().QueryRowContext(ctx, `SELECT last_position FROM global_positions WHERE singleton = 1`).Scan(&position)
	if events != 2 || revision != 2 || position != 2 {
		t.Fatalf("rollback evidence: events=%d revision=%d position=%d", events, revision, position)
	}
}

func TestTransitionCommandRetryReturnsCommittedEventAndConflictsFail(t *testing.T) {
	t.Parallel()
	database := openEventTestDatabase(t)
	ctx := context.Background()
	runID := testID("run", 'V')
	created := pendingEvent(testID("event", 'K'), statestore.AggregateRun, runID, 0, "run.created", `{"workItemId":"work_01K3Z1C1AAAAAAAAAAAAAAAAAA","workflowId":"delivery","workflowVersion":"1"}`)
	first, err := database.Append(ctx, created)
	if err != nil {
		t.Fatal(err)
	}
	retry := created
	retry.ID = testID("event", 'N')
	replayed, err := database.Append(ctx, retry)
	if err != nil {
		t.Fatalf("retry exact command: %v", err)
	}
	if len(replayed) != 1 || replayed[0].ID != first[0].ID || replayed[0].GlobalPosition != first[0].GlobalPosition {
		t.Fatalf("replayed event = %#v, want %#v", replayed, first)
	}
	conflict := retry
	conflict.ID = testID("event", 'M')
	conflict.Kind = "run.route_frozen"
	if _, err := database.Append(ctx, conflict); err == nil {
		t.Fatal("conflicting command reuse unexpectedly succeeded")
	} else {
		var idempotency *IdempotencyConflictError
		if !errors.As(err, &idempotency) {
			t.Fatalf("conflict error = %v, want IdempotencyConflictError", err)
		}
	}
	var count uint64
	_ = database.SQL().QueryRowContext(ctx, `SELECT count(*) FROM events`).Scan(&count)
	run, _ := database.Run(ctx, runID)
	if count != 1 || run.ResourceVersion != 1 || run.Status != statestore.RunDraft {
		t.Fatalf("state after retries: count=%d run=%#v", count, run)
	}
}
