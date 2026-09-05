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
	defer func() {
		_ = database.Close()
	}()

	version, err := database.Version(ctx)
	if err != nil {
		t.Fatalf("read schema version: %v", err)
	}
	if version != 19 {
		t.Fatalf("schema version = %d, want 19", version)
	}
	history, err := database.AppliedMigrations(ctx)
	if err != nil {
		t.Fatalf("read migration history: %v", err)
	}
	if len(history) != 19 || history[0].Version != 1 || history[0].Name != "initial" || history[1].Version != 2 || history[1].Name != "constrain_state" || history[2].Version != 3 || history[2].Name != "append_only_events" || history[3].Version != 4 || history[3].Name != "leases_queues" || history[4].Version != 5 || history[4].Name != "startup_recovery" || history[5].Version != 6 || history[5].Name != "attempt_projection" || history[6].Version != 7 || history[6].Name != "workflow_snapshots" || history[7].Version != 8 || history[7].Name != "workflow_state_machines" || history[8].Version != 9 || history[8].Name != "artifact_registry" || history[9].Version != 10 || history[9].Name != "artifact_lineage_bindings" || history[10].Version != 11 || history[10].Name != "artifact_representations" || history[11].Version != 12 || history[11].Name != "context_manifests" || history[12].Version != 13 || history[12].Name != "work_aggregates" || history[13].Version != 14 || history[13].Name != "artifact_checkpoints" || history[14].Version != 15 || history[14].Name != "capability_registry" || history[15].Version != 16 || history[15].Name != "readiness_assessments" || history[16].Version != 17 || history[16].Name != "input_requests" || history[17].Version != 18 || history[17].Name != "provider_permissions" || history[18].Version != 19 || history[18].Name != "workflow_authoring" {
		t.Fatalf("migration history = %#v, want initial through readiness_assessments migrations", history)
	}
	for _, item := range history {
		if len(item.Checksum) != 64 {
			t.Errorf("migration %d checksum length = %d, want 64", item.Version, len(item.Checksum))
		}
		if _, err := time.Parse(time.RFC3339Nano, item.AppliedAt); err != nil {
			t.Errorf("migration %d applied_at %q is not RFC 3339: %v", item.Version, item.AppliedAt, err)
		}
	}

	wantTables := []string{
		"aggregates", "approval_projection", "artifact_binding_versions", "artifact_dependencies", "artifact_invalidations", "artifact_representations", "artifact_versions", "attempt_projection", "capability_records", "commands", "context_manifests", "events", "external_refs",
		"global_positions", "leases", "lease_scopes", "outbox", "projection_checkpoints",
		"input_request_projection", "node_projection", "point_dependencies", "point_projection", "project_projection", "provider_permission_projection", "queue_entries", "readiness_assessment_projection", "recovery_decisions", "run_projection", "run_workflow_snapshots", "schema_migrations", "story_projection", "work_item_projection", "workflow_versions",
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
	if migrationCount != 19 {
		t.Errorf("migration count = %d, want 19", migrationCount)
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
	defer func() {
		_ = second.Close()
	}()

	var count int
	if err := second.SQL().QueryRowContext(ctx, `SELECT count(*) FROM schema_migrations`).Scan(&count); err != nil {
		t.Fatalf("count migrations: %v", err)
	}
	if count != 19 {
		t.Fatalf("migration count after reopen = %d, want 19", count)
	}
}

func TestStateConstraintMigrationPreservesValidData(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	db, err := openSQLite(filepath.Join(t.TempDir(), "upgrade-state.db"), Options{})
	if err != nil {
		t.Fatalf("open SQLite: %v", err)
	}
	defer func() {
		_ = db.Close()
	}()
	migrations, err := embeddedMigrationSet()
	if err != nil {
		t.Fatalf("read migrations: %v", err)
	}
	if err := migrate(ctx, db, migrations[:1], fixedNow); err != nil {
		t.Fatalf("apply initial migration: %v", err)
	}

	statements := []string{
		`INSERT INTO aggregates VALUES ('run_01UPGRADE', 'run', 1, 'created', 'updated')`,
		`INSERT INTO events VALUES (1, 'event_01UPGRADE', 1, 'run_01UPGRADE', 1, 'run', 'run_01UPGRADE', 1, 'run.created', 'occurred', 'recorded', 'run_01UPGRADE', NULL, 'command', '{}', '{}', '{}')`,
		`INSERT INTO commands(scope, idempotency_key, request_digest, status, created_at) VALUES ('run_01UPGRADE', 'pending-key', 'digest', 'pending', 'created')`,
		`INSERT INTO outbox(operation_id, operation_kind, aggregate_id, request_json, state, available_at, created_at, updated_at) VALUES ('operation_01UPGRADE', 'publish', 'run_01UPGRADE', '{}', 'prepared', 'available', 'created', 'updated')`,
		`INSERT INTO run_projection VALUES ('run_01UPGRADE', 'work_01UPGRADE', 'workflow', '1.0.0', 'running', 1, 1, 'created', 'updated')`,
		`INSERT INTO approval_projection VALUES ('approval_01UPGRADE', 'run_01UPGRADE', 'workflow_checkpoint', 'pending', 'scope', 'policy', 1, 1, 'created', 'updated')`,
		`INSERT INTO external_refs VALUES ('run_01UPGRADE', 'adapter', 'thread', X'00', 'created')`,
	}
	for _, statement := range statements {
		if _, err := db.ExecContext(ctx, statement); err != nil {
			t.Fatalf("seed initial schema with %q: %v", statement, err)
		}
	}

	if err := migrate(ctx, db, migrations, fixedNow); err != nil {
		t.Fatalf("apply state constraint migration: %v", err)
	}
	for _, table := range []string{"aggregates", "events", "commands", "outbox", "run_projection", "approval_projection", "external_refs"} {
		var count int
		if err := db.QueryRowContext(ctx, `SELECT count(*) FROM `+table).Scan(&count); err != nil {
			t.Fatalf("count %s after migration: %v", table, err)
		}
		if count != 1 {
			t.Errorf("%s rows after migration = %d, want 1", table, count)
		}
	}
	rows, err := db.QueryContext(ctx, `PRAGMA foreign_key_check`)
	if err != nil {
		t.Fatalf("check foreign keys: %v", err)
	}
	defer func() {
		_ = rows.Close()
	}()
	if rows.Next() {
		t.Fatal("state constraint migration left a foreign key violation")
	}
}

func TestFinalSchemaRejectsContradictoryStates(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	database, err := Open(ctx, filepath.Join(t.TempDir(), "constraints.db"), Options{})
	if err != nil {
		t.Fatalf("open constrained database: %v", err)
	}
	defer func() {
		_ = database.Close()
	}()
	db := database.SQL()

	expectConstraint(t, db, `INSERT INTO aggregates VALUES ('run_01TEST', 'work', 0, 'now', 'now')`)
	if _, err := db.ExecContext(ctx, `INSERT INTO aggregates VALUES ('run_01TEST', 'run', 0, 'now', 'now')`); err != nil {
		t.Fatalf("insert valid aggregate: %v", err)
	}

	var removedColumns int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM pragma_table_info('events') WHERE name IN ('stream_id', 'stream_sequence', 'aggregate_type')`).Scan(&removedColumns); err != nil {
		t.Fatalf("inspect event columns: %v", err)
	}
	if removedColumns != 0 {
		t.Fatalf("events retains %d duplicated stream columns", removedColumns)
	}

	expectConstraint(t, db, `INSERT INTO commands(scope, idempotency_key, request_digest, status, completed_at, created_at) VALUES ('run', 'bad-pending', 'digest', 'pending', 'now', 'now')`)
	if _, err := db.ExecContext(ctx, `INSERT INTO commands(scope, idempotency_key, request_digest, status, created_at) VALUES ('run', 'pending-ok', 'digest', 'pending', 'now')`); err != nil {
		t.Fatalf("insert valid pending command: %v", err)
	}
	expectConstraint(t, db, `INSERT INTO commands(scope, idempotency_key, request_digest, status, created_at, completed_at) VALUES ('run', 'bad-complete', 'digest', 'completed', 'now', 'now')`)

	expectConstraint(t, db, `INSERT INTO outbox(operation_id, operation_kind, aggregate_id, request_json, state, available_at, created_at, updated_at) VALUES ('operation_bad', 'publish', 'run_01TEST', '{}', 'leased', 'now', 'now', 'now')`)
	if _, err := db.ExecContext(ctx, `INSERT INTO outbox(operation_id, operation_kind, aggregate_id, request_json, state, available_at, lease_owner, lease_expires_at, created_at, updated_at) VALUES ('operation_ok', 'publish', 'run_01TEST', '{}', 'leased', 'now', 'daemon', 'later', 'now', 'now')`); err != nil {
		t.Fatalf("insert valid leased operation: %v", err)
	}

	expectConstraint(t, db, `INSERT INTO run_projection VALUES ('run_bad', 'work', 'workflow', '1.0.0', 'nonsense', 1, 0, 'now', 'now')`)
	expectConstraint(t, db, `INSERT INTO approval_projection VALUES ('approval_bad', 'run_01TEST', 'nonsense', 'pending', 'scope', 'policy', 1, 0, 'now', 'now')`)

	if _, err := db.ExecContext(ctx, `INSERT INTO lease_scopes VALUES ('repository', 'repo_01TEST', 1, 'now')`); err != nil {
		t.Fatalf("insert valid lease scope: %v", err)
	}
	expectConstraint(t, db, `INSERT INTO leases(lease_id, scope_kind, scope_id, holder_attempt_id, daemon_instance_id, fencing_token, acquired_at, heartbeat_at, expires_at, host_boot_id, state) VALUES ('lease_bad', 'repository', 'repo_01TEST', 'attempt', 'daemon', 1, 'now', 'now', 'later', 'boot', 'released')`)
	expectConstraint(t, db, `INSERT INTO queue_entries VALUES ('repository_write', 'repo_01TEST', 'attempt', -1, 'now', 'now', '{}')`)
	expectConstraint(t, db, `INSERT INTO recovery_decisions VALUES ('daemon', 'lease', 'lease_01', 'process', 'held', '{}', 'guess', '{}', 'now')`)
	if _, err := db.ExecContext(ctx, `INSERT INTO recovery_decisions VALUES ('daemon', 'lease', 'lease_01', 'process', 'held', '{}', 'resume', '{}', 'now')`); err != nil {
		t.Fatalf("insert valid recovery decision: %v", err)
	}
	expectConstraint(t, db, `UPDATE recovery_decisions SET outcome = 'retry' WHERE subject_id = 'lease_01'`)
}

func TestMigrateUpgradesInOrder(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	db, err := openSQLite(filepath.Join(t.TempDir(), "upgrade.db"), Options{})
	if err != nil {
		t.Fatalf("open SQLite: %v", err)
	}
	defer func() {
		_ = db.Close()
	}()

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
	defer func() {
		_ = db.Close()
	}()
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
	defer func() {
		_ = db.Close()
	}()

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
	defer func() {
		_ = db.Close()
	}()

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

func expectConstraint(t *testing.T, db *sql.DB, statement string) {
	t.Helper()
	if _, err := db.ExecContext(context.Background(), statement); err == nil {
		t.Fatalf("statement unexpectedly bypassed a schema constraint: %s", statement)
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
