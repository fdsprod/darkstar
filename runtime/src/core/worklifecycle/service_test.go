package worklifecycle

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"darkstar/src/core/runexecution"
	"darkstar/src/ports/statestore"
)

var testTime = time.Date(2026, 9, 7, 12, 0, 0, 0, time.UTC)

type memoryStore struct {
	statestore.Store
	project     statestore.ProjectProjection
	work        statestore.WorkItemProjection
	runs        []statestore.RunProjection
	nodes       map[string][]statestore.NodeProjection
	attempts    map[string][]statestore.AttemptProjection
	inputs      map[string][]statestore.InputRequestProjection
	permissions map[string][]statestore.ProviderPermissionProjection
	readiness   map[string]statestore.ReadinessAssessmentProjection
	commands    map[string]statestore.CommandEvidence
}

func newMemoryStore() *memoryStore {
	return &memoryStore{
		project: statestore.ProjectProjection{ProjectID: "project_01K3Z1C1AAAAAAAAAAAAAAAAAA", Status: statestore.ProjectActive, ResourceVersion: 1, LastGlobalPosition: 1},
		work:    statestore.WorkItemProjection{WorkItemID: "work_01K3Z1C1AAAAAAAAAAAAAAAAAAA", ProjectID: "project_01K3Z1C1AAAAAAAAAAAAAAAAAA", Status: statestore.WorkItemOpen, ResourceVersion: 1, LastGlobalPosition: 2},
		nodes:   map[string][]statestore.NodeProjection{}, attempts: map[string][]statestore.AttemptProjection{}, inputs: map[string][]statestore.InputRequestProjection{}, permissions: map[string][]statestore.ProviderPermissionProjection{}, readiness: map[string]statestore.ReadinessAssessmentProjection{}, commands: map[string]statestore.CommandEvidence{},
	}
}

func (s *memoryStore) Project(context.Context, string) (statestore.ProjectProjection, error) {
	return s.project, nil
}
func (s *memoryStore) WorkItem(context.Context, string) (statestore.WorkItemProjection, error) {
	return s.work, nil
}
func (s *memoryStore) RunsForWorkItem(context.Context, string) ([]statestore.RunProjection, error) {
	return append([]statestore.RunProjection(nil), s.runs...), nil
}
func (s *memoryStore) Run(_ context.Context, id string) (statestore.RunProjection, error) {
	for _, run := range s.runs {
		if run.RunID == id {
			return run, nil
		}
	}
	return statestore.RunProjection{}, statestore.ErrNotFound
}
func (s *memoryStore) NodesForRun(_ context.Context, id string) ([]statestore.NodeProjection, error) {
	return append([]statestore.NodeProjection(nil), s.nodes[id]...), nil
}
func (s *memoryStore) AttemptsForRun(_ context.Context, id string) ([]statestore.AttemptProjection, error) {
	return append([]statestore.AttemptProjection(nil), s.attempts[id]...), nil
}
func (s *memoryStore) InputRequestsForRun(_ context.Context, id string) ([]statestore.InputRequestProjection, error) {
	return append([]statestore.InputRequestProjection(nil), s.inputs[id]...), nil
}
func (s *memoryStore) ProviderPermissionsForAttempt(_ context.Context, id string) ([]statestore.ProviderPermissionProjection, error) {
	return append([]statestore.ProviderPermissionProjection(nil), s.permissions[id]...), nil
}
func (s *memoryStore) LatestReadinessAssessmentForRun(_ context.Context, id string) (statestore.ReadinessAssessmentProjection, error) {
	value, ok := s.readiness[id]
	if !ok {
		return statestore.ReadinessAssessmentProjection{}, statestore.ErrNotFound
	}
	return value, nil
}

func (s *memoryStore) BeginCommand(_ context.Context, request statestore.BeginCommandRequest) (statestore.CommandEvidence, bool, error) {
	id := request.Scope + "/" + request.IdempotencyKey
	if existing, ok := s.commands[id]; ok {
		if existing.RequestDigest != request.RequestDigest {
			return statestore.CommandEvidence{}, true, errors.New("idempotency key reused for different request")
		}
		return existing, true, nil
	}
	value := statestore.CommandEvidence{Scope: request.Scope, IdempotencyKey: request.IdempotencyKey, RequestDigest: request.RequestDigest, Status: "pending", CreatedAt: request.CreatedAt}
	s.commands[id] = value
	return value, false, nil
}

func (s *memoryStore) CompleteCommand(_ context.Context, request statestore.CompleteCommandRequest) (statestore.CommandEvidence, error) {
	id := request.Scope + "/" + request.IdempotencyKey
	value, ok := s.commands[id]
	if !ok || value.Status != "pending" {
		return statestore.CommandEvidence{}, errors.New("command is missing or complete")
	}
	value.Status, value.ResponseStatus, value.Response, value.CompletedAt = "completed", &request.ResponseStatus, append(json.RawMessage(nil), request.Response...), &request.CompletedAt
	s.commands[id] = value
	return value, nil
}

type fakeRuntime struct {
	prepareError              error
	store                     *memoryStore
	prepareCalls, launchCalls int
	prepareKey, launchKey     string
	launchVersion             uint64
}

func (r *fakeRuntime) Prepare(_ context.Context, request runexecution.CreateRequest, key string) (statestore.RunProjection, error) {
	r.prepareCalls++
	r.prepareKey = key
	if r.prepareError != nil {
		return statestore.RunProjection{}, r.prepareError
	}
	run := statestore.RunProjection{RunID: "run_01K3Z1D1AAAAAAAAAAAAAAAAAAA", WorkItemID: request.WorkItemID, WorkflowID: request.WorkflowID, WorkflowVersion: request.WorkflowVersion, Status: statestore.RunReady, ResourceVersion: 2, LastGlobalPosition: 3}
	r.store.runs = append(r.store.runs, run)
	return run, nil
}

func (r *fakeRuntime) Launch(_ context.Context, request runexecution.ControlRequest) (statestore.RunProjection, error) {
	r.launchCalls++
	r.launchKey = request.IdempotencyKey
	r.launchVersion = request.ExpectedResourceVersion
	for index := range r.store.runs {
		if r.store.runs[index].RunID == request.RunID {
			r.store.runs[index].Status = statestore.RunQueued
			r.store.runs[index].ResourceVersion++
			r.store.runs[index].LastGlobalPosition++
			r.store.work.Status = statestore.WorkItemActive
			r.store.work.ResourceVersion++
			return r.store.runs[index], nil
		}
	}
	return statestore.RunProjection{}, statestore.ErrNotFound
}
func (r *fakeRuntime) Pause(context.Context, runexecution.ControlRequest) (statestore.RunProjection, error) {
	panic("unexpected Pause")
}
func (r *fakeRuntime) Resume(context.Context, runexecution.ControlRequest) (statestore.RunProjection, error) {
	panic("unexpected Resume")
}
func (r *fakeRuntime) Retry(context.Context, runexecution.RetryRequest) (statestore.RunProjection, error) {
	panic("unexpected Retry")
}
func (r *fakeRuntime) Cancel(context.Context, runexecution.ControlRequest) (statestore.RunProjection, error) {
	panic("unexpected Cancel")
}

func readyRun() statestore.RunProjection {
	return statestore.RunProjection{RunID: "run_01K3Z1D1AAAAAAAAAAAAAAAAAAA", WorkItemID: "work_01K3Z1C1AAAAAAAAAAAAAAAAAAA", Status: statestore.RunReady, ResourceVersion: 2, LastGlobalPosition: 3}
}

func target(plan Plan, state State) TargetDecision {
	for _, decision := range plan.Targets {
		if decision.Target == state {
			return decision
		}
	}
	panic("missing target " + state)
}

func contains(values []DisabledReason, wanted DisabledReason) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}

func TestPlanIsExhaustiveAndAllowsServerResolvedReadyPreparation(t *testing.T) {
	store := newMemoryStore()
	runtime := &fakeRuntime{store: store}
	service, _ := New(store, runtime)
	without, err := service.Plan(context.Background(), store.work.WorkItemID, PlanRequest{Target: StateReady})
	if err != nil {
		t.Fatal(err)
	}
	if without.State != StateBacklog || without.ResourceVersion != store.work.LastGlobalPosition || len(without.Targets) != len(allStates) {
		t.Fatalf("plan = %#v", without)
	}
	if decision := target(without, StateReady); decision.Availability != AvailabilityEnabled || len(decision.DisabledReasons) != 0 {
		t.Fatalf("ready = %#v", decision)
	}
	sibling, err := service.Plan(context.Background(), store.work.WorkItemID, PlanRequest{Target: StateRunning})
	if err != nil {
		t.Fatal(err)
	}
	if decision := target(sibling, StateReady); decision.Availability != AvailabilityEnabled || len(decision.DisabledReasons) != 0 {
		t.Fatalf("sibling ready = %#v", decision)
	}
	with, err := service.Plan(context.Background(), store.work.WorkItemID, PlanRequest{Target: StateReady, Preparation: &Preparation{WorkflowID: " delivery ", WorkflowVersion: " 1.0.0 "}})
	if err != nil {
		t.Fatal(err)
	}
	if decision := target(with, StateReady); decision.Availability != AvailabilityEnabled || len(decision.DisabledReasons) != 0 {
		t.Fatalf("ready = %#v", decision)
	}
	if decision := target(with, StateDone); decision.Availability != AvailabilityEnabled || decision.Confirmation != ConfirmationRequired {
		t.Fatalf("done = %#v", decision)
	}
	seen := map[State]bool{}
	for _, decision := range with.Targets {
		if seen[decision.Target] {
			t.Fatalf("duplicate target %s", decision.Target)
		}
		seen[decision.Target] = true
	}
}

func TestApplyBacklogToReadyAndReadyToRunningAreReplaySafe(t *testing.T) {
	ctx := context.Background()
	store := newMemoryStore()
	runtime := &fakeRuntime{store: store}
	service, _ := New(store, runtime)
	readyRequest := ApplyRequest{PlanRequest: PlanRequest{Target: StateReady, Preparation: &Preparation{WorkflowID: "delivery", WorkflowVersion: "1.0.0"}}, ExpectedResourceVersion: store.work.LastGlobalPosition, IdempotencyKey: "transition-ready"}
	ready, err := service.Apply(ctx, store.work.WorkItemID, readyRequest)
	if err != nil {
		t.Fatal(err)
	}
	replayedReady, err := service.Apply(ctx, store.work.WorkItemID, readyRequest)
	if err != nil {
		t.Fatal(err)
	}
	if ready.After.State != StateReady || replayedReady.Run == nil || replayedReady.Run.RunID != ready.Run.RunID || runtime.prepareCalls != 1 || runtime.prepareKey != readyRequest.IdempotencyKey {
		t.Fatalf("ready=(%#v, %#v), calls=%d", ready, replayedReady, runtime.prepareCalls)
	}
	runningRequest := ApplyRequest{PlanRequest: PlanRequest{Target: StateRunning}, ExpectedResourceVersion: ready.After.ResourceVersion, IdempotencyKey: "transition-start"}
	running, err := service.Apply(ctx, store.work.WorkItemID, runningRequest)
	if err != nil {
		t.Fatal(err)
	}
	replayedRunning, err := service.Apply(ctx, store.work.WorkItemID, runningRequest)
	if err != nil {
		t.Fatal(err)
	}
	if running.Before.State != StateReady || running.After.State != StateRunning || running.After.ResourceVersion != 4 || replayedRunning.Effect != EffectStarted || runtime.launchCalls != 1 || runtime.launchKey != runningRequest.IdempotencyKey || runtime.launchVersion != 2 {
		t.Fatalf("running=(%#v, %#v), calls=%d", running, replayedRunning, runtime.launchCalls)
	}
}

func TestApplyStaleWorkVersionReturnsRefreshedPlanWithoutEffect(t *testing.T) {
	store := newMemoryStore()
	store.work.ResourceVersion = 4
	store.work.LastGlobalPosition = 4
	runtime := &fakeRuntime{store: store}
	service, _ := New(store, runtime)
	_, err := service.Apply(context.Background(), store.work.WorkItemID, ApplyRequest{PlanRequest: PlanRequest{Target: StateReady, Preparation: &Preparation{WorkflowID: "delivery", WorkflowVersion: "1"}}, ExpectedResourceVersion: 3, IdempotencyKey: "transition-stale"})
	var conflict *VersionConflictError
	if !errors.As(err, &conflict) || conflict.Current.ResourceVersion != 4 || len(conflict.Current.Targets) != len(allStates) || runtime.prepareCalls != 0 {
		t.Fatalf("conflict=(%#v, %v), calls=%d", conflict, err, runtime.prepareCalls)
	}
	_, err = service.Apply(context.Background(), store.work.WorkItemID, ApplyRequest{PlanRequest: PlanRequest{Target: StateReady, Preparation: &Preparation{WorkflowID: "delivery", WorkflowVersion: "1"}}, ExpectedResourceVersion: 3, IdempotencyKey: "transition-stale"})
	if !errors.Is(err, ErrVersionConflict) || runtime.prepareCalls != 0 {
		t.Fatalf("replay = %v, calls=%d", err, runtime.prepareCalls)
	}
}

func TestApplyRejectsStaleDependentProjectionVersionWithoutEffect(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*memoryStore, statestore.RunProjection)
	}{
		{"run", func(store *memoryStore, run statestore.RunProjection) {
			store.runs[0].ResourceVersion++
			store.runs[0].LastGlobalPosition = 11
		}},
		{"checkpoint", func(store *memoryStore, run statestore.RunProjection) {
			store.nodes[run.RunID] = []statestore.NodeProjection{{RunID: run.RunID, Status: statestore.NodeWaitingCheckpoint, ResourceVersion: 1, LastGlobalPosition: 11}}
		}},
		{"readiness", func(store *memoryStore, run statestore.RunProjection) {
			store.readiness[run.RunID] = statestore.ReadinessAssessmentProjection{RunID: run.RunID, Disposition: "choice_required", Status: statestore.ReadinessAssessmentPending, ResourceVersion: 1, LastGlobalPosition: 11}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store := newMemoryStore()
			run := readyRun()
			run.LastGlobalPosition = 10
			store.runs = []statestore.RunProjection{run}
			runtime := &fakeRuntime{store: store}
			service, _ := New(store, runtime)
			plan, err := service.Plan(context.Background(), store.work.WorkItemID, PlanRequest{Target: StateRunning})
			if err != nil || plan.ResourceVersion != 10 {
				t.Fatalf("initial plan = %#v, %v", plan, err)
			}
			test.mutate(store, run)
			_, err = service.Apply(context.Background(), store.work.WorkItemID, ApplyRequest{PlanRequest: PlanRequest{Target: StateRunning}, ExpectedResourceVersion: plan.ResourceVersion, IdempotencyKey: "stale-" + test.name})
			var conflict *VersionConflictError
			if !errors.As(err, &conflict) || conflict.Current.ResourceVersion != 11 || runtime.launchCalls != 0 {
				t.Fatalf("conflict=(%#v, %v), calls=%d", conflict, err, runtime.launchCalls)
			}
		})
	}
}

func TestPlanVersionCoversEveryAuthorityProjection(t *testing.T) {
	store := newMemoryStore()
	run := readyRun()
	run.LastGlobalPosition = 4
	store.runs = []statestore.RunProjection{run, {RunID: "run_history", WorkItemID: store.work.WorkItemID, Status: statestore.RunCompleted, LastGlobalPosition: 5}}
	store.nodes[run.RunID] = []statestore.NodeProjection{{RunID: run.RunID, LastGlobalPosition: 6}}
	store.attempts[run.RunID] = []statestore.AttemptProjection{{AttemptID: "attempt_1", RunID: run.RunID, LastGlobalPosition: 7}}
	store.inputs[run.RunID] = []statestore.InputRequestProjection{{RunID: run.RunID, Status: statestore.InputRequestAnswered, LastGlobalPosition: 8}}
	store.permissions["attempt_1"] = []statestore.ProviderPermissionProjection{{RunID: run.RunID, AttemptID: "attempt_1", Status: statestore.ProviderPermissionResponded, LastGlobalPosition: 9}}
	store.readiness[run.RunID] = statestore.ReadinessAssessmentProjection{RunID: run.RunID, Disposition: "ready", LastGlobalPosition: 10}
	service, _ := New(store, &fakeRuntime{store: store})
	plan, err := service.Plan(context.Background(), store.work.WorkItemID, PlanRequest{Target: StateRunning})
	if err != nil || plan.ResourceVersion != 10 {
		t.Fatalf("plan = %#v, %v", plan, err)
	}
}

func TestPlanRejectsCheckpointReadinessPolicyAndConcurrentRunConflicts(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*memoryStore)
		reason DisabledReason
	}{
		{"checkpoint", func(store *memoryStore) {
			store.nodes[store.runs[0].RunID] = []statestore.NodeProjection{{Status: statestore.NodeWaitingCheckpoint}}
		}, ReasonUnresolvedCheckpoint},
		{"readiness", func(store *memoryStore) {
			store.readiness[store.runs[0].RunID] = statestore.ReadinessAssessmentProjection{Disposition: "choice_required", Status: statestore.ReadinessAssessmentPending}
		}, ReasonReadinessRequired},
		{"policy", func(store *memoryStore) {
			store.readiness[store.runs[0].RunID] = statestore.ReadinessAssessmentProjection{Disposition: "policy_blocked", Status: statestore.ReadinessAssessmentPending}
		}, ReasonPolicyBlocked},
		{"concurrency", func(store *memoryStore) {
			store.runs = append(store.runs, statestore.RunProjection{RunID: "run_other", WorkItemID: store.work.WorkItemID, Status: statestore.RunWaiting, ResourceVersion: 1, LastGlobalPosition: 2})
		}, ReasonConcurrencyConflict},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store := newMemoryStore()
			store.runs = []statestore.RunProjection{readyRun()}
			test.mutate(store)
			service, _ := New(store, &fakeRuntime{store: store})
			plan, err := service.Plan(context.Background(), store.work.WorkItemID, PlanRequest{Target: StateRunning})
			if err != nil {
				t.Fatal(err)
			}
			decision := target(plan, StateRunning)
			if decision.Availability != AvailabilityDisabled || !contains(decision.DisabledReasons, test.reason) {
				t.Fatalf("decision = %#v", decision)
			}
		})
	}
}

func TestApprovedReadinessOverrideAllowsReadyToRunning(t *testing.T) {
	store := newMemoryStore()
	store.runs = []statestore.RunProjection{readyRun()}
	decidedAt := testTime
	store.readiness[store.runs[0].RunID] = statestore.ReadinessAssessmentProjection{Disposition: "choice_required", Status: statestore.ReadinessAssessmentDecided, Decision: &statestore.ReadinessDecisionProjection{Choice: "accept_route_change", DecidedAt: decidedAt}}
	service, _ := New(store, &fakeRuntime{store: store})
	plan, err := service.Plan(context.Background(), store.work.WorkItemID, PlanRequest{Target: StateRunning})
	if err != nil {
		t.Fatal(err)
	}
	if decision := target(plan, StateRunning); decision.Availability != AvailabilityEnabled {
		t.Fatalf("decision = %#v", decision)
	}
}

func TestPlanPrefersNewestNonTerminalRunOverNewerTerminalHistory(t *testing.T) {
	store := newMemoryStore()
	live := readyRun()
	live.LastGlobalPosition = 8
	terminal := readyRun()
	terminal.RunID = "run_terminal"
	terminal.Status = statestore.RunCancelled
	terminal.LastGlobalPosition = 9
	store.runs = []statestore.RunProjection{terminal, live}
	service, _ := New(store, &fakeRuntime{store: store})
	plan, err := service.Plan(context.Background(), store.work.WorkItemID, PlanRequest{Target: StateRunning})
	if err != nil {
		t.Fatal(err)
	}
	if plan.RunID != live.RunID || plan.State != StateReady || plan.ResourceVersion != terminal.LastGlobalPosition || target(plan, StateRunning).Availability != AvailabilityEnabled {
		t.Fatalf("plan = %#v", plan)
	}
}

func TestPreparationCannotLeakIntoSiblingTargets(t *testing.T) {
	store := newMemoryStore()
	service, _ := New(store, &fakeRuntime{store: store})
	_, err := service.Plan(context.Background(), store.work.WorkItemID, PlanRequest{Target: StateRunning, Preparation: &Preparation{WorkflowID: "delivery", WorkflowVersion: "1"}})
	if !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("error = %v", err)
	}
}

func TestPreparationFailurePreservesCauseOnReplay(t *testing.T) {
	store := newMemoryStore()
	runtime := &fakeRuntime{store: store, prepareError: errors.New("unsupported provider version")}
	service, _ := New(store, runtime)
	request := ApplyRequest{PlanRequest: PlanRequest{Target: StateReady}, ExpectedResourceVersion: 2, IdempotencyKey: "prepare-provider-failure"}
	_, first := service.Apply(context.Background(), store.work.WorkItemID, request)
	_, replayed := service.Apply(context.Background(), store.work.WorkItemID, request)
	if first == nil || replayed == nil || first.Error() != replayed.Error() || errors.Is(first, ErrRejected) || runtime.prepareCalls != 1 {
		t.Fatalf("errors = %v / %v, calls = %d", first, replayed, runtime.prepareCalls)
	}
}
