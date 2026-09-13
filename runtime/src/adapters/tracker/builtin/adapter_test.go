package builtin_test

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"darkstar/src/adapters/statestore/sqlite"
	"darkstar/src/adapters/tracker/builtin"
	"darkstar/src/core/identity"
	"darkstar/src/core/workmanagement"
	"darkstar/src/ports"
	"darkstar/src/ports/statestore"
	"darkstar/src/ports/ticketwriter"
	"darkstar/src/ports/tracker"
	"darkstar/src/ports/worksource"
)

func fixture(t *testing.T) (*sqlite.Database, *builtin.Adapter, tracker.Manifest, string, string) {
	t.Helper()
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "native.db")
	db, err := sqlite.Open(ctx, path, sqlite.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = db.Close()
	})
	service, err := workmanagement.New(db)
	if err != nil {
		t.Fatal(err)
	}
	project, err := service.RegisterProject(ctx, workmanagement.ProjectRegistration{Name: "Factory", Source: "native-fixture"}, "project-fixture")
	if err != nil {
		t.Fatal(err)
	}
	work, err := service.CreateWork(ctx, workmanagement.CreateWorkRequest{ProjectID: project.ProjectID, Title: "Legacy native ticket", Details: "Original body", Evidence: []string{"artifact:original"}, Priority: 3}, "native-work-fixture")
	if err != nil {
		t.Fatal(err)
	}
	adapter, err := builtin.New(db, project.ProjectID)
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := adapter.Discover(ctx, adapter.ConfigPin())
	if err != nil {
		t.Fatal(err)
	}
	return db, adapter, manifest, work.WorkItemID, path
}

func options(t *testing.T, a *builtin.Adapter, m tracker.Manifest, id string) tracker.TicketScope {
	t.Helper()
	value, err := a.Inspect(context.Background(), ticketwriter.InspectRequest{Pin: m.Pin, Destination: m.Scope, Scope: tracker.TicketScope{Ref: tracker.TicketRef{Namespace: m.Scope.Namespace, ID: id}}})
	if err != nil {
		t.Fatal(err)
	}
	return value.Scope.(tracker.TicketScope)
}

func intent(m tracker.Manifest, id string, effect tracker.Effect) tracker.Intent {
	return tracker.Intent{OperationID: id, DesiredDigest: strings.Repeat("a", 64), AuthorizationRef: "authorized-user-command:" + id, Pin: m.Pin, Destination: m.Scope, Effect: effect}
}

func apply(t *testing.T, a *builtin.Adapter, i tracker.Intent) tracker.Receipt {
	t.Helper()
	result, err := a.Apply(context.Background(), i)
	if err != nil {
		t.Fatal(err)
	}
	return result.(tracker.Applied).Receipt
}

func TestNativeBusinessStateAndHistorySurviveExecutionRebuildAndRestart(t *testing.T) {
	ctx := context.Background()
	db, a, m, workID, path := fixture(t)
	before, err := db.WorkItem(ctx, workID)
	if err != nil {
		t.Fatal(err)
	}
	target := options(t, a, m, workID)
	edit := intent(m, "edit-native", tracker.EditTicket{Target: target, Fields: map[string]tracker.FieldValue{"title": tracker.TextValue("Edited title"), "description": tracker.TextValue("Human body"), "labels": tracker.IDsValue{"review"}}})
	receipt := apply(t, a, edit)
	if replay := apply(t, a, edit); !reflect.DeepEqual(replay, receipt) {
		t.Fatalf("replay changed receipt: %#v", replay)
	}
	transition := intent(m, "transition-native", tracker.TakeTransition{Target: options(t, a, m, workID), TransitionID: "set-state:active"})
	apply(t, a, transition)
	data, _ := json.Marshal(map[string]any{})
	_, err = db.Append(ctx, statestore.PendingEvent{SchemaVersion: 1, ID: identity.Random("event_"), AggregateType: statestore.AggregateWork, AggregateID: workID, ExpectedRevision: before.ResourceVersion, Kind: "work.completed", OccurredAt: time.Now().UTC(), CorrelationID: workID, CommandID: "execution-completed", Actor: statestore.Actor{Type: statestore.ActorSystem, ID: "daemon"}, Data: data, Metadata: json.RawMessage(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.RebuildProjections(ctx); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := sqlite.Open(ctx, path, sqlite.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = reopened.Close()
	}()
	ticket, err := reopened.NativeTicket(ctx, m.Scope.ContainerID, workID)
	if err != nil || ticket.State != statestore.NativeActive || ticket.Title != "Edited title" || ticket.Description != "Human body" || ticket.Revision != 3 {
		t.Fatalf("native business record changed on execution/rebuild/restart: %#v, %v", ticket, err)
	}
	history, err := reopened.NativeTicketHistory(ctx, m.Scope.ContainerID, workID)
	if err != nil || len(history) != 3 || history[0].Snapshot.Title != before.Title || history[0].Snapshot.Evidence[0] != "artifact:original" {
		t.Fatalf("native history lost: %#v, %v", history, err)
	}
	execution, err := reopened.WorkItem(ctx, workID)
	if err != nil || execution.Status != statestore.WorkItemCompleted {
		t.Fatalf("execution compatibility lost: %#v %v", execution, err)
	}
	restarted, _ := builtin.New(reopened, m.Scope.ContainerID)
	result, err := restarted.Reconcile(ctx, edit)
	if err != nil || !reflect.DeepEqual(result, tracker.Applied{Receipt: receipt}) {
		t.Fatalf("restart reconciliation: %#v %v", result, err)
	}
}

func TestNativeSharedWriterCreateProgressRelationshipsAndPaging(t *testing.T) {
	ctx := context.Background()
	db, a, m, original, _ := fixture(t)
	create := intent(m, "create-native", tracker.CreateTicket{IssueTypeID: "ticket", Origin: tracker.DirectCreation{Request: tracker.ArtifactRef{ArtifactID: "request", Version: 1, SHA256: strings.Repeat("a", 64)}}, Fields: map[string]tracker.FieldValue{"title": tracker.TextValue("New standalone ticket"), "priority": tracker.NumberValue(2)}})
	created := apply(t, a, create)
	if _, err := db.WorkItem(ctx, created.Target.ID); err == nil {
		t.Fatal("ticket creation unexpectedly created execution work")
	}
	progress := intent(m, "progress-native", tracker.ReportProgress{Target: options(t, a, m, original), Body: "A retained clarification", Evidence: tracker.GeneralProgress{Artifacts: []tracker.ArtifactRef{{ArtifactID: "clarification", Version: 1, SHA256: strings.Repeat("b", 64)}}}})
	apply(t, a, progress)
	history, err := db.NativeTicketHistory(ctx, m.Scope.ContainerID, original)
	if err != nil || !strings.Contains(string(history[1].Request), "A retained clarification") {
		t.Fatalf("progress report missing from history: %#v %v", history, err)
	}
	relation := intent(m, "relationship-native", tracker.LinkStories{Target: options(t, a, m, original), Story: tracker.StoryRef{ProjectID: m.Scope.ContainerID, FeatureKey: "feature", StoryKey: "one"}, RelatedStory: tracker.StoryRef{ProjectID: m.Scope.ContainerID, FeatureKey: "feature", StoryKey: "two"}, Backlog: tracker.ArtifactRef{ArtifactID: "backlog", Version: 1, SHA256: strings.Repeat("c", 64)}, Relation: tracker.Relation{Kind: tracker.DependsOn, Target: created.Target}, Representation: tracker.NativeRelation{}})
	apply(t, a, relation)
	request := worksource.BrowseTicketsRequest{Pin: m.Pin, Scope: m.Scope, Query: tracker.Query{PageSize: 1}}
	first, err := a.Browse(ctx, request)
	if err != nil || len(first.Tickets) != 1 {
		t.Fatalf("browse: %#v %v", first, err)
	}
	request.Query.Cursor = first.Next.(tracker.More).Cursor
	second, err := a.Browse(ctx, request)
	if err != nil || len(second.Tickets) != 1 || first.Tickets[0].Ref == second.Tickets[0].Ref {
		t.Fatalf("second page: %#v %v", second, err)
	}
	request.Query.Text = "different query"
	if _, err := a.Browse(ctx, request); err == nil {
		t.Fatal("accepted cursor for another query")
	}
	unchanged, err := a.Read(ctx, worksource.ReadTicketRequest{Pin: m.Pin, Ref: created.Target, KnownRevision: "1"})
	if _, ok := unchanged.(tracker.Unchanged); err != nil || !ok {
		t.Fatalf("conditional read: %#v %v", unchanged, err)
	}
	missing, err := a.Read(ctx, worksource.ReadTicketRequest{Pin: m.Pin, Ref: tracker.TicketRef{Namespace: m.Scope.Namespace, ID: "absent"}})
	if _, ok := missing.(tracker.Missing); err != nil || !ok {
		t.Fatalf("missing read: %#v %v", missing, err)
	}
}

func TestNativeWriterRejectsDriftAndConflictingOperations(t *testing.T) {
	ctx := context.Background()
	db, a, m, id, _ := fixture(t)
	target := options(t, a, m, id)
	first := intent(m, "duplicate-op", tracker.EditTicket{Target: target, Fields: map[string]tracker.FieldValue{"description": tracker.TextValue("first")}})
	apply(t, a, first)
	cases := []tracker.Intent{
		intent(m, "duplicate-op", tracker.EditTicket{Target: target, Fields: map[string]tracker.FieldValue{"description": tracker.TextValue("different")}}),
		intent(m, "stale-op", tracker.EditTicket{Target: target, Fields: map[string]tracker.FieldValue{"description": tracker.TextValue("stale")}}),
		intent(m, "unknown-transition", tracker.TakeTransition{Target: options(t, a, m, id), TransitionID: "complete-workflow"}),
		intent(m, "status-field", tracker.EditTicket{Target: options(t, a, m, id), Fields: map[string]tracker.FieldValue{"business_state": tracker.TextValue("completed")}}),
	}
	for _, request := range cases {
		if _, err := a.Apply(ctx, request); err == nil {
			t.Fatalf("accepted invalid operation %s", request.OperationID)
		}
	}
	drift := m.Pin
	drift.ConfigRevision = "2"
	if _, err := a.Read(ctx, worksource.ReadTicketRequest{Pin: drift, Ref: target.Ref}); err == nil {
		t.Fatal("accepted drifted adapter pin")
	}
	unapplied := intent(m, "not-applied", tracker.EditTicket{Target: options(t, a, m, id), Fields: map[string]tracker.FieldValue{"description": tracker.TextValue("not written")}})
	result, err := a.Reconcile(ctx, unapplied)
	if _, ok := result.(tracker.NotApplied); err != nil || !ok {
		t.Fatalf("positive absence proof: %#v %v", result, err)
	}
	history, err := db.NativeTicketHistory(ctx, m.Scope.ContainerID, id)
	if err != nil || len(history) != 2 {
		t.Fatalf("rejected effects wrote history: %#v %v", history, err)
	}
	_, err = db.SQL().ExecContext(ctx, `DELETE FROM native_ticket_history WHERE ticket_id=?`, id)
	if err == nil {
		t.Fatal("native history was deletable")
	}
}

func TestLegacyImportRemainsUnresolved(t *testing.T) {
	ctx := context.Background()
	db, _, m, _, _ := fixture(t)
	service, _ := workmanagement.New(db)
	work, err := service.ImportWork(ctx, workmanagement.ImportWorkRequest{ProjectID: m.Scope.ContainerID, SourceReference: "https://provider.example/issue/42", Title: "Imported"}, "legacy-import-fixture")
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.NativeTicket(ctx, m.Scope.ContainerID, work.WorkItemID)
	var failure *ports.Failure
	if !errors.As(err, &failure) || failure.Code != ports.FailureNotFound {
		t.Fatalf("unverified import became native ticket: %v", err)
	}
	var kind string
	if err := db.SQL().QueryRowContext(ctx, `SELECT mapping_kind FROM native_work_mappings WHERE work_id=?`, work.WorkItemID).Scan(&kind); err != nil || kind != "legacy_unresolved" {
		t.Fatalf("missing unresolved mapping: %s %v", kind, err)
	}
}
