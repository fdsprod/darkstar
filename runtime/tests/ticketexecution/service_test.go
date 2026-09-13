package ticketexecution_test

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"darkstar/src/adapters/statestore/sqlite"
	"darkstar/src/adapters/tracker/builtin"
	"darkstar/src/core/backlog"
	"darkstar/src/core/identity"
	"darkstar/src/core/ticketexecution"
	"darkstar/src/core/trackercontract"
	"darkstar/src/ports"
	"darkstar/src/ports/statestore"
	"darkstar/src/ports/tracker"
	"darkstar/src/ports/worksource"
)

type fixture struct {
	db            *sqlite.Database
	path, project string
	now           time.Time
	state         statestore.BacklogRefreshState
	scope         tracker.Scope
	service       *ticketexecution.Service
	read          tracker.ReadResult
	readError     error
}

func setup(t *testing.T) *fixture {
	t.Helper()
	f := &fixture{path: filepath.Join(t.TempDir(), "source.db"), project: identity.Deterministic("project_", t.Name()), now: time.Now().UTC()}
	var err error
	f.db, err = sqlite.Open(context.Background(), f.path, sqlite.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = f.db.Close()
	})
	f.append(t, statestore.AggregateProject, f.project, "project.created", map[string]any{"name": "Source project", "sourceHash": strings.Repeat("a", 64)})
	f.scope = tracker.Scope{Namespace: tracker.Namespace{Provider: "github_issues", Host: "github.com", TenantID: "owner", ScopeID: "repository"}, ContainerID: "repository"}
	binding, err := f.db.SelectBacklogSource(context.Background(), f.project, 1, statestore.ExternalBacklogSource{ConnectionID: "connection", ConnectionRevision: "1", Scope: f.scope}, f.now)
	if err != nil {
		t.Fatal(err)
	}
	f.state = statestore.BacklogRefreshState{ProjectID: f.project, BindingRevision: binding.Revision, Generation: 1, Phase: statestore.BacklogComplete, Query: tracker.Query{PageSize: 10}, Pin: tracker.Pin{AdapterConfigPin: tracker.AdapterConfigPin{ContractVersion: tracker.Version, AdapterID: "github_issues", AdapterVersion: "1", InstallationID: "connection", AccountID: "account", BindingRevision: strconv.FormatUint(binding.Revision, 10), ConfigRevision: "1", ConfigDigest: strings.Repeat("b", 64)}, CapabilitiesDigest: strings.Repeat("c", 64)}, StartedAt: f.now, UpdatedAt: f.now, LastSuccessAt: f.now}
	f.service, err = ticketexecution.New(f.db, f, ticketexecution.Options{Now: func() time.Time {
		return f.now
	}})
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func (f *fixture) append(t *testing.T, kind statestore.AggregateType, id, event string, data any) {
	t.Helper()
	encoded, _ := json.Marshal(data)
	_, err := f.db.Append(context.Background(), statestore.PendingEvent{SchemaVersion: 1, ID: identity.Deterministic("event_", id+event), AggregateType: kind, AggregateID: id, Kind: event, OccurredAt: f.now, CorrelationID: id, CommandID: id + event, Actor: statestore.Actor{Type: statestore.ActorUser, ID: "test"}, Data: encoded, Metadata: json.RawMessage(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
}

func (f *fixture) Resolve(ctx context.Context, binding statestore.BacklogBinding) (backlog.ResolvedSource, error) {
	if _, native := binding.Source.(statestore.NativeBacklogSource); native {
		adapter, err := builtin.NewForBinding(f.db, binding.ProjectID, strconv.FormatUint(binding.Revision, 10))
		if err != nil {
			return backlog.ResolvedSource{}, err
		}
		return backlog.ResolvedSource{Source: adapter, Browser: adapter, Config: adapter.ConfigPin()}, nil
	}
	return backlog.ResolvedSource{Source: f, Config: f.state.Pin.AdapterConfigPin}, nil
}

func (f *fixture) Discover(context.Context, tracker.AdapterConfigPin) (tracker.Manifest, error) {
	if f.readError != nil {
		return tracker.Manifest{}, f.readError
	}
	return tracker.Manifest{Pin: f.state.Pin, Scope: f.scope, ObservedAt: f.now, EvidenceRef: "source-discovery"}, nil
}

func (f *fixture) Read(context.Context, worksource.ReadTicketRequest) (tracker.ReadResult, error) {
	return f.read, f.readError
}

func (f *fixture) observation(t *testing.T, id, revision, title string) statestore.BacklogObservation {
	t.Helper()
	ticket := tracker.Ticket{Ref: tracker.TicketRef{Namespace: f.scope.Namespace, ID: id}, Revision: revision, Key: "repo#1", URL: "https://github.com/owner/repo/issues/1", Title: title, Description: "Untrusted source description", BusinessState: tracker.Known[tracker.NamedID]{Value: tracker.NamedID{ID: "open", Name: "Open"}}, BusinessStateReason: tracker.Unsupported[tracker.NamedID]{Reason: "unavailable"}, IssueType: tracker.Known[tracker.NamedID]{Value: tracker.NamedID{ID: "issue", Name: "Issue"}}, Sprint: tracker.Unsupported[[]tracker.NamedID]{Reason: "unavailable"}, Assignees: tracker.Known[[]tracker.NamedID]{Value: []tracker.NamedID{}}, Labels: tracker.Known[[]tracker.NamedID]{Value: []tracker.NamedID{}}, Priority: tracker.Unsupported[tracker.NamedID]{Reason: "unavailable"}, Relationships: tracker.Unknown[[]tracker.Relation]{Reason: "unobserved"}, Archived: tracker.Known[bool]{Value: false}, UpdatedAt: tracker.Known[time.Time]{Value: f.now}, Placement: tracker.Known[tracker.Scope]{Value: f.scope}, Freshness: tracker.Fresh{ObservedAt: f.now, Revision: revision}, EvidenceRef: "evidence:" + id + ":" + revision}
	encoded, err := trackercontract.EncodeTicket(ticket)
	if err != nil {
		t.Fatal(err)
	}
	observationID, key, digest, err := trackercontract.ObservationIdentity(ticket)
	if err != nil {
		t.Fatal(err)
	}
	return statestore.BacklogObservation{ID: observationID, TicketKey: key, NativeRevision: revision, ContentDigest: digest, Ref: ticket.Ref, Ticket: encoded, ObservedAt: f.now, EvidenceRef: ticket.EvidenceRef}
}

func (f *fixture) retain(t *testing.T, observation statestore.BacklogObservation) {
	t.Helper()
	expected := f.state.Revision
	f.state.Revision++
	f.state.UpdatedAt, f.state.LastSuccessAt = f.now, f.now
	err := f.db.CommitBacklogRefresh(context.Background(), statestore.BacklogCommit{ExpectedRevision: expected, State: f.state, Observations: []statestore.BacklogObservation{observation}, Tickets: []statestore.BacklogCachedTicket{{ProjectID: f.project, BindingRevision: f.state.BindingRevision, TicketKey: observation.TicketKey, ObservationID: observation.ID, State: statestore.BacklogAvailable, CheckedAt: f.now, SeenGeneration: 1, EvidenceRef: observation.EvidenceRef}}})
	if err != nil {
		t.Fatal(err)
	}
}

func (f *fixture) admit(t *testing.T, observation statestore.BacklogObservation, key string) ticketexecution.Result {
	t.Helper()
	result, err := f.service.Admit(context.Background(), ticketexecution.AdmissionRequest{ProjectID: f.project, BindingRevision: f.state.BindingRevision, ObservationID: observation.ID}, key)
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func TestSourceAdmissionConcurrentKeysRestartAndReplayPreserveOneWork(t *testing.T) {
	f := setup(t)
	observation := f.observation(t, "issue", "1", "Original")
	f.retain(t, observation)
	works, _ := f.db.WorkItems(context.Background())
	if len(works) != 0 {
		t.Fatal("observation created execution")
	}
	var wg sync.WaitGroup
	errors := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			_, err := f.service.Admit(context.Background(), ticketexecution.AdmissionRequest{ProjectID: f.project, BindingRevision: 2, ObservationID: observation.ID}, fmt.Sprintf("concurrent-key-%d", index))
			errors <- err
		}(i)
	}
	wg.Wait()
	close(errors)
	for err := range errors {
		if err != nil {
			t.Fatal(err)
		}
	}
	works, _ = f.db.WorkItems(context.Background())
	if len(works) != 1 {
		t.Fatalf("duplicate intake: %d", len(works))
	}
	var native, events int
	if err := f.db.SQL().QueryRow(`SELECT (SELECT count(*) FROM native_tickets),(SELECT count(*) FROM events WHERE kind='work.created')`).Scan(&native, &events); err != nil || native != 0 || events != 1 {
		t.Fatalf("shadow native or duplicate work event: %d %d %v", native, events, err)
	}
	if err := f.db.Close(); err != nil {
		t.Fatal(err)
	}
	var err error
	f.db, err = sqlite.Open(context.Background(), f.path, sqlite.Options{})
	if err != nil {
		t.Fatal(err)
	}
	f.service, err = ticketexecution.New(f.db, f, ticketexecution.Options{Now: func() time.Time {
		return f.now
	}})
	if err != nil {
		t.Fatal(err)
	}
	if err := f.db.RebuildProjections(context.Background()); err != nil {
		t.Fatal(err)
	}
	replay := f.admit(t, observation, "concurrent-key-0")
	if replay.Work.WorkItemID != works[0].WorkItemID {
		t.Fatal("restart changed local identity")
	}
	var count int
	if err := f.db.SQL().QueryRow(`SELECT count(*) FROM native_tickets`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("replay fabricated native source: %d %v", count, err)
	}
}

func TestPinnedSourceChangesAccessLossApprovalAndRebind(t *testing.T) {
	f := setup(t)
	first := f.observation(t, "issue", "1", "Original")
	f.retain(t, first)
	admitted := f.admit(t, first, "initial-admission")
	work := admitted.Work.WorkItemID
	f.now = f.now.Add(time.Second)
	changed := f.observation(t, "issue", "2", "Changed current source")
	f.retain(t, changed)
	view, err := f.service.Inspect(context.Background(), work)
	if err != nil || view.Assessment.State != ticketexecution.SourceActionRequired || view.Work.Title != "Original" || view.Current.Title != "Changed current source" {
		t.Fatalf("source overwritten execution or missed drift: %#v %v", view, err)
	}
	if _, err := f.service.Approve(context.Background(), work, first.ID, "approve-old-version"); err == nil {
		t.Fatal("stale version approved")
	}
	if _, err := f.service.Approve(context.Background(), work, changed.ID, "approve-new-version"); err != nil {
		t.Fatal(err)
	}
	f.readError = &ports.Failure{Code: ports.FailurePermissionDenied, Message: "secret provider diagnostic"}
	if _, err := f.service.RefreshSource(context.Background(), work); err == nil {
		t.Fatal("permission loss appeared successful")
	}
	view, err = f.service.Inspect(context.Background(), work)
	if err != nil || view.Assessment.State != ticketexecution.SourceActionRequired || view.Current == nil {
		t.Fatalf("access loss discarded retained content: %#v %v", view, err)
	}
	if _, err := f.service.Approve(context.Background(), work, changed.ID, "approve-inaccessible"); err == nil {
		t.Fatal("inaccessible source approved")
	}
	f.readError = nil
	f.now = f.now.Add(time.Second)
	f.read = tracker.Unchanged{Ref: changed.Ref, Fresh: tracker.Fresh{ObservedAt: f.now, Revision: changed.NativeRevision}}
	if _, err := f.service.RefreshSource(context.Background(), work); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.SelectBacklogSource(context.Background(), f.project, 2, statestore.NativeBacklogSource{Namespace: tracker.Namespace{Provider: "built_in", Host: "darkstar.local", TenantID: "local", ScopeID: f.project}}, f.now); err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.RefreshSource(context.Background(), work); err != nil {
		t.Fatal(err)
	}
	view, err = f.service.Inspect(context.Background(), work)
	if err != nil || view.Lineage.BindingRevision != 2 || view.Current.Ref != changed.Ref {
		t.Fatal("project selection retargeted existing source")
	}
	if _, err := f.service.Approve(context.Background(), work, changed.ID, "pinned-after-switch"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.Admit(context.Background(), ticketexecution.AdmissionRequest{ProjectID: f.project, BindingRevision: 2, ObservationID: changed.ID}, "intake-stale-binding"); err == nil {
		t.Fatal("stale selected binding admitted")
	}
	// Original successful request remains replayable after source selection changes.
	if replay := f.admit(t, first, "initial-admission"); replay.Work.WorkItemID != work {
		t.Fatal("replay changed work")
	}
}

func TestNativeWorkFirstRefreshAndApprovalReusesIdentity(t *testing.T) {
	f := setup(t)
	work := identity.Deterministic("work_", "native-work")
	f.append(t, statestore.AggregateWork, work, "work.created", map[string]any{"projectId": f.project, "title": "Native source", "details": "Native description", "sourceHash": strings.Repeat("d", 64), "priority": 0})
	lineage, err := f.db.WorkTicketLineage(context.Background(), work)
	if err != nil || lineage.Origin != statestore.SourceNative {
		t.Fatalf("new native work bypasses source semantics: %#v %v", lineage, err)
	}
	view, err := f.service.RefreshSource(context.Background(), work)
	if err != nil || view.CurrentObservationID == "" || view.Assessment.State != ticketexecution.SourceActionRequired {
		t.Fatalf("first native exact refresh: %#v %v", view, err)
	}
	approved, err := f.service.Approve(context.Background(), work, view.CurrentObservationID, "native-source-approval")
	if err != nil || approved.Work.WorkItemID != work {
		t.Fatalf("native mapping not reused: %#v %v", approved, err)
	}
	works, _ := f.db.WorkItems(context.Background())
	if len(works) != 1 {
		t.Fatal("native approval duplicated local work")
	}
}

func TestRebindRequiresSettlementRetainsHistoryAndRejectsCompetingWork(t *testing.T) {
	f := setup(t)
	first := f.observation(t, "first", "1", "First")
	second := f.observation(t, "second", "1", "Second")
	third := f.observation(t, "third", "1", "Third")
	f.retain(t, first)
	f.retain(t, second)
	f.retain(t, third)
	work := f.admit(t, first, "rebind-first-admit").Work.WorkItemID
	f.admit(t, third, "rebind-other-admit")
	request := ticketexecution.RebindRequest{WorkID: work, ExpectedLineageRevision: 1, BindingRevision: 2, ObservationID: second.ID}
	now := f.now.Format(time.RFC3339Nano)
	if _, err := f.db.SQL().Exec(`INSERT INTO outbox(operation_id,operation_kind,aggregate_id,request_json,state,available_at,created_at,updated_at) VALUES ('old-source-operation','tracker.comment',?,'{}','prepared',?,?,?)`, work, now, now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.Rebind(context.Background(), request, "rebind-with-operation"); err == nil {
		t.Fatal("unsettled source operation allowed rebind")
	}
	if _, err := f.db.SQL().Exec(`UPDATE outbox SET state='committed',observation_json='{}' WHERE operation_id='old-source-operation'`); err != nil {
		t.Fatal(err)
	}
	result, err := f.service.Rebind(context.Background(), request, "settled-rebind-target")
	if err != nil || result.Source.Lineage.Ref != second.Ref || len(result.Source.Lineages) != 2 || result.Source.Lineages[0].Ref != first.Ref {
		t.Fatalf("rebind lost lineage: %#v %v", result, err)
	}
	if result.Work.Title != "First" {
		t.Fatal("rebind rewrote historical execution content")
	}
	if _, err := f.service.Rebind(context.Background(), request, "stale-lineage-rebind"); err == nil {
		t.Fatal("stale lineage rebind succeeded")
	}
	request.ExpectedLineageRevision = 2
	request.ObservationID = third.ID
	if _, err := f.service.Rebind(context.Background(), request, "competing-ticket-rebind"); err == nil {
		t.Fatal("competing local work acquired target")
	}
	if _, err := f.service.Admit(context.Background(), ticketexecution.AdmissionRequest{ProjectID: f.project, BindingRevision: 2, ObservationID: first.ID}, "reintake-old-lineage"); err == nil {
		t.Fatal("historical work silently retargeted after rebind")
	}
}

func TestAdmissionRejectsStaleAndMissingWhilePreservingApproval(t *testing.T) {
	f := setup(t)
	observation := f.observation(t, "issue", "1", "Original")
	f.retain(t, observation)
	work := f.admit(t, observation, "fresh-initial-intake").Work.WorkItemID
	f.now = f.now.Add(6 * time.Minute)
	if _, err := f.service.Approve(context.Background(), work, observation.ID, "stale-check-approval"); err == nil {
		t.Fatal("stale observation acquired authority")
	}
	f.now = time.Now().UTC()
	f.read = tracker.Missing{Ref: observation.Ref, ObservedAt: f.now, EvidenceRef: "exact-missing-evidence"}
	view, err := f.service.RefreshSource(context.Background(), work)
	if err != nil || view.CurrentObservationID != observation.ID || view.Approval == nil || view.Assessment.State != ticketexecution.SourceActionRequired {
		t.Fatalf("missing discarded history or approval: %#v %v", view, err)
	}
	if _, err := f.service.Approve(context.Background(), work, observation.ID, "missing-check-approval"); err == nil {
		t.Fatal("missing source acquired authority")
	}
}
