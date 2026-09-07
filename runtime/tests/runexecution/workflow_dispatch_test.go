package runexecution_test

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"darkstar/src/adapters/provider/fake"
	"darkstar/src/adapters/statestore/sqlite"
	"darkstar/src/core/identity"
	. "darkstar/src/core/runexecution"
	"darkstar/src/core/workflow"
	"darkstar/src/ports/provider"
	"darkstar/src/ports/statestore"
)

func TestCreateAtomicallyCreatesAndDispatchesWorkflowEntry(t *testing.T) {
	service, database, _ := newControlTestService(t, false)
	projectID, workID := seedWorkflowWork(t, database)
	planner := workflowDispatchPlannerFor(workflow.ApproveCheckpoint{}, true)
	if err := service.SetWorkflowPlanner(planner); err != nil {
		t.Fatal(err)
	}
	builder := &capturingAttemptBuilder{}
	if err := service.SetWorkflowDispatch(workflowFakeFactory{}, builder); err != nil {
		t.Fatal(err)
	}

	created, err := service.Create(context.Background(), CreateRequest{WorkItemID: workID, WorkflowID: planner.preview.Workflow.Name, WorkflowVersion: planner.preview.Workflow.Version}, "create-workflow-entry")
	if err != nil {
		t.Fatal(err)
	}
	view := waitForControlRun(t, service, created.RunID, func(value View) bool { return value.Run.Status == statestore.RunWaiting })
	if len(view.Nodes) != 1 || len(view.Attempts) != 1 || view.Nodes[0].NodeID != "design" || view.Attempts[0].Provider != ProviderCodex || view.Attempts[0].Scenario != ScenarioWorkflow {
		t.Fatalf("workflow entry projections = %#v", view)
	}
	captured := builder.context()
	reasoning, ok := captured.Node.(workflow.ReasoningNode)
	if !ok || reasoning.Executor.Agent != "designer" || captured.WorkItem.WorkItemID != workID || captured.Project.ProjectID != projectID || captured.Workflow.Version.Digest != planner.preview.Workflow.Digest {
		t.Fatalf("attempt request context = %#v", captured)
	}
	evidence, err := database.RunEvidence(context.Background(), created.RunID)
	if err != nil {
		t.Fatal(err)
	}
	assertControlEventCount(t, evidence.Events, "attempt.created", 1)
	assertControlEventCount(t, evidence.Events, "visit.waiting_checkpoint", 1)
}

func TestCreatePersistsExplicitFailureWhenWorkflowDispatchIsUnavailable(t *testing.T) {
	service, database, _ := newControlTestService(t, false)
	_, workID := seedWorkflowWork(t, database)
	planner := workflowDispatchPlannerFor(workflow.NoCheckpoint{}, true)
	if err := service.SetWorkflowPlanner(planner); err != nil {
		t.Fatal(err)
	}
	created, err := service.Create(context.Background(), CreateRequest{WorkItemID: workID, WorkflowID: planner.preview.Workflow.Name, WorkflowVersion: planner.preview.Workflow.Version}, "missing-workflow-dispatch")
	if err != nil {
		t.Fatal(err)
	}
	view := waitForControlRun(t, service, created.RunID, func(value View) bool { return value.Run.Status == statestore.RunFailed })
	if len(view.Attempts) != 1 || view.Attempts[0].Status != statestore.AttemptFailed || view.Nodes[0].Status != statestore.NodeFailed {
		t.Fatalf("failed workflow dispatch = %#v", view)
	}
	assertRunFailureCode(t, database, created.RunID, "WORKFLOW_DISPATCH_UNAVAILABLE")
}

func TestCreatePersistsInputRequiredWaitWithoutDispatch(t *testing.T) {
	service, database, _ := newControlTestService(t, false)
	_, workID := seedWorkflowWork(t, database)
	planner := workflowDispatchPlannerFor(workflow.NoCheckpoint{}, true)
	planner.preview.Route.InputRequirements = []workflow.InputRequirement{{Code: workflow.ValidationRunInputRequired, Node: "design", Input: "story", Source: "run.input.story"}}
	node := planner.definition.Document.Spec.Nodes["design"].(workflow.ReasoningNode)
	node.Common.Inputs["story"] = workflow.RequiredBinding{From: "run.input.story", Type: workflow.ValueObject}
	planner.definition.Document.Spec.Nodes["design"] = node
	if err := service.SetWorkflowPlanner(planner); err != nil {
		t.Fatal(err)
	}
	if err := service.SetWorkflowDispatch(workflowFakeFactory{}, &capturingAttemptBuilder{}); err != nil {
		t.Fatal(err)
	}

	created, err := service.Create(context.Background(), CreateRequest{WorkItemID: workID, WorkflowID: planner.preview.Workflow.Name, WorkflowVersion: planner.preview.Workflow.Version}, "missing-run-input")
	if err != nil {
		t.Fatal(err)
	}
	view, err := service.Get(context.Background(), created.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if view.Run.Status != statestore.RunWaiting || len(view.Nodes) != 0 || len(view.Attempts) != 0 {
		t.Fatalf("input-required run = %#v", view)
	}
	if view.Issue == nil || view.Issue.Kind != "input_required" || view.Issue.Code != "RUN_INPUT_REQUIRED" || !strings.Contains(view.Issue.Message, "run.input.story") {
		t.Fatalf("input-required issue = %#v", view.Issue)
	}
	if _, err := service.Resume(context.Background(), ControlRequest{RunID: created.RunID, ExpectedResourceVersion: view.Run.ResourceVersion, IdempotencyKey: "resume-without-input"}); !errors.Is(err, ErrInvalidControl) {
		t.Fatalf("Resume(input-required) error = %v, want ErrInvalidControl", err)
	}
	after, _ := service.Get(context.Background(), created.RunID)
	if after.Run.Status != statestore.RunWaiting || after.Issue == nil || after.Issue.Code != "RUN_INPUT_REQUIRED" {
		t.Fatalf("input-required run changed after rejected resume = %#v", after)
	}
}

func TestCreateRejectsSchedulingWhileRecoveryIsBlocked(t *testing.T) {
	service, database, _ := newControlTestService(t, false)
	_, workID := seedWorkflowWork(t, database)
	planner := workflowDispatchPlannerFor(workflow.NoCheckpoint{}, true)
	if err := service.SetWorkflowPlanner(planner); err != nil {
		t.Fatal(err)
	}
	if err := service.SetSchedulingAllowed(false); err != nil {
		t.Fatal(err)
	}
	_, err := service.Create(context.Background(), CreateRequest{WorkItemID: workID, WorkflowID: planner.preview.Workflow.Name, WorkflowVersion: planner.preview.Workflow.Version}, "blocked-schedule")
	if !errors.Is(err, ErrSchedulingBlocked) {
		t.Fatalf("Create() error = %v, want ErrSchedulingBlocked", err)
	}
}

func TestPrepareUsesPersistedRoutingOverrideAndResolvesOmittedVersion(t *testing.T) {
	service, database, _ := newControlTestService(t, false)
	projectID, workID := identity.Deterministic("project_", "routing-project"), identity.Deterministic("work_", "routing-work")
	now := time.Now().UTC()
	if _, err := database.Append(context.Background(),
		pendingEvent("project.created", statestore.AggregateProject, projectID, 0, projectID, "routing-project", statestore.ActorUser, "test", now, map[string]any{"name": "test", "sourceHash": strings.Repeat("c", 64)}),
		pendingEvent("work.created", statestore.AggregateWork, workID, 0, workID, "routing-work", statestore.ActorUser, "test", now, map[string]any{"projectId": projectID, "title": "Bounded route", "sourceHash": strings.Repeat("d", 64), "priority": 0, "routingIntent": map[string]any{"mode": "override", "workflowId": "test/workflow", "entryNodeId": "design", "terminalNodeIds": []string{"delivery"}}}),
	); err != nil {
		t.Fatal(err)
	}
	planner := &routeCapturePlanner{workflowDispatchPlanner: workflowDispatchPlannerFor(workflow.NoCheckpoint{}, false)}
	if err := service.SetWorkflowPlanner(planner); err != nil {
		t.Fatal(err)
	}
	prepared, err := service.Prepare(context.Background(), CreateRequest{WorkItemID: workID}, "prepare-routing-override")
	if err != nil {
		t.Fatal(err)
	}
	if prepared.WorkflowVersion != "1.0.0" || planner.version != "" || planner.request.From != "design" || !reflect.DeepEqual(planner.request.Until, []workflow.Identifier{"delivery"}) {
		t.Fatalf("prepared=%#v planner version=%q route=%#v", prepared, planner.version, planner.request)
	}
}

func TestPrepareResolvesAutomaticWorkToServerDefault(t *testing.T) {
	service, database, _ := newControlTestService(t, false)
	_, workID := seedWorkflowWork(t, database)
	planner := &routeCapturePlanner{workflowDispatchPlanner: workflowDispatchPlannerFor(workflow.NoCheckpoint{}, true)}
	if err := service.SetWorkflowPlanner(planner); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Prepare(context.Background(), CreateRequest{WorkItemID: workID}, "prepare-automatic-route"); err != nil {
		t.Fatal(err)
	}
	if planner.name != DefaultWorkflowID || planner.version != DefaultWorkflowVersion || planner.request.From != "" || len(planner.request.Until) != 0 {
		t.Fatalf("automatic planner input = %s@%s %#v", planner.name, planner.version, planner.request)
	}
}

func TestPrepareRejectsInvalidRoutingOverrideBeforeRunCreation(t *testing.T) {
	service, database, _ := newControlTestService(t, false)
	projectID, workID := identity.Deterministic("project_", "invalid-routing-project"), identity.Deterministic("work_", "invalid-routing-work")
	now := time.Now().UTC()
	if _, err := database.Append(context.Background(),
		pendingEvent("project.created", statestore.AggregateProject, projectID, 0, projectID, "invalid-routing-project", statestore.ActorUser, "test", now, map[string]any{"name": "test", "sourceHash": strings.Repeat("c", 64)}),
		pendingEvent("work.created", statestore.AggregateWork, workID, 0, workID, "invalid-routing-work", statestore.ActorUser, "test", now, map[string]any{"projectId": projectID, "title": "Invalid route", "sourceHash": strings.Repeat("d", 64), "priority": 0, "routingIntent": map[string]any{"mode": "override", "workflowId": "test/workflow", "entryNodeId": "ghost"}}),
	); err != nil {
		t.Fatal(err)
	}
	planner := &routeCapturePlanner{workflowDispatchPlanner: workflowDispatchPlannerFor(workflow.NoCheckpoint{}, false), issues: workflow.ValidationErrors{{Code: workflow.ValidationRouteEntryInvalid, Message: "unknown entry"}}}
	if err := service.SetWorkflowPlanner(planner); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Prepare(context.Background(), CreateRequest{WorkItemID: workID}, "prepare-invalid-routing"); err == nil {
		t.Fatal("invalid route unexpectedly prepared")
	}
	runs, err := database.RunsForWorkItem(context.Background(), workID)
	if err != nil || len(runs) != 0 {
		t.Fatalf("runs after invalid route = %#v, %v", runs, err)
	}
}

func TestWorkflowProviderHealthFailureDoesNotClaimReadiness(t *testing.T) {
	service, database, _ := newControlTestService(t, false)
	_, workID := seedWorkflowWork(t, database)
	planner := workflowDispatchPlannerFor(workflow.NoCheckpoint{}, true)
	if err := service.SetWorkflowPlanner(planner); err != nil {
		t.Fatal(err)
	}
	if err := service.SetWorkflowDispatch(workflowUnavailableFactory{}, &capturingAttemptBuilder{}); err != nil {
		t.Fatal(err)
	}
	created, err := service.Create(context.Background(), CreateRequest{WorkItemID: workID, WorkflowID: planner.preview.Workflow.Name, WorkflowVersion: planner.preview.Workflow.Version}, "provider-not-ready")
	if err != nil {
		t.Fatal(err)
	}
	view := waitForControlRun(t, service, created.RunID, func(value View) bool { return value.Run.Status == statestore.RunFailed })
	if view.Issue == nil || view.Issue.Code != "PROVIDER_NOT_READY" || view.Nodes[0].Status != statestore.NodeFailed {
		t.Fatalf("provider admission failure = %#v", view)
	}
	evidence, err := database.RunEvidence(context.Background(), created.RunID)
	if err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"run.visit_ready", "visit.ready", "visit.started"} {
		assertControlEventCount(t, evidence.Events, kind, 0)
	}
}

func TestResumeActiveRepairsQueuedWorkflowWithoutEntryAttempt(t *testing.T) {
	service, database, _ := newControlTestService(t, false)
	_, workID := seedWorkflowWork(t, database)
	planner := workflowDispatchPlannerFor(workflow.NoCheckpoint{}, false)
	if err := service.SetWorkflowPlanner(planner); err != nil {
		t.Fatal(err)
	}
	if err := service.SetWorkflowDispatch(workflowFakeFactory{}, &capturingAttemptBuilder{}); err != nil {
		t.Fatal(err)
	}
	runID := identity.Deterministic("run_", "repair-workflow-run")
	routeJSON, _ := json.Marshal(planner.preview.Route)
	now := time.Now().UTC()
	if _, err := database.Append(context.Background(),
		pendingEvent("run.created", statestore.AggregateRun, runID, 0, runID, "repair-create", statestore.ActorUser, "test", now, map[string]any{"workItemId": workID, "workflowId": planner.preview.Workflow.Name, "workflowVersion": planner.preview.Workflow.Version}),
		pendingEvent("run.route_frozen", statestore.AggregateRun, runID, 1, runID, "repair-route", statestore.ActorSystem, "test", now, map[string]any{"workflowDigest": planner.preview.Workflow.Digest, "routeDigest": strings.Repeat("b", 64), "routeSnapshot": json.RawMessage(routeJSON)}),
		pendingEvent("run.started", statestore.AggregateRun, runID, 2, runID, "repair-start", statestore.ActorUser, "test", now, map[string]any{}),
	); err != nil {
		t.Fatal(err)
	}
	if err := service.ResumeActive(context.Background()); err != nil {
		t.Fatal(err)
	}
	view := waitForControlRun(t, service, runID, func(value View) bool { return value.Run.Status == statestore.RunCompleted })
	if len(view.Attempts) != 2 || view.Attempts[0].NodeID != "design" || view.Attempts[0].Status != statestore.AttemptSucceeded || view.Attempts[1].NodeID != "delivery" || view.Attempts[1].Status != statestore.AttemptSucceeded {
		t.Fatalf("repaired workflow entry = %#v", view)
	}
}

func TestResumeActiveMovesLegacyQueuedRunToInputWait(t *testing.T) {
	service, database, _ := newControlTestService(t, false)
	_, workID := seedWorkflowWork(t, database)
	planner := workflowDispatchPlannerFor(workflow.NoCheckpoint{}, true)
	planner.preview.Route.InputRequirements = []workflow.InputRequirement{{Code: workflow.ValidationRunInputRequired, Node: "design", Input: "story", Source: "run.input.story"}}
	if err := service.SetWorkflowPlanner(planner); err != nil {
		t.Fatal(err)
	}
	runID := identity.Deterministic("run_", "legacy-input-wait")
	routeJSON, _ := json.Marshal(planner.preview.Route)
	now := time.Now().UTC()
	if _, err := database.Append(context.Background(),
		pendingEvent("run.created", statestore.AggregateRun, runID, 0, runID, "legacy-create", statestore.ActorUser, "test", now, map[string]any{"workItemId": workID, "workflowId": planner.preview.Workflow.Name, "workflowVersion": planner.preview.Workflow.Version}),
		pendingEvent("run.route_frozen", statestore.AggregateRun, runID, 1, runID, "legacy-route", statestore.ActorSystem, "test", now, map[string]any{"workflowDigest": planner.preview.Workflow.Digest, "routeDigest": strings.Repeat("b", 64), "routeSnapshot": json.RawMessage(routeJSON)}),
		pendingEvent("run.started", statestore.AggregateRun, runID, 2, runID, "legacy-start", statestore.ActorUser, "test", now, map[string]any{}),
	); err != nil {
		t.Fatal(err)
	}
	if err := service.ResumeActive(context.Background()); err != nil {
		t.Fatal(err)
	}
	view, err := service.Get(context.Background(), runID)
	if err != nil {
		t.Fatal(err)
	}
	if view.Run.Status != statestore.RunWaiting || len(view.Attempts) != 0 || view.Issue == nil || view.Issue.Code != "RUN_INPUT_REQUIRED" {
		t.Fatalf("legacy input wait = %#v", view)
	}
}

func TestResumeActiveRefusesCreatedWorkflowAttemptAfterCrashWindow(t *testing.T) {
	service, database, _ := newControlTestService(t, false)
	_, workID := seedWorkflowWork(t, database)
	planner := workflowDispatchPlannerFor(workflow.NoCheckpoint{}, true)
	if err := service.SetWorkflowPlanner(planner); err != nil {
		t.Fatal(err)
	}
	if err := service.SetWorkflowDispatch(workflowFakeFactory{}, &capturingAttemptBuilder{}); err != nil {
		t.Fatal(err)
	}
	runID := identity.Deterministic("run_", "uncertain-workflow-run")
	visitID := identity.Deterministic("visit_", "uncertain-workflow-visit")
	attemptID := identity.Deterministic("attempt_", "uncertain-workflow-attempt")
	routeJSON, _ := json.Marshal(planner.preview.Route)
	now := time.Now().UTC()
	if _, err := database.Append(context.Background(),
		pendingEvent("run.created", statestore.AggregateRun, runID, 0, runID, "uncertain-create", statestore.ActorUser, "test", now, map[string]any{"workItemId": workID, "workflowId": planner.preview.Workflow.Name, "workflowVersion": planner.preview.Workflow.Version}),
		pendingEvent("run.route_frozen", statestore.AggregateRun, runID, 1, runID, "uncertain-route", statestore.ActorSystem, "test", now, map[string]any{"workflowDigest": planner.preview.Workflow.Digest, "routeDigest": strings.Repeat("b", 64), "routeSnapshot": json.RawMessage(routeJSON)}),
		pendingEvent("run.started", statestore.AggregateRun, runID, 2, runID, "uncertain-start", statestore.ActorUser, "test", now, map[string]any{}),
		pendingEvent("visit.created", statestore.AggregateVisit, visitID, 0, runID, "uncertain-visit", statestore.ActorSystem, "test", now, map[string]any{"runId": runID, "nodeId": "design"}),
		pendingEvent("attempt.created", statestore.AggregateAttempt, attemptID, 0, runID, "uncertain-attempt", statestore.ActorSystem, "test", now, map[string]any{"runId": runID, "visitId": visitID, "nodeId": "design", "scenario": ScenarioWorkflow, "provider": ProviderCodex, "logReference": "uncertain.log", "priority": 7}),
	); err != nil {
		t.Fatal(err)
	}
	if err := service.ResumeActive(context.Background()); err != nil {
		t.Fatal(err)
	}
	view := waitForControlRun(t, service, runID, func(value View) bool { return value.Run.Status == statestore.RunReconcileRequired })
	if view.Attempts[0].Status != statestore.AttemptReconcileRequired || view.Nodes[0].Status != statestore.NodePending {
		t.Fatalf("uncertain crash-window state = %#v", view)
	}
	if view.Issue == nil || view.Issue.Kind != "reconcile_required" || view.Issue.Code != "WORKFLOW_START_UNCERTAIN" || !strings.Contains(view.Issue.Message, "must be reconciled") {
		t.Fatalf("uncertain crash-window issue summary = %#v", view.Issue)
	}
	assertRunEventCode(t, database, runID, "run.reconcile_required", "WORKFLOW_START_UNCERTAIN")
}

type workflowDispatchPlanner struct {
	preview    workflow.RoutePreview
	definition workflow.Definition
}

type routeCapturePlanner struct {
	workflowDispatchPlanner
	request workflow.RouteRequest
	name    string
	version string
	issues  workflow.ValidationErrors
}

func (planner *routeCapturePlanner) Preview(_ context.Context, name string, version string, request workflow.RouteRequest, _ workflow.RouteContext) (workflow.RoutePreview, workflow.ValidationErrors, error) {
	planner.request, planner.name, planner.version = request, name, version
	if len(planner.issues) != 0 {
		return workflow.RoutePreview{}, planner.issues, nil
	}
	return planner.preview, nil, nil
}

func (planner workflowDispatchPlanner) Preview(context.Context, string, string, workflow.RouteRequest, workflow.RouteContext) (workflow.RoutePreview, workflow.ValidationErrors, error) {
	return planner.preview, nil, nil
}

func (planner workflowDispatchPlanner) Definition(context.Context, string, string) (workflow.Definition, error) {
	return planner.definition, nil
}

func workflowDispatchPlannerFor(checkpoint workflow.Checkpoint, terminal bool) workflowDispatchPlanner {
	digest := strings.Repeat("a", 64)
	fields := workflow.NodeFields{Entry: true, Terminal: terminal, Inputs: map[workflow.Identifier]workflow.Binding{}, Checkpoint: checkpoint, Outputs: map[workflow.Identifier]workflow.OutputDeclaration{"artifact": {Type: workflow.ValueString}}}
	if !terminal {
		fields.Transitions = []workflow.Transition{workflow.NormalTransition{Common: workflow.TransitionFields{TransitionID: "deliver", To: "delivery"}}}
	}
	nodes := map[workflow.Identifier]workflow.Node{"design": workflow.ReasoningNode{Common: fields, Executor: workflow.ReasoningExecutor{Agent: "designer", Skills: []string{"design"}, Tools: []string{"repository-search"}}}}
	route := workflow.Route{Entry: "design", Terminals: []workflow.Identifier{"delivery"}, Nodes: []workflow.RouteNode{{ID: "design"}, {ID: "delivery"}}, Transitions: []workflow.RouteTransition{{ID: "deliver", From: "design", To: "delivery"}}}
	if terminal {
		route.Terminals, route.Nodes, route.Transitions = []workflow.Identifier{"design"}, []workflow.RouteNode{{ID: "design"}}, nil
	} else {
		nodes["delivery"] = workflow.ReasoningNode{Common: workflow.NodeFields{Terminal: true, Inputs: map[workflow.Identifier]workflow.Binding{}, Checkpoint: workflow.NoCheckpoint{}, Outputs: map[workflow.Identifier]workflow.OutputDeclaration{"artifact": {Type: workflow.ValueString}}}, Executor: workflow.ReasoningExecutor{Agent: "deliverer"}}
	}
	identity := workflow.WorkflowIdentity{Name: "test/workflow", Version: "1.0.0", Digest: digest}
	return workflowDispatchPlanner{
		preview: workflow.RoutePreview{Workflow: identity, Route: route},
		definition: workflow.Definition{Version: workflow.VersionSummary{Name: identity.Name, Version: identity.Version, Digest: identity.Digest}, Document: workflow.Document{
			APIVersion: workflow.APIVersionV1Alpha2, Kind: workflow.KindWorkflow, Metadata: workflow.Metadata{Name: identity.Name, Version: identity.Version},
			Spec: workflow.Spec{RouteDefaults: workflow.RouteDefaults{Entry: route.Entry, Terminals: route.Terminals}, Nodes: nodes},
		}},
	}
}

type capturingAttemptBuilder struct {
	mu       sync.Mutex
	captured AttemptRequestContext
}

func (builder *capturingAttemptBuilder) BuildAttemptRequest(_ context.Context, value AttemptRequestContext) (provider.AttemptRequest, error) {
	builder.mu.Lock()
	builder.captured = value
	builder.mu.Unlock()
	return provider.AttemptRequest{
		AttemptID: value.Attempt.AttemptID, RunID: value.Run.RunID, NodeID: value.Attempt.NodeID,
		IdempotencyKey: "start:" + value.Attempt.AttemptID, Workspace: "C:/workspace", Prompt: "Execute the installed workflow node.",
		Access: provider.AccessReadOnly, Network: provider.NetworkDenied, CommandPolicy: provider.InteractionDeny, FilePolicy: provider.InteractionDeny, ToolPolicy: provider.InteractionDeny,
	}, nil
}

func (builder *capturingAttemptBuilder) context() AttemptRequestContext {
	builder.mu.Lock()
	defer builder.mu.Unlock()
	return builder.captured
}

type workflowFakeFactory struct{}

func (workflowFakeFactory) Provider(_ context.Context, request ProviderRequest) (provider.Provider, error) {
	return fake.New(fake.Scenario{
		Health:   provider.Health{State: provider.HealthAvailable, Provider: ProviderCodex, ProviderVersion: "test-v1"},
		Attempts: []fake.AttemptScenario{{AttemptID: request.AttemptID, Result: provider.SucceededResult{StructuredOutput: json.RawMessage(`{"artifact":"candidate"}`)}}},
	})
}

type workflowUnavailableFactory struct{}

func (workflowUnavailableFactory) Provider(_ context.Context, request ProviderRequest) (provider.Provider, error) {
	return fake.New(fake.Scenario{
		Health:   provider.Health{State: provider.HealthUnauthenticated, Provider: ProviderCodex},
		Attempts: []fake.AttemptScenario{{AttemptID: request.AttemptID, Result: provider.SucceededResult{StructuredOutput: json.RawMessage(`{"artifact":"candidate"}`)}}},
	})
}

func seedWorkflowWork(t *testing.T, database *sqlite.Database) (string, string) {
	t.Helper()
	projectID, workID := identity.Deterministic("project_", "workflow-project"), identity.Deterministic("work_", "workflow-work")
	now := time.Now().UTC()
	if _, err := database.Append(context.Background(),
		pendingEvent("project.created", statestore.AggregateProject, projectID, 0, projectID, "seed-project", statestore.ActorUser, "test", now, map[string]any{"name": "test", "sourceHash": strings.Repeat("c", 64)}),
		pendingEvent("work.created", statestore.AggregateWork, workID, 0, workID, "seed-work", statestore.ActorUser, "test", now, map[string]any{"projectId": projectID, "title": "Test workflow", "sourceHash": strings.Repeat("d", 64), "priority": 7}),
	); err != nil {
		t.Fatal(err)
	}
	return projectID, workID
}

func assertRunFailureCode(t *testing.T, database *sqlite.Database, runID, code string) {
	t.Helper()
	evidence, err := database.RunEvidence(context.Background(), runID)
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range evidence.Events {
		if (event.Kind == "run.failed" || event.Kind == "run.admission_failed") && strings.Contains(string(event.Data), `"code":"`+code+`"`) {
			return
		}
	}
	t.Fatalf("run %s has no failure evidence with code %s", runID, code)
}

func assertRunEventCode(t *testing.T, database *sqlite.Database, runID, kind, code string) {
	t.Helper()
	evidence, err := database.RunEvidence(context.Background(), runID)
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range evidence.Events {
		if event.Kind == kind && strings.Contains(string(event.Data), `"code":"`+code+`"`) {
			return
		}
	}
	t.Fatalf("run %s has no %s evidence with code %s", runID, kind, code)
}
