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
	"darkstar/src/adapters/tracker/builtin"
	"darkstar/src/core/backlog"
	"darkstar/src/core/identity"
	. "darkstar/src/core/runexecution"
	"darkstar/src/core/trackercontract"
	"darkstar/src/core/workflow"
	"darkstar/src/core/worklifecycle"
	"darkstar/src/core/workmanagement"
	"darkstar/src/ports/provider"
	"darkstar/src/ports/statestore"
	"darkstar/src/ports/tracker"
)

func TestSourceRunsFreezeApprovedInputsAcrossChangesRestartAndSequentialRuns(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "source-runs.db")
	db, err := sqlite.Open(ctx, path, sqlite.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = db.Close()
	}()
	project, work, observation, state := seedAdmittedSource(t, db)
	service, builder, request := sourceRunService(t, db, work, observation.ID)
	defer func() {
		_ = service.Close()
	}()
	missing := request
	missing.SourceObservationID = ""
	if _, err := service.Create(ctx, missing, "missing-approved-source"); err == nil {
		t.Fatal("legacy create path bypassed explicit source approval")
	}
	ready, err := service.Prepare(ctx, request, "source-first-prepare")
	if err != nil {
		t.Fatal(err)
	}
	frozen, err := db.RunSourceSnapshot(ctx, ready.RunID)
	if err != nil || frozen.ObservationID != observation.ID || frozen.Pin != state.Pin {
		t.Fatalf("source was not atomically frozen: %#v %v", frozen, err)
	}
	before, err := db.RunExecutionContext(ctx, ready.RunID)
	if err != nil || !strings.Contains(string(before.RunInputs["story"]), "Original approved description") {
		t.Fatalf("declared story input is not approved source content: %#v %v", before.RunInputs, err)
	}
	prepared, err := service.Get(ctx, ready.RunID)
	if err != nil || prepared.Assessment == nil || len(prepared.Assessment.Input.Work.Evidence) != 2 || prepared.Assessment.Input.Work.Evidence[0] != "docs/local-plan.md" {
		t.Fatalf("source assessment discarded or duplicated local evidence: %#v %v", prepared.Assessment, err)
	}
	changed := sourceRunObservation(t, state, "2", "Changed after preparation")
	commitSourceRunObservation(t, db, &state, changed)
	launched, err := service.Launch(ctx, ControlRequest{RunID: ready.RunID, ExpectedResourceVersion: ready.ResourceVersion, IdempotencyKey: "source-first-launch", Actor: statestore.Actor{Type: statestore.ActorUser, ID: "test"}})
	if err != nil {
		t.Fatal(err)
	}
	waitForControlRun(t, service, launched.RunID, func(value View) bool {
		return value.Run.Status == statestore.RunCompleted
	})
	captured := builder.context()
	if captured.WorkItem.Details != "Original approved description" || !strings.Contains(string(captured.NodeInputs["story"]), "Original approved description") || !strings.Contains(string(captured.NodeInputs["task"]), "Original approved description") || strings.Contains(string(captured.NodeInputs["story"]), work) {
		t.Fatalf("attempt did not receive only frozen source task content: %#v", captured.NodeInputs)
	}
	if err := service.Close(); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = sqlite.Open(ctx, path, sqlite.Options{})
	if err != nil {
		t.Fatal(err)
	}
	service, _, request = sourceRunService(t, db, work, changed.ID)
	// A source switch governs future intake. A later run of existing work
	// continues with its old binding and its separately approved observation.
	if _, err := db.SelectBacklogSource(ctx, project, state.BindingRevision, statestore.NativeBacklogSource{Namespace: tracker.Namespace{Provider: "built_in", Host: "darkstar.local", TenantID: "local", ScopeID: project}}, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	approveSourceRun(t, db, project, work, changed, state.BindingRevision, true)
	lifecycle, err := worklifecycle.New(db, service)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := lifecycle.Plan(ctx, work, worklifecycle.PlanRequest{Target: worklifecycle.StateReady, Preparation: &worklifecycle.Preparation{SourceObservationID: changed.ID}})
	if err != nil {
		t.Fatal(err)
	}
	readyEnabled := false
	for _, target := range plan.Targets {
		if target.Target == worklifecycle.StateReady && target.Availability == worklifecycle.AvailabilityEnabled {
			readyEnabled = true
		}
	}
	if !readyEnabled || plan.State != worklifecycle.StateDone {
		t.Fatalf("completed source execution cannot prepare another approved run: %#v", plan)
	}
	second, err := service.Create(ctx, request, "source-second-run")
	if err != nil {
		t.Fatal(err)
	}
	waitForControlRun(t, service, second.RunID, func(value View) bool {
		return value.Run.Status == statestore.RunCompleted
	})
	retained, err := db.RunSourceSnapshot(ctx, ready.RunID)
	if err != nil || string(retained.Ticket) != string(frozen.Ticket) || retained.ObservationID != frozen.ObservationID || retained.BindingRevision != frozen.BindingRevision {
		t.Fatalf("historical source snapshot changed: %#v %v", retained, err)
	}
	newSnapshot, err := db.RunSourceSnapshot(ctx, second.RunID)
	if err != nil || newSnapshot.BindingRevision != state.BindingRevision || newSnapshot.ObservationID != changed.ID || newSnapshot.Ref != frozen.Ref {
		t.Fatalf("next run silently followed the new selected source: %#v %v", newSnapshot, err)
	}
	works, _ := db.WorkItems(ctx)
	runs, _ := db.RunsForWorkItem(ctx, work)
	if len(works) != 1 || len(runs) != 2 {
		t.Fatalf("sequential source runs duplicated intake: works=%d runs=%d", len(works), len(runs))
	}
	if _, err := db.NativeTicket(ctx, project, work); err == nil {
		t.Fatal("external admission fabricated a built-in business ticket")
	}
}

func TestSourceFailedOwnerBlocksNewRunUntilExplicitCancellation(t *testing.T) {
	service, db, _ := newControlTestService(t, false)
	_, work, observation, _ := seedAdmittedSource(t, db)
	planner := sourceRunPlanner()
	if err := service.SetWorkflowPlanner(planner); err != nil {
		t.Fatal(err)
	}
	request := sourceRunRequest(work, observation.ID)
	failed, err := service.Create(context.Background(), request, "source-failed-owner")
	if err != nil {
		t.Fatal(err)
	}
	current := waitForControlRun(t, service, failed.RunID, func(value View) bool {
		return value.Run.Status == statestore.RunFailed
	}).Run
	if _, err := service.Prepare(context.Background(), request, "source-competing-owner"); err == nil {
		t.Fatal("a failed resumable source run released execution ownership")
	}
	if _, err := service.Cancel(context.Background(), ControlRequest{RunID: current.RunID, ExpectedResourceVersion: current.ResourceVersion, IdempotencyKey: "source-cancel-owner"}); err != nil {
		t.Fatal(err)
	}
	ready, err := service.Prepare(context.Background(), request, "source-new-settled-owner")
	if err != nil || ready.Status != statestore.RunReady {
		t.Fatalf("settled cancellation blocked next source run: %#v %v", ready, err)
	}
}

func TestConcurrentSourcePreparationHasOneUnresolvedOwner(t *testing.T) {
	service, db, _ := newControlTestService(t, false)
	_, work, observation, _ := seedAdmittedSource(t, db)
	if err := service.SetWorkflowPlanner(sourceRunPlanner()); err != nil {
		t.Fatal(err)
	}
	request := sourceRunRequest(work, observation.ID)
	var group sync.WaitGroup
	for _, key := range []string{"source-concurrent-one", "source-concurrent-two"} {
		group.Add(1)
		go func(key string) {
			defer group.Done()
			_, _ = service.Prepare(context.Background(), request, key)
		}(key)
	}
	group.Wait()
	runs, err := db.RunsForWorkItem(context.Background(), work)
	if err != nil {
		t.Fatal(err)
	}
	owners := 0
	for _, run := range runs {
		if !run.Status.Terminal() {
			owners++
		}
		if _, err := db.RunSourceSnapshot(context.Background(), run.RunID); err != nil {
			t.Fatal("created a run without its source snapshot", err)
		}
	}
	if owners != 1 {
		t.Fatalf("concurrent preparation has %d unresolved owners", owners)
	}
}

func TestSourceRetryAfterRestartUsesOriginalInputs(t *testing.T) {
	service, db, _ := newControlTestService(t, false)
	_, work, observation, state := seedAdmittedSource(t, db)
	if err := service.SetWorkflowPlanner(sourceRunPlanner()); err != nil {
		t.Fatal(err)
	}
	failed, err := service.Create(context.Background(), sourceRunRequest(work, observation.ID), "source-retry-original")
	if err != nil {
		t.Fatal(err)
	}
	current := waitForControlRun(t, service, failed.RunID, func(value View) bool {
		return value.Run.Status == statestore.RunFailed
	}).Run
	changed := sourceRunObservation(t, state, "2", "Later unapproved source edit")
	commitSourceRunObservation(t, db, &state, changed)
	if err := service.Close(); err != nil {
		t.Fatal(err)
	}
	restarted, builder, _ := sourceRunService(t, db, work, changed.ID)
	defer func() {
		if err := restarted.Close(); err != nil {
			t.Errorf("close restarted service: %v", err)
		}
	}()
	if _, err := restarted.Retry(context.Background(), RetryRequest{ControlRequest: ControlRequest{RunID: current.RunID, ExpectedResourceVersion: current.ResourceVersion, IdempotencyKey: "source-retry-frozen"}}); err != nil {
		t.Fatal(err)
	}
	waitForControlRun(t, restarted, current.RunID, func(value View) bool {
		return value.Run.Status == statestore.RunCompleted
	})
	if captured := builder.context(); captured.WorkItem.Details != "Original approved description" || !strings.Contains(string(captured.NodeInputs["task"]), "Original approved description") {
		t.Fatalf("retry used newer unapproved source: %#v", captured.NodeInputs)
	}
	view, err := restarted.Get(context.Background(), current.RunID)
	if err != nil || view.SourceSnapshot == nil || view.SourceSnapshot.ObservationID != observation.ID {
		t.Fatalf("run view lost immutable source evidence: %#v %v", view.SourceSnapshot, err)
	}
}

func TestSourceUncertainCancellationRetainsExecutionOwnership(t *testing.T) {
	service, db, _ := newControlTestService(t, false)
	_, work, observation, _ := seedAdmittedSource(t, db)
	if err := service.SetWorkflowPlanner(sourceRunPlanner()); err != nil {
		t.Fatal(err)
	}
	if err := service.SetWorkflowDispatch(sourceUncertainFactory{}, &capturingAttemptBuilder{}); err != nil {
		t.Fatal(err)
	}
	request := sourceRunRequest(work, observation.ID)
	run, err := service.Create(context.Background(), request, "source-uncertain-create")
	if err != nil {
		t.Fatal(err)
	}
	current := waitForControlRun(t, service, run.RunID, func(value View) bool {
		return value.Run.Status == statestore.RunRunning && len(value.Attempts) == 1 && value.Attempts[0].LastSequence == 1
	}).Run
	cancelled, err := service.Cancel(context.Background(), ControlRequest{RunID: current.RunID, ExpectedResourceVersion: current.ResourceVersion, IdempotencyKey: "source-uncertain-cancel"})
	if err != nil || cancelled.Status != statestore.RunReconcileRequired {
		t.Fatalf("uncertain provider cancellation was not retained: %#v %v", cancelled, err)
	}
	if _, err := service.Prepare(context.Background(), request, "source-uncertain-competing"); err == nil {
		t.Fatal("unconfirmed cancellation released source execution ownership")
	}
	if _, err := service.Cancel(context.Background(), ControlRequest{RunID: cancelled.RunID, ExpectedResourceVersion: cancelled.ResourceVersion, IdempotencyKey: "source-uncertain-again"}); err == nil {
		t.Fatal("repeated cancellation assumed remote success")
	}
}

func TestSourceLaunchRepairsContextFromAtomicFrozenInputs(t *testing.T) {
	_, db, _ := newControlTestService(t, false)
	_, work, observation, state := seedAdmittedSource(t, db)
	interrupted := &sourceContextFailure{Database: db, fail: true}
	service, _, request := sourceRunService(t, interrupted, work, observation.ID)
	if _, err := service.Prepare(context.Background(), request, "source-interrupted-context"); err == nil {
		t.Fatal("expected injected interruption after atomic source and route freeze")
	}
	if err := service.Close(); err != nil {
		t.Fatal(err)
	}
	runs, err := db.RunsForWorkItem(context.Background(), work)
	if err != nil || len(runs) != 1 {
		t.Fatalf("source freeze did not commit with run: %#v %v", runs, err)
	}
	changed := sourceRunObservation(t, state, "2", "Changed before context repair")
	commitSourceRunObservation(t, db, &state, changed)
	restarted, builder, _ := sourceRunService(t, db, work, changed.ID)
	defer func() {
		if err := restarted.Close(); err != nil {
			t.Errorf("close restarted service: %v", err)
		}
	}()
	if _, err := restarted.Launch(context.Background(), ControlRequest{RunID: runs[0].RunID, ExpectedResourceVersion: runs[0].ResourceVersion, IdempotencyKey: "source-repair-launch"}); err != nil {
		t.Fatal(err)
	}
	waitForControlRun(t, restarted, runs[0].RunID, func(value View) bool {
		return value.Run.Status == statestore.RunCompleted
	})
	if captured := builder.context(); !strings.Contains(string(captured.NodeInputs["task"]), "Original approved description") {
		t.Fatalf("context recovery substituted current source content: %#v", captured.NodeInputs)
	}
}

func TestSourceSnapshotReadRejectsContentAndAuthorityCorruption(t *testing.T) {
	_, db, _ := newControlTestService(t, false)
	_, work, observation, _ := seedAdmittedSource(t, db)
	service, _, request := sourceRunService(t, db, work, observation.ID)
	defer func() {
		if err := service.Close(); err != nil {
			t.Errorf("close service: %v", err)
		}
	}()
	run, err := service.Prepare(context.Background(), request, "source-validation-prepare")
	if err != nil {
		t.Fatal(err)
	}
	original, err := db.RunSourceSnapshot(context.Background(), run.RunID)
	if err != nil {
		t.Fatal(err)
	}
	// Simulate corrupt on-disk bytes after first proving the normal immutable
	// write guard. Each case retains the indexed IDs to exercise the read boundary.
	if _, err := db.SQL().Exec(`UPDATE run_source_snapshots SET snapshot_json=snapshot_json WHERE run_id=?`, run.RunID); err == nil {
		t.Fatal("run source snapshots are writable")
	}
	if _, err := db.SQL().Exec(`DROP TRIGGER run_source_no_update`); err != nil {
		t.Fatal(err)
	}
	changes := map[string]func(*statestore.RunSourceSnapshot){
		"content at same native revision": func(value *statestore.RunSourceSnapshot) {
			ticket, err := trackercontract.DecodeTicket(value.Ticket)
			if err != nil {
				t.Fatal(err)
			}
			ticket.Description = "Forged source content"
			value.Ticket, err = trackercontract.EncodeTicket(ticket)
			if err != nil {
				t.Fatal(err)
			}
		},
		"different account": func(value *statestore.RunSourceSnapshot) {
			value.Pin.AccountID = "another-account"
		},
		"different binding": func(value *statestore.RunSourceSnapshot) {
			value.BindingRevision++
		},
		"different approval": func(value *statestore.RunSourceSnapshot) {
			value.ApprovedAt = value.ApprovedAt.Add(time.Second)
		},
		"different lineage": func(value *statestore.RunSourceSnapshot) {
			value.LineageRevision++
		},
	}
	for name, change := range changes {
		t.Run(name, func(t *testing.T) {
			value := original
			change(&value)
			encoded, err := json.Marshal(map[string]any{"version": "darkstar.run-source/v1", "snapshot": value})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := db.SQL().Exec(`UPDATE run_source_snapshots SET snapshot_json=? WHERE run_id=?`, string(encoded), run.RunID); err != nil {
				t.Fatal(err)
			}
			if _, err := db.RunSourceSnapshot(context.Background(), run.RunID); err == nil {
				t.Fatal("accepted contradictory source snapshot")
			}
		})
	}
}

func TestNewNativeWorkRequiresApprovedObservationAndPreservesBusinessStatus(t *testing.T) {
	ctx := context.Background()
	_, db, _ := newControlTestService(t, false)
	workService, err := workmanagement.New(db)
	if err != nil {
		t.Fatal(err)
	}
	project, err := workService.RegisterProject(ctx, workmanagement.ProjectRegistration{Name: "Native source execution", Source: t.TempDir()}, "native-source-project")
	if err != nil {
		t.Fatal(err)
	}
	work, err := workService.CreateWork(ctx, workmanagement.CreateWorkRequest{ProjectID: project.ProjectID, Title: "Native requested outcome", Details: "Native source content"}, "native-source-create")
	if err != nil {
		t.Fatal(err)
	}
	service, _, request := sourceRunService(t, db, work.WorkItemID, "")
	defer func() {
		if err := service.Close(); err != nil {
			t.Errorf("close service: %v", err)
		}
	}()
	if _, err := service.Prepare(ctx, request, "native-source-unapproved"); err == nil {
		t.Fatal("new native work bypassed version-bound source approval")
	}
	cache, err := backlog.New(db, nativeRunSourceResolver{db: db}, backlog.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := cache.Refresh(ctx, project.ProjectID, 1, tracker.Query{PageSize: 10}); err != nil {
		t.Fatal(err)
	}
	tickets, err := db.BacklogTickets(ctx, project.ProjectID, 1, true, "", 10)
	if err != nil || len(tickets) != 1 {
		t.Fatalf("native source did not enter backlog: %#v %v", tickets, err)
	}
	approveSourceRun(t, db, project.ProjectID, work.WorkItemID, tickets[0].Observation, 1, true)
	request.SourceObservationID = tickets[0].ObservationID
	run, err := service.Create(ctx, request, "native-source-approved-run")
	if err != nil {
		t.Fatal(err)
	}
	waitForControlRun(t, service, run.RunID, func(value View) bool {
		return value.Run.Status == statestore.RunCompleted
	})
	ticket, err := db.NativeTicket(ctx, project.ProjectID, work.WorkItemID)
	if err != nil || ticket.State != statestore.NativeOpen {
		t.Fatalf("run completion mutated native business state: %#v %v", ticket, err)
	}
	retained, err := db.RunSourceSnapshot(ctx, run.RunID)
	if err != nil || retained.Ref.ID != work.WorkItemID || retained.ObservationID != tickets[0].ObservationID {
		t.Fatalf("native run did not retain exact native identity: %#v %v", retained, err)
	}
}

type nativeRunSourceResolver struct {
	db *sqlite.Database
}

func (r nativeRunSourceResolver) Resolve(_ context.Context, binding statestore.BacklogBinding) (backlog.ResolvedSource, error) {
	adapter, err := builtin.New(r.db, binding.ProjectID)
	if err != nil {
		return backlog.ResolvedSource{}, err
	}
	return backlog.ResolvedSource{Source: adapter, Browser: adapter, Config: adapter.ConfigPin()}, nil
}

func TestSyntheticStartDoesNotFabricateBusinessTickets(t *testing.T) {
	service, db, _ := newControlTestService(t, true)
	if _, err := service.Start(context.Background(), StartRequest{Scenario: ScenarioSuccess}, "synthetic-source-isolation"); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := db.SQL().QueryRow(`SELECT (SELECT count(*) FROM work_item_projection)+(SELECT count(*) FROM native_tickets)+(SELECT count(*) FROM native_work_mappings)`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("synthetic compatibility scenario fabricated business state: count=%d %v", count, err)
	}
}

type sourceContextFailure struct {
	*sqlite.Database
	fail bool
}

func (s *sourceContextFailure) SaveRunExecutionContext(ctx context.Context, value statestore.RunExecutionContext, revision uint64) (statestore.RunExecutionContext, error) {
	if s.fail {
		s.fail = false
		return statestore.RunExecutionContext{}, errors.New("injected context persistence interruption")
	}
	return s.Database.SaveRunExecutionContext(ctx, value, revision)
}

type sourceUncertainFactory struct{}

func (sourceUncertainFactory) Provider(_ context.Context, request ProviderRequest) (provider.Provider, error) {
	return fake.New(fake.Scenario{Health: provider.Health{State: provider.HealthAvailable, Provider: ProviderCodex, ProviderVersion: "test"}, Attempts: []fake.AttemptScenario{{AttemptID: request.AttemptID, Steps: []fake.Step{fake.Emit(provider.Event{Sequence: 1, Kind: provider.EventTurnStarted, Payload: json.RawMessage(`{}`)}), fake.Pause(time.Hour)}, Result: provider.SucceededResult{StructuredOutput: json.RawMessage(`{"artifact":"candidate"}`)}, CancelResult: provider.CancelResult{Disposition: provider.CancelUncertain}}}}, fake.WithClock(fake.NewManualClock(time.Now().UTC())))
}

func sourceRunPlanner() workflowDispatchPlanner {
	planner := workflowDispatchPlannerFor(workflow.NoCheckpoint{}, true)
	planner.definition.Document.Spec.Inputs = map[workflow.Identifier]workflow.ValueDeclaration{"story": {Type: workflow.ValueObject}, "task": {Type: workflow.ValueTask, Resource: &workflow.Resource{Source: workflow.TaskResource{}}}}
	node := planner.definition.Document.Spec.Nodes["design"].(workflow.ReasoningNode)
	node.Common.Inputs["story"] = workflow.RequiredBinding{From: "run.input.story", Type: workflow.ValueObject}
	node.Common.Inputs["task"] = workflow.RequiredBinding{From: "run.input.task", Type: workflow.ValueTask}
	planner.definition.Document.Spec.Nodes["design"] = node
	return planner
}

func sourceRunRequest(work, observation string) CreateRequest {
	return CreateRequest{WorkItemID: work, SourceObservationID: observation, WorkflowID: "test/workflow", WorkflowVersion: "1.0.0", Preparation: &PreparationInput{RouteOverride: &workflow.RouteRequest{From: "design", Until: []workflow.Identifier{"design"}}}}
}

func sourceRunService(t *testing.T, db statestore.Store, work, observation string) (*Service, *capturingAttemptBuilder, CreateRequest) {
	t.Helper()
	service, err := New(context.Background(), db, &controlTestFactory{}, controlTestLogs{})
	if err != nil {
		t.Fatal(err)
	}
	if err := service.SetWorkflowPlanner(sourceRunPlanner()); err != nil {
		t.Fatal(err)
	}
	builder := &capturingAttemptBuilder{}
	if err := service.SetWorkflowDispatch(workflowFakeFactory{}, builder); err != nil {
		t.Fatal(err)
	}
	return service, builder, sourceRunRequest(work, observation)
}

func seedAdmittedSource(t *testing.T, db *sqlite.Database) (string, string, statestore.BacklogObservation, statestore.BacklogRefreshState) {
	t.Helper()
	ctx := context.Background()
	project, work := identity.Deterministic("project_", "source-run-project"), identity.Deterministic("work_", "source-run-work")
	now := time.Now().UTC()
	if _, err := db.Append(ctx, pendingEvent("project.created", statestore.AggregateProject, project, 0, project, "source-project", statestore.ActorUser, "test", now, map[string]any{"name": "Source runs", "sourceHash": strings.Repeat("c", 64)})); err != nil {
		t.Fatal(err)
	}
	scope := tracker.Scope{Namespace: tracker.Namespace{Provider: "github_issues", Host: "github.com", TenantID: "owner-node", ScopeID: "repo-node"}, ContainerID: "repo-node"}
	selected, err := db.SelectBacklogSource(ctx, project, 1, statestore.ExternalBacklogSource{ConnectionID: "test-connection", ConnectionRevision: "1", Scope: scope}, now)
	if err != nil {
		t.Fatal(err)
	}
	pin := tracker.Pin{AdapterConfigPin: tracker.AdapterConfigPin{ContractVersion: tracker.Version, AdapterID: "github_issues", AdapterVersion: "1", InstallationID: "test-connection", AccountID: "account-node", BindingRevision: "2", ConfigRevision: "1", ConfigDigest: strings.Repeat("a", 64)}, CapabilitiesDigest: strings.Repeat("b", 64)}
	state := statestore.BacklogRefreshState{ProjectID: project, BindingRevision: selected.Revision, Generation: 1, Phase: statestore.BacklogComplete, Query: tracker.Query{PageSize: 10}, Pin: pin, StartedAt: now, UpdatedAt: now, LastSuccessAt: now}
	observation := sourceRunObservation(t, state, "1", "Original approved description")
	commitSourceRunObservation(t, db, &state, observation)
	approveSourceRun(t, db, project, work, observation, selected.Revision, false)
	return project, work, observation, state
}

func sourceRunObservation(t *testing.T, state statestore.BacklogRefreshState, revision, description string) statestore.BacklogObservation {
	t.Helper()
	scope := tracker.Scope{Namespace: tracker.Namespace{Provider: "github_issues", Host: "github.com", TenantID: "owner-node", ScopeID: "repo-node"}, ContainerID: "repo-node"}
	now := time.Now().UTC()
	ticket := tracker.Ticket{Ref: tracker.TicketRef{Namespace: scope.Namespace, ID: "issue-node"}, Revision: revision, Key: "owner/repo#1", URL: "https://github.com/owner/repo/issues/1", Title: "Approved source title", Description: description, BusinessState: tracker.Known[tracker.NamedID]{Value: tracker.NamedID{ID: "open", Name: "Open"}}, BusinessStateReason: tracker.Unsupported[tracker.NamedID]{Reason: "not supported"}, IssueType: tracker.Known[tracker.NamedID]{Value: tracker.NamedID{ID: "issue", Name: "Issue"}}, Sprint: tracker.Unsupported[[]tracker.NamedID]{Reason: "not supported"}, Assignees: tracker.Known[[]tracker.NamedID]{Value: []tracker.NamedID{}}, Labels: tracker.Known[[]tracker.NamedID]{Value: []tracker.NamedID{}}, Priority: tracker.Unsupported[tracker.NamedID]{Reason: "not supported"}, Relationships: tracker.Unknown[[]tracker.Relation]{Reason: "not queried"}, Archived: tracker.Known[bool]{Value: false}, UpdatedAt: tracker.Known[time.Time]{Value: now}, Placement: tracker.Known[tracker.Scope]{Value: scope}, Freshness: tracker.Fresh{ObservedAt: now, Revision: revision}, EvidenceRef: "source-evidence-" + revision}
	encoded, err := trackercontract.EncodeTicket(ticket)
	if err != nil {
		t.Fatal(err)
	}
	id, key, digest, err := trackercontract.ObservationIdentity(ticket)
	if err != nil {
		t.Fatal(err)
	}
	return statestore.BacklogObservation{ID: id, TicketKey: key, NativeRevision: revision, ContentDigest: digest, Ref: ticket.Ref, Ticket: encoded, ObservedAt: now, EvidenceRef: ticket.EvidenceRef}
}

func commitSourceRunObservation(t *testing.T, db *sqlite.Database, state *statestore.BacklogRefreshState, observation statestore.BacklogObservation) {
	t.Helper()
	expected := state.Revision
	state.Revision++
	state.UpdatedAt, state.LastSuccessAt = observation.ObservedAt, observation.ObservedAt
	if err := db.CommitBacklogRefresh(context.Background(), statestore.BacklogCommit{State: *state, ExpectedRevision: expected, Observations: []statestore.BacklogObservation{observation}, Tickets: []statestore.BacklogCachedTicket{{ProjectID: state.ProjectID, BindingRevision: state.BindingRevision, TicketKey: observation.TicketKey, ObservationID: observation.ID, State: statestore.BacklogAvailable, CheckedAt: observation.ObservedAt, SeenGeneration: state.Generation, EvidenceRef: observation.EvidenceRef}}}); err != nil {
		t.Fatal(err)
	}
}

func approveSourceRun(t *testing.T, db *sqlite.Database, project, work string, observation statestore.BacklogObservation, binding uint64, existing bool) {
	t.Helper()
	now := time.Now().UTC()
	ticket, err := trackercontract.DecodeTicket(observation.Ticket)
	if err != nil {
		t.Fatal(err)
	}
	event := pendingEvent("work.created", statestore.AggregateWork, work, 0, work, "admit-source-work", statestore.ActorUser, "test", now, map[string]any{"projectId": project, "title": ticket.Title, "details": ticket.Description, "sourceHash": observation.ContentDigest, "priority": 0, "evidence": []string{"docs/local-plan.md", ticket.EvidenceRef}})
	event.Metadata = json.RawMessage(`{"sourceAdmission":true}`)
	mutation := statestore.SourceAdmissionMutation{ProjectID: project, WorkID: work, ObservationID: observation.ID, BindingRevision: binding, IdempotencyKey: "approve-source-" + observation.NativeRevision, RequestDigest: observation.ContentDigest, AdmissionID: identity.Deterministic("admission_", observation.ID), Actor: "test", ApprovedAt: now, NewWork: event}
	if existing {
		mutation.ExistingWorkID = work
	}
	if _, err := db.AdmitSourceTicket(context.Background(), mutation); err != nil {
		t.Fatalf("admit source: %v", err)
	}
}
