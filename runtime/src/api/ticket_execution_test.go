package api

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"darkstar/src/adapters/statestore/sqlite"
	"darkstar/src/core/backlog"
	"darkstar/src/core/ticketexecution"
	"darkstar/src/core/workmanagement"
	"darkstar/src/ports/statestore"
	"darkstar/src/ports/tracker"
)

func TestTicketExecutionAPIRequiresExactApprovalAndReplaysWithoutBusinessWrites(t *testing.T) {
	ctx := context.Background()
	database, err := sqlite.Open(ctx, filepath.Join(t.TempDir(), "admission.db"), sqlite.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = database.Close()
	})
	work, _ := workmanagement.New(database)
	project, err := work.RegisterProject(ctx, workmanagement.ProjectRegistration{Name: "Admission", Source: t.TempDir()}, "admission-project")
	if err != nil {
		t.Fatal(err)
	}
	created, err := work.CreateWork(ctx, workmanagement.CreateWorkRequest{ProjectID: project.ProjectID, Title: "Approved source"}, "admission-work")
	if err != nil {
		t.Fatal(err)
	}
	resolver := &apiBacklogResolver{store: database}
	cache, err := backlog.New(database, resolver, backlog.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := cache.Refresh(ctx, project.ProjectID, 1, tracker.Query{PageSize: 10}); err != nil {
		t.Fatal(err)
	}
	page, err := cache.View(ctx, project.ProjectID, backlog.ViewRequest{})
	if err != nil || len(page.Tickets) != 1 {
		t.Fatalf("cache = %#v, %v", page, err)
	}
	engine, err := ticketexecution.New(database, resolver, ticketexecution.Options{})
	if err != nil {
		t.Fatal(err)
	}
	server, _ := NewServer(t.TempDir())
	if err := server.SetTicketExecution(engine); err != nil {
		t.Fatal(err)
	}
	if err := server.Start(ctx, 1234, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	defer closeTestServer(t, server)
	endpoint, _ := server.Endpoint()
	base := "/api/v1/projects/" + project.ProjectID + "/backlog/admit"
	input := TicketAdmissionRequest{SchemaVersion: 1, ExpectedBindingRevision: 1, ObservationID: page.Tickets[0].ObservationID}
	encoded, _ := json.Marshal(input)
	call := func(body, key string, status int) TicketAdmissionResponse {
		t.Helper()
		response := workRequest(t, endpoint, http.MethodPost, base, body, key)
		defer func() {
			_ = response.Body.Close()
		}()
		assertNativeStatus(t, response, status)
		var result TicketAdmissionResponse
		if status == http.StatusOK {
			decodeJSON(t, response, &result)
		} else {
			_, _ = io.Copy(io.Discard, response.Body)
		}
		return result
	}
	call(string(encoded), "", http.StatusBadRequest)
	before := resolver.resolutions
	first := call(string(encoded), "approve-source-version", http.StatusOK)
	replay := call(string(encoded), "approve-source-version", http.StatusOK)
	if first.WorkItemID != created.WorkItemID || replay.Source.Approval.ID != first.Source.Approval.ID || first.SourceObservationID != input.ObservationID || first.Source.ApprovedTicket == nil || first.Source.ApprovedTicket.ObservationID != input.ObservationID {
		t.Fatalf("admission replay = %#v / %#v", first, replay)
	}
	if resolver.resolutions != before {
		t.Fatal("approval silently refreshed provider state")
	}
	input.ExpectedBindingRevision = 2
	stale, _ := json.Marshal(input)
	call(string(stale), "approve-stale-version", http.StatusConflict)
	call(string(stale), "approve-source-version", http.StatusConflict)
	items, err := database.WorkItemsForProject(ctx, project.ProjectID)
	if err != nil || len(items) != 1 {
		t.Fatalf("admission duplicated native work: %#v, %v", items, err)
	}
	history, err := database.NativeTicketHistory(ctx, project.ProjectID, created.WorkItemID)
	if err != nil || len(history) != 1 {
		t.Fatalf("admission mutated business data: %#v, %v", history, err)
	}
	runs, err := database.Runs(ctx)
	if err != nil || len(runs) != 0 {
		t.Fatalf("admission started unprepared execution: %#v, %v", runs, err)
	}
}

func TestWorkSourceProjectionSeparatesLiveActivityOutcomeAndAcceptance(t *testing.T) {
	now := time.Now().UTC()
	view := sourceView(ticketexecution.WorkSourceView{Work: statestore.WorkItemProjection{Status: statestore.WorkItemCompleted}, Runs: []statestore.RunProjection{
		{RunID: "old", Status: statestore.RunCompleted, CreatedAt: now.Add(-time.Hour), UpdatedAt: now.Add(-time.Minute)},
		{RunID: "new", Status: statestore.RunRunning, CreatedAt: now, UpdatedAt: now},
	}, LocalActivity: "running", LastRunOutcome: "completed", ExternalAcceptance: tracker.Unknown[string]{Reason: "No external acceptance observation."}})
	if view.LocalActivity != "running" || view.RunOutcome != "completed" || view.ExternalAcceptance.State != "unknown" {
		t.Fatalf("independent source view = %#v", view)
	}
}
