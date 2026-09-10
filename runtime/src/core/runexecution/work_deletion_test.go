package runexecution

import (
	"context"
	"darkstar/src/ports/provider"
	"darkstar/src/ports/statestore"
	"encoding/json"
	"errors"
	"testing"
)

func TestDeleteWorkRetainsHistoryAndCancelsCheckpoint(t *testing.T) {
	s, db, runID, visitID, attemptID := checkpointFixture(t)
	ctx := t.Context()
	run, _ := db.Run(ctx, runID)
	visit, _ := db.Node(ctx, visitID)
	attempt, _ := db.Attempt(ctx, attemptID)
	if err := s.completeWorkflowSucceeded(ctx, attempt, run, visit, provider.SucceededResult{StructuredOutput: json.RawMessage(`{"plan":"# Retained plan"}`)}); err != nil {
		t.Fatal(err)
	}
	work, _ := db.WorkItem(ctx, run.WorkItemID)
	_, err := s.DeleteWork(ctx, work.WorkItemID, work.ResourceVersion+1, "stale-delete-key")
	if !errors.Is(err, ErrControlConflict) {
		t.Fatalf("stale deletion: %v", err)
	}
	deleted, err := s.DeleteWork(ctx, work.WorkItemID, work.ResourceVersion, "valid-delete-key")
	if err != nil || deleted.Deletion != statestore.WorkDeleted {
		t.Fatalf("delete: %#v %v", deleted, err)
	}
	run, _ = db.Run(ctx, runID)
	approval, _ := db.Approval(ctx, executionApprovalID(visitID))
	if run.Status != statestore.RunCancelled || approval.Status != statestore.ApprovalCancelled {
		t.Fatalf("run/checkpoint not cancelled: %s %s", run.Status, approval.Status)
	}
	history, err := db.EventsForAggregate(ctx, attemptID)
	if err != nil || len(history) < 3 {
		t.Fatalf("attempt history lost: %v", err)
	}
	replay, err := s.DeleteWork(ctx, work.WorkItemID, work.ResourceVersion, "valid-delete-key")
	if err != nil || replay.ResourceVersion != deleted.ResourceVersion {
		t.Fatalf("delete replay: %#v %v", replay, err)
	}
	if err := db.RebuildProjections(ctx); err != nil {
		t.Fatal(err)
	}
	rebuilt, _ := db.WorkItem(ctx, work.WorkItemID)
	if rebuilt.Deletion != statestore.WorkDeleted {
		t.Fatal("deletion did not survive replay")
	}
}

func TestDeleteWorkKeepsUnconfirmedCancellationVisible(t *testing.T) {
	s, db, runID, _, _ := checkpointFixture(t)
	ctx := t.Context()
	run, _ := db.Run(ctx, runID)
	work, _ := db.WorkItem(ctx, run.WorkItemID)
	value, err := s.DeleteWork(ctx, work.WorkItemID, work.ResourceVersion, "uncertain-delete-key")
	if err != nil || value.Deletion != statestore.WorkDeleting {
		t.Fatalf("uncertain work hidden: %#v %v", value, err)
	}
	// A second reconciliation must not treat reconcile_required as proof of stopping.
	if err := s.ReconcileWorkDeletions(ctx); err != nil {
		t.Fatal(err)
	}
	value, _ = db.WorkItem(ctx, work.WorkItemID)
	if value.Deletion != statestore.WorkDeleting {
		t.Fatal("unconfirmed provider was hidden")
	}
	_, err = s.Resume(ctx, ControlRequest{RunID: runID, ExpectedResourceVersion: run.ResourceVersion, IdempotencyKey: "resume-deleted-work"})
	if err == nil {
		t.Fatal("resume accepted after deletion intent")
	}
}

type deletionProvider struct{ provider.Provider }

func (deletionProvider) CancelAttempt(context.Context, provider.CancelRequest) (provider.CancelResult, error) {
	return provider.CancelResult{Disposition: provider.CancelAlreadyDone}, nil
}

func TestDeletionReconcilesAfterRestartAndStopsEveryRun(t *testing.T) {
	s, db, runID, _, attemptID := checkpointFixture(t)
	ctx := t.Context()
	run, _ := db.Run(ctx, runID)
	work, _ := db.WorkItem(ctx, run.WorkItemID)
	otherID := randomID("run_")
	_, err := db.Append(ctx, pendingEvent("run.created", statestore.AggregateRun, otherID, 0, otherID, "other-created", statestore.ActorSystem, "test", s.now(), map[string]any{"workItemId": work.WorkItemID, "workflowId": "test", "workflowVersion": "1.0.0"}))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.DeleteWork(ctx, work.WorkItemID, work.ResourceVersion, "delete-all-runs"); err != nil {
		t.Fatal(err)
	}
	other, _ := db.Run(ctx, otherID)
	if other.Status != statestore.RunCancelled {
		t.Fatal("uncertain first run prevented cancelling sibling")
	}
	// Recreate the service around the same durable store with a provider that can
	// now prove the old session stopped. No workflow is restarted.
	restarted := &Service{store: db, now: s.now, ctx: ctx, workers: map[string]*worker{}, workflowFactory: WorkflowProviderFactoryFunc(func(context.Context, ProviderRequest) (provider.Provider, error) {
		return deletionProvider{}, nil
	})}
	if err = restarted.reconcileWorkDeletion(ctx, work.WorkItemID); err != nil {
		t.Fatal(err)
	}
	work, _ = db.WorkItem(ctx, work.WorkItemID)
	attempt, _ := db.Attempt(ctx, attemptID)
	if work.Deletion != statestore.WorkDeleted || attempt.Status != statestore.AttemptCancelled {
		t.Fatalf("not reconciled: %s %s", work.Deletion, attempt.Status)
	}
	// Even direct event writers cannot bypass the deletion barrier.
	fresh := randomID("run_")
	_, err = db.Append(ctx, pendingEvent("run.created", statestore.AggregateRun, fresh, 0, fresh, "late-run-created", statestore.ActorSystem, "test", s.now(), map[string]any{"workItemId": work.WorkItemID, "workflowId": "test", "workflowVersion": "1.0.0"}))
	if err == nil {
		t.Fatal("new run committed after deletion")
	}
	if err = db.RebuildProjections(ctx); err != nil {
		t.Fatal(err)
	}
}
