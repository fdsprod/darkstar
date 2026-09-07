package api

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"darkstar/src/core/runexecution"
	"darkstar/src/ports/statestore"
)

func TestRunAPIExposesVersionedIdempotentControls(t *testing.T) {
	server, err := NewServer(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	runs := &recordingRunService{run: apiTestRun()}
	if err := server.SetRuns(runs); err != nil {
		t.Fatal(err)
	}
	if err := server.Start(context.Background(), 1234, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	defer closeTestServer(t, server)
	endpoint, _ := server.Endpoint()
	runID := runs.run.RunID

	launched := runControlRequest(t, endpoint, "/api/v1/runs/"+runID+"/start", "", "launch-api", `"3"`)
	if launched.StatusCode != http.StatusOK || launched.Header.Get("ETag") != `"3"` || runs.action != "start" || runs.control.RunID != runID || runs.control.ExpectedResourceVersion != 3 || runs.control.IdempotencyKey != "launch-api" {
		t.Fatalf("start status=%d etag=%q action=%q request=%#v", launched.StatusCode, launched.Header.Get("ETag"), runs.action, runs.control)
	}
	_ = launched.Body.Close()

	digest := strings.Repeat("b", 64)
	confirmed := runControlRequest(t, endpoint, "/api/v1/runs/"+runID+"/start", `{"assessmentDigest":"`+digest+`"}`, "confirmed-launch", `"3"`)
	if confirmed.StatusCode != http.StatusOK || runs.control.ConfirmationDigest != digest {
		t.Fatalf("confirmation status=%d request=%#v", confirmed.StatusCode, runs.control)
	}
	_ = confirmed.Body.Close()
	for _, body := range []string{`{}`, `null`, `{"assessmentDigest":"bad"}`, `{"assessmentDigest":"` + digest + `","confirmed":true}`} {
		invalid := runControlRequest(t, endpoint, "/api/v1/runs/"+runID+"/start", body, "invalid-launch", `"3"`)
		assertAPIError(t, invalid, http.StatusBadRequest, "VALIDATION_FAILED")
		_ = invalid.Body.Close()
	}
	pause := runControlRequest(t, endpoint, "/api/v1/runs/"+runID+"/pause", "", "pause-api", `"3"`)
	if pause.StatusCode != http.StatusOK || pause.Header.Get("ETag") != `"3"` || runs.action != "pause" || runs.control.ExpectedResourceVersion != 3 {
		t.Fatalf("pause status=%d etag=%q action=%q request=%#v", pause.StatusCode, pause.Header.Get("ETag"), runs.action, runs.control)
	}
	_ = pause.Body.Close()

	retry := runControlRequest(t, endpoint, "/api/v1/runs/"+runID+"/retry", `{"nodeId":"technical_design"}`, "retry-api", `"3"`)
	if retry.StatusCode != http.StatusOK || runs.action != "retry" || runs.nodeID != "technical_design" {
		t.Fatalf("retry status=%d action=%q node=%q", retry.StatusCode, runs.action, runs.nodeID)
	}
	_ = retry.Body.Close()

	continued := runControlRequest(t, endpoint, "/api/v1/runs/"+runID+"/continue", `{"until":"delivery"}`, "continue-api", `"3"`)
	if continued.StatusCode != http.StatusOK || runs.action != "continue" || runs.until != "delivery" {
		t.Fatalf("continue status=%d action=%q until=%q", continued.StatusCode, runs.action, runs.until)
	}
	_ = continued.Body.Close()

	missingVersion := runControlRequest(t, endpoint, "/api/v1/runs/"+runID+"/cancel", "", "cancel-api", "")
	assertAPIError(t, missingVersion, http.StatusBadRequest, "VALIDATION_FAILED")
	_ = missingVersion.Body.Close()

	runs.controlErr = &runexecution.InvalidTransitionError{Action: "resume", RunID: runID, Status: statestore.RunCompleted}
	illegal := runControlRequest(t, endpoint, "/api/v1/runs/"+runID+"/resume", "", "resume-api", `"3"`)
	assertAPIError(t, illegal, http.StatusConflict, "RUN_CONTROL_INVALID_TRANSITION")
	_ = illegal.Body.Close()

	runs.controlErr = &runexecution.ControlConflictError{RunID: runID, Expected: 3, Current: 4}
	stale := runControlRequest(t, endpoint, "/api/v1/runs/"+runID+"/pause", "", "pause-stale", `"3"`)
	assertAPIError(t, stale, http.StatusPreconditionFailed, "RUN_VERSION_CONFLICT")
	_ = stale.Body.Close()

	if !errors.Is(runs.controlErr, runexecution.ErrControlConflict) {
		t.Fatal("control conflict lost its portable classification")
	}
}

func TestRunAPIPreparesWorkBackedRunWithOptionalProfile(t *testing.T) {
	server, err := NewServer(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	runs := &recordingRunService{run: apiTestRun()}
	if err := server.SetRuns(runs); err != nil {
		t.Fatal(err)
	}
	if err := server.Start(context.Background(), 1234, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	defer closeTestServer(t, server)
	endpoint, _ := server.Endpoint()

	prepared := workRequest(t, endpoint, http.MethodPost, "/api/v1/runs/prepare", `{"workItemId":"work_00000000000000000000000000","workflowId":"darkstar/story-execution","workflowVersion":"1.4.0","profile":"implementation"}`, "prepare-run-command")
	if prepared.StatusCode != http.StatusCreated || prepared.Header.Get("Location") != "/api/v1/runs/"+runs.run.RunID || prepared.Header.Get("ETag") != `"3"` || runs.prepared != 1 {
		t.Fatalf("prepare status=%d location=%q etag=%q prepared=%d", prepared.StatusCode, prepared.Header.Get("Location"), prepared.Header.Get("ETag"), runs.prepared)
	}
	if runs.createRequest.WorkItemID != "work_00000000000000000000000000" || runs.createRequest.WorkflowID != "darkstar/story-execution" || runs.createRequest.WorkflowVersion != "1.4.0" || runs.createRequest.Profile != "implementation" || runs.commandKey != "prepare-run-command" {
		t.Fatalf("prepare request=%#v key=%q", runs.createRequest, runs.commandKey)
	}
	_ = prepared.Body.Close()

	supplied := workRequest(t, endpoint, http.MethodPost, "/api/v1/runs/prepare", `{"workItemId":"work_00000000000000000000000000","preparation":{"answers":{"goal":"deliver code"},"runInputs":{"request":true}}}`, "supply-preparation")
	if supplied.StatusCode != http.StatusCreated || runs.createRequest.Preparation == nil || runs.createRequest.Preparation.Answers["goal"] != "deliver code" || string(runs.createRequest.Preparation.RunInputs["request"]) != "true" {
		t.Fatalf("supplied status=%d request=%#v", supplied.StatusCode, runs.createRequest)
	}
	_ = supplied.Body.Close()
	query := workRequest(t, endpoint, http.MethodPost, "/api/v1/runs/prepare?launch=true", `{"workItemId":"work_00000000000000000000000000","workflowId":"delivery","workflowVersion":"1.0.0"}`, "prepare-query-command")
	assertAPIError(t, query, http.StatusBadRequest, "VALIDATION_FAILED")
	_ = query.Body.Close()

	unknown := workRequest(t, endpoint, http.MethodPost, "/api/v1/runs/prepare", `{"workItemId":"work_00000000000000000000000000","workflowId":"delivery","workflowVersion":"1.0.0","inputs":{}}`, "prepare-invalid-command")
	assertAPIError(t, unknown, http.StatusBadRequest, "VALIDATION_FAILED")
	_ = unknown.Body.Close()
}

func TestRunAPISelectsExactStartVariantAndListsRuns(t *testing.T) {
	server, err := NewServer(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	runs := &recordingRunService{run: apiTestRun()}
	if err := server.SetRuns(runs); err != nil {
		t.Fatal(err)
	}
	if err := server.Start(context.Background(), 1234, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	defer closeTestServer(t, server)
	endpoint, _ := server.Endpoint()

	created := workRequest(t, endpoint, http.MethodPost, "/api/v1/runs", `{"workItemId":"work_00000000000000000000000000","workflowId":"delivery","workflowVersion":"1.0.0","profile":"implementation"}`, "create-run-command")
	if created.StatusCode != http.StatusCreated || runs.created != 1 || created.Header.Get("ETag") != `"3"` || runs.createRequest.Profile != "implementation" {
		t.Fatalf("create status=%d created=%d etag=%q", created.StatusCode, runs.created, created.Header.Get("ETag"))
	}
	var createdRun statestore.RunProjection
	decodeJSON(t, created, &createdRun)
	_ = created.Body.Close()

	fake := workRequest(t, endpoint, http.MethodPost, "/api/v1/runs", `{"scenario":"fake-success"}`, "fake-run-command")
	if fake.StatusCode != http.StatusAccepted || runs.started != 1 {
		t.Fatalf("fake status=%d started=%d", fake.StatusCode, runs.started)
	}
	var fakeView runexecution.View
	decodeJSON(t, fake, &fakeView)
	_ = fake.Body.Close()

	mixed := workRequest(t, endpoint, http.MethodPost, "/api/v1/runs", `{"scenario":"fake-success","workItemId":"work_00000000000000000000000000","workflowId":"delivery","workflowVersion":"1.0.0"}`, "mixed-run-command")
	assertAPIError(t, mixed, http.StatusBadRequest, "VALIDATION_FAILED")
	_ = mixed.Body.Close()

	listed := workRequest(t, endpoint, http.MethodGet, "/api/v1/runs?limit=1", "", "")
	var page runexecution.Page
	decodeJSON(t, listed, &page)
	_ = listed.Body.Close()
	if len(page.Items) != 1 || page.Items[0].RunID != runs.run.RunID {
		t.Fatalf("page = %#v", page)
	}
}

type recordingRunService struct {
	run                        statestore.RunProjection
	created, prepared, started int
	action                     string
	control                    runexecution.ControlRequest
	createRequest              runexecution.CreateRequest
	commandKey                 string
	nodeID, until              string
	controlErr                 error
}

func (s *recordingRunService) Create(_ context.Context, request runexecution.CreateRequest, key string) (statestore.RunProjection, error) {
	s.created++
	s.createRequest, s.commandKey = request, key
	return s.run, nil
}

func (s *recordingRunService) Prepare(_ context.Context, request runexecution.CreateRequest, key string) (statestore.RunProjection, error) {
	s.prepared++
	s.createRequest, s.commandKey = request, key
	return s.run, nil
}

func (s *recordingRunService) Start(context.Context, runexecution.StartRequest, string) (runexecution.View, error) {
	s.started++
	return runexecution.View{SchemaVersion: 1, Run: s.run, Attempts: []statestore.AttemptProjection{}}, nil
}

func (s *recordingRunService) List(context.Context, int, string) (runexecution.Page, error) {
	return runexecution.Page{Items: []statestore.RunProjection{s.run}, PageInfo: runexecution.PageInfo{}}, nil
}

func (s *recordingRunService) Get(context.Context, string) (runexecution.View, error) {
	return runexecution.View{SchemaVersion: 1, Run: s.run, Attempts: []statestore.AttemptProjection{}}, nil
}

func (s *recordingRunService) Pause(_ context.Context, request runexecution.ControlRequest) (statestore.RunProjection, error) {
	s.action, s.control = "pause", request
	return s.run, s.controlErr
}

func (s *recordingRunService) Launch(_ context.Context, request runexecution.ControlRequest) (statestore.RunProjection, error) {
	s.action, s.control = "start", request
	return s.run, s.controlErr
}

func (s *recordingRunService) Resume(_ context.Context, request runexecution.ControlRequest) (statestore.RunProjection, error) {
	s.action, s.control = "resume", request
	return s.run, s.controlErr
}

func (s *recordingRunService) Retry(_ context.Context, request runexecution.RetryRequest) (statestore.RunProjection, error) {
	s.action, s.control, s.nodeID = "retry", request.ControlRequest, request.NodeID
	return s.run, s.controlErr
}

func (s *recordingRunService) Continue(_ context.Context, request runexecution.ContinueRequest) (statestore.RunProjection, error) {
	s.action, s.control, s.until = "continue", request.ControlRequest, request.UntilNodeID
	return s.run, s.controlErr
}

func (s *recordingRunService) Cancel(_ context.Context, request runexecution.ControlRequest) (statestore.RunProjection, error) {
	s.action, s.control = "cancel", request
	return s.run, s.controlErr
}

func runControlRequest(t *testing.T, endpoint Endpoint, resource, body, key, ifMatch string) *http.Response {
	t.Helper()
	request, err := http.NewRequest(http.MethodPost, endpoint.BaseURL()+resource, bytes.NewBufferString(body))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", endpoint.AuthorizationHeader())
	request.Close = true
	if body != "" {
		request.Header.Set("Content-Type", "application/json")
	}
	request.Header.Set("Idempotency-Key", key)
	if ifMatch != "" {
		request.Header.Set("If-Match", ifMatch)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	return response
}

func apiTestRun() statestore.RunProjection {
	now := time.Now().UTC()
	return statestore.RunProjection{RunID: "run_00000000000000000000000000", WorkItemID: "work_00000000000000000000000000", WorkflowID: "delivery", WorkflowVersion: "1.0.0", Status: statestore.RunQueued, ResourceVersion: 3, LastGlobalPosition: 3, CreatedAt: now, UpdatedAt: now}
}
