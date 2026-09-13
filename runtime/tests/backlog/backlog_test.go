package backlog_test

import (
	"context"
	"errors"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"darkstar/src/adapters/statestore/sqlite"
	"darkstar/src/core/backlog"
	"darkstar/src/ports"
	"darkstar/src/ports/statestore"
	"darkstar/src/ports/tracker"
	"darkstar/src/ports/worksource"
)

type feed struct {
	now           time.Time
	pages         map[string][]tracker.Ticket
	next          map[string]string
	queries       []tracker.Query
	failPage      string
	pageFailure   error
	probeFailure  error
	readFailure   error
	readResult    tracker.ReadResult
	hook          func()
	waitForCancel bool
}

type resolver struct {
	feed *feed
}

type source struct {
	feed  *feed
	scope tracker.Scope
	pin   tracker.Pin
}

func (r resolver) Resolve(_ context.Context, binding statestore.BacklogBinding) (backlog.ResolvedSource, error) {
	scope := backlog.Scope(binding)
	installation, revision := "local", "1"
	if external, ok := binding.Source.(statestore.ExternalBacklogSource); ok {
		installation, revision = external.ConnectionID, external.ConnectionRevision
	}
	pin := tracker.Pin{AdapterConfigPin: tracker.AdapterConfigPin{ContractVersion: tracker.Version, AdapterID: scope.Namespace.Provider, AdapterVersion: "1", InstallationID: installation, AccountID: "account", BindingRevision: strconv.FormatUint(binding.Revision, 10), ConfigRevision: revision, ConfigDigest: strings.Repeat("a", 64)}, CapabilitiesDigest: strings.Repeat("b", 64)}
	bound := source{feed: r.feed, scope: scope, pin: pin}
	return backlog.ResolvedSource{Source: bound, Browser: bound, Config: pin.AdapterConfigPin}, nil
}

func (s source) Discover(_ context.Context, config tracker.AdapterConfigPin) (tracker.Manifest, error) {
	if s.feed.probeFailure != nil {
		return tracker.Manifest{}, s.feed.probeFailure
	}
	if config != s.pin.AdapterConfigPin {
		return tracker.Manifest{}, errors.New("wrong config")
	}
	return tracker.Manifest{Pin: s.pin, Scope: s.scope, MaxPageSize: 100, ObservedAt: s.feed.now, EvidenceRef: "probe:evidence", Capabilities: map[tracker.Capability]tracker.Knowledge[bool]{tracker.Fetch: tracker.Known[bool]{Value: true}, tracker.List: tracker.Known[bool]{Value: true}, tracker.Search: tracker.Known[bool]{Value: true}, tracker.Filter: tracker.Known[bool]{Value: true}, tracker.Page: tracker.Known[bool]{Value: true}, tracker.Refresh: tracker.Known[bool]{Value: true}}, Filters: map[string][]tracker.FilterOperator{"business_state": {tracker.Equals}}}, nil
}

func (s source) Browse(ctx context.Context, request worksource.BrowseTicketsRequest) (tracker.TicketPage, error) {
	s.feed.queries = append(s.feed.queries, request.Query)
	if s.feed.hook != nil {
		hook := s.feed.hook
		s.feed.hook = nil
		hook()
	}
	if s.feed.waitForCancel {
		<-ctx.Done()
		return tracker.TicketPage{}, ctx.Err()
	}
	if s.feed.pageFailure != nil && request.Query.Cursor == s.feed.failPage {
		return tracker.TicketPage{}, s.feed.pageFailure
	}
	page := tracker.TicketPage{Tickets: []tracker.Ticket{}, Next: tracker.End{}, Freshness: tracker.Fresh{ObservedAt: s.feed.now, Revision: "page"}}
	for _, ticket := range s.feed.pages[request.Query.Cursor] {
		if request.Query.Text != "" && !strings.Contains(strings.ToLower(ticket.Title), strings.ToLower(request.Query.Text)) {
			continue
		}
		ticket.Freshness = tracker.Fresh{ObservedAt: s.feed.now, Revision: ticket.Revision}
		ticket.EvidenceRef = "evidence:" + s.feed.now.Format(time.RFC3339Nano)
		page.Tickets = append(page.Tickets, ticket)
	}
	if next := s.feed.next[request.Query.Cursor]; next != "" {
		page.Next = tracker.More{Cursor: next}
	}
	return page, nil
}

func (s source) Read(_ context.Context, request worksource.ReadTicketRequest) (tracker.ReadResult, error) {
	if s.feed.readFailure != nil {
		return nil, s.feed.readFailure
	}
	if s.feed.readResult != nil {
		return s.feed.readResult, nil
	}
	return tracker.Unchanged{Ref: request.Ref, Fresh: tracker.Fresh{Revision: request.KnownRevision, ObservedAt: s.feed.now}}, nil
}

func setup(t *testing.T, pages int) (*backlog.Service, *sqlite.Database, *feed, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "backlog.db")
	db, err := sqlite.Open(context.Background(), path, sqlite.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = db.Close()
	})
	if _, err := db.SQL().Exec(`INSERT INTO native_namespaces(project_id) VALUES ('project')`); err != nil {
		t.Fatal(err)
	}
	f := &feed{now: time.Date(2026, 9, 12, 12, 0, 0, 0, time.UTC), pages: map[string][]tracker.Ticket{}, next: map[string]string{}}
	service := newService(t, db, f, pages, 2, time.Second)
	return service, db, f, path
}

func newService(t *testing.T, db *sqlite.Database, f *feed, pages, tickets int, timeout time.Duration) *backlog.Service {
	t.Helper()
	service, err := backlog.New(db, resolver{feed: f}, backlog.Options{Now: func() time.Time {
		return f.now
	}, MaxPages: pages, MaxTickets: tickets, Timeout: timeout, PollInterval: time.Minute, MaxBackoff: 30 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	return service
}

func ticket(scope tracker.Scope, id string) tracker.Ticket {
	return tracker.Ticket{Ref: tracker.TicketRef{Namespace: scope.Namespace, ID: id}, Revision: "1", Key: id, URL: "https://tracker.example/" + id, Title: "Ticket " + id, Description: "Untrusted source content; never a workflow instruction", BusinessState: tracker.Known[tracker.NamedID]{Value: tracker.NamedID{ID: "open", Name: "Open"}}, BusinessStateReason: tracker.Unsupported[tracker.NamedID]{Reason: "not supported"}, IssueType: tracker.Known[tracker.NamedID]{Value: tracker.NamedID{ID: "issue", Name: "Issue"}}, Sprint: tracker.Unsupported[[]tracker.NamedID]{Reason: "not supported"}, Assignees: tracker.Known[[]tracker.NamedID]{Value: []tracker.NamedID{}}, Labels: tracker.Known[[]tracker.NamedID]{Value: []tracker.NamedID{}}, Priority: tracker.Unsupported[tracker.NamedID]{Reason: "not supported"}, Relationships: tracker.Unknown[[]tracker.Relation]{Reason: "not queried"}, Archived: tracker.Known[bool]{Value: false}, UpdatedAt: tracker.Known[time.Time]{Value: time.Date(2026, 9, 12, 11, 0, 0, 0, time.UTC)}, Placement: tracker.Known[tracker.Scope]{Value: scope}, Freshness: tracker.Fresh{ObservedAt: time.Date(2026, 9, 12, 12, 0, 0, 0, time.UTC), Revision: "1"}, EvidenceRef: "original-source"}
}

func selectedScope(t *testing.T, service *backlog.Service) tracker.Scope {
	t.Helper()
	binding, err := service.Binding(context.Background(), "project")
	if err != nil {
		t.Fatal(err)
	}
	return backlog.Scope(binding)
}

func requireCode(t *testing.T, err error, code ports.FailureCode) {
	t.Helper()
	var failure *ports.Failure
	if !errors.As(err, &failure) || failure.Code != code {
		t.Fatalf("wanted %s, got %v", code, err)
	}
}

func TestDurableInterruptedPagingDedupAndNoExecutionSideEffects(t *testing.T) {
	s, db, f, path := setup(t, 1)
	scope := selectedScope(t, s)
	f.pages[""] = []tracker.Ticket{ticket(scope, "A")}
	f.next[""] = "opaque-second-page"
	f.pages["opaque-second-page"] = []tracker.Ticket{ticket(scope, "A"), ticket(scope, "B")}
	initial, err := s.View(context.Background(), "project", backlog.ViewRequest{})
	if err != nil || initial.Refresh != nil || len(initial.Tickets) != 0 || len(f.queries) != 0 {
		t.Fatalf("cache loading contacted source or created content: %+v %v", initial, err)
	}
	first, err := s.Refresh(context.Background(), "project", 1, tracker.Query{PageSize: 2})
	if err != nil || first.Complete || first.Refresh.Cursor != "opaque-second-page" {
		t.Fatalf("bounded page: %+v %v", first, err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := sqlite.Open(context.Background(), path, sqlite.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = reopened.Close()
	}()
	s = newService(t, reopened, f, 1, 2, time.Second)
	f.now = f.now.Add(time.Second)
	second, err := s.Poll(context.Background(), "project")
	if err != nil || !second.Complete || second.Refresh.LastSuccessAt.IsZero() || f.queries[1].Cursor != "opaque-second-page" {
		t.Fatalf("restart did not resume durable cursor: %+v %v", second, err)
	}
	var observations int
	if err := reopened.SQL().QueryRow(`SELECT count(*) FROM backlog_observations`).Scan(&observations); err != nil || observations != 2 {
		t.Fatalf("duplicate observation retained twice: %d %v", observations, err)
	}
	view, err := s.View(context.Background(), "project", backlog.ViewRequest{})
	if err != nil || len(view.Tickets) != 2 {
		t.Fatalf("cache: %+v %v", view, err)
	}
	for _, entry := range view.Tickets {
		if entry.Status != backlog.Fresh {
			t.Fatalf("observed ticket is not fresh: %+v", entry)
		}
	}
	for _, table := range []string{"work_item_projection", "run_projection", "events", "queue_entries"} {
		var count int
		if err := reopened.SQL().QueryRow("SELECT count(*) FROM " + table).Scan(&count); err != nil || count != 0 {
			t.Fatalf("backlog mutated %s: %d %v", table, count, err)
		}
	}
}

func TestFilterChangesRetainTicketsAndNeverInferMissing(t *testing.T) {
	s, _, f, _ := setup(t, 1)
	scope := selectedScope(t, s)
	f.pages[""] = []tracker.Ticket{ticket(scope, "A"), ticket(scope, "B")}
	first, err := s.Refresh(context.Background(), "project", 1, tracker.Query{PageSize: 2})
	if err != nil {
		t.Fatal(err)
	}
	f.now = f.now.Add(time.Second)
	filtered, err := s.Refresh(context.Background(), "project", 1, tracker.Query{Text: "does-not-match", PageSize: 2})
	if err != nil || !filtered.Complete || filtered.Refresh.Generation <= first.Refresh.Generation {
		t.Fatalf("new filter did not reset scan: %+v %v", filtered, err)
	}
	view, err := s.View(context.Background(), "project", backlog.ViewRequest{})
	if err != nil || len(view.Tickets) != 2 {
		t.Fatalf("filter discarded retained context: %+v %v", view, err)
	}
	for _, entry := range view.Tickets {
		if entry.Status == backlog.Missing || entry.CurrentQueryMatch {
			t.Fatalf("filter absence was misrepresented: %+v", entry)
		}
	}
	_, err = s.Refresh(context.Background(), "project", 1, tracker.Query{PageSize: 2, Predicates: []tracker.Predicate{{FieldID: "unknown", Operator: tracker.Equals, Values: []string{"x"}}}})
	requireCode(t, err, ports.FailureUnsupported)
}

func TestExactMissingInaccessibleReconnectArchiveAndPlacement(t *testing.T) {
	s, _, f, _ := setup(t, 1)
	scope := selectedScope(t, s)
	value := ticket(scope, "A")
	f.pages[""] = []tracker.Ticket{value}
	if _, err := s.Refresh(context.Background(), "project", 1, tracker.Query{PageSize: 2}); err != nil {
		t.Fatal(err)
	}
	f.readResult = tracker.Missing{Ref: value.Ref, ObservedAt: f.now, EvidenceRef: "exact-missing-proof"}
	if _, err := s.ReadRefresh(context.Background(), "project", 1, value.Ref); err != nil {
		t.Fatal(err)
	}
	view, err := s.View(context.Background(), "project", backlog.ViewRequest{})
	if err != nil || view.Tickets[0].Status != backlog.Missing || view.Tickets[0].Ticket.Title != value.Title {
		t.Fatalf("missing lost retained content: %+v %v", view, err)
	}
	f.readResult = nil
	f.readFailure = &ports.Failure{Code: ports.FailurePermissionDenied, Message: "secret-token-provider-body"}
	_, err = s.ReadRefresh(context.Background(), "project", 1, value.Ref)
	requireCode(t, err, ports.FailurePermissionDenied)
	view, err = s.View(context.Background(), "project", backlog.ViewRequest{})
	if err != nil || view.Tickets[0].Status != backlog.Inaccessible || strings.Contains(view.Refresh.Failure.Message, "secret-token") {
		t.Fatalf("permission loss: %+v %v", view, err)
	}
	f.now = f.now.Add(time.Minute)
	f.readFailure = nil
	value.Revision = "2"
	value.Archived = tracker.Known[bool]{Value: true}
	value.Freshness = tracker.Fresh{ObservedAt: f.now, Revision: "2"}
	f.readResult = tracker.Found{Ticket: value}
	if _, err := s.ReadRefresh(context.Background(), "project", 1, value.Ref); err != nil {
		t.Fatal(err)
	}
	view, err = s.View(context.Background(), "project", backlog.ViewRequest{})
	if err != nil || view.Tickets[0].Status != backlog.Archived {
		t.Fatalf("reconnect/archive: %+v %v", view, err)
	}
	value.Revision = "3"
	value.Freshness = tracker.Fresh{ObservedAt: f.now, Revision: "3"}
	moved := scope
	moved.ContainerID = "different-team"
	value.Placement = tracker.Known[tracker.Scope]{Value: moved}
	f.readResult = tracker.Found{Ticket: value}
	if _, err := s.ReadRefresh(context.Background(), "project", 1, value.Ref); err != nil {
		t.Fatal(err)
	}
	view, err = s.View(context.Background(), "project", backlog.ViewRequest{})
	if err != nil || view.Tickets[0].Status != backlog.OutOfScope {
		t.Fatalf("scope move misrepresented: %+v %v", view, err)
	}
}

func TestSourceSwitchCASHistoryAndInFlightRefresh(t *testing.T) {
	s, db, f, _ := setup(t, 1)
	native := selectedScope(t, s)
	f.pages[""] = []tracker.Ticket{ticket(native, "A")}
	if _, err := s.Refresh(context.Background(), "project", 1, tracker.Query{PageSize: 2}); err != nil {
		t.Fatal(err)
	}
	external := statestore.ExternalBacklogSource{ConnectionID: "connection", ConnectionRevision: "immutable-revision", Scope: tracker.Scope{Namespace: tracker.Namespace{Provider: "linear", Host: "linear.app", TenantID: "workspace", ScopeID: "workspace"}, ContainerID: "team"}}
	binding, err := s.SelectSource(context.Background(), "project", 1, external)
	if err != nil || binding.Revision != 2 {
		t.Fatalf("source select: %+v %v", binding, err)
	}
	_, err = s.SelectSource(context.Background(), "project", 1, external)
	requireCode(t, err, ports.FailureConflict)
	view, err := s.View(context.Background(), "project", backlog.ViewRequest{IncludePrevious: true})
	if err != nil || len(view.Tickets) != 1 || view.Tickets[0].Status != backlog.OutOfScope {
		t.Fatalf("switch lost old source: %+v %v", view, err)
	}
	f.probeFailure = &ports.Failure{Code: ports.FailureUnauthenticated, Message: "credentials unavailable"}
	_, err = s.Refresh(context.Background(), "project", 2, tracker.Query{PageSize: 2})
	requireCode(t, err, ports.FailureUnauthenticated)
	current, _ := s.Binding(context.Background(), "project")
	if _, ok := current.Source.(statestore.ExternalBacklogSource); !ok {
		t.Fatal("external failure silently fell back to native")
	}
	f.probeFailure = nil
	f.now = f.now.Add(time.Minute)
	f.pages[""] = []tracker.Ticket{ticket(external.Scope, "external-A")}
	f.hook = func() {
		if _, err := s.SelectSource(context.Background(), "project", 2, statestore.NativeBacklogSource{Namespace: native.Namespace}); err != nil {
			t.Fatal(err)
		}
	}
	_, err = s.Refresh(context.Background(), "project", 2, tracker.Query{PageSize: 2})
	requireCode(t, err, ports.FailureConflict)
	var count int
	if err := db.SQL().QueryRow(`SELECT count(*) FROM backlog_cached_tickets WHERE binding_revision=2`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("in-flight old source page committed: %d %v", count, err)
	}
	history, err := s.History(context.Background(), "project")
	if err != nil || len(history) != 3 {
		t.Fatalf("binding history lost: %+v %v", history, err)
	}
}

func TestRateLimitScheduleResumesCursorAndHonorsProviderMinimum(t *testing.T) {
	s, _, f, _ := setup(t, 2)
	scope := selectedScope(t, s)
	f.pages[""] = []tracker.Ticket{ticket(scope, "A")}
	f.next[""] = "second"
	f.pages["second"] = []tracker.Ticket{ticket(scope, "B")}
	f.failPage = "second"
	f.pageFailure = &ports.Failure{Code: ports.FailureResourceExhausted, Retryable: true, Details: map[string]string{"retry_after_seconds": "120", "raw": "secret-token"}}
	result, err := s.Refresh(context.Background(), "project", 1, tracker.Query{PageSize: 1})
	requireCode(t, err, ports.FailureResourceExhausted)
	if result.Refresh.Cursor != "second" || result.Refresh.NextAttemptAt.Before(f.now.Add(120*time.Second)) {
		t.Fatalf("lost cursor or retried before provider allows: %+v", result)
	}
	before := len(f.queries)
	if _, err := s.Poll(context.Background(), "project"); err != nil || len(f.queries) != before {
		t.Fatal("poll ignored durable rate-limit schedule")
	}
	f.now = f.now.Add(121 * time.Second)
	f.pageFailure = nil
	result, err = s.Poll(context.Background(), "project")
	if err != nil || !result.Complete || f.queries[len(f.queries)-1].Cursor != "second" {
		t.Fatalf("rate-limit recovery did not resume: %+v %v", result, err)
	}
}

func TestRefreshTimeBoundAndConflictingDuplicateCannotAdvance(t *testing.T) {
	s, db, f, _ := setup(t, 1)
	scope := selectedScope(t, s)
	a := ticket(scope, "A")
	b := a
	b.Title = "different content under same revision"
	f.pages[""] = []tracker.Ticket{a, b}
	_, err := s.Refresh(context.Background(), "project", 1, tracker.Query{PageSize: 2})
	requireCode(t, err, ports.FailureProtocolDrift)
	var count int
	if err := db.SQL().QueryRow(`SELECT count(*) FROM backlog_observations`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("conflicting page partially committed: %d %v", count, err)
	}
	f.now = f.now.Add(time.Minute)
	f.waitForCancel = true
	s = newService(t, db, f, 1, 2, 20*time.Millisecond)
	started := time.Now()
	_, err = s.Refresh(context.Background(), "project", 1, tracker.Query{PageSize: 2})
	requireCode(t, err, ports.FailureTimeout)
	if time.Since(started) > time.Second {
		t.Fatal("refresh did not enforce total context deadline")
	}
}

func TestRefreshDeduplicatesSameRevisionAcrossSweepsAndCachesBecomeStale(t *testing.T) {
	s, db, f, _ := setup(t, 1)
	scope := selectedScope(t, s)
	f.pages[""] = []tracker.Ticket{ticket(scope, "A")}
	for i := 0; i < 2; i++ {
		if _, err := s.Refresh(context.Background(), "project", 1, tracker.Query{PageSize: 2}); err != nil {
			t.Fatal(err)
		}
		f.now = f.now.Add(time.Second)
	}
	var count int
	if err := db.SQL().QueryRow(`SELECT count(*) FROM backlog_observations`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("refresh duplicated immutable content: %d %v", count, err)
	}
	f.now = f.now.Add(3 * time.Minute)
	view, err := s.View(context.Background(), "project", backlog.ViewRequest{})
	if err != nil || view.Tickets[0].Status != backlog.Cached {
		t.Fatalf("old cache presented as live: %+v %v", view, err)
	}
	if view.Refresh.LastSuccessAt.IsZero() {
		t.Fatal("last successful refresh time was not retained")
	}
}

func TestConflictingPersistedRevisionRecordsFailureWithoutAdvancingPage(t *testing.T) {
	s, db, f, _ := setup(t, 1)
	scope := selectedScope(t, s)
	f.pages[""] = []tracker.Ticket{ticket(scope, "A")}
	if _, err := s.Refresh(context.Background(), "project", 1, tracker.Query{PageSize: 2}); err != nil {
		t.Fatal(err)
	}
	f.pages[""][0].Title = "changed without changing immutable revision"
	result, err := s.Refresh(context.Background(), "project", 1, tracker.Query{PageSize: 2})
	requireCode(t, err, ports.FailureProtocolDrift)
	if result.Complete || result.Refresh.Phase != statestore.BacklogFailed {
		t.Fatalf("rejected final page claimed success: %+v", result)
	}
	state, err := db.BacklogRefresh(context.Background(), "project", 1)
	if err != nil || state.Phase != statestore.BacklogFailed || state.NextAttemptAt.IsZero() || state.Cursor != "" {
		t.Fatalf("rejected page did not retain durable failure: %+v %v", state, err)
	}
}
