package cli

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"darkstar/src/api"
	"darkstar/src/ports/platform"
	"darkstar/src/ports/statestore"
)

func TestBacklogCLIRefreshOnlyLoadsObservations(t *testing.T) {
	root := t.TempDir()
	paths := platform.Paths{Config: filepath.Join(root, "config"), Data: filepath.Join(root, "data"), Cache: filepath.Join(root, "cache"), Logs: filepath.Join(root, "logs"), Runtime: filepath.Join(root, "runtime")}
	for _, directory := range []string{paths.Config, paths.Data, paths.Cache, paths.Logs, paths.Runtime} {
		if err := os.MkdirAll(directory, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	original := resolveApplicationPaths
	resolveApplicationPaths = func(context.Context) (platform.Paths, error) {
		return paths, nil
	}
	t.Cleanup(func() {
		resolveApplicationPaths = original
	})
	service := startAcceptanceService(t, paths, "66666666666666666666666666666666")
	t.Cleanup(func() {
		_ = service.Close()
	})
	var project statestore.ProjectProjection
	runCLIJSON(t, []string{"project", "add", root, "--name", "Backlog", "--idempotency-key", "backlog-cli-project", "--json"}, &struct {
		Result *statestore.ProjectProjection `json:"result"`
	}{Result: &project})
	var work statestore.WorkItemProjection
	runCLIJSON(t, []string{"work", "create", "Backlog title", "--project", project.ProjectID, "--idempotency-key", "backlog-cli-create", "--json"}, &struct {
		Result *statestore.WorkItemProjection `json:"result"`
	}{Result: &work})
	var source api.BacklogSourceResponse
	runCLIJSON(t, []string{"backlog", "source", project.ProjectID, "--json"}, &struct {
		Result *api.BacklogSourceResponse `json:"result"`
	}{Result: &source})
	if source.Binding.Revision != 1 || source.Binding.Source.Kind != "built_in" {
		t.Fatalf("default source = %#v", source)
	}
	var view api.BacklogView
	result := &struct {
		Result *api.BacklogView `json:"result"`
	}{Result: &view}
	runCLIJSON(t, []string{"backlog", "list", project.ProjectID, "--json"}, result)
	if len(view.Tickets) != 0 {
		t.Fatal("cache listing loaded source implicitly")
	}
	var refresh api.BacklogRefreshResponse
	runCLIJSON(t, []string{"backlog", "refresh", project.ProjectID, "--revision", "1", "--search", "Backlog", "--page-size", "10", "--json"}, &struct {
		Result *api.BacklogRefreshResponse `json:"result"`
	}{Result: &refresh})
	runCLIJSON(t, []string{"backlog", "list", project.ProjectID, "--json"}, result)
	if len(view.Tickets) != 1 || view.Tickets[0].Ref.ID != work.WorkItemID || refresh.Refresh.Phase != statestore.BacklogComplete {
		t.Fatalf("cache = %#v, refresh = %#v", view, refresh)
	}
	items, err := service.database.WorkItemsForProject(context.Background(), project.ProjectID)
	if err != nil || len(items) != 1 || items[0].ResourceVersion != work.ResourceVersion {
		t.Fatalf("source reads changed work: %#v, %v", items, err)
	}
	runs, err := service.database.Runs(context.Background())
	if err != nil || len(runs) != 0 {
		t.Fatalf("source reads started execution: %#v, %v", runs, err)
	}
}

func TestBacklogPollingStopsWithDaemonCancellation(t *testing.T) {
	service := &daemonAPIService{}
	ctx, cancel := context.WithCancel(context.Background())
	service.startBacklogPolling(ctx)
	cancel()
	select {
	case <-service.backlogDone:
	case <-time.After(time.Second):
		t.Fatal("polling ignored daemon cancellation")
	}
	service.stopBacklogPolling()
	service.stopBacklogPolling()
}
