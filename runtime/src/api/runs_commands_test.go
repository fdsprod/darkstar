package api

import (
	"context"
	"net/http"
	"testing"
	"time"

	"darkstar/src/core/runexecution"
	"darkstar/src/ports/statestore"
)

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

	created := workRequest(t, endpoint, http.MethodPost, "/api/v1/runs", `{"workItemId":"work_00000000000000000000000000","workflowId":"delivery","workflowVersion":"1.0.0"}`, "create-run-command")
	if created.StatusCode != http.StatusCreated || runs.created != 1 || created.Header.Get("ETag") != `"3"` {
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
	run              statestore.RunProjection
	created, started int
}

func (s *recordingRunService) Create(context.Context, runexecution.CreateRequest, string) (statestore.RunProjection, error) {
	s.created++
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

func apiTestRun() statestore.RunProjection {
	now := time.Now().UTC()
	return statestore.RunProjection{RunID: "run_00000000000000000000000000", WorkItemID: "work_00000000000000000000000000", WorkflowID: "delivery", WorkflowVersion: "1.0.0", Status: statestore.RunQueued, ResourceVersion: 3, LastGlobalPosition: 3, CreatedAt: now, UpdatedAt: now}
}
