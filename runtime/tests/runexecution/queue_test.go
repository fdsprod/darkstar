package runexecution_test

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"darkstar/src/adapters/provider/fake"
	"darkstar/src/core/identity"
	. "darkstar/src/core/runexecution"
	"darkstar/src/core/workflow"
	"darkstar/src/ports/provider"
	"darkstar/src/ports/statestore"
)

type queueBlockingFactory struct{}

func (queueBlockingFactory) Provider(_ context.Context, request ProviderRequest) (provider.Provider, error) {
	return fake.New(fake.Scenario{
		Health:   provider.Health{State: provider.HealthAvailable, Provider: ProviderCodex, ProviderVersion: "test"},
		Attempts: []fake.AttemptScenario{{AttemptID: request.AttemptID, Steps: []fake.Step{fake.Pause(24 * time.Hour)}, Result: provider.SucceededResult{StructuredOutput: json.RawMessage(`{"artifact":"candidate"}`)}}},
	}, fake.WithClock(fake.NewManualClock(time.Unix(0, 0))))
}

func TestQueueAutomaticPickupCapacityAndRelease(t *testing.T) {
	s, db, _ := newControlTestService(t, false)
	projectID, firstWork := seedWorkflowWork(t, db)
	planner := workflowDispatchPlannerFor(workflow.NoCheckpoint{}, true)
	if err := s.SetWorkflowPlanner(planner); err != nil {
		t.Fatal(err)
	}
	if err := s.SetWorkflowDispatch(queueBlockingFactory{}, &capturingAttemptBuilder{}); err != nil {
		t.Fatal(err)
	}
	var limit atomic.Int32
	limit.Store(3)
	if err := s.EnableQueue(func() (int, error) {
		return int(limit.Load()), nil
	}); err != nil {
		t.Fatal(err)
	}
	ids := []string{}
	for i := 0; i < 4; i++ {
		workID := firstWork
		if i > 0 {
			workID = identity.Deterministic("work_", fmt.Sprint("queued-work", i))
			_, err := db.Append(context.Background(), pendingEvent("work.created", statestore.AggregateWork, workID, 0, workID, workID, statestore.ActorUser, "test", time.Now(), map[string]any{"projectId": projectID, "title": "Queued work", "sourceHash": strings.Repeat("d", 64), "priority": 7}))
			if err != nil {
				t.Fatal(err)
			}
		}
		run, err := s.Prepare(context.Background(), CreateRequest{WorkItemID: workID, WorkflowID: planner.preview.Workflow.Name, WorkflowVersion: planner.preview.Workflow.Version}, fmt.Sprint("prepare-queue-", i))
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, run.RunID)
	}
	if err := s.DispatchQueue(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, id := range ids[:3] {
		waitForControlRun(t, s, id, func(v View) bool {
			return v.Run.Status == statestore.RunRunning
		})
	}
	fourth, _ := s.Get(context.Background(), ids[3])
	if fourth.Run.Status != statestore.RunQueued || len(fourth.Attempts) != 0 {
		t.Fatalf("fourth run consumed capacity: %#v", fourth)
	}
	// Lowering the limit never kills active work, and still prevents admission.
	limit.Store(1)
	if err := s.DispatchQueue(context.Background()); err != nil {
		t.Fatal(err)
	}
	for i, id := range ids[:3] {
		view, _ := s.Get(context.Background(), id)
		if _, err := s.Pause(context.Background(), ControlRequest{RunID: id, ExpectedResourceVersion: view.Run.ResourceVersion, IdempotencyKey: fmt.Sprint("pause-queue-", i)}); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.DispatchQueue(context.Background()); err != nil {
		t.Fatal(err)
	}
	waitForControlRun(t, s, ids[3], func(v View) bool {
		return v.Run.Status == statestore.RunRunning
	})
	if err := s.DispatchQueue(context.Background()); err != nil {
		t.Fatal(err)
	}
	evidence, _ := db.RunEvidence(context.Background(), ids[3])
	assertControlEventCount(t, evidence.Events, "attempt.created", 1)
	fourth, _ = s.Get(context.Background(), ids[3])
	if _, err := s.Pause(context.Background(), ControlRequest{RunID: ids[3], ExpectedResourceVersion: fourth.Run.ResourceVersion, IdempotencyKey: "pause-fourth-queue"}); err != nil {
		t.Fatal(err)
	}
	first, _ := s.Get(context.Background(), ids[0])
	if _, err := s.Resume(context.Background(), ControlRequest{RunID: ids[0], ExpectedResourceVersion: first.Run.ResourceVersion, IdempotencyKey: "resume-first-queue"}); err != nil {
		t.Fatal(err)
	}
	waitForControlRun(t, s, ids[0], func(v View) bool {
		return v.Run.Status == statestore.RunRunning
	})
}

func TestAuthorizedQueueSurvivesRestartWithoutAttempt(t *testing.T) {
	s, db, factory := newControlTestService(t, false)
	_, workID := seedWorkflowWork(t, db)
	planner := workflowDispatchPlannerFor(workflow.NoCheckpoint{}, true)
	if err := s.SetWorkflowPlanner(planner); err != nil {
		t.Fatal(err)
	}
	if err := s.EnableQueue(func() (int, error) {
		return 3, nil
	}); err != nil {
		t.Fatal(err)
	}
	run, err := s.Prepare(context.Background(), CreateRequest{WorkItemID: workID, WorkflowID: planner.preview.Workflow.Name, WorkflowVersion: planner.preview.Workflow.Version}, "restart-queue-prepare")
	if err != nil {
		t.Fatal(err)
	}
	run, err = s.Launch(context.Background(), ControlRequest{RunID: run.RunID, ExpectedResourceVersion: run.ResourceVersion, IdempotencyKey: "restart-queue-authorize"})
	if err != nil {
		t.Fatal(err)
	}
	view, _ := s.Get(context.Background(), run.RunID)
	if len(view.Attempts) != 0 {
		t.Fatal("waiting queue allocated an attempt")
	}
	_ = s.Close()
	next, err := New(context.Background(), db, factory, controlTestLogs{})
	if err != nil {
		t.Fatal(err)
	}
	defer next.Close()
	if err := next.SetWorkflowPlanner(planner); err != nil {
		t.Fatal(err)
	}
	if err := next.SetWorkflowDispatch(queueBlockingFactory{}, &capturingAttemptBuilder{}); err != nil {
		t.Fatal(err)
	}
	if err := next.EnableQueue(func() (int, error) {
		return 3, nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := next.ResumeActive(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := next.DispatchQueue(context.Background()); err != nil {
		t.Fatal(err)
	}
	waitForControlRun(t, next, run.RunID, func(v View) bool {
		return v.Run.Status == statestore.RunRunning
	})
	if err := next.DispatchQueue(context.Background()); err != nil {
		t.Fatal(err)
	}
	evidence, _ := db.RunEvidence(context.Background(), run.RunID)
	assertControlEventCount(t, evidence.Events, "attempt.created", 1)
}
