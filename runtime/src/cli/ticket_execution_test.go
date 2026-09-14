package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	"darkstar/src/api"
	"darkstar/src/ports/platform"
	"darkstar/src/ports/statestore"
)

func TestTicketExecutionCLIApprovesCachedVersionWithoutScheduling(t *testing.T) {
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
	service := startAcceptanceService(t, paths, "77777777777777777777777777777777")
	t.Cleanup(func() {
		_ = service.Close()
	})
	project := createPlanningCLIProject(t, "Admission", "admission-cli-project")
	var work statestore.WorkItemProjection
	runCLIJSON(t, []string{"work", "create", "Source version", "--project", project.ProjectID, "--idempotency-key", "admission-cli-create", "--json"}, &struct {
		Result *statestore.WorkItemProjection `json:"result"`
	}{Result: &work})
	var refreshed api.BacklogRefreshResponse
	runCLIJSON(t, []string{"backlog", "refresh", project.ProjectID, "--revision", "1", "--json"}, &struct {
		Result *api.BacklogRefreshResponse `json:"result"`
	}{Result: &refreshed})
	var page api.BacklogView
	runCLIJSON(t, []string{"backlog", "list", project.ProjectID, "--json"}, &struct {
		Result *api.BacklogView `json:"result"`
	}{Result: &page})
	if len(page.Tickets) != 1 {
		t.Fatalf("ticket cache = %#v", page)
	}
	arguments := []string{"ticket-execution", "admit", project.ProjectID, "--revision", "1", "--observation", page.Tickets[0].ObservationID, "--idempotency-key", "cli-exact-admission", "--json"}
	var first, replay api.TicketAdmissionResponse
	runCLIJSON(t, arguments, &struct {
		Result *api.TicketAdmissionResponse `json:"result"`
	}{Result: &first})
	runCLIJSON(t, arguments, &struct {
		Result *api.TicketAdmissionResponse `json:"result"`
	}{Result: &replay})
	if first.WorkItemID != work.WorkItemID || replay.SourceObservationID != first.SourceObservationID || replay.Source.Approval.ID != first.Source.Approval.ID {
		t.Fatalf("admission replay = %#v / %#v", first, replay)
	}
	var stdout, stderr bytes.Buffer
	stale := append([]string{}, arguments...)
	stale[4] = "2"
	if code := Run(stale, &stdout, &stderr); code == 0 {
		t.Fatal("changed binding accepted under an existing approval command")
	}
	runs, err := service.database.Runs(context.Background())
	if err != nil || len(runs) != 0 {
		t.Fatalf("admission scheduled execution: %#v, %v", runs, err)
	}
	history, err := service.database.NativeTicketHistory(context.Background(), project.ProjectID, work.WorkItemID)
	if err != nil || len(history) != 1 {
		t.Fatalf("admission changed source business state: %#v, %v", history, err)
	}
}

func TestRunCLIRequiresExplicitSingleSourceObservation(t *testing.T) {
	workID := "work_01ARZ3NDEKTSV4RRFFQ69G5FAV"
	input, _, err := parseWorkRun([]string{workID, "--source-observation", "observation-v1"}, "prepare")
	if err != nil || input.SourceObservationID != "observation-v1" {
		t.Fatalf("explicit source input = %#v, %v", input, err)
	}
	if _, _, err := parseWorkRun([]string{workID, "--source-observation", "first", "--source-observation", "second"}, "start"); err == nil {
		t.Fatal("ambiguous source approval accepted")
	}
	transition, err := parseWorkTransition([]string{"plan", workID, "--to", "ready", "--source-observation", "observation-v1"})
	if err != nil || transition.request.Preparation == nil || transition.request.Preparation.SourceObservationID != "observation-v1" || transition.request.Preparation.WorkflowID != "" {
		t.Fatalf("automatic route source approval = %#v, %v", transition, err)
	}
}
