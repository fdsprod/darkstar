package cli

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"darkstar/src/adapters/statestore/sqlite"
	"darkstar/src/core/identity"
	"darkstar/src/ports/statestore"
)

func TestRepositoryWriterSerializesProjectsAndCancelsWaiters(t *testing.T) {
	ctx := context.Background()
	database, err := sqlite.Open(ctx, filepath.Join(t.TempDir(), "state.db"), sqlite.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = database.Close()
	}()
	first := statestore.AttemptProjection{AttemptID: "attempt_first", RunID: "run_project_a", CreatedAt: time.Now().Add(-time.Second)}
	_, release, err := acquireRepositoryAttemptLease(ctx, database, "canonical-shared-git-directory", "daemon", first)
	if err != nil {
		t.Fatal(err)
	}
	second := statestore.AttemptProjection{AttemptID: "attempt_second", RunID: "run_project_b", CreatedAt: first.CreatedAt}
	wait, cancel := context.WithTimeout(ctx, 150*time.Millisecond)
	defer cancel()
	if _, _, err := acquireRepositoryAttemptLease(wait, database, "canonical-shared-git-directory", "daemon", second); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("another project bypassed shared writer: %v", err)
	}
	if _, err := database.QueueHead(ctx, statestore.QueueRepositoryWrite, "canonical-shared-git-directory"); !errors.Is(err, statestore.ErrNotFound) {
		t.Fatalf("cancelled waiter retained queue ownership: %v", err)
	}
	if err := release(true); err != nil {
		t.Fatal(err)
	}
	_, finish, err := acquireRepositoryAttemptLease(ctx, database, "canonical-shared-git-directory", "daemon", second)
	if err != nil {
		t.Fatalf("settled writer did not release shared scope: %v", err)
	}
	if err := finish(true); err != nil {
		t.Fatal(err)
	}
}

func TestRepositoryWriterPauseFinishAndResumePreserveActualLease(t *testing.T) {
	ctx := context.Background()
	db, err := sqlite.Open(ctx, filepath.Join(t.TempDir(), "pause.db"), sqlite.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = db.Close()
	}()
	runID := identity.Random("run_")
	attemptID := identity.Random("attempt_")
	makeEvent := func(kind string, typ statestore.AggregateType, id string, revision uint64, data any) statestore.PendingEvent {
		encoded, _ := json.Marshal(data)
		return statestore.PendingEvent{SchemaVersion: 1, ID: identity.Random("event_"), AggregateType: typ, AggregateID: id, ExpectedRevision: revision, Kind: kind, OccurredAt: time.Now().UTC(), CorrelationID: runID, CommandID: kind, Actor: statestore.Actor{Type: statestore.ActorSystem, ID: "test"}, Data: encoded, Metadata: json.RawMessage(`{}`)}
	}
	if _, err := db.Append(ctx,
		makeEvent("run.created", statestore.AggregateRun, runID, 0, map[string]any{"workItemId": "legacy-work", "workflowId": "delivery", "workflowVersion": "1"}),
		makeEvent("context.frozen", statestore.AggregateRun, runID, 1, map[string]any{}),
		makeEvent("run.started", statestore.AggregateRun, runID, 2, map[string]any{}),
		makeEvent("attempt.created", statestore.AggregateAttempt, attemptID, 0, map[string]any{"runId": runID, "nodeId": "implement", "scenario": "workflow", "provider": "fake", "logReference": "log"}),
		makeEvent("attempt.resources_acquired", statestore.AggregateAttempt, attemptID, 1, map[string]any{}),
		makeEvent("attempt.started", statestore.AggregateAttempt, attemptID, 2, map[string]any{"providerThreadId": "thread", "providerTurnId": "turn", "processOwnerId": "process"}),
	); err != nil {
		t.Fatal(err)
	}
	// A prior owner makes this attempt's token greater than one, exercising
	// adoption with actual durable fencing rather than an assumed first token.
	_, releasePrior, err := acquireRepositoryAttemptLease(ctx, db, "shared", "daemon", statestore.AttemptProjection{AttemptID: "prior", RunID: "prior-run", CreatedAt: time.Now().Add(-time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	if err := releasePrior(true); err != nil {
		t.Fatal(err)
	}
	attempt, err := db.Attempt(ctx, attemptID)
	if err != nil {
		t.Fatal(err)
	}
	_, finish, err := acquireRepositoryAttemptLease(ctx, db, "shared", "daemon", attempt)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.PrepareRepositoryPause(ctx, runID, "daemon", "run.paused"); err != nil {
		t.Fatal(err)
	}
	if err := finish(false); err != nil {
		t.Fatal("reader stop overwrote durable pause intent", err)
	}
	if _, _, err := acquireRepositoryAttemptLease(ctx, db, "shared", "daemon", attempt); err == nil {
		t.Fatal("incomplete pause allowed reader reactivation")
	}
	if _, err := db.Append(ctx,
		makeEvent("run.paused", statestore.AggregateRun, runID, 3, map[string]any{}),
		makeEvent("run.resumed", statestore.AggregateRun, runID, 4, map[string]any{}),
	); err != nil {
		t.Fatal(err)
	}
	_, settle, err := acquireRepositoryAttemptLease(ctx, db, "shared", "daemon", attempt)
	if err != nil {
		t.Fatal("resumed writer could not retain prior fencing ownership", err)
	}
	if err := settle(true); err != nil {
		t.Fatal(err)
	}
	_, next, err := acquireRepositoryAttemptLease(ctx, db, "shared", "daemon", statestore.AttemptProjection{AttemptID: "next", RunID: "next-run", CreatedAt: time.Now().Add(-time.Minute)})
	if err != nil {
		t.Fatal("proven settled resumed writer did not release", err)
	}
	if err := next(true); err != nil {
		t.Fatal(err)
	}
}

func TestUncertainRepositoryWriterSurvivesRestartAndCannotBeReleasedByRetry(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "state.db")
	database, err := sqlite.Open(ctx, path, sqlite.Options{})
	if err != nil {
		t.Fatal(err)
	}
	first := statestore.AttemptProjection{AttemptID: "attempt_uncertain", RunID: "run_a", CreatedAt: time.Now().Add(-time.Second)}
	_, finish, err := acquireRepositoryAttemptLease(ctx, database, "shared", "daemon_old", first)
	if err != nil {
		t.Fatal(err)
	}
	if err := finish(false); err != nil {
		t.Fatal(err)
	}
	if err := finish(true); err != nil {
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := sqlite.Open(ctx, path, sqlite.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = reopened.Close()
	}()
	second := statestore.AttemptProjection{AttemptID: "attempt_retry", RunID: "run_b", CreatedAt: first.CreatedAt}
	if _, _, err := acquireRepositoryAttemptLease(ctx, reopened, "shared", "daemon_new", second); err == nil || !strings.Contains(err.Error(), "reconciliation") {
		t.Fatalf("restart silently reclaimed uncertain writer: %v", err)
	}
	if _, _, err := acquireRepositoryAttemptLease(ctx, reopened, "shared", "daemon_new", first); err == nil {
		t.Fatal("new daemon adopted old holder without reconciliation")
	}
}

type terminalQueueStore struct {
	statestore.Store
}

func (s terminalQueueStore) Attempt(ctx context.Context, id string) (statestore.AttemptProjection, error) {
	if id == "abandoned" {
		return statestore.AttemptProjection{AttemptID: id, Status: statestore.AttemptCancelled}, nil
	}
	return s.Store.Attempt(ctx, id)
}

func TestRepositoryWriterClearsAdoptedAndAbandonedQueueEntries(t *testing.T) {
	ctx := context.Background()
	database, err := sqlite.Open(ctx, filepath.Join(t.TempDir(), "state.db"), sqlite.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = database.Close()
	}()
	store := terminalQueueStore{database}
	created := time.Now().Add(-time.Second)
	if _, err := store.Enqueue(ctx, statestore.EnqueueRequest{Kind: statestore.QueueRepositoryWrite, ScopeID: "shared", ItemID: "abandoned", Priority: 1, AvailableAt: created, Payload: []byte(`{}`)}); err != nil {
		t.Fatal(err)
	}
	attempt := statestore.AttemptProjection{AttemptID: "attempt_adopt", RunID: "run", CreatedAt: created}
	_, first, err := acquireRepositoryAttemptLease(ctx, store, "shared", "daemon", attempt)
	if err != nil {
		t.Fatal(err)
	}
	_, second, err := acquireRepositoryAttemptLease(ctx, store, "shared", "daemon", attempt)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.QueueHead(ctx, statestore.QueueRepositoryWrite, "shared"); !errors.Is(err, statestore.ErrNotFound) {
		t.Fatalf("adoption left a queue head: %v", err)
	}
	if err := second(true); err != nil {
		t.Fatal(err)
	}
	// Both holders represent the same proven attempt. A second completion must
	// be harmless, and both heartbeat goroutines must stop.
	_ = first(true)
	_, finish, err := acquireRepositoryAttemptLease(ctx, store, "shared", "daemon", statestore.AttemptProjection{AttemptID: "next", RunID: "run2", CreatedAt: created})
	if err != nil {
		t.Fatal(err)
	}
	if err := finish(true); err != nil {
		t.Fatal(err)
	}
}
