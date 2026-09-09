package runexecution

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"darkstar/src/adapters/statestore/sqlite"
	"darkstar/src/core/attention"
	"darkstar/src/core/workflow"
	"darkstar/src/ports/provider"
	"darkstar/src/ports/statestore"
)

type checkpointPlanner struct {
	WorkflowPlanner
	definition workflow.Definition
}

func (p checkpointPlanner) Definition(context.Context, string, string) (workflow.Definition, error) {
	return p.definition, nil
}

func checkpointFixture(t *testing.T) (*Service, *sqlite.Database, string, string, string) {
	t.Helper()
	ctx := t.Context()
	db, err := sqlite.Open(ctx, filepath.Join(t.TempDir(), "state.db"), sqlite.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	runID, visitID, attemptID := randomID("run_"), randomID("visit_"), randomID("attempt_")
	workID, projectID := randomID("work_"), randomID("project_")
	digest := strings.Repeat("a", 64)
	node := workflow.ReasoningNode{Common: workflow.NodeFields{Outputs: map[workflow.Identifier]workflow.OutputDeclaration{"plan": {Type: workflow.ValueMarkdown}}, Checkpoint: workflow.ApproveCheckpoint{}}}
	document := workflow.Document{}
	document.Spec.Nodes = map[workflow.Identifier]workflow.Node{"plan": node}
	definition := workflow.Definition{Version: workflow.VersionSummary{Name: "test", Version: "1.0.0", Digest: digest}, Document: document}
	route := workflow.Route{Entry: "plan", Terminals: []workflow.Identifier{"plan"}, Nodes: []workflow.RouteNode{{ID: "plan"}}}
	frame := workflow.FrameSnapshot{ID: "test-frame", Workflow: workflow.WorkflowIdentity{Name: "test", Version: "1.0.0", Digest: digest}, Origin: workflow.RootFrameOrigin{RunID: runID}, Route: route, Inputs: map[workflow.Identifier]json.RawMessage{}}
	frameJSON, _ := json.Marshal(frame)

	now := time.Now()
	makeEvent := func(kind string, aggregate statestore.AggregateType, id string, revision uint64, data any) statestore.PendingEvent {
		return pendingEvent(kind, aggregate, id, revision, runID, kind+":"+id, statestore.ActorSystem, "test", now, data)
	}
	_, err = db.Append(ctx,
		makeEvent("project.created", statestore.AggregateProject, projectID, 0, map[string]any{"name": "test", "sourceHash": digest}),
		makeEvent("work.created", statestore.AggregateWork, workID, 0, map[string]any{"projectId": projectID, "title": "Update README", "sourceHash": digest}),
		makeEvent("run.created", statestore.AggregateRun, runID, 0, map[string]any{"workItemId": workID, "workflowId": "test", "workflowVersion": "1.0.0"}),
		makeEvent("run.route_frozen", statestore.AggregateRun, runID, 1, map[string]any{"workflowDigest": digest, "routeDigest": digest, "routeSnapshot": route}),
		makeEvent("run.started", statestore.AggregateRun, runID, 2, map[string]any{}), makeEvent("run.visit_ready", statestore.AggregateRun, runID, 3, map[string]any{}),
		makeEvent("visit.created", statestore.AggregateVisit, visitID, 0, map[string]any{"runId": runID, "nodeId": "plan"}), makeEvent("visit.ready", statestore.AggregateVisit, visitID, 1, map[string]any{}), makeEvent("visit.started", statestore.AggregateVisit, visitID, 2, map[string]any{}),
		makeEvent("attempt.created", statestore.AggregateAttempt, attemptID, 0, map[string]any{"runId": runID, "visitId": visitID, "nodeId": "plan", "scenario": ScenarioWorkflow, "provider": ProviderCodex, "logReference": "test.log"}),
		makeEvent("attempt.started", statestore.AggregateAttempt, attemptID, 1, map[string]any{"providerThreadId": "thread", "providerTurnId": "turn", "processOwnerId": "test"}),
	)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.SaveRunExecutionContext(ctx, statestore.RunExecutionContext{SchemaVersion: 1, RunID: runID, RunInputs: map[string]json.RawMessage{}, AcceptedOutputs: map[string]map[string]json.RawMessage{}, FrameSnapshot: frameJSON}, 0)
	if err != nil {
		t.Fatal(err)
	}
	s := &Service{store: db, planner: checkpointPlanner{definition: definition}, now: time.Now, ctx: ctx, workers: map[string]*worker{}}
	return s, db, runID, visitID, attemptID
}

func TestWorkflowCheckpointIsActionableAndHumanApprovalCompletesRun(t *testing.T) {
	s, db, runID, visitID, attemptID := checkpointFixture(t)
	ctx := t.Context()
	run, _ := db.Run(ctx, runID)
	visit, _ := db.Node(ctx, visitID)
	attempt, _ := db.Attempt(ctx, attemptID)
	if err := s.completeWorkflowSucceeded(ctx, attempt, run, visit, provider.SucceededResult{StructuredOutput: json.RawMessage(`{"plan":"# Plan\nUpdate the README."}`)}); err != nil {
		t.Fatal(err)
	}
	approval, err := db.Approval(ctx, executionApprovalID(visitID))
	if err != nil {
		t.Fatal(err)
	}
	run, _ = db.Run(ctx, runID)
	if run.Status != statestore.RunWaiting || approval.Status != statestore.ApprovalPending {
		t.Fatal("waiting run has no pending decision")
	}
	service, _ := attention.New(db)
	service.SetDecisionHandler(s.ApplyWorkflowCheckpointDecision)
	page, err := service.List(ctx, attention.ListRequest{RunID: runID})
	if err != nil || len(page.Items) != 1 {
		t.Fatalf("inline/global queue: %#v %v", page, err)
	}
	request := attention.DecisionRequest{ID: approval.ApprovalID, Kind: attention.KindWorkflowControl, Action: attention.DecisionApprove, ExpectedResourceVersion: approval.ResourceVersion, ScopeDigest: approval.ScopeDigest, PolicyDigest: approval.PolicyDigest, IdempotencyKey: "human-approval-1", Actor: statestore.Actor{Type: statestore.ActorUser, ID: "test-user"}}
	if _, err = service.Decide(ctx, request); err != nil {
		t.Fatal(err)
	}
	run, _ = db.Run(ctx, runID)
	visit, _ = db.Node(ctx, visitID)
	work, _ := db.WorkItem(ctx, run.WorkItemID)
	if run.Status != statestore.RunCompleted || visit.Status != statestore.NodeSucceeded || work.Status != statestore.WorkItemCompleted {
		t.Fatalf("approval did not complete: %s %s %s", run.Status, visit.Status, work.Status)
	}
	if _, err = service.Decide(ctx, request); err != nil {
		t.Fatalf("idempotent approval: %v", err)
	}
}

func TestLegacyCheckpointRepairDoesNotApproveOrLaunch(t *testing.T) {
	s, db, runID, visitID, attemptID := checkpointFixture(t)
	ctx := t.Context()
	execution, _ := db.RunExecutionContext(ctx, runID)
	execution.AcceptedOutputs = map[string]map[string]json.RawMessage{"plan": {"plan": json.RawMessage(`"# Saved plan"`)}}
	if _, err := db.SaveRunExecutionContext(ctx, execution, execution.Revision); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	events := []statestore.PendingEvent{}
	for _, e := range []struct {
		kind string
		agg  statestore.AggregateType
		id   string
		rev  uint64
	}{{"attempt.result_received", statestore.AggregateAttempt, attemptID, 2}, {"attempt.succeeded", statestore.AggregateAttempt, attemptID, 3}, {"visit.result_received", statestore.AggregateVisit, visitID, 3}, {"visit.waiting_checkpoint", statestore.AggregateVisit, visitID, 4}, {"run.waiting", statestore.AggregateRun, runID, 4}} {
		events = append(events, pendingEvent(e.kind, e.agg, e.id, e.rev, runID, "legacy:"+e.kind, statestore.ActorSystem, "test", now, map[string]any{}))
	}
	if _, err := db.Append(ctx, events...); err != nil {
		t.Fatal(err)
	}
	run, _ := db.Run(ctx, runID)
	for i := 0; i < 2; i++ {
		if err := s.repairWorkflowCheckpoints(ctx, run); err != nil {
			t.Fatal(err)
		}
	}
	approvals, err := db.Approvals(ctx, statestore.ApprovalPending)
	if err != nil || len(approvals) != 1 {
		t.Fatalf("repair should create exactly one pending question: %v %v", approvals, err)
	}
	current, _ := db.Run(ctx, runID)
	if current.ResourceVersion != run.ResourceVersion || current.Status != statestore.RunWaiting || len(s.workers) != 0 {
		t.Fatal("repair changed or launched the waiting run")
	}
}

func TestStopClosesPendingCheckpointAndResumeCannotBypassIt(t *testing.T) {
	s, db, runID, visitID, attemptID := checkpointFixture(t)
	ctx := t.Context()
	s.schedulingAllowed = true
	run, _ := db.Run(ctx, runID)
	visit, _ := db.Node(ctx, visitID)
	attempt, _ := db.Attempt(ctx, attemptID)
	if err := s.completeWorkflowSucceeded(ctx, attempt, run, visit, provider.SucceededResult{StructuredOutput: json.RawMessage(`{"plan":"# Plan"}`)}); err != nil {
		t.Fatal(err)
	}
	run, _ = db.Run(ctx, runID)
	request := ControlRequest{RunID: runID, ExpectedResourceVersion: run.ResourceVersion, IdempotencyKey: "resume-checkpoint", Actor: statestore.Actor{Type: statestore.ActorUser, ID: "human"}}
	if _, err := s.Resume(ctx, request); err == nil {
		t.Fatal("resume bypassed human checkpoint")
	}
	request.IdempotencyKey = "cancel-checkpoint"
	if _, err := s.Cancel(ctx, request); err != nil {
		t.Fatal(err)
	}
	approval, _ := db.Approval(ctx, executionApprovalID(visitID))
	visit, _ = db.Node(ctx, visitID)
	if approval.Status != statestore.ApprovalCancelled || visit.Status != statestore.NodeCancelled {
		t.Fatal("stop left an unresolved checkpoint")
	}
}

type guidanceProvider struct {
	provider.Provider
	calls  int
	before func()
}

func (p *guidanceProvider) Steer(_ context.Context, _ provider.AttemptHandle, _ string, _ string) error {
	p.before()
	p.calls++
	return nil
}
func TestGuidanceIsPersistedBeforeDeliveryAndReplayDoesNotSendTwice(t *testing.T) {
	s, db, runID, _, attemptID := checkpointFixture(t)
	ctx := t.Context()
	run, _ := db.Run(ctx, runID)
	p := &guidanceProvider{before: func() {
		if _, err := db.EventByCommand(ctx, runID, "guidance-message-1"); err != nil {
			t.Fatal("message was delivered before persistence")
		}
	}}
	s.workers[attemptID] = &worker{attempt: statestore.AttemptProjection{RunID: runID}, adapter: p, handle: provider.AttemptHandle{AttemptID: attemptID}}
	request := ControlRequest{RunID: runID, ExpectedResourceVersion: run.ResourceVersion, IdempotencyKey: "guidance-message-1", Actor: statestore.Actor{Type: statestore.ActorUser, ID: "human"}}
	for i := 0; i < 2; i++ {
		result, err := s.Guide(ctx, request, "Include Windows installation.")
		if err != nil || result.Status != "accepted" {
			t.Fatalf("guidance: %v %v", result, err)
		}
	}
	if p.calls != 1 {
		t.Fatalf("sent %d times", p.calls)
	}
}

func TestTranscriptPagesBeyondDashboardWindowAndKeepsRunsSeparate(t *testing.T) {
	s, db, runID, _, attemptID := checkpointFixture(t)
	ctx := t.Context()
	attempt, _ := db.Attempt(ctx, attemptID)
	events := []statestore.PendingEvent{}
	for i := uint64(1); i <= 301; i++ {
		events = append(events, pendingEvent("attempt.provider_event", statestore.AggregateAttempt, attemptID, attempt.ResourceVersion+i-1, runID, fmt.Sprintf("test-stream:%d", i), statestore.ActorProvider, "codex", time.Now(), map[string]any{"sequence": i, "kind": provider.EventMessageDelta, "payload": map[string]any{"params": map[string]any{"delta": "saved text"}}}))
	}
	if _, err := db.Append(ctx, events...); err != nil {
		t.Fatal(err)
	}
	cursor := uint64(0)
	count := 0
	for {
		page, err := s.Transcript(ctx, runID, cursor, 70)
		if err != nil {
			t.Fatal(err)
		}
		for _, event := range page.Events {
			if event.Kind == "attempt.provider_event" {
				count++
				if !strings.Contains(string(event.Data), "saved text") {
					t.Fatal("payload omitted")
				}
			}
		}
		cursor = page.Next
		if !page.HasMore {
			break
		}
	}
	if count != 301 {
		t.Fatalf("transcript retained %d of 301 events", count)
	}
	empty, err := db.RunEventsAfter(ctx, randomID("run_"), 0, 100)
	if err != nil || len(empty) != 0 {
		t.Fatal("cross-run event leak")
	}
}
