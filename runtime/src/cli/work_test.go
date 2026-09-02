package cli

import (
	"context"
	"os"
	"path/filepath"
	"testing"

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
}
