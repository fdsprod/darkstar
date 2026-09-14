package cli

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"darkstar/src/adapters/statestore/sqlite"
	localapi "darkstar/src/api"
	"darkstar/src/core/workmanagement"
	platformport "darkstar/src/ports/platform"
	"darkstar/src/ports/statestore"
)

func TestProjectRepositoryCLIUsesVersionedDaemonRepresentation(t *testing.T) {
	root := t.TempDir()
	paths := platformport.Paths{Config: filepath.Join(root, "config"), Data: filepath.Join(root, "data"), Cache: filepath.Join(root, "cache"), Logs: filepath.Join(root, "logs"), Runtime: filepath.Join(root, "runtime")}
	for _, directory := range []string{paths.Config, paths.Data, paths.Cache, paths.Logs, paths.Runtime} {
		if err := os.MkdirAll(directory, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	originalResolver := resolveApplicationPaths
	resolveApplicationPaths = func(context.Context) (platformport.Paths, error) {
		return paths, nil
	}
	t.Cleanup(func() {
		resolveApplicationPaths = originalResolver
	})
	database, err := sqlite.Open(context.Background(), filepath.Join(paths.Data, "test.db"), sqlite.Options{})
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
	server, err := localapi.NewServer(paths.Runtime)
	if err != nil {
		t.Fatal(err)
	}
	if err := server.SetWork(work); err != nil {
		t.Fatal(err)
	}
	if err := server.Start(context.Background(), 1234, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = server.Close()
	})
	var created workmanagement.ProjectRepositoriesView
	runCLIJSON(t, []string{"project", "create", "Planning product", "--idempotency-key", "planning-product", "--json"}, &struct {
		Result *workmanagement.ProjectRepositoriesView `json:"result"`
	}{Result: &created})
	if created.SchemaVersion != 2 || len(created.Repositories) != 0 || created.Project.Name != "Planning product" {
		t.Fatalf("created = %#v", created)
	}
	var listed []workmanagement.ProjectRepositoriesView
	runCLIJSON(t, []string{"project", "list-v2", "--json"}, &struct {
		Result *[]workmanagement.ProjectRepositoriesView `json:"result"`
	}{Result: &listed})
	if len(listed) != 1 || listed[0].Project.ProjectID != created.Project.ProjectID {
		t.Fatalf("listed = %#v", listed)
	}
	var shown workmanagement.ProjectRepositoriesView
	runCLIJSON(t, []string{"project", "repository", "list", created.Project.ProjectID, "--json"}, &struct {
		Result *workmanagement.ProjectRepositoriesView `json:"result"`
	}{Result: &shown})
	if shown.Project.ProjectID != created.Project.ProjectID {
		t.Fatalf("shown = %#v", shown)
	}
	var item statestore.WorkItemProjection
	runCLIJSON(t, []string{"work", "create", "Plan the new product", "--idempotency-key", "plan-product", "--json"}, &struct {
		Result *statestore.WorkItemProjection `json:"result"`
	}{Result: &item})
	if item.ProjectID != created.Project.ProjectID {
		t.Fatal("zero-repository project was not selected for planning work")
	}
}

func TestProjectRepositoryCLIRejectsAmbiguousAndStaleInputBeforeConnecting(t *testing.T) {
	projectID := "project_01ARZ3NDEKTSV4RRFFQ69G5FAV"
	for _, args := range [][]string{
		{"repository", "add", projectID, ".", "--revision", "1", "--label", "Code"},
		{"repository", "add", projectID, ".", "--revision", "1", "--role", "implementation"},
		{"repository", "remove", projectID, "repository-id", "--revision", "1"},
		{"repository", "remove", projectID, "repository-id", "--revision", "0", "--membership-revision", "1"},
		{"create", "Product", "--role", "implementation"},
		{"list-v2", "--revision", "1"},
	} {
		if _, _, _, _, err := projectRepositoryCommand(args); err == nil {
			t.Fatalf("accepted invalid arguments: %v", args)
		}
	}
	method, resource, body, options, err := projectRepositoryCommand([]string{"repository", "remove", projectID, "repository-id", "--revision", "5", "--membership-revision", "3", "--idempotency-key", "remove-test"})
	if err != nil {
		t.Fatal(err)
	}
	if method != http.MethodDelete || resource != "projects-v2/"+projectID+"/repositories/repository-id" || body.(localapi.RepositoryMembershipRemovalInput).ExpectedMembershipRevision != 3 {
		t.Fatalf("unexpected command: %s %s %#v", method, resource, body)
	}
	request, _ := http.NewRequest(method, "http://localhost", nil)
	for _, option := range options {
		option(request)
	}
	if request.Header.Get("If-Match") != `"5"` || request.Header.Get("Idempotency-Key") != "remove-test" {
		t.Fatalf("mutation headers = %#v", request.Header)
	}
}

func createPlanningCLIProject(t *testing.T, name, key string) statestore.ProjectProjection {
	t.Helper()
	var view workmanagement.ProjectRepositoriesView
	runCLIJSON(t, []string{"project", "create", name, "--idempotency-key", key, "--json"}, &struct {
		Result *workmanagement.ProjectRepositoriesView `json:"result"`
	}{Result: &view})
	if len(view.Repositories) != 0 {
		t.Fatal("planning fixture unexpectedly acquired a repository")
	}
	return view.Project
}
