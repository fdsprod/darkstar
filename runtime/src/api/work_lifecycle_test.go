package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"darkstar/src/adapters/statestore/sqlite"
	"darkstar/src/core/worklifecycle"
	"darkstar/src/core/workmanagement"
)

type lifecycleStub struct {
	plan       worklifecycle.Plan
	result     worklifecycle.Result
	applyError error
	plannedID  string
	planned    worklifecycle.PlanRequest
	appliedID  string
	applied    worklifecycle.ApplyRequest
}

func (s *lifecycleStub) Plan(_ context.Context, id string, request worklifecycle.PlanRequest) (worklifecycle.Plan, error) {
	s.plannedID, s.planned = id, request
	return s.plan, nil
}

func (s *lifecycleStub) Apply(_ context.Context, id string, request worklifecycle.ApplyRequest) (worklifecycle.Result, error) {
	s.appliedID, s.applied = id, request
	return s.result, s.applyError
}

func startWorkLifecycleAPI(t *testing.T, lifecycle WorkLifecycleService) (*Server, Endpoint) {
	t.Helper()
	ctx := context.Background()
	database, err := sqlite.Open(ctx, filepath.Join(t.TempDir(), "work-lifecycle-api.db"), sqlite.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = database.Close()
	})
	work, err := workmanagement.New(database)
	if err != nil {
		t.Fatal(err)
	}
	server, err := NewServer(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := server.SetWork(work); err != nil {
		t.Fatal(err)
	}
	if err := server.SetWorkLifecycle(lifecycle); err != nil {
		t.Fatal(err)
	}
	if err := server.Start(ctx, 4321, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		closeTestServer(t, server)
	})
	endpoint, ok := server.Endpoint()
	if !ok {
		t.Fatal("server endpoint unavailable")
	}
	return server, endpoint
}

func TestWorkTransitionPlanAndApplyRoutesPreserveTypedContract(t *testing.T) {
	workID := "work_01K3Z1C1AAAAAAAAAAAAAAAAAA"
	plan := worklifecycle.Plan{SchemaVersion: 1, WorkItemID: workID, State: worklifecycle.StateBacklog, ResourceVersion: 7, Targets: []worklifecycle.TargetDecision{{Target: worklifecycle.StateReady, Availability: worklifecycle.AvailabilityEnabled, DisabledReasons: []worklifecycle.DisabledReason{}, Confirmation: worklifecycle.ConfirmationNone}}}
	stub := &lifecycleStub{plan: plan, result: worklifecycle.Result{SchemaVersion: 1, Target: worklifecycle.StateReady, Effect: worklifecycle.EffectPrepared, Before: plan, After: plan}}
	_, endpoint := startWorkLifecycleAPI(t, stub)

	planResponse := lifecycleRequest(t, endpoint, http.MethodGet, "/api/v1/work-items/"+workID+"/transition-plan?target=ready&workflowId=delivery&workflowVersion=1.0.0&profile=fast", "", "", "")
	if planResponse.StatusCode != http.StatusOK || planResponse.Header.Get("ETag") != `"7"` {
		t.Fatalf("plan status=%d etag=%q", planResponse.StatusCode, planResponse.Header.Get("ETag"))
	}
	var receivedPlan worklifecycle.Plan
	decodeJSON(t, planResponse, &receivedPlan)
	_ = planResponse.Body.Close()
	if receivedPlan.WorkItemID != workID || stub.plannedID != workID || stub.planned.Preparation == nil || stub.planned.Preparation.Profile != "fast" {
		t.Fatalf("plan=(%#v), request=(%s, %#v)", receivedPlan, stub.plannedID, stub.planned)
	}

	applyResponse := lifecycleRequest(t, endpoint, http.MethodPost, "/api/v1/work-items/"+workID+"/transitions", `{"target":"ready","preparation":{"workflowId":"delivery","workflowVersion":"1.0.0"}}`, "work-transition-key", `"7"`)
	if applyResponse.StatusCode != http.StatusOK || applyResponse.Header.Get("ETag") != `"7"` {
		t.Fatalf("apply status=%d etag=%q", applyResponse.StatusCode, applyResponse.Header.Get("ETag"))
	}
	var result worklifecycle.Result
	decodeJSON(t, applyResponse, &result)
	_ = applyResponse.Body.Close()
	if result.Effect != worklifecycle.EffectPrepared || stub.appliedID != workID || stub.applied.ExpectedResourceVersion != 7 || stub.applied.IdempotencyKey != "work-transition-key" || stub.applied.Preparation == nil {
		t.Fatalf("result=%#v request=(%s, %#v)", result, stub.appliedID, stub.applied)
	}
}

func TestWorkTransitionStaleApplyReturnsRefreshedPlan(t *testing.T) {
	workID := "work_01K3Z1C1AAAAAAAAAAAAAAAAAA"
	current := worklifecycle.Plan{SchemaVersion: 1, WorkItemID: workID, State: worklifecycle.StateRunning, ResourceVersion: 9, Targets: []worklifecycle.TargetDecision{{Target: worklifecycle.StateRunning, Availability: worklifecycle.AvailabilityDisabled, DisabledReasons: []worklifecycle.DisabledReason{worklifecycle.ReasonCurrentState}, Confirmation: worklifecycle.ConfirmationNone}}}
	stub := &lifecycleStub{applyError: &worklifecycle.VersionConflictError{Expected: 7, Current: current}}
	_, endpoint := startWorkLifecycleAPI(t, stub)
	response := lifecycleRequest(t, endpoint, http.MethodPost, "/api/v1/work-items/"+workID+"/transitions", `{"target":"running"}`, "work-transition-stale", `"7"`)
	defer func() {
		_ = response.Body.Close()
	}()
	if response.StatusCode != http.StatusPreconditionFailed {
		t.Fatalf("status = %d", response.StatusCode)
	}
	var problem struct {
		Code               string             `json:"code"`
		ResourceVersion    int64              `json:"resourceVersion"`
		WorkTransitionPlan worklifecycle.Plan `json:"workTransitionPlan"`
	}
	if err := json.NewDecoder(response.Body).Decode(&problem); err != nil {
		t.Fatal(err)
	}
	if problem.Code != "WORK_TRANSITION_VERSION_CONFLICT" || problem.ResourceVersion != 9 || problem.WorkTransitionPlan.State != worklifecycle.StateRunning || len(problem.WorkTransitionPlan.Targets) != 1 {
		t.Fatalf("problem = %#v", problem)
	}
}

func lifecycleRequest(t *testing.T, endpoint Endpoint, method, resource, body, key, ifMatch string) *http.Response {
	t.Helper()
	request, err := http.NewRequest(method, endpoint.BaseURL()+resource, bytes.NewBufferString(body))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", endpoint.AuthorizationHeader())
	if body != "" {
		request.Header.Set("Content-Type", "application/json")
	}
	if key != "" {
		request.Header.Set("Idempotency-Key", key)
	}
	if ifMatch != "" {
		request.Header.Set("If-Match", ifMatch)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	return response
}
