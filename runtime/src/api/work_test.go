package api

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"darkstar/src/adapters/statestore/sqlite"
	"darkstar/src/core/identity"
	"darkstar/src/core/workmanagement"
	"darkstar/src/ports/statestore"
)

func TestProjectAndWorkAPICommands(t *testing.T) {
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
	storyLater, storyFirst := identity.Random("story_"), identity.Random("story_")
	pointLater, pointFirst, pointSecond := identity.Random("point_"), identity.Random("point_"), identity.Random("point_")
	const sourceHash = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	_, err = database.Append(ctx,
		workProjectionEvent(statestore.AggregateStory, storyLater, "story.created", fmt.Sprintf(`{"workItemId":%q,"title":"Later story","sourceHash":%q,"priority":90,"position":2}`, created.WorkItemID, sourceHash)),
		workProjectionEvent(statestore.AggregateStory, storyFirst, "story.created", fmt.Sprintf(`{"workItemId":%q,"title":"First story","sourceHash":%q,"priority":10,"position":1}`, created.WorkItemID, sourceHash)),
		workProjectionEvent(statestore.AggregatePoint, pointLater, "point.created", fmt.Sprintf(`{"storyId":%q,"revision":1,"title":"Later point","sourceHash":%q,"priority":90,"position":1,"dependencies":[]}`, storyLater, sourceHash)),
		workProjectionEvent(statestore.AggregatePoint, pointSecond, "point.created", fmt.Sprintf(`{"storyId":%q,"revision":1,"title":"Second point","sourceHash":%q,"priority":90,"position":2,"dependencies":[]}`, storyFirst, sourceHash)),
		workProjectionEvent(statestore.AggregatePoint, pointFirst, "point.created", fmt.Sprintf(`{"storyId":%q,"revision":1,"title":"First point","sourceHash":%q,"priority":10,"position":1,"dependencies":[]}`, storyFirst, sourceHash)),
	)
	if err != nil {
		t.Fatalf("append work hierarchy: %v", err)
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
	if len(view.Stories) != 2 || view.Stories[0].StoryID != storyFirst || view.Stories[1].StoryID != storyLater {
		t.Fatalf("work stories = %#v", view.Stories)
	}
	if len(view.Points) != 3 || view.Points[0].PointID != pointFirst || view.Points[1].PointID != pointSecond || view.Points[2].PointID != pointLater {
		t.Fatalf("work points = %#v", view.Points)
	}
}

func TestWorkAPIRejectsMixedOrUnidentifiedInput(t *testing.T) {
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

func workProjectionEvent(aggregateType statestore.AggregateType, aggregateID, kind, data string) statestore.PendingEvent {
	return statestore.PendingEvent{
		SchemaVersion: 1, ID: identity.Random("event_"), AggregateType: aggregateType, AggregateID: aggregateID,
		Kind: kind, OccurredAt: time.Now().UTC(), CorrelationID: aggregateID, CommandID: identity.Random("command_"),
		Actor: statestore.Actor{Type: statestore.ActorSystem, ID: "work-api-test"}, Data: []byte(data), Metadata: []byte(`{}`),
	}
}
