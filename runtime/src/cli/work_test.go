package cli

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"darkstar/src/core/runexecution"
	"darkstar/src/core/worklifecycle"
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
	runCLIJSON(t, []string{"work", "create", "Implement CLI commands", "--details", "Keep the final node", "--evidence", "DAR-144", "--routing", "override", "--workflow", "cli-workflow", "--entry-node", "finish", "--terminal-node", "finish", "--priority", "80", "--idempotency-key", "work-cli-command", "--json"}, &struct {
		SchemaVersion int                            `json:"schemaVersion"`
		Result        *statestore.WorkItemProjection `json:"result"`
	}{Result: &created})
	if created.ProjectID != project.ProjectID || created.Priority != 80 || created.Details != "Keep the final node" || len(created.Evidence) != 1 || created.RoutingIntent.Mode != statestore.WorkRoutingOverride {
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
	for deadline := time.Now().Add(5 * time.Second); ; {
		runCLIJSON(t, []string{"run", "show", started.RunID, "--json"}, &runView)
		if len(runView.Attempts) > 0 || time.Now().After(deadline) {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if runView.Run.RunID != started.RunID || len(runView.Nodes) != 1 || runView.Nodes[0].NodeID != "finish" || len(runView.Attempts) != 1 ||
		runView.Attempts[0].NodeID != "finish" || runView.Attempts[0].Scenario != runexecution.ScenarioWorkflow || runView.Attempts[0].Provider != runexecution.ProviderCodex {
		t.Fatalf("run view = %#v", runView)
	}
}

func TestParseWorkTransitionKeepsReadyPreparationTargetSpecific(t *testing.T) {
	workID := "work_01K3Z1C1AAAAAAAAAAAAAAAAAA"
	planned, err := parseWorkTransition([]string{"plan", workID, "--to", "ready", "--workflow", "delivery", "--version", "1.0.0", "--profile", "fast"})
	if err != nil {
		t.Fatal(err)
	}
	if planned.action != "plan" || planned.request.Target != worklifecycle.StateReady || planned.request.Preparation == nil || planned.request.Preparation.Profile != "fast" || planned.expected != 0 || planned.key != "" {
		t.Fatalf("planned = %#v", planned)
	}
	applied, err := parseWorkTransition([]string{"apply", workID, "--to", "running", "--if-match", "7", "--idempotency-key", "transition-command"})
	if err != nil {
		t.Fatal(err)
	}
	if applied.action != "apply" || applied.expected != 7 || applied.key != "transition-command" || applied.request.Preparation != nil {
		t.Fatalf("applied = %#v", applied)
	}
}

func TestParseWorkTransitionRejectsContradictorySiblingFields(t *testing.T) {
	workID := "work_01K3Z1C1AAAAAAAAAAAAAAAAAA"
	for _, args := range [][]string{
		{"apply", workID, "--to", "running", "--workflow", "delivery", "--version", "1", "--if-match", "1"},
		{"plan", workID, "--to", "ready", "--confirm"},
		{"apply", workID, "--to", "running"},
	} {
		if _, err := parseWorkTransition(args); err == nil {
			t.Fatalf("args=%v unexpectedly accepted", args)
		}
	}
}
