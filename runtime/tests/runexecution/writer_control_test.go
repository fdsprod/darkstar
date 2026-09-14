package runexecution_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"darkstar/src/adapters/statestore/sqlite"
	. "darkstar/src/core/runexecution"
	"darkstar/src/core/workflow"
	"darkstar/src/ports/provider"
	"darkstar/src/ports/statestore"
)

func TestWorkflowPausePreparesRepositoryBeforeQuiescingWorker(t *testing.T) {
	service, database, builder, run := writerControlFixture(t)
	builder.failPause.Store(true)
	request := ControlRequest{RunID: run.RunID, ExpectedResourceVersion: run.ResourceVersion, IdempotencyKey: "writer-pause-command"}
	if _, err := service.Pause(context.Background(), request); !errors.Is(err, errWriterControlRetry) {
		t.Fatalf("pause preparation failure = %v", err)
	}
	if builder.workerFinished.Load() || !builder.held.Load() {
		t.Fatal("failed pause preparation stopped the worker or released repository ownership")
	}
	current, err := database.Run(context.Background(), run.RunID)
	if err != nil || current.Status != statestore.RunRunning {
		t.Fatalf("failed pause changed run state: %#v %v", current, err)
	}
	paused, err := service.Pause(context.Background(), request)
	if err != nil || paused.Status != statestore.RunWaiting {
		t.Fatalf("pause retry = %#v, %v", paused, err)
	}
	if builder.pauseCalls.Load() != 2 || !builder.workerFinished.Load() || !builder.held.Load() {
		t.Fatal("pause did not preserve repository ownership after preparing and quiescing the worker")
	}
	evidence, err := database.RunEvidence(context.Background(), run.RunID)
	if err != nil {
		t.Fatal(err)
	}
	assertControlEventCount(t, evidence.Events, "run.paused", 1)
}

func TestWorkflowCancellationReconcilesAfterEvidenceAndOnDurableReplays(t *testing.T) {
	service, database, builder, run := writerControlFixture(t)
	builder.failReconciliation.Store(true)
	request := ControlRequest{RunID: run.RunID, ExpectedResourceVersion: run.ResourceVersion, IdempotencyKey: "writer-cancel-command"}
	if _, err := service.Cancel(context.Background(), request); !errors.Is(err, errWriterControlRetry) {
		t.Fatalf("cancellation reconciliation failure = %v", err)
	}
	current, err := database.Run(context.Background(), run.RunID)
	if err != nil || current.Status != statestore.RunCancelled || !builder.held.Load() {
		t.Fatalf("failed reconciliation must retain ownership with durable cancellation: %#v %v", current, err)
	}
	// First retry repairs the gap after cancellation events committed but before
	// the original command could complete. The second replays the completed command.
	for _, replay := range []string{"event commit", "command commit"} {
		cancelled, err := service.Cancel(context.Background(), request)
		if err != nil || cancelled.Status != statestore.RunCancelled {
			t.Fatalf("replay after %s = %#v, %v", replay, cancelled, err)
		}
	}
	if builder.reconcileCalls.Load() != 3 || builder.held.Load() {
		t.Fatalf("cancellation replay did not reconcile ownership: calls=%d held=%v", builder.reconcileCalls.Load(), builder.held.Load())
	}
	evidence, err := database.RunEvidence(context.Background(), run.RunID)
	if err != nil {
		t.Fatal(err)
	}
	assertControlEventCount(t, evidence.Events, "run.cancelled", 1)
	assertControlEventCount(t, evidence.Events, "attempt.cancelled", 1)
}

var errWriterControlRetry = errors.New("injected repository control persistence interruption")

type writerControlBuilder struct {
	*writerGuardBuilder
	database           *sqlite.Database
	workerFinished     atomic.Bool
	pauseCalls         atomic.Int32
	reconcileCalls     atomic.Int32
	failPause          atomic.Bool
	failReconciliation atomic.Bool
	workerContext      context.Context
}

func (builder *writerControlBuilder) AcquireWorkflowWrite(ctx context.Context, value AttemptRequestContext) (context.Context, func(bool) error, error) {
	child, finish, err := builder.writerGuardBuilder.AcquireWorkflowWrite(ctx, value)
	if err != nil {
		return child, finish, err
	}
	builder.workerContext = child
	return child, func(settled bool) error {
		builder.workerFinished.Store(true)
		return finish(settled)
	}, nil
}

func (builder *writerControlBuilder) PrepareWorkflowPause(ctx context.Context, runID, key string) error {
	builder.pauseCalls.Add(1)
	if builder.workerContext.Err() != nil || builder.workerFinished.Load() || !builder.held.Load() || key == "" {
		return errors.New("repository pause preparation happened after worker quiescence")
	}
	run, err := builder.database.Run(ctx, runID)
	if err != nil {
		return err
	}
	if run.Status != statestore.RunRunning {
		return errors.New("pause preparation did not see the live run")
	}
	if builder.failPause.CompareAndSwap(true, false) {
		return errWriterControlRetry
	}
	return nil
}

func (builder *writerControlBuilder) ReconcileWorkflowCancellation(ctx context.Context, runID string) error {
	builder.reconcileCalls.Add(1)
	if !builder.workerFinished.Load() {
		return errors.New("cancellation reconciliation ran before provider quiescence")
	}
	evidence, err := builder.database.RunEvidence(ctx, runID)
	if err != nil {
		return err
	}
	runCancelled, providerCancelled := false, false
	for _, event := range evidence.Events {
		if event.Kind == "run.cancelled" {
			runCancelled = true
		}
		if event.Kind == "attempt.cancelled" && strings.Contains(string(event.Data), `"providerCancellation"`) {
			providerCancelled = true
		}
	}
	if !runCancelled || !providerCancelled {
		return errors.New("repository cancellation reconciliation ran without persisted provider cancellation evidence")
	}
	if builder.failReconciliation.CompareAndSwap(true, false) {
		return errWriterControlRetry
	}
	builder.held.Store(false)
	return nil
}

func writerControlFixture(t *testing.T) (*Service, *sqlite.Database, *writerControlBuilder, statestore.RunProjection) {
	t.Helper()
	service, database, _ := newControlTestService(t, false)
	_, workID := seedWorkflowWork(t, database)
	planner := workflowDispatchPlannerFor(workflow.NoCheckpoint{}, true)
	if err := service.SetWorkflowPlanner(planner); err != nil {
		t.Fatal(err)
	}
	guard := &writerGuardBuilder{released: make(chan bool, 1)}
	builder := &writerControlBuilder{writerGuardBuilder: guard, database: database}
	factory := &guardedWriterFactory{guard: guard, result: provider.SucceededResult{StructuredOutput: json.RawMessage(`{"artifact":"candidate"}`)}, readingResult: make(chan struct{}), allowResult: make(chan struct{})}
	if err := service.SetWorkflowDispatch(factory, builder); err != nil {
		t.Fatal(err)
	}
	run, err := service.Create(context.Background(), CreateRequest{WorkItemID: workID, WorkflowID: planner.preview.Workflow.Name, WorkflowVersion: planner.preview.Workflow.Version}, "writer-control-create")
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-factory.readingResult:
	case <-time.After(5 * time.Second):
		t.Fatal("provider did not enter the controlled result wait")
	}
	current, err := database.Run(context.Background(), run.RunID)
	if err != nil {
		t.Fatal(err)
	}
	return service, database, builder, current
}
