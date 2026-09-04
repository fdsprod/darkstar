package runexecution_test

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"darkstar/src/adapters/provider/fake"
	"darkstar/src/adapters/statestore/sqlite"
	"darkstar/src/core/identity"
	. "darkstar/src/core/runexecution"
	"darkstar/src/core/workflow"
	"darkstar/src/ports"
	manifestport "darkstar/src/ports/contextmanifest"
	"darkstar/src/ports/provider"
	"darkstar/src/ports/statestore"
)

func TestPauseResumePreservesAttemptCursorAndIsIdempotent(t *testing.T) {
	service, database, factory := newControlTestService(t, false)
	view, err := service.Start(context.Background(), StartRequest{Scenario: ScenarioRestart}, "start-pause-resume")
	if err != nil {
		t.Fatal(err)
	}
	running := waitForControlRun(t, service, view.Run.RunID, func(value View) bool {
		return value.Run.Status == statestore.RunRunning && len(value.Attempts) == 1 && value.Attempts[0].LastSequence == 1
	})
	pause := ControlRequest{RunID: view.Run.RunID, ExpectedResourceVersion: running.Run.ResourceVersion, IdempotencyKey: "pause-command"}
	paused, err := service.Pause(context.Background(), pause)
	if err != nil || paused.Status != statestore.RunWaiting {
		t.Fatalf("Pause() = %#v, %v", paused, err)
	}
	afterPause, _ := service.Get(context.Background(), view.Run.RunID)
	if afterPause.Attempts[0].Status != statestore.AttemptRunning || afterPause.Attempts[0].LastSequence != 1 {
		t.Fatalf("pause changed resumable attempt = %#v", afterPause.Attempts[0])
	}
	replayed, err := service.Pause(context.Background(), pause)
	if err != nil || replayed.ResourceVersion != paused.ResourceVersion || replayed.Status != statestore.RunWaiting {
		t.Fatalf("Pause(replay) = %#v, %v; want original response", replayed, err)
	}
	resumed, err := service.Resume(context.Background(), ControlRequest{
		RunID: view.Run.RunID, ExpectedResourceVersion: paused.ResourceVersion, IdempotencyKey: "resume-command",
	})
	if err != nil || resumed.Status != statestore.RunQueued {
		t.Fatalf("Resume() = %#v, %v", resumed, err)
	}
	completed := waitForControlRun(t, service, view.Run.RunID, func(value View) bool { return value.Run.Status == statestore.RunCompleted })
	if completed.Attempts[0].LastSequence != 3 || factory.callCount(fake.CallResume) != 1 {
		t.Fatalf("resume result = %#v; resume calls=%d", completed, factory.callCount(fake.CallResume))
	}
	evidence, err := database.RunEvidence(context.Background(), view.Run.RunID)
	if err != nil {
		t.Fatal(err)
	}
	assertControlEventCount(t, evidence.Events, "run.paused", 1)
	assertControlEventCount(t, evidence.Events, "run.resumed", 1)
}

func TestCancelTerminatesProviderAndClosesChildren(t *testing.T) {
	service, _, factory := newControlTestService(t, false)
	view, err := service.Start(context.Background(), StartRequest{Scenario: ScenarioRestart}, "start-cancel-run")
	if err != nil {
		t.Fatal(err)
	}
	running := waitForControlRun(t, service, view.Run.RunID, func(value View) bool {
		return value.Run.Status == statestore.RunRunning && value.Attempts[0].Status == statestore.AttemptRunning
	})
	cancelled, err := service.Cancel(context.Background(), ControlRequest{
		RunID: view.Run.RunID, ExpectedResourceVersion: running.Run.ResourceVersion, IdempotencyKey: "cancel-command",
	})
	if err != nil || cancelled.Status != statestore.RunCancelled {
		t.Fatalf("Cancel() = %#v, %v", cancelled, err)
	}
	final, _ := service.Get(context.Background(), view.Run.RunID)
	if final.Attempts[0].Status != statestore.AttemptCancelled || factory.callCount(fake.CallCancel) != 1 {
		t.Fatalf("cancel result = %#v; cancel calls=%d", final, factory.callCount(fake.CallCancel))
	}
	if _, err := service.Resume(context.Background(), ControlRequest{
		RunID: view.Run.RunID, ExpectedResourceVersion: cancelled.ResourceVersion, IdempotencyKey: "resume-cancelled",
	}); err == nil {
		t.Fatal("Resume accepted a cancelled run")
	} else {
		var transition *InvalidTransitionError
		if !errors.As(err, &transition) {
			t.Fatalf("Resume(cancelled) error = %v", err)
		}
	}
}

func TestCancelMarksUncertainProviderTerminationForReconciliation(t *testing.T) {
	service, _, factory := newControlTestService(t, false)
	factory.cancelDisposition = provider.CancelUncertain
	view, err := service.Start(context.Background(), StartRequest{Scenario: ScenarioRestart}, "start-uncertain-cancel")
	if err != nil {
		t.Fatal(err)
	}
	running := waitForControlRun(t, service, view.Run.RunID, func(value View) bool {
		return value.Run.Status == statestore.RunRunning && value.Attempts[0].Status == statestore.AttemptRunning
	})
	reconciled, err := service.Cancel(context.Background(), ControlRequest{
		RunID: view.Run.RunID, ExpectedResourceVersion: running.Run.ResourceVersion, IdempotencyKey: "cancel-uncertain",
	})
	if err != nil || reconciled.Status != statestore.RunReconcileRequired {
		t.Fatalf("Cancel() = %#v, %v; want run reconciliation", reconciled, err)
	}
	final, err := service.Get(context.Background(), view.Run.RunID)
	if err != nil || final.Attempts[0].Status != statestore.AttemptReconcileRequired || factory.callCount(fake.CallCancel) != 1 {
		t.Fatalf("uncertain cancel result = %#v, %v; cancel calls=%d", final, err, factory.callCount(fake.CallCancel))
	}
}

func TestAgentViewListsExecutionContextAndCancelsSelectedAttempt(t *testing.T) {
	service, database, factory := newControlTestService(t, false)
	if err := service.SetAgentWorkspace("C:/workspace"); err != nil {
		t.Fatal(err)
	}
	view, err := service.Start(context.Background(), StartRequest{Scenario: ScenarioRestart}, "start-agent-view")
	if err != nil {
		t.Fatal(err)
	}
	running := waitForControlRun(t, service, view.Run.RunID, func(value View) bool {
		return value.Run.Status == statestore.RunRunning && value.Attempts[0].LastSequence == 1
	})
	list, err := service.ListAgents(context.Background())
	if err != nil || len(list.Items) != 1 {
		t.Fatalf("ListAgents() = %#v, %v", list, err)
	}
	agent := list.Items[0]
	if agent.AttemptID != running.Attempts[0].AttemptID || agent.Provider != "fake" || agent.Execution.Workspace.ID != "C:/workspace" ||
		agent.Execution.Workspace.Access != "read_only" || !containsString(agent.Execution.Permissions, "network:denied") {
		t.Fatalf("agent view = %#v", agent)
	}
	manifest := manifestport.Manifest{
		ManifestID: "manifest_agent_view", RunID: agent.RunID, NodeID: agent.NodeID, AttemptID: agent.AttemptID,
		PolicyVersion: "context/v1", Permissions: []string{"repository.write"},
		Workspace: manifestport.Workspace{ID: "workspace/frozen", Digest: strings.Repeat("a", 64), Access: manifestport.WorkspaceWrite},
		Digest:    strings.Repeat("b", 64), FrozenAt: time.Now().UTC(),
	}
	if _, _, err := database.StoreManifest(context.Background(), manifest, "manifest-agent-view"); err != nil {
		t.Fatal(err)
	}
	frozen, err := service.Agent(context.Background(), agent.AttemptID)
	if err != nil || frozen.Execution.Source != "context_manifest" || frozen.Execution.Workspace.ID != "workspace/frozen" ||
		!containsString(frozen.Execution.Permissions, "repository.write") {
		t.Fatalf("Agent(frozen context) = %#v, %v", frozen, err)
	}
	cancelled, err := service.CancelAgent(context.Background(), agent.AttemptID, agent.ResourceVersion, "cancel-agent-view")
	if err != nil || cancelled.Status != statestore.AttemptCancelled || factory.callCount(fake.CallCancel) != 1 {
		t.Fatalf("CancelAgent() = %#v, %v; cancel calls=%d", cancelled, err, factory.callCount(fake.CallCancel))
	}
	replayed, err := service.CancelAgent(context.Background(), agent.AttemptID, agent.ResourceVersion, "cancel-agent-view")
	if err != nil || replayed.Status != statestore.AttemptCancelled {
		t.Fatalf("CancelAgent(replay) = %#v, %v", replayed, err)
	}
	after, err := service.ListAgents(context.Background())
	if err != nil || len(after.Items) != 0 {
		t.Fatalf("ListAgents(after cancel) = %#v, %v", after, err)
	}
}

func containsString(values []string, expected string) bool {
	for _, value := range values {
		if value == expected {
			return true
		}
	}
	return false
}

func TestStartupLeavesExplicitlyPausedRunQuiescent(t *testing.T) {
	service, database, _ := newControlTestService(t, false)
	view, err := service.Start(context.Background(), StartRequest{Scenario: ScenarioRestart}, "start-paused-restart")
	if err != nil {
		t.Fatal(err)
	}
	running := waitForControlRun(t, service, view.Run.RunID, func(value View) bool {
		return value.Run.Status == statestore.RunRunning && value.Attempts[0].LastSequence == 1
	})
	paused, err := service.Pause(context.Background(), ControlRequest{
		RunID: view.Run.RunID, ExpectedResourceVersion: running.Run.ResourceVersion, IdempotencyKey: "pause-before-restart",
	})
	if err != nil || paused.Status != statestore.RunWaiting {
		t.Fatalf("Pause() = %#v, %v", paused, err)
	}
	if err := service.Close(); err != nil {
		t.Fatal(err)
	}
	replacementFactory := &controlTestFactory{}
	replacement, err := New(context.Background(), database, replacementFactory, controlTestLogs{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = replacement.Close() })
	if err := replacement.ResumeActive(context.Background()); err != nil {
		t.Fatal(err)
	}
	time.Sleep(50 * time.Millisecond)
	afterRestart, err := replacement.Get(context.Background(), view.Run.RunID)
	if err != nil || afterRestart.Run.Status != statestore.RunWaiting || replacementFactory.callCount(fake.CallResume) != 0 {
		t.Fatalf("paused restart = %#v, %v; resume calls=%d", afterRestart, err, replacementFactory.callCount(fake.CallResume))
	}
	cancelled, err := replacement.Cancel(context.Background(), ControlRequest{
		RunID: view.Run.RunID, ExpectedResourceVersion: afterRestart.Run.ResourceVersion, IdempotencyKey: "cancel-after-restart",
	})
	if err != nil || cancelled.Status != statestore.RunCancelled || replacementFactory.callCount(fake.CallCancel) != 1 {
		t.Fatalf("Cancel(paused restart) = %#v, %v; cancel calls=%d", cancelled, err, replacementFactory.callCount(fake.CallCancel))
	}
}

func TestRetryCreatesFreshAttemptAndRejectsStaleVersion(t *testing.T) {
	service, database, _ := newControlTestService(t, true)
	view, err := service.Start(context.Background(), StartRequest{Scenario: ScenarioSuccess}, "start-failed-run")
	if err != nil {
		t.Fatal(err)
	}
	failed := waitForControlRun(t, service, view.Run.RunID, func(value View) bool { return value.Run.Status == statestore.RunFailed })
	if _, err := service.Retry(context.Background(), RetryRequest{ControlRequest: ControlRequest{
		RunID: view.Run.RunID, ExpectedResourceVersion: failed.Run.ResourceVersion - 1, IdempotencyKey: "retry-stale-version",
	}}); !errors.Is(err, ErrControlConflict) {
		t.Fatalf("Retry(stale) error = %v, want conflict", err)
	}
	retried, err := service.Retry(context.Background(), RetryRequest{ControlRequest: ControlRequest{
		RunID: view.Run.RunID, ExpectedResourceVersion: failed.Run.ResourceVersion, IdempotencyKey: "retry-failed-run",
	}, NodeID: "technical_design"})
	if err != nil || retried.Status != statestore.RunQueued {
		t.Fatalf("Retry() = %#v, %v", retried, err)
	}
	final := waitForControlRun(t, service, view.Run.RunID, func(value View) bool {
		return value.Run.Status == statestore.RunFailed && len(value.Attempts) == 2
	})
	if final.Attempts[0].AttemptID == final.Attempts[1].AttemptID {
		t.Fatal("retry reused the failed attempt identity")
	}
	evidence, _ := database.RunEvidence(context.Background(), view.Run.RunID)
	assertControlEventCount(t, evidence.Events, "run.retried", 1)
	assertControlEventCount(t, evidence.Events, "visit.retrying", 1)
}

func TestContinueExtendsCompletedFrozenRoute(t *testing.T) {
	service, database, _ := newControlTestService(t, false)
	runID, workID := identity.Deterministic("run_", "continue-run"), identity.Deterministic("work_", "continue-work")
	digest := strings.Repeat("a", 64)
	current := workflow.Route{Entry: "design", Terminals: []workflow.Identifier{"design"}, Nodes: []workflow.RouteNode{{ID: "design"}}, ExcludedNodes: []workflow.ExcludedNode{{ID: "delivery", Reason: workflow.ExclusionPastTerminal}}}
	extended := workflow.Route{Entry: "design", Terminals: []workflow.Identifier{"delivery"}, Nodes: []workflow.RouteNode{{ID: "design"}, {ID: "delivery"}}, Transitions: []workflow.RouteTransition{{ID: "deliver", From: "design", To: "delivery"}}}
	currentJSON, _ := json.Marshal(current)
	now := time.Now().UTC()
	if _, err := database.Append(context.Background(),
		pendingEvent("run.created", statestore.AggregateRun, runID, 0, runID, "continue-create", statestore.ActorUser, "test", now, map[string]any{"workItemId": workID, "workflowId": "delivery", "workflowVersion": "1.0.0"}),
		pendingEvent("run.route_frozen", statestore.AggregateRun, runID, 1, runID, "continue-route", statestore.ActorSystem, "test", now, map[string]any{"workflowDigest": digest, "routeDigest": digest, "routeSnapshot": json.RawMessage(currentJSON)}),
		pendingEvent("run.started", statestore.AggregateRun, runID, 2, runID, "continue-start", statestore.ActorUser, "test", now, map[string]any{}),
		pendingEvent("run.visit_ready", statestore.AggregateRun, runID, 3, runID, "continue-running", statestore.ActorSystem, "test", now, map[string]any{}),
		pendingEvent("run.completed", statestore.AggregateRun, runID, 4, runID, "continue-completed", statestore.ActorSystem, "test", now, map[string]any{}),
	); err != nil {
		t.Fatal(err)
	}
	if err := service.SetWorkflowPlanner(staticControlPlanner{preview: workflow.RoutePreview{Workflow: workflow.WorkflowIdentity{Name: "delivery", Version: "1.0.0", Digest: digest}, Route: extended}}); err != nil {
		t.Fatal(err)
	}
	value, err := service.Continue(context.Background(), ContinueRequest{ControlRequest: ControlRequest{
		RunID: runID, ExpectedResourceVersion: 5, IdempotencyKey: "continue-command",
	}, UntilNodeID: "delivery"})
	if err != nil || value.Status != statestore.RunQueued || value.RouteDigest == digest {
		t.Fatalf("Continue() = %#v, %v", value, err)
	}
	var frozen workflow.Route
	if err := json.Unmarshal([]byte(value.RouteSnapshot), &frozen); err != nil || len(frozen.Nodes) != 2 || frozen.Terminals[0] != "delivery" {
		t.Fatalf("continued route = %#v, %v", frozen, err)
	}
}

type controlTestFactory struct {
	mu                sync.Mutex
	fail              bool
	cancelDisposition provider.CancelDisposition
	providers         []*fake.Fake
}

func (factory *controlTestFactory) Provider(_ string, attemptID string, resume bool) (provider.Provider, error) {
	factory.mu.Lock()
	fail, cancelDisposition := factory.fail, factory.cancelDisposition
	factory.mu.Unlock()
	steps := []fake.Step{
		fake.Emit(provider.Event{Sequence: 1, Kind: provider.EventTurnStarted, Payload: json.RawMessage(`{"phase":"started"}`)}),
		fake.Pause(24 * time.Hour),
		fake.Emit(provider.Event{Sequence: 2, Kind: provider.EventMessageCompleted, Payload: json.RawMessage(`{"phase":"continued"}`)}),
		fake.Emit(provider.Event{Sequence: 3, Kind: provider.EventTurnCompleted, Payload: json.RawMessage(`{"phase":"completed"}`)}),
	}
	result := provider.AttemptResult(provider.SucceededResult{StructuredOutput: json.RawMessage(`{"ok":true}`)})
	cancelResult := provider.CancelResult{Disposition: cancelDisposition}
	options := []fake.Option{}
	if fail {
		steps = nil
		result = provider.FailedResult{Failure: ports.Failure{Code: ports.FailureInternal, Message: "scripted failure", Retryable: false}}
	} else if !resume {
		options = append(options, fake.WithClock(fake.NewManualClock(time.Unix(0, 0).UTC())))
	}
	adapter, err := fake.New(fake.Scenario{Attempts: []fake.AttemptScenario{{AttemptID: attemptID, Steps: steps, Result: result, CancelResult: cancelResult}}}, options...)
	if err != nil {
		return nil, err
	}
	factory.mu.Lock()
	factory.providers = append(factory.providers, adapter)
	factory.mu.Unlock()
	return adapter, nil
}

func (factory *controlTestFactory) callCount(kind fake.CallKind) int {
	factory.mu.Lock()
	providers := append([]*fake.Fake(nil), factory.providers...)
	factory.mu.Unlock()
	count := 0
	for _, adapter := range providers {
		for _, call := range adapter.Calls() {
			if call.Kind == kind {
				count++
			}
		}
	}
	return count
}

type controlTestLogs struct{}

func (controlTestLogs) AppendLog(context.Context, string, []byte) error { return nil }

type staticControlPlanner struct{ preview workflow.RoutePreview }

func (planner staticControlPlanner) Preview(context.Context, string, string, workflow.RouteRequest, workflow.RouteContext) (workflow.RoutePreview, workflow.ValidationErrors, error) {
	return planner.preview, nil, nil
}

func newControlTestService(t *testing.T, fail bool) (*Service, *sqlite.Database, *controlTestFactory) {
	t.Helper()
	database, err := sqlite.Open(context.Background(), filepath.Join(t.TempDir(), "control.db"), sqlite.Options{})
	if err != nil {
		t.Fatal(err)
	}
	factory := &controlTestFactory{fail: fail}
	service, err := New(context.Background(), database, factory, controlTestLogs{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = service.Close()
		_ = database.Close()
	})
	return service, database, factory
}

func waitForControlRun(t *testing.T, service *Service, runID string, ready func(View) bool) View {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		value, err := service.Get(context.Background(), runID)
		if err == nil && ready(value) {
			return value
		}
		if time.Now().After(deadline) {
			t.Fatalf("run %s did not reach expected state; last=%#v err=%v", runID, value, err)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func assertControlEventCount(t *testing.T, events []statestore.Event, kind string, want int) {
	t.Helper()
	count := 0
	for _, event := range events {
		if event.Kind == kind {
			count++
		}
	}
	if count != want {
		t.Fatalf("%s count = %d, want %d", kind, count, want)
	}
}

func pendingEvent(kind string, aggregateType statestore.AggregateType, aggregateID string, revision uint64, correlationID, commandID string,
	actorType statestore.ActorType, actorID string, occurredAt time.Time, data any) statestore.PendingEvent {
	encoded, _ := json.Marshal(data)
	return statestore.PendingEvent{
		SchemaVersion: 1, ID: identity.Random("event_"), AggregateType: aggregateType, AggregateID: aggregateID,
		ExpectedRevision: revision, Kind: kind, OccurredAt: occurredAt.UTC().Round(0), CorrelationID: correlationID,
		CommandID: commandID, Actor: statestore.Actor{Type: actorType, ID: actorID}, Data: encoded, Metadata: json.RawMessage(`{}`),
	}
}
