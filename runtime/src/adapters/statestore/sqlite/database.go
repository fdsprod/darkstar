// Package sqlite owns DARKSTAR's durable SQLite connection and schema migrations.
package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"path/filepath"
	"strconv"
	"time"

	_ "modernc.org/sqlite"
)

const defaultBusyTimeout = 5 * time.Second

// Options configures a database connection.
type Options struct {
	// BusyTimeout bounds how long SQLite waits for a conflicting lock. Zero uses
	// the five-second default.
	BusyTimeout time.Duration
}

// Database is a migrated DARKSTAR SQLite database.
type Database struct {
	sql *sql.DB
	now func() time.Time
}

// Open opens path, applies every pending migration, and returns only after the
// schema is ready to serve traffic.
func Open(ctx context.Context, path string, options Options) (*Database, error) {
	db, err := openSQLite(path, options)
	if err != nil {
		return nil, err
	}

	migrations, err := embeddedMigrationSet()
	if err == nil {
		err = migrate(ctx, db, migrations, time.Now)
	}
	if err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("migrate SQLite database: %w", err)
	}

	return &Database{sql: db, now: time.Now}, nil
}

// SQL returns the configured database/sql handle. The handle is intentionally
// limited to one open connection so every operation uses the same PRAGMA policy
// and writes are coordinated in process.
func (d *Database) SQL() *sql.DB {
	return d.sql
}

// Close releases the SQLite connection.
func (d *Database) Close() error {
	return d.sql.Close()
}

// Version returns the greatest applied migration version, or zero for an
// unmigrated database.
func (d *Database) Version(ctx context.Context) (int, error) {
	migrations, err := d.AppliedMigrations(ctx)
	if err != nil {
		return 0, err
	}
	if len(migrations) == 0 {
		return 0, nil
	}
	return migrations[len(migrations)-1].Version, nil
}

// AppliedMigrations returns the ordered migration history recorded by the
// database.
func (d *Database) AppliedMigrations(ctx context.Context) ([]AppliedMigration, error) {
	return readAppliedMigrations(ctx, d.sql)
}

func openSQLite(path string, options Options) (*sql.DB, error) {
	if path == "" {
		return nil, errors.New("SQLite database path is empty")
	}

	timeout := options.BusyTimeout
	if timeout == 0 {
		timeout = defaultBusyTimeout
	}
	if timeout < time.Millisecond {
		return nil, fmt.Errorf("SQLite busy timeout must be at least one millisecond: %s", timeout)
	}

	absolutePath, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("resolve SQLite database path: %w", err)
	}

	query := make(url.Values)
	query.Add("_pragma", "busy_timeout("+strconv.FormatInt(timeout.Milliseconds(), 10)+")")
	query.Add("_pragma", "foreign_keys(ON)")
	query.Add("_pragma", "journal_mode(WAL)")
	query.Add("_pragma", "synchronous(FULL)")
	query.Set("_txlock", "immediate")
	dsn := absolutePath + "?" + query.Encode()

	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open SQLite database: %w", err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)

	if err := db.Ping(); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("connect to SQLite database: %w", err)
	}
	return db, nil
}
