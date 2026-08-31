package sqlite

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const testMigrationTable = `
CREATE TABLE schema_migrations (
  version INTEGER PRIMARY KEY CHECK (version > 0),
  name TEXT NOT NULL UNIQUE,
  checksum TEXT NOT NULL CHECK (length(checksum) = 64),
  applied_at TEXT NOT NULL
) STRICT;
`

var testTime = time.Date(2026, time.August, 31, 12, 34, 56, 0, time.UTC)

func TestOpenCreatesAndRecordsFreshSchema(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	database, err := Open(ctx, filepath.Join(t.TempDir(), "darkstar.db"), Options{})
	if err != nil {
		t.Fatalf("open fresh database: %v", err)
	}
	defer database.Close()

	version, err := database.Version(ctx)
	if err != nil {
		t.Fatalf("read schema version: %v", err)
	}
	if version != 1 {
		t.Fatalf("schema version = %d, want 1", version)
	}
	history, err := database.AppliedMigrations(ctx)
	if err != nil {
		t.Fatalf("read migration history: %v", err)
	}
	if len(history) != 1 || history[0].Version != 1 || history[0].Name != "initial" || len(history[0].Checksum) != 64 {
		t.Fatalf("migration history = %#v, want one checksummed initial migration", history)
	}
	if _, err := time.Parse(time.RFC3339Nano, history[0].AppliedAt); err != nil {
		t.Errorf("migration applied_at %q is not RFC 3339: %v", history[0].AppliedAt, err)
	}

	wantTables := []string{
		"aggregates", "approval_projection", "commands", "events", "external_refs",
		"global_positions", "outbox", "projection_checkpoints", "run_projection", "schema_migrations",
	}
	for _, table := range wantTables {
		var count int
		if err := database.SQL().QueryRowContext(ctx,
			`SELECT count(*) FROM sqlite_schema WHERE type = 'table' AND name = ?`, table).Scan(&count); err != nil {
			t.Fatalf("inspect table %s: %v", table, err)
		}
		if count != 1 {
			t.Errorf("table %s count = %d, want 1", table, count)
		}
	}

	var migrationCount, initialPosition int
	if err := database.SQL().QueryRowContext(ctx, `SELECT count(*) FROM schema_migrations`).Scan(&migrationCount); err != nil {
		t.Fatalf("count migrations: %v", err)
	}
	if migrationCount != 1 {
		t.Errorf("migration count = %d, want 1", migrationCount)
	}
	if err := database.SQL().QueryRowContext(ctx,
		`SELECT last_position FROM global_positions WHERE singleton = 1`).Scan(&initialPosition); err != nil {
		t.Fatalf("read global position: %v", err)
	}
	if initialPosition != 0 {
		t.Errorf("initial global position = %d, want 0", initialPosition)
	}

	assertPragma(t, database, "foreign_keys", "1")
	assertPragma(t, database, "journal_mode", "wal")
	assertPragma(t, database, "busy_timeout", "5000")
}

func TestOpenIsIdempotent(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "darkstar.db")
	first, err := Open(ctx, path, Options{})
	if err != nil {
		t.Fatalf("first open: %v", err)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("close first database: %v", err)
	}

	second, err := Open(ctx, path, Options{})
	if err != nil {
		t.Fatalf("second open: %v", err)
	}
	defer second.Close()

	var count int
	if err := second.SQL().QueryRowContext(ctx, `SELECT count(*) FROM schema_migrations`).Scan(&count); err != nil {
		t.Fatalf("count migrations: %v", err)
	}
	if count != 1 {
		t.Fatalf("migration count after reopen = %d, want 1", count)
	}
}

func TestMigrateUpgradesInOrder(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	db, err := openSQLite(filepath.Join(t.TempDir(), "upgrade.db"), Options{})
	if err != nil {
		t.Fatalf("open SQLite: %v", err)
	}
	defer db.Close()

	first := testMigration(1, "initial", testMigrationTable+`CREATE TABLE settings (id INTEGER PRIMARY KEY) STRICT;`)
	second := testMigration(2, "add_value", `ALTER TABLE settings ADD COLUMN value TEXT;`)
	if err := migrate(ctx, db, []migration{first}, fixedNow); err != nil {
		t.Fatalf("apply initial migration: %v", err)
	}
	if err := migrate(ctx, db, []migration{second, first}, fixedNow); err != nil {
		t.Fatalf("apply upgrade migration: %v", err)
	}

	var count int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM schema_migrations`).Scan(&count); err != nil {
		t.Fatalf("count migrations: %v", err)
	}
	if count != 2 {
		t.Fatalf("migration count = %d, want 2", count)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO settings(id, value) VALUES (1, 'ready')`); err != nil {
		t.Fatalf("use upgraded column: %v", err)
	}
}

func TestFailedMigrationRollsBackAndCanBeRetried(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "recoverable.db")
	db, err := openSQLite(path, Options{})
	if err != nil {
		t.Fatalf("open SQLite: %v", err)
	}

	first := testMigration(1, "initial", testMigrationTable+`CREATE TABLE durable (id INTEGER PRIMARY KEY) STRICT;`)
	failing := testMigration(2, "broken", `CREATE TABLE must_rollback (id INTEGER PRIMARY KEY) STRICT; SELECT * FROM missing_table;`)
	if err := migrate(ctx, db, []migration{first}, fixedNow); err != nil {
		t.Fatalf("apply initial migration: %v", err)
	}
	if err := migrate(ctx, db, []migration{first, failing}, fixedNow); err == nil {
		t.Fatal("broken migration unexpectedly succeeded")
	}

	assertTableCount(t, db, "durable", 1)
	assertTableCount(t, db, "must_rollback", 0)
	var count int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM schema_migrations`).Scan(&count); err != nil {
		t.Fatalf("count migrations after failure: %v", err)
	}
	if count != 1 {
		t.Fatalf("migration count after failure = %d, want 1", count)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close database after failed migration: %v", err)
	}

	db, err = openSQLite(path, Options{})
	if err != nil {
		t.Fatalf("reopen database after failed migration: %v", err)
	}
	defer db.Close()
	fixed := testMigration(2, "broken", `CREATE TABLE recovered (id INTEGER PRIMARY KEY) STRICT;`)
	if err := migrate(ctx, db, []migration{first, fixed}, fixedNow); err != nil {
		t.Fatalf("retry corrected migration: %v", err)
	}
	assertTableCount(t, db, "recovered", 1)
}

func TestMigrateRejectsChecksumMismatch(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	db, err := openSQLite(filepath.Join(t.TempDir(), "checksum.db"), Options{})
	if err != nil {
		t.Fatalf("open SQLite: %v", err)
	}
	defer db.Close()

	first := testMigration(1, "initial", testMigrationTable+`CREATE TABLE original (id INTEGER PRIMARY KEY) STRICT;`)
	if err := migrate(ctx, db, []migration{first}, fixedNow); err != nil {
		t.Fatalf("apply initial migration: %v", err)
	}
	changed := testMigration(1, "initial", testMigrationTable+`CREATE TABLE changed (id INTEGER PRIMARY KEY) STRICT;`)
	err = migrate(ctx, db, []migration{changed}, fixedNow)
	var mismatch *ChecksumMismatchError
	if !errors.As(err, &mismatch) {
		t.Fatalf("checksum error = %v, want ChecksumMismatchError", err)
	}
	assertTableCount(t, db, "original", 1)
	assertTableCount(t, db, "changed", 0)
}

func TestMigrateRejectsDatabaseNewerThanBinary(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	db, err := openSQLite(filepath.Join(t.TempDir(), "newer.db"), Options{})
	if err != nil {
		t.Fatalf("open SQLite: %v", err)
	}
	defer db.Close()

	first := testMigration(1, "initial", testMigrationTable)
	if err := migrate(ctx, db, []migration{first}, fixedNow); err != nil {
		t.Fatalf("apply initial migration: %v", err)
	}
	if _, err := db.ExecContext(ctx,
		`INSERT INTO schema_migrations(version, name, checksum, applied_at) VALUES (2, 'future', ?, ?)`,
		strings.Repeat("0", 64), testTime.Format(time.RFC3339Nano)); err != nil {
		t.Fatalf("record future migration: %v", err)
	}

	err = migrate(ctx, db, []migration{first}, fixedNow)
	var unknown *UnknownMigrationError
	if !errors.As(err, &unknown) {
		t.Fatalf("newer database error = %v, want UnknownMigrationError", err)
	}
}

func assertPragma(t *testing.T, database *Database, name, want string) {
	t.Helper()
	var got string
	if err := database.SQL().QueryRow(`PRAGMA ` + name).Scan(&got); err != nil {
		t.Fatalf("read PRAGMA %s: %v", name, err)
	}
	if got != want {
		t.Errorf("PRAGMA %s = %q, want %q", name, got, want)
	}
}

func assertTableCount(t *testing.T, db interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, name string, want int) {
	t.Helper()
	var got int
	if err := db.QueryRowContext(context.Background(),
		`SELECT count(*) FROM sqlite_schema WHERE type = 'table' AND name = ?`, name).Scan(&got); err != nil {
		t.Fatalf("inspect table %s: %v", name, err)
	}
	if got != want {
		t.Fatalf("table %s count = %d, want %d", name, got, want)
	}
}

func testMigration(version int, name, statement string) migration {
	digest := sha256.Sum256([]byte(statement))
	return migration{
		Version:  version,
		Name:     name,
		Checksum: hex.EncodeToString(digest[:]),
		SQL:      statement,
	}
}

func fixedNow() time.Time {
	return testTime
}
