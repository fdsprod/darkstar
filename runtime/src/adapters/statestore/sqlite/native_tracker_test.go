package sqlite

import (
	"context"
	"encoding/json"
	"path/filepath"
	"reflect"
	"testing"

	"darkstar/src/ports/statestore"
)

func TestNativeMigrationRollbackBackfillRestartAndImmutableLegacyEvents(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "native-upgrade.db")
	connection, err := openSQLite(path, Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = connection.Close()
	}()
	migrations, err := embeddedMigrationSet()
	if err != nil {
		t.Fatal(err)
	}
	if err := migrate(ctx, connection, migrations[:26], fixedNow); err != nil {
		t.Fatal(err)
	}
	db := &Database{sql: connection, now: fixedNow}
	projectID := testID("project", 'P')
	workID := testID("work", 'W')
	importID := testID("work", 'J')
	events := []statestore.PendingEvent{
		pendingEvent(testID("event", 'P'), statestore.AggregateProject, projectID, 0, "project.created", `{"name":"Retained project","sourceHash":"`+aggregateSourceHash+`"}`),
		pendingEvent(testID("event", 'W'), statestore.AggregateWork, workID, 0, "work.created", `{"projectId":"`+projectID+`","title":"Retained title","details":"Retained details","evidence":["artifact:original"],"sourceHash":"`+aggregateSourceHash+`","priority":7}`),
		pendingEvent(testID("event", 'X'), statestore.AggregateWork, workID, 1, "work.completed", `{}`),
		pendingEvent(testID("event", 'J'), statestore.AggregateWork, importID, 0, "work.created", `{"projectId":"`+projectID+`","title":"External import","sourceHash":"`+aggregateSourceHash+`","priority":1}`),
	}
	events[3].CommandID = "legacy-import-command"
	if _, _, err := db.BeginCommand(ctx, statestore.BeginCommandRequest{Scope: "work.import", IdempotencyKey: events[3].CommandID, RequestDigest: aggregateSourceHash, CreatedAt: fixedNow()}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Append(ctx, events...); err != nil {
		t.Fatal(err)
	}
	before, err := db.EventsAfter(ctx, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	beforeJSON, _ := json.Marshal(before)
	// A crash/failure after the backfill statements leaves no partial schema,
	// mapping, ticket, or migration marker because the runner is transactional.
	broken := migrations[26]
	broken.SQL += "\nINSERT INTO deliberately_missing_table VALUES (1);"
	if err := applyMigration(ctx, connection, broken, fixedNow()); err == nil {
		t.Fatal("expected interrupted migration rollback")
	}
	var count int
	if err := connection.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_schema WHERE name='native_tickets'`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("partial native schema survived rollback: %d %v", count, err)
	}
	if err := connection.Close(); err != nil {
		t.Fatal(err)
	}
	for attempt := 0; attempt < 2; attempt++ {
		reopened, err := Open(ctx, path, Options{})
		if err != nil {
			t.Fatal(err)
		}
		ticket, err := reopened.NativeTicket(ctx, projectID, workID)
		if err != nil || ticket.ID != workID || ticket.State != statestore.NativeCompleted || ticket.Title != "Retained title" || ticket.Description != "Retained details" || ticket.Priority != 7 || ticket.Revision != 1 {
			t.Fatalf("migration changed native data: %#v %v", ticket, err)
		}
		history, err := reopened.NativeTicketHistory(ctx, projectID, workID)
		if err != nil || len(history) != 1 || history[0].Kind != "legacy_migration" || history[0].EvidenceRef != "legacy-business-state/v1" {
			t.Fatalf("migration observation missing or duplicated: %#v %v", history, err)
		}
		var kind string
		if err := reopened.sql.QueryRowContext(ctx, `SELECT mapping_kind FROM native_work_mappings WHERE work_id=?`, importID).Scan(&kind); err != nil || kind != "legacy_unresolved" {
			t.Fatalf("legacy import mapped incorrectly: %s %v", kind, err)
		}
		after, err := reopened.EventsAfter(ctx, 0, 100)
		if err != nil {
			t.Fatal(err)
		}
		afterJSON, _ := json.Marshal(after)
		if !reflect.DeepEqual(beforeJSON, afterJSON) {
			t.Fatal("native migration rewrote original event history")
		}
		if err := reopened.RebuildProjections(ctx); err != nil {
			t.Fatal(err)
		}
		if err := reopened.Close(); err != nil {
			t.Fatal(err)
		}
	}
}
