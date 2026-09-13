package api

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"darkstar/src/adapters/statestore/sqlite"
	"darkstar/src/adapters/tracker/builtin"
	"darkstar/src/core/backlog"
	"darkstar/src/core/workmanagement"
	"darkstar/src/ports"
	"darkstar/src/ports/statestore"
)

type apiBacklogResolver struct {
	store       statestore.NativeTrackerStore
	resolutions int
}

func (resolver *apiBacklogResolver) Resolve(ctx context.Context, binding statestore.BacklogBinding) (backlog.ResolvedSource, error) {
	resolver.resolutions++
	if _, native := binding.Source.(statestore.NativeBacklogSource); !native {
		return backlog.ResolvedSource{}, &ports.Failure{Code: ports.FailureUnavailable, Message: "Connection is unavailable.", Retryable: true}
	}
	adapter, err := builtin.NewForBinding(resolver.store, binding.ProjectID, strconv.FormatUint(binding.Revision, 10))
	if err != nil {
		return backlog.ResolvedSource{}, err
	}
	return backlog.ResolvedSource{Source: adapter, Browser: adapter, Config: adapter.ConfigPin()}, nil
}

func TestBacklogAPIRefreshRetainsHistoryWithoutExecution(t *testing.T) {
	ctx := context.Background()
	database, err := sqlite.Open(ctx, filepath.Join(t.TempDir(), "backlog.db"), sqlite.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = database.Close()
	})
	work, _ := workmanagement.New(database)
	project, err := work.RegisterProject(ctx, workmanagement.ProjectRegistration{Name: "Backlog", Source: t.TempDir()}, "backlog-project")
	if err != nil {
		t.Fatal(err)
	}
	created, err := work.CreateWork(ctx, workmanagement.CreateWorkRequest{ProjectID: project.ProjectID, Title: "Cached ticket"}, "backlog-ticket")
	if err != nil {
		t.Fatal(err)
	}
	resolver := &apiBacklogResolver{store: database}
	engine, err := backlog.New(database, resolver, backlog.Options{})
	if err != nil {
		t.Fatal(err)
	}
	server, _ := NewServer(t.TempDir())
	if err := server.SetBacklog(engine); err != nil {
		t.Fatal(err)
	}
	if err := server.Start(ctx, 1234, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	defer closeTestServer(t, server)
	endpoint, _ := server.Endpoint()
	base := "/api/v1/projects/" + project.ProjectID + "/backlog"
	request := func(method, path, body string, result any, status int) {
		t.Helper()
		response := workRequest(t, endpoint, method, base+path, body, "")
		defer func() {
			_ = response.Body.Close()
		}()
		assertNativeStatus(t, response, status)
		if result != nil {
			decodeJSON(t, response, result)
		} else {
			_, _ = io.Copy(io.Discard, response.Body)
		}
	}
	var view BacklogView
	request(http.MethodGet, "", "", &view, http.StatusOK)
	if len(view.Tickets) != 0 || resolver.resolutions != 0 {
		t.Fatalf("cached read contacted source: %#v, %d", view, resolver.resolutions)
	}
	request(http.MethodPost, "/refresh", `{"schemaVersion":1,"expectedBindingRevision":1,"query":{"text":"Cached","pageSize":10,"predicates":[]}}`, nil, http.StatusOK)
	request(http.MethodGet, "", "", &view, http.StatusOK)
	if len(view.Tickets) != 1 || view.Tickets[0].Ref.ID != created.WorkItemID || !view.Tickets[0].CurrentSource || !view.Tickets[0].CurrentQueryMatch || view.Refresh == nil || view.Refresh.Phase != statestore.BacklogComplete {
		t.Fatalf("refreshed view = %#v", view)
	}
	ref, _ := json.Marshal(BacklogTicketRefreshRequest{SchemaVersion: 1, ExpectedBindingRevision: 1, Ref: view.Tickets[0].Ref})
	request(http.MethodPost, "/refresh-ticket", string(ref), nil, http.StatusOK)
	var selected BacklogSourceResponse
	external := `{"schemaVersion":1,"expectedRevision":1,"source":{"kind":"external","connectionId":"missing","connectionRevision":"1","scope":{"namespace":{"provider":"linear","host":"api.linear.app","tenantId":"workspace","scopeId":"workspace"},"containerId":"team"}}}`
	request(http.MethodPut, "/source", external, &selected, http.StatusOK)
	if selected.Binding.Revision != 2 || len(selected.History) != 2 {
		t.Fatalf("selection = %#v", selected)
	}
	request(http.MethodPut, "/source", external, nil, http.StatusConflict)
	request(http.MethodPost, "/refresh-ticket", string(ref), nil, http.StatusConflict)
	request(http.MethodPost, "/refresh", `{"schemaVersion":1,"expectedBindingRevision":2,"query":{"pageSize":10}}`, nil, http.StatusServiceUnavailable)
	request(http.MethodGet, "?includePrevious=true", "", &view, http.StatusOK)
	if len(view.Tickets) != 1 || view.Tickets[0].CurrentSource || view.Refresh == nil || view.Refresh.Error == nil || view.Binding.Source.Kind != "external" {
		t.Fatalf("failure lost retained source or hid error: %#v", view)
	}
	items, err := database.WorkItemsForProject(ctx, project.ProjectID)
	if err != nil || len(items) != 1 || items[0].ResourceVersion != created.ResourceVersion {
		t.Fatalf("refresh changed work: %#v, %v", items, err)
	}
	runs, err := database.Runs(ctx)
	if err != nil || len(runs) != 0 {
		t.Fatalf("refresh started runs: %#v, %v", runs, err)
	}
}
