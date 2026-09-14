package cli

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"darkstar/src/api"
	"darkstar/src/core/ticketmanagement"
	"darkstar/src/core/trackerrules"
	"darkstar/src/core/workmanagement"
	"darkstar/src/ports/platform"
	"darkstar/src/ports/statestore"
	"darkstar/src/ports/tracker"
)

func mappingCLIFixture(t *testing.T) (*daemonAPIService, string, statestore.ProjectProjection, statestore.WorkItemProjection) {
	t.Helper()
	root := t.TempDir()
	paths := platform.Paths{Config: filepath.Join(root, "config"), Data: filepath.Join(root, "data"), Cache: filepath.Join(root, "cache"), Logs: filepath.Join(root, "logs"), Runtime: filepath.Join(root, "runtime")}
	for _, directory := range []string{paths.Config, paths.Data, paths.Cache, paths.Logs, paths.Runtime} {
		if err := os.MkdirAll(directory, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	prior := resolveApplicationPaths
	resolveApplicationPaths = func(context.Context) (platform.Paths, error) {
		return paths, nil
	}
	t.Cleanup(func() {
		resolveApplicationPaths = prior
	})
	service := startAcceptanceService(t, paths, "77777777777777777777777777777777")
	t.Cleanup(func() {
		_ = service.Close()
	})
	var project statestore.ProjectProjection
	runCLIJSON(t, []string{"project", "add", root, "--name", "Mappings", "--idempotency-key", "mapping-project", "--json"}, &struct {
		Result *statestore.ProjectProjection `json:"result"`
	}{Result: &project})
	var work statestore.WorkItemProjection
	runCLIJSON(t, []string{"work", "create", "Mapped native ticket", "--project", project.ProjectID, "--idempotency-key", "mapping-ticket", "--json"}, &struct {
		Result *statestore.WorkItemProjection `json:"result"`
	}{Result: &work})
	var refresh api.BacklogRefreshResponse
	runCLIJSON(t, []string{"backlog", "refresh", project.ProjectID, "--revision", "1", "--json"}, &struct {
		Result *api.BacklogRefreshResponse `json:"result"`
	}{Result: &refresh})
	return service, root, project, work
}

func writeMappingRequest(t *testing.T, root, name string, value any) string {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	filename := filepath.Join(root, name+".json")
	if err := os.WriteFile(filename, encoded, 0o600); err != nil {
		t.Fatal(err)
	}
	return filename
}

func TestTrackerMappingCLIConfigPreviewActivationAndNativeBoard(t *testing.T) {
	service, root, project, work := mappingCLIFixture(t)
	ctx := context.Background()
	var view api.BacklogView
	runCLIJSON(t, []string{"backlog", "list", project.ProjectID, "--json"}, &struct {
		Result *api.BacklogView `json:"result"`
	}{Result: &view})
	if len(view.Tickets) != 1 {
		t.Fatalf("tickets = %#v", view.Tickets)
	}
	observationID := view.Tickets[0].ObservationID
	var discovery struct {
		Template json.RawMessage `json:"template"`
		Creation struct {
			Available bool `json:"available"`
		} `json:"creation"`
	}
	runCLIJSON(t, []string{"tracker", "mapping", "discover", project.ProjectID, "--observation", observationID, "--json"}, &struct {
		Result any `json:"result"`
	}{Result: &discovery})
	if !discovery.Creation.Available {
		t.Fatal("native source creation capability was lost")
	}
	rules, err := trackerrules.Decode(discovery.Template)
	if err != nil {
		t.Fatal(err)
	}
	rules.Display.Groups = []trackerrules.DisplayGroup{{ID: "active-work", Name: "In our hands", StateIDs: []string{"open", "active"}}}
	rules.Display.UnknownGroup = trackerrules.DisplayGroup{ID: "other-stages", Name: "Other business stages", StateIDs: []string{}}
	rules.Intake = []trackerrules.IntakeRule{{ID: "observe-open", When: trackerrules.Conditions{Fields: []trackerrules.Predicate{{FieldID: "state", Values: []string{"open"}}}}, Action: trackerrules.Noop{}}}
	encoded, err := trackerrules.Encode(rules)
	if err != nil {
		t.Fatal(err)
	}
	request := api.TrackerMappingRequest{SchemaVersion: 1, Rules: encoded, ObservationID: observationID}
	filename := writeMappingRequest(t, root, "save", request)
	var saved api.TrackerMappingRevision
	runCLIJSON(t, []string{"tracker", "mapping", "save", project.ProjectID, "--file", filename, "--json"}, &struct {
		Result *api.TrackerMappingRevision `json:"result"`
	}{Result: &saved})
	if saved.Revision != 1 {
		t.Fatalf("revision = %d", saved.Revision)
	}
	var preview struct {
		Valid  bool `json:"valid"`
		Intake struct {
			Matched bool `json:"matched"`
		} `json:"intake"`
	}
	runCLIJSON(t, []string{"tracker", "mapping", "preview", project.ProjectID, "--file", filename, "--json"}, &struct {
		Result any `json:"result"`
	}{Result: &preview})
	if !preview.Valid || !preview.Intake.Matched {
		t.Fatalf("preview = %#v", preview)
	}
	activateFile := writeMappingRequest(t, root, "activate", api.TrackerMappingActivation{SchemaVersion: 1, Revision: 1, ObservationID: observationID})
	var history api.TrackerMappingHistory
	runCLIJSON(t, []string{"tracker", "mapping", "activate", project.ProjectID, "--file", activateFile, "--json"}, &struct {
		Result *api.TrackerMappingHistory `json:"result"`
	}{Result: &history})
	if history.ActiveRevision != 1 || len(history.Revisions) != 1 {
		t.Fatalf("history = %#v", history)
	}
	if _, err := service.trackerMapping.Activate(ctx, project.ProjectID, api.TrackerMappingActivation{SchemaVersion: 1, Revision: 1, ObservationID: observationID}); err == nil {
		t.Fatal("stale activation accepted")
	}
	var board api.TrackerBoard
	runCLIJSON(t, []string{"tracker", "mapping", "board", project.ProjectID, "--json"}, &struct {
		Result *api.TrackerBoard `json:"result"`
	}{Result: &board})
	if len(board.Columns) != 1 || board.UnknownGroup.ID != "other-stages" || len(board.Actions[observationID]) != 3 {
		t.Fatalf("board = %#v", board)
	}
	transitionFile := writeMappingRequest(t, root, "transition", api.TrackerBoardTransition{SchemaVersion: 1, ObservationID: observationID, ExpectedBindingRevision: 1, ExpectedMappingRevision: 1, TransitionID: "set-state:active"})
	var transitioned json.RawMessage
	runCLIJSON(t, []string{"tracker", "mapping", "transition", project.ProjectID, "--file", transitionFile, "--key", "mapping-native-transition", "--json"}, &struct {
		Result *json.RawMessage `json:"result"`
	}{Result: &transitioned})
	ticket, err := service.database.NativeTicket(ctx, project.ProjectID, work.WorkItemID)
	if err != nil || ticket.State != statestore.NativeActive {
		t.Fatalf("ticket = %#v, %v", ticket, err)
	}
	// Retry returns the original receipt even though the transition refreshed
	// the source observation and invalidated the original board snapshot.
	runCLIJSON(t, []string{"tracker", "mapping", "transition", project.ProjectID, "--file", transitionFile, "--key", "mapping-native-transition", "--json"}, &struct {
		Result *json.RawMessage `json:"result"`
	}{Result: &transitioned})
	runs, err := service.database.Runs(ctx)
	if err != nil || len(runs) != 0 {
		t.Fatalf("preview/display/transition created execution: %#v, %v", runs, err)
	}
	request.ObservationID = observationID
	stale, err := service.trackerMapping.Preview(ctx, project.ProjectID, request)
	if err != nil || stale.(map[string]any)["valid"] != false {
		t.Fatalf("stale preview = %#v, %v", stale, err)
	}
}

func TestTrackerMappingKeepsHistoryAndRejectsNativeCreationAfterSourceSwitch(t *testing.T) {
	service, _, project, work := mappingCLIFixture(t)
	ctx := context.Background()
	manager, err := workmanagement.New(service.database)
	if err != nil {
		t.Fatal(err)
	}
	source := statestore.ExternalBacklogSource{ConnectionID: "read-only", ConnectionRevision: "1", Scope: tracker.Scope{Namespace: tracker.Namespace{Provider: "linear", Host: "linear.app", TenantID: "workspace", ScopeID: "workspace"}, ContainerID: "team"}}
	if _, err := service.database.SelectBacklogSource(ctx, project.ProjectID, 1, source, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.CreateWork(ctx, workmanagement.CreateWorkRequest{ProjectID: project.ProjectID, Title: "Must use selected source"}, "forbidden-shadow-create"); err == nil {
		t.Fatal("external source silently created a native ticket")
	}
	if _, err := manager.ImportWork(ctx, workmanagement.ImportWorkRequest{ProjectID: project.ProjectID, SourceReference: "https://example.test/issue"}, "retained-legacy-import"); err != nil {
		t.Fatal("explicit external import capability was lost:", err)
	}
	// The original idempotent command still refers to its retained native work.
	replayed, err := manager.CreateWork(ctx, workmanagement.CreateWorkRequest{ProjectID: project.ProjectID, Title: "Mapped native ticket"}, "mapping-ticket")
	if err != nil || replayed.WorkItemID != work.WorkItemID {
		t.Fatalf("retained native replay = %#v, %v", replayed, err)
	}
	tickets, err := service.database.NativeTickets(ctx, project.ProjectID)
	if err != nil || len(tickets) != 1 {
		t.Fatalf("shadow tickets = %#v, %v", tickets, err)
	}
}

func TestTrackerMappingTransitionRecoversAfterNativeReceiptBeforeBoardResponse(t *testing.T) {
	service, _, project, work := mappingCLIFixture(t)
	ctx := context.Background()
	board, err := service.trackerMapping.Board(ctx, project.ProjectID)
	if err != nil {
		t.Fatal(err)
	}
	observationID := ""
	for id := range board.Actions {
		observationID = id
	}
	_, observation, err := service.trackerMapping.discoveryFor(ctx, project.ProjectID, observationID)
	if err != nil || observation == nil {
		t.Fatalf("observation = %#v, %v", observation, err)
	}
	input := api.TrackerBoardTransition{SchemaVersion: 1, ObservationID: observationID, ExpectedBindingRevision: 1, TransitionID: "set-state:active"}
	encoded, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(encoded)
	scope := "tracker-board/transition/" + project.ProjectID
	key := "mapping-crash-transition"
	// Reproduce the durable preflight at the crash boundary: target recorded,
	// native effect committed, top-level board response not yet completed.
	for _, commandScope := range []string{scope, scope + "/target"} {
		if _, _, err := service.database.BeginCommand(ctx, statestore.BeginCommandRequest{Scope: commandScope, IdempotencyKey: key, RequestDigest: fmt.Sprintf("%x", digest), CreatedAt: time.Now().UTC()}); err != nil {
			t.Fatal(err)
		}
	}
	frozen, err := json.Marshal(boardTransitionTarget{Ref: observation.Ticket.Ref, Revision: observation.Ticket.Revision})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.database.CompleteCommand(ctx, statestore.CompleteCommandRequest{Scope: scope + "/target", IdempotencyKey: key, ResponseStatus: 200, Response: frozen, CompletedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	if _, err := service.trackerMapping.native.Transition(ctx, project.ProjectID, work.WorkItemID, ticketmanagement.TransitionRequest{SchemaVersion: 1, Revision: observation.Ticket.Revision, TransitionID: input.TransitionID}, key); err != nil {
		t.Fatal(err)
	}
	if _, err := service.backlog.ReadRefresh(ctx, project.ProjectID, 1, observation.Ticket.Ref); err != nil {
		t.Fatal(err)
	}
	restarted := *service.trackerMapping
	if _, err := restarted.Transition(ctx, project.ProjectID, input, key); err != nil {
		t.Fatal("pending board response could not recover the durable native receipt:", err)
	}
	ticket, err := service.database.NativeTicket(ctx, project.ProjectID, work.WorkItemID)
	if err != nil || ticket.Revision != 2 || ticket.State != statestore.NativeActive {
		t.Fatalf("recovery repeated native transition: %#v, %v", ticket, err)
	}
}
