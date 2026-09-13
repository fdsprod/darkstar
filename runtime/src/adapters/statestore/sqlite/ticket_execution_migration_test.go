package sqlite

import (
	"context"
	"path/filepath"
	"testing"

	"darkstar/src/ports/statestore"
)

func TestTicketExecutionMigrationMarksOnlyExistingNativeWork(t *testing.T) {
	ctx := context.Background()
	connection, err := openSQLite(filepath.Join(t.TempDir(), "admission-upgrade.db"), Options{})
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
	if err := migrate(ctx, connection, migrations[:28], fixedNow); err != nil {
		t.Fatal(err)
	}
	db := &Database{sql: connection, now: fixedNow}
	project, oldWork, newWork := testID("project", 'P'), testID("work", 'W'), testID("work", 'N')
	if _, err := db.Append(ctx, pendingEvent(testID("event", 'P'), statestore.AggregateProject, project, 0, "project.created", `{"name":"Upgrade","sourceHash":"`+aggregateSourceHash+`"}`), pendingEvent(testID("event", 'W'), statestore.AggregateWork, oldWork, 0, "work.created", `{"projectId":"`+project+`","title":"Existing native","sourceHash":"`+aggregateSourceHash+`","priority":0}`)); err != nil {
		t.Fatal(err)
	}
	broken := migrations[28]
	broken.SQL += "\nINSERT INTO missing_source_admission_table VALUES (1);"
	if err := applyMigration(ctx, connection, broken, fixedNow()); err == nil {
		t.Fatal("broken source migration committed")
	}
	var count int
	if err := connection.QueryRow(`SELECT count(*) FROM sqlite_schema WHERE name='source_legacy_work'`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("source migration did not roll back: %d %v", count, err)
	}
	if err := applyMigration(ctx, connection, migrations[28], fixedNow()); err != nil {
		t.Fatal(err)
	}
	before, err := db.WorkTicketLineage(ctx, oldWork)
	if err != nil || before.Origin != statestore.SourceLegacyNative {
		t.Fatalf("old work lost legacy classification: %#v %v", before, err)
	}
	if _, err := db.Append(ctx, pendingEvent(testID("event", 'N'), statestore.AggregateWork, newWork, 0, "work.created", `{"projectId":"`+project+`","title":"New native","sourceHash":"`+aggregateSourceHash+`","priority":0}`)); err != nil {
		t.Fatal(err)
	}
	after, err := db.WorkTicketLineage(ctx, newWork)
	if err != nil || after.Origin != statestore.SourceNative {
		t.Fatalf("new work bypasses source approval: %#v %v", after, err)
	}
	if err := db.RebuildProjections(ctx); err != nil {
		t.Fatal(err)
	}
	if err := connection.QueryRow(`SELECT count(*) FROM source_legacy_work`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("projection replay changed legacy marker: %d %v", count, err)
	}
}
