package cli

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"darkstar/src/core/runexecution"
	"darkstar/src/core/workmanagement"
	platformport "darkstar/src/ports/platform"
	"darkstar/src/ports/statestore"
)

func TestProjectAndWorkCLICommandsUseStableMachineResults(t *testing.T) {
	root := t.TempDir()
	paths := platformport.Paths{Config: filepath.Join(root, "config"), Data: filepath.Join(root, "data"), Cache: filepath.Join(root, "cache"), Logs: filepath.Join(root, "logs"), Runtime: filepath.Join(root, "runtime")}
	for _, directory := range []string{paths.Config, paths.Data, paths.Cache, paths.Logs, paths.Runtime} {
		if err := os.MkdirAll(directory, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	originalResolver := resolveApplicationPaths
	resolveApplicationPaths = func(context.Context) (platformport.Paths, error) { return paths, nil }
	t.Cleanup(func() { resolveApplicationPaths = originalResolver })
	service := startAcceptanceService(t, paths, "44444444444444444444444444444444")
	t.Cleanup(func() { _ = service.Close() })

	var project statestore.ProjectProjection
	runCLIJSON(t, []string{"project", "add", root, "--name", "acceptance", "--idempotency-key", "project-cli-command", "--json"}, &struct {
		SchemaVersion int                           `json:"schemaVersion"`
		Result        *statestore.ProjectProjection `json:"result"`
	}{Result: &project})
	if project.Name != "acceptance" || !projectIdentityPattern.MatchString(project.ProjectID) {
		t.Fatalf("project = %#v", project)
	}

	var created statestore.WorkItemProjection
	runCLIJSON(t, []string{"work", "create", "Implement CLI commands", "--priority", "80", "--idempotency-key", "work-cli-command", "--json"}, &struct {
		SchemaVersion int                            `json:"schemaVersion"`
		Result        *statestore.WorkItemProjection `json:"result"`
	}{Result: &created})
	if created.ProjectID != project.ProjectID || created.Priority != 80 {
		t.Fatalf("created = %#v", created)
	}

	var imported statestore.WorkItemProjection
	runCLIJSON(t, []string{"work", "import", "DAR-65", "--project", project.ProjectID, "--idempotency-key", "import-cli-command", "--json"}, &struct {
		SchemaVersion int                            `json:"schemaVersion"`
		Result        *statestore.WorkItemProjection `json:"result"`
	}{Result: &imported})
	if imported.Title != "DAR-65" || imported.WorkItemID == created.WorkItemID {
		t.Fatalf("imported = %#v", imported)
	}

	var shown workmanagement.WorkView
	runCLIJSON(t, []string{"work", "show", created.WorkItemID, "--json"}, &struct {
		SchemaVersion int                      `json:"schemaVersion"`
		Result        *workmanagement.WorkView `json:"result"`
	}{Result: &shown})
	if shown.Work.WorkItemID != created.WorkItemID || shown.Runs == nil || shown.Stories == nil {
		t.Fatalf("shown = %#v", shown)
	}

	workflowPath := filepath.Join(root, "workflow.json")
	if err := os.WriteFile(workflowPath, []byte(cliWorkflowDocument()), 0o600); err != nil {
		t.Fatal(err)
	}
	runCLIJSON(t, []string{"workflow", "install", workflowPath, "--json"}, &struct {
		SchemaVersion int `json:"schemaVersion"`
		Result        any `json:"result"`
	}{})
	var started statestore.RunProjection
	runCLIJSON(t, []string{"run", "start", created.WorkItemID, "--workflow", "cli-workflow", "--version", "1.0.0", "--idempotency-key", "run-cli-command", "--json"}, &struct {
		SchemaVersion int                       `json:"schemaVersion"`
		Result        *statestore.RunProjection `json:"result"`
	}{Result: &started})
	if started.WorkItemID != created.WorkItemID || started.Status != statestore.RunQueued || started.RouteDigest == "" {
		t.Fatalf("started = %#v", started)
	}
	var replayed statestore.RunProjection
	runCLIJSON(t, []string{"run", "start", created.WorkItemID, "--workflow", "cli-workflow", "--version", "1.0.0", "--idempotency-key", "run-cli-command", "--json"}, &struct {
		SchemaVersion int                       `json:"schemaVersion"`
		Result        *statestore.RunProjection `json:"result"`
	}{Result: &replayed})
	if replayed.RunID != started.RunID || replayed.ResourceVersion != started.ResourceVersion {
		t.Fatalf("replayed = %#v, started = %#v", replayed, started)
	}
	var page runexecution.Page
	runCLIJSON(t, []string{"run", "list", "--limit", "1", "--json"}, &struct {
		SchemaVersion int                `json:"schemaVersion"`
		Result        *runexecution.Page `json:"result"`
	}{Result: &page})
	if len(page.Items) != 1 || page.Items[0].RunID != started.RunID {
		t.Fatalf("run page = %#v", page)
	}
	var runView runexecution.View
	runCLIJSON(t, []string{"run", "show", started.RunID, "--json"}, &runView)
	if runView.Run.RunID != started.RunID || len(runView.Attempts) != 0 {
		t.Fatalf("run view = %#v", runView)
	}
}
