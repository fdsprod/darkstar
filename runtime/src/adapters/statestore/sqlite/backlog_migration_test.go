package sqlite

import (
	"context"
	"encoding/json"
	"path/filepath"
	"reflect"
	"testing"

	"darkstar/src/ports/statestore"
)

func TestBacklogMigrationBackfillRollbackAndProjectionRebuild(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "backlog-upgrade.db")
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
	if err := migrate(ctx, connection, migrations[:27], fixedNow); err != nil {
		t.Fatal(err)
	}
	db := &Database{sql: connection, now: fixedNow}
	project := testID("project", 'P')
	work := testID("work", 'W')
	_, err = db.Append(ctx,
		pendingEvent(testID("event", 'P'), statestore.AggregateProject, project, 0, "project.created", `{"name":"Backlog migration","sourceHash":"`+aggregateSourceHash+`"}`),
		pendingEvent(testID("event", 'W'), statestore.AggregateWork, work, 0, "work.created", `{"projectId":"`+project+`","title":"Preserved work","sourceHash":"`+aggregateSourceHash+`","priority":1}`),
	)
	if err != nil {
		t.Fatal(err)
	}
	before, err := db.EventsAfter(ctx, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	beforeJSON, _ := json.Marshal(before)
	broken := migrations[27]
	broken.SQL += "\nINSERT INTO deliberately_missing_backlog_table VALUES (1);"
	if err := applyMigration(ctx, connection, broken, fixedNow()); err == nil {
		t.Fatal("broken backlog migration unexpectedly committed")
	}
	var count int
	if err := connection.QueryRow(`SELECT count(*) FROM sqlite_schema WHERE name='backlog_bindings'`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("partial backlog schema survived rollback: %d %v", count, err)
	}
	if err := connection.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(ctx, path, Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = reopened.Close()
	}()
	binding, err := reopened.BacklogBinding(ctx, project)
	if err != nil || binding.Revision != 1 {
		t.Fatalf("missing migrated native source: %+v %v", binding, err)
	}
	if _, ok := binding.Source.(statestore.NativeBacklogSource); !ok {
		t.Fatal("existing project was not given its native source")
	}
	if err := reopened.RebuildProjections(ctx); err != nil {
		t.Fatal(err)
	}
	bindings, err := reopened.BacklogBindingHistory(ctx, project)
	if err != nil || len(bindings) != 1 {
		t.Fatalf("projection rebuild duplicated source history: %+v %v", bindings, err)
	}
	after, err := reopened.EventsAfter(ctx, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	afterJSON, _ := json.Marshal(after)
	if !reflect.DeepEqual(beforeJSON, afterJSON) {
		t.Fatal("backlog migration or rebuild changed execution history")
	}
	second := testID("project", 'Q')
	if _, err := reopened.Append(ctx, pendingEvent(testID("event", 'Q'), statestore.AggregateProject, second, 0, "project.created", `{"name":"New project","sourceHash":"`+aggregateSourceHash+`"}`)); err != nil {
		t.Fatal(err)
	}
	if binding, err := reopened.BacklogBinding(ctx, second); err != nil || binding.Revision != 1 {
		t.Fatalf("new project lacks native default: %+v %v", binding, err)
	}
}
