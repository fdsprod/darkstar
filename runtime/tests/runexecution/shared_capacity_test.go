package runexecution_test

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"

	. "darkstar/src/core/runexecution"
	"darkstar/src/core/workflow"
	"darkstar/src/ports/statestore"
)

func TestWorkflowQueueSharesCapacityWithInvestigationAttempts(t *testing.T) {
	s, db, _ := newControlTestService(t, false)
	_, workID := seedWorkflowWork(t, db)
	planner := workflowDispatchPlannerFor(workflow.NoCheckpoint{}, true)
	if err := s.SetWorkflowPlanner(planner); err != nil {
		t.Fatal(err)
	}
	if err := s.SetWorkflowDispatch(queueBlockingFactory{}, &capturingAttemptBuilder{}); err != nil {
		t.Fatal(err)
	}
	if err := s.EnableQueue(func() (int, error) {
		return 1, nil
	}); err != nil {
		t.Fatal(err)
	}
	var admission sync.Mutex
	var external atomic.Int32
	external.Store(1)
	if err := s.SetSharedAdmission(&admission, func(context.Context) (int, error) {
		return int(external.Load()), nil
	}); err != nil {
		t.Fatal(err)
	}
	run, err := s.Prepare(t.Context(), CreateRequest{WorkItemID: workID, WorkflowID: planner.preview.Workflow.Name, WorkflowVersion: planner.preview.Workflow.Version}, "prepare-shared-capacity")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.DispatchQueue(t.Context()); err != nil {
		t.Fatal(err)
	}
	queued, err := s.Get(t.Context(), run.RunID)
	if err != nil || queued.Run.Status != statestore.RunQueued || len(queued.Attempts) != 0 {
		t.Fatalf("investigation occupancy was ignored: %#v, %v", queued, err)
	}
	external.Store(0)
	if err := s.DispatchQueue(t.Context()); err != nil {
		t.Fatal(err)
	}
	waitForControlRun(t, s, run.RunID, func(value View) bool {
		return value.Run.Status == statestore.RunRunning
	})
	admission.Lock()
	occupied, err := s.OccupiedRunSlots(t.Context())
	admission.Unlock()
	if err != nil || occupied != 1 {
		t.Fatalf("investigation admission could not observe workflow occupancy: %d, %v", occupied, err)
	}
}
