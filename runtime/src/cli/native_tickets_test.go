package cli

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"darkstar/src/core/ticketmanagement"
	"darkstar/src/ports/platform"
	"darkstar/src/ports/statestore"
)

func TestNativeTicketCLIUsesDaemonCapabilitiesAndStableRevision(t *testing.T) {
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
	service := startAcceptanceService(t, paths, "55555555555555555555555555555555")
	t.Cleanup(func() {
		_ = service.Close()
	})
	var project statestore.ProjectProjection
	runCLIJSON(t, []string{"project", "add", root, "--name", "Native", "--idempotency-key", "native-cli-project", "--json"}, &struct {
		Result *statestore.ProjectProjection `json:"result"`
	}{Result: &project})
	var work statestore.WorkItemProjection
	runCLIJSON(t, []string{"work", "create", "Native title", "--project", project.ProjectID, "--idempotency-key", "native-cli-create", "--json"}, &struct {
		Result *statestore.WorkItemProjection `json:"result"`
	}{Result: &work})
	var page ticketmanagement.Page
	runCLIJSON(t, []string{"ticket", "list", project.ProjectID, "--state", "open", "--json"}, &struct {
		Result *ticketmanagement.Page `json:"result"`
	}{Result: &page})
	if len(page.Tickets) != 1 || page.Tickets[0].ID != work.WorkItemID {
		t.Fatalf("native page = %#v", page)
	}
	var detail ticketmanagement.Detail
	result := &struct {
		Result *ticketmanagement.Detail `json:"result"`
	}{Result: &detail}
	runCLIJSON(t, []string{"ticket", "show", project.ProjectID, work.WorkItemID, "--json"}, result)
	if !detail.Capabilities.Edit || len(detail.Transitions) == 0 {
		t.Fatalf("native capabilities = %#v", detail)
	}
	runCLIJSON(t, []string{"ticket", "edit", project.ProjectID, work.WorkItemID, "--revision", detail.Ticket.Revision, "--idempotency-key", "native-cli-edit", "--title", "Edited title", "--json"}, result)
	if detail.Ticket.Title != "Edited title" || detail.Ticket.Revision != "2" || len(detail.History) != 2 {
		t.Fatalf("native edit = %#v", detail)
	}
	transition := detail.Transitions[0]
	runCLIJSON(t, []string{"ticket", "transition", project.ProjectID, work.WorkItemID, "--revision", detail.Ticket.Revision, "--idempotency-key", "native-cli-transition", "--transition", transition.ID, "--json"}, result)
	if detail.Ticket.BusinessState.ID != transition.ToState.ID || len(detail.History) != 3 {
		t.Fatalf("native transition = %#v", detail)
	}
}
