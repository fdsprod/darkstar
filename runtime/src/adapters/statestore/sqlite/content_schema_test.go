package sqlite

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestContentLibrarySchemaSnapshotMatchesMigratedColumns(t *testing.T) {
	ctx := context.Background()
	actual := openEventTestDatabase(t)
	snapshot, err := openSQLite(filepath.Join(t.TempDir(), "snapshot.db"), Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = snapshot.Close()
	}()
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "..", "..", "schemas", "sqlite-v1alpha1.sql"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := snapshot.ExecContext(ctx, string(raw)); err != nil {
		t.Fatalf("load published SQLite snapshot: %v", err)
	}
	columns := func(database *sql.DB, table string) []string {
		t.Helper()
		rows, err := database.QueryContext(ctx, `SELECT name,type,"notnull",coalesce(dflt_value,''),pk FROM pragma_table_info(?) ORDER BY cid`, table)
		if err != nil {
			t.Fatal(err)
		}
		defer func() {
			_ = rows.Close()
		}()
		var result []string
		for rows.Next() {
			var name, kind, required, defaultValue, key string
			if err := rows.Scan(&name, &kind, &required, &defaultValue, &key); err != nil {
				t.Fatal(err)
			}
			result = append(result, name+"|"+kind+"|"+required+"|"+defaultValue+"|"+key)
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		return result
	}
	for _, table := range []string{"content_library_items", "content_library_versions", "content_library_events", "run_execution_contexts", "artifact_representations"} {
		got, want := columns(snapshot, table), columns(actual.SQL(), table)
		if len(got) == 0 || !reflect.DeepEqual(got, want) {
			t.Fatalf("%s snapshot columns = %#v; migrated = %#v", table, got, want)
		}
	}
}
