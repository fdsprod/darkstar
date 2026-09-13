package sqlite

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"darkstar/src/adapters/tracker/builtin"
	"darkstar/src/core/trackercontract"
	"darkstar/src/ports/statestore"
	"darkstar/src/ports/tracker"
	"darkstar/src/ports/worksource"
)

func backlogValidationFixture(t *testing.T) (*Database, statestore.BacklogRefreshState, statestore.BacklogObservation) {
	t.Helper()
	database := openEventTestDatabase(t)
	ctx := context.Background()
	project := testID("project", 'A')
	work := testID("work", 'B')
	if _, err := database.Append(ctx,
		pendingEvent(testID("event", 'A'), statestore.AggregateProject, project, 0, "project.created", `{"name":"Backlog validation","sourceHash":"`+aggregateSourceHash+`"}`),
		pendingEvent(testID("event", 'B'), statestore.AggregateWork, work, 0, "work.created", `{"projectId":"`+project+`","title":"Retained ticket","sourceHash":"`+aggregateSourceHash+`","priority":1}`),
	); err != nil {
		t.Fatal(err)
	}
	adapter, err := builtin.New(database, project)
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := adapter.Discover(ctx, adapter.ConfigPin())
	if err != nil {
		t.Fatal(err)
	}
	read, err := adapter.Read(ctx, worksource.ReadTicketRequest{Pin: manifest.Pin, Ref: tracker.TicketRef{Namespace: manifest.Scope.Namespace, ID: work}})
	if err != nil {
		t.Fatal(err)
	}
	ticket := read.(tracker.Found).Ticket
	observation := validationObservation(t, ticket)
	now := time.Now().UTC()
	state := statestore.BacklogRefreshState{ProjectID: project, BindingRevision: 1, Revision: 1, Generation: 1, Phase: statestore.BacklogRefreshing, Query: tracker.Query{PageSize: 50}, Pin: manifest.Pin, StartedAt: now, UpdatedAt: now}
	return database, state, observation
}

func validationObservation(t *testing.T, ticket tracker.Ticket) statestore.BacklogObservation {
	t.Helper()
	encoded, err := trackercontract.EncodeTicket(ticket)
	if err != nil {
		t.Fatal(err)
	}
	id, key, digest, err := trackercontract.ObservationIdentity(ticket)
	if err != nil {
		t.Fatal(err)
	}
	return statestore.BacklogObservation{ID: id, TicketKey: key, NativeRevision: ticket.Revision, ContentDigest: digest, Ref: ticket.Ref, Ticket: encoded, ObservedAt: ticket.Freshness.(tracker.Fresh).ObservedAt, EvidenceRef: ticket.EvidenceRef}
}

func TestBacklogCommitValidatesObservationAndRollsBackCheckpointAtomically(t *testing.T) {
	database, state, observation := backlogValidationFixture(t)
	ctx := context.Background()
	bad := observation
	bad.ContentDigest = strings.Repeat("f", 64)
	if err := database.CommitBacklogRefresh(ctx, statestore.BacklogCommit{State: state, Observations: []statestore.BacklogObservation{bad}}); err == nil {
		t.Fatal("accepted caller-supplied digest that did not match retained ticket content")
	}
	var count int
	if err := database.sql.QueryRowContext(ctx, `SELECT (SELECT count(*) FROM backlog_observations)+(SELECT count(*) FROM backlog_refreshes)`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("invalid page advanced persistent state: %d %v", count, err)
	}
	if err := database.CommitBacklogRefresh(ctx, statestore.BacklogCommit{State: state, Observations: []statestore.BacklogObservation{observation}}); err != nil {
		t.Fatal(err)
	}
	ticket, err := trackercontract.DecodeTicket(observation.Ticket)
	if err != nil {
		t.Fatal(err)
	}
	ticket.Ref.Namespace.ScopeID = "another-native-project"
	other := validationObservation(t, ticket)
	state.Revision = 2
	if err := database.CommitBacklogRefresh(ctx, statestore.BacklogCommit{State: state, ExpectedRevision: 1, Observations: []statestore.BacklogObservation{other}}); err == nil {
		t.Fatal("accepted cross-source observation")
	}
	checkpoint, err := database.BacklogRefresh(ctx, state.ProjectID, 1)
	if err != nil || checkpoint.Revision != 1 {
		t.Fatalf("failed source page advanced checkpoint: %#v %v", checkpoint, err)
	}
	if _, err := database.SelectBacklogSource(ctx, state.ProjectID, 1, statestore.NativeBacklogSource{Namespace: observation.Ref.Namespace}, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if err := database.CommitBacklogRefresh(ctx, statestore.BacklogCommit{State: state, ExpectedRevision: 1}); err == nil {
		t.Fatal("old in-flight source page committed after source selection changed")
	}
}

func TestBacklogCheckpointReadRejectsUnsupportedAndContradictoryState(t *testing.T) {
	database, state, _ := backlogValidationFixture(t)
	ctx := context.Background()
	if err := database.CommitBacklogRefresh(ctx, statestore.BacklogCommit{State: state}); err != nil {
		t.Fatal(err)
	}
	for _, change := range []func(*backlogCheckpointJSON){
		func(value *backlogCheckpointJSON) {
			value.Version = "future-version"
		},
		func(value *backlogCheckpointJSON) {
			value.State.ProjectID = "unrelated-project"
		},
		func(value *backlogCheckpointJSON) {
			value.State.Revision = 9
		},
		func(value *backlogCheckpointJSON) {
			value.State.Phase = "unknown"
		},
		func(value *backlogCheckpointJSON) {
			value.State.Pin.AccountID = ""
		},
		func(value *backlogCheckpointJSON) {
			value.State.Query.Cursor = "duplicate-cursor-owner"
		},
	} {
		envelope := backlogCheckpointJSON{Version: "darkstar.backlog-checkpoint/v1", State: state}
		change(&envelope)
		encoded, err := json.Marshal(envelope)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := database.sql.ExecContext(ctx, `UPDATE backlog_refreshes SET state_json=? WHERE project_id=? AND binding_revision=1`, string(encoded), state.ProjectID); err != nil {
			t.Fatal(err)
		}
		if _, err := database.BacklogRefresh(ctx, state.ProjectID, 1); err == nil {
			t.Fatal("accepted unsupported or contradictory persisted checkpoint")
		}
	}
	source, err := encodeBacklogSource(state.ProjectID, statestore.NativeBacklogSource{Namespace: tracker.Namespace{Provider: "built_in", Host: "darkstar.local", TenantID: "local", ScopeID: state.ProjectID}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := decodeBacklogSource(state.ProjectID, append(source, []byte(` {}`)...)); err == nil {
		t.Fatal("source decoder ignored trailing JSON")
	}
	duplicate := strings.Replace(string(source), `"version":1`, `"version":2,"Version":1`, 1)
	if _, err := decodeBacklogSource(state.ProjectID, []byte(duplicate)); err == nil {
		t.Fatal("source decoder accepted contradictory duplicate version fields")
	}
}
