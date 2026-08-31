package sqlite

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"embed"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"regexp"
	"sort"
	"strconv"
	"time"
)

var migrationFileName = regexp.MustCompile(`^(\d{4})_([a-z0-9]+(?:_[a-z0-9]+)*)\.sql$`)

//go:embed migrations/*.sql
var migrationFiles embed.FS

type migration struct {
	Version  int
	Name     string
	Checksum string
	SQL      string
}

// AppliedMigration is a migration record read from schema_migrations.
type AppliedMigration struct {
	Version   int
	Name      string
	Checksum  string
	AppliedAt string
}

// ChecksumMismatchError reports that an already-applied migration no longer
// matches the bytes embedded in the binary.
type ChecksumMismatchError struct {
	Version int
	Name    string
	Want    string
	Got     string
}

func (e *ChecksumMismatchError) Error() string {
	return fmt.Sprintf("migration %04d_%s checksum mismatch: database has %s, binary has %s", e.Version, e.Name, e.Got, e.Want)
}

// UnknownMigrationError reports a database migration version that this binary
// does not know. This commonly means the database was opened by a newer binary.
type UnknownMigrationError struct {
	Version int
	Name    string
}

func (e *UnknownMigrationError) Error() string {
	return fmt.Sprintf("database contains unknown migration %04d_%s", e.Version, e.Name)
}

func embeddedMigrationSet() ([]migration, error) {
	entries, err := fs.ReadDir(migrationFiles, "migrations")
	if err != nil {
		return nil, fmt.Errorf("read embedded migrations: %w", err)
	}

	migrations := make([]migration, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		match := migrationFileName.FindStringSubmatch(entry.Name())
		if match == nil {
			return nil, fmt.Errorf("invalid migration filename %q", entry.Name())
		}
		version, err := strconv.Atoi(match[1])
		if err != nil || version == 0 {
			return nil, fmt.Errorf("invalid migration version in %q", entry.Name())
		}
		contents, err := migrationFiles.ReadFile(path.Join("migrations", entry.Name()))
		if err != nil {
			return nil, fmt.Errorf("read migration %q: %w", entry.Name(), err)
		}
		digest := sha256.Sum256(contents)
		migrations = append(migrations, migration{
			Version:  version,
			Name:     match[2],
			Checksum: hex.EncodeToString(digest[:]),
			SQL:      string(contents),
		})
	}
	return validateMigrationSet(migrations)
}

func validateMigrationSet(migrations []migration) ([]migration, error) {
	if len(migrations) == 0 {
		return nil, errors.New("no SQLite migrations are embedded")
	}
	migrations = append([]migration(nil), migrations...)
	sort.Slice(migrations, func(i, j int) bool { return migrations[i].Version < migrations[j].Version })
	for index, item := range migrations {
		if item.Version <= 0 || item.Name == "" || item.SQL == "" || item.Checksum == "" {
			return nil, fmt.Errorf("migration at index %d is incomplete", index)
		}
		if index > 0 && migrations[index-1].Version == item.Version {
			return nil, fmt.Errorf("duplicate migration version %d", item.Version)
		}
	}
	return migrations, nil
}

func migrate(ctx context.Context, db *sql.DB, migrations []migration, now func() time.Time) error {
	migrations, err := validateMigrationSet(migrations)
	if err != nil {
		return err
	}

	applied, err := readAppliedMigrations(ctx, db)
	if err != nil {
		return fmt.Errorf("read applied migrations: %w", err)
	}
	for index, item := range applied {
		if index >= len(migrations) {
			return &UnknownMigrationError{Version: item.Version, Name: item.Name}
		}
		expected := migrations[index]
		if expected.Version != item.Version || expected.Name != item.Name {
			return &UnknownMigrationError{Version: item.Version, Name: item.Name}
		}
		if expected.Checksum != item.Checksum {
			return &ChecksumMismatchError{Version: item.Version, Name: item.Name, Want: expected.Checksum, Got: item.Checksum}
		}
	}

	for _, item := range migrations[len(applied):] {
		if err := applyMigration(ctx, db, item, now().UTC()); err != nil {
			return err
		}
	}
	return nil
}

func applyMigration(ctx context.Context, db *sql.DB, item migration, appliedAt time.Time) (err error) {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin migration %04d_%s: %w", item.Version, item.Name, err)
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()

	if _, err = tx.ExecContext(ctx, item.SQL); err != nil {
		return fmt.Errorf("apply migration %04d_%s: %w", item.Version, item.Name, err)
	}
	if _, err = tx.ExecContext(ctx,
		`INSERT INTO schema_migrations(version, name, checksum, applied_at) VALUES (?, ?, ?, ?)`,
		item.Version, item.Name, item.Checksum, appliedAt.Format(time.RFC3339Nano)); err != nil {
		return fmt.Errorf("record migration %04d_%s: %w", item.Version, item.Name, err)
	}
	if err = tx.Commit(); err != nil {
		return fmt.Errorf("commit migration %04d_%s: %w", item.Version, item.Name, err)
	}
	return nil
}

func readAppliedMigrations(ctx context.Context, db *sql.DB) ([]AppliedMigration, error) {
	var exists int
	if err := db.QueryRowContext(ctx,
		`SELECT count(*) FROM sqlite_schema WHERE type = 'table' AND name = 'schema_migrations'`).Scan(&exists); err != nil {
		return nil, fmt.Errorf("inspect SQLite schema: %w", err)
	}
	if exists == 0 {
		return nil, nil
	}

	rows, err := db.QueryContext(ctx,
		`SELECT version, name, checksum, applied_at FROM schema_migrations ORDER BY version`)
	if err != nil {
		return nil, fmt.Errorf("query schema_migrations: %w", err)
	}
	defer rows.Close()

	var migrations []AppliedMigration
	for rows.Next() {
		var item AppliedMigration
		if err := rows.Scan(&item.Version, &item.Name, &item.Checksum, &item.AppliedAt); err != nil {
			return nil, fmt.Errorf("scan schema_migrations: %w", err)
		}
		migrations = append(migrations, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate schema_migrations: %w", err)
	}
	return migrations, nil
}
