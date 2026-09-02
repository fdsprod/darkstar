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
	"darkstar/src/core/workmanagement"
	"darkstar/src/ports/statestore"
)

func TestProjectAndWorkAPICommands(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	database, err := sqlite.Open(ctx, filepath.Join(t.TempDir(), "work-api.db"), sqlite.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	service, _ := workmanagement.New(database)
	server, err := NewServer(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := server.SetWork(service); err != nil {
		t.Fatal(err)
	}
	if err := server.Start(ctx, 1234, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	defer closeTestServer(t, server)
	endpoint, _ := server.Endpoint()

	projectResponse := workRequest(t, endpoint, http.MethodPost, "/api/v1/projects", `{"name":"DARKSTAR","source":"C:\\src\\darkstar"}`, "project-command")
	if projectResponse.StatusCode != http.StatusCreated {
		t.Fatalf("project status = %d", projectResponse.StatusCode)
	}
	var project statestore.ProjectProjection
	decodeJSON(t, projectResponse, &project)
	_ = projectResponse.Body.Close()
	if !projectIDPattern.MatchString(project.ProjectID) || projectResponse.Header.Get("Location") != "/api/v1/projects/"+project.ProjectID {
		t.Fatalf("project = %#v, location = %q", project, projectResponse.Header.Get("Location"))
	}

	createdResponse := workRequest(t, endpoint, http.MethodPost, "/api/v1/work-items", `{"projectId":"`+project.ProjectID+`","title":"Implement commands","priority":80}`, "work-create-command")
	var created statestore.WorkItemProjection
	decodeJSON(t, createdResponse, &created)
	_ = createdResponse.Body.Close()
	importResponse := workRequest(t, endpoint, http.MethodPost, "/api/v1/work-items/import", `{"projectId":"`+project.ProjectID+`","sourceReference":"DAR-65","priority":90}`, "work-import-command")
	var imported statestore.WorkItemProjection
	decodeJSON(t, importResponse, &imported)
	_ = importResponse.Body.Close()
	if created.WorkItemID == imported.WorkItemID || imported.Title != "DAR-65" {
		t.Fatalf("created = %#v, imported = %#v", created, imported)
	}

	listResponse := workRequest(t, endpoint, http.MethodGet, "/api/v1/work-items?projectId="+project.ProjectID, "", "")
	var workItems []statestore.WorkItemProjection
	decodeJSON(t, listResponse, &workItems)
	_ = listResponse.Body.Close()
	if len(workItems) != 2 || workItems[0].WorkItemID != imported.WorkItemID {
		t.Fatalf("work list = %#v", workItems)
	}
	showResponse := workRequest(t, endpoint, http.MethodGet, "/api/v1/work-items/"+created.WorkItemID, "", "")
	var view workmanagement.WorkView
	decodeJSON(t, showResponse, &view)
	_ = showResponse.Body.Close()
	if view.SchemaVersion != 1 || view.Work.WorkItemID != created.WorkItemID {
		t.Fatalf("work view = %#v", view)
	}
}

func TestWorkAPIRejectsMixedOrUnidentifiedInput(t *testing.T) {
	t.Parallel()
	server, endpoint := startTestServer(t)
	defer closeTestServer(t, server)

	missingService := workRequest(t, endpoint, http.MethodGet, "/api/v1/projects", "", "")
	assertAPIError(t, missingService, http.StatusServiceUnavailable, "WORK_SERVICE_UNAVAILABLE")
	_ = missingService.Body.Close()
}

func workRequest(t *testing.T, endpoint Endpoint, method, resource, body, key string) *http.Response {
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
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	return response
}

func encodeWorkBody(t *testing.T, value any) string {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(encoded)
}
