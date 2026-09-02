package sqlite

import (
	"context"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"regexp"
	"sort"
	"strings"

	registryport "darkstar/src/ports/capabilityregistry"
)

var _ registryport.Registry = (*Database)(nil)

var capabilityNamePattern = regexp.MustCompile(`^(?:darkstar|project|user|admin):[a-z0-9][a-z0-9._-]*$|^(?:plugin:[a-z0-9][a-z0-9._-]*/|mcp:[a-z0-9][a-z0-9._-]*/|codex-inherited:[a-z0-9][a-z0-9._-]*/)[a-z0-9][a-z0-9._/-]*$`)

const capabilitySelect = `SELECT capability_id, idempotency_key, schema_version, canonical_name, kind,
	provenance_class, declared_version, fingerprint, source_json, interfaces_json,
	dependencies_json, risk_json, availability, observed_at FROM capability_records`

func (d *Database) RegisterCapability(ctx context.Context, record registryport.Record, idempotencyKey string) (registryport.Record, bool, error) {
	normalized, err := normalizeCapabilityRecord(record)
	if err != nil {
		return registryport.Record{}, false, err
	}
	if strings.TrimSpace(idempotencyKey) == "" || idempotencyKey != strings.TrimSpace(idempotencyKey) {
		return registryport.Record{}, false, errors.New("capability idempotency key is required and must be trimmed")
	}
	encoded, err := encodeCapability(normalized)
	if err != nil {
		return registryport.Record{}, false, err
	}
	tx, err := d.sql.BeginTx(ctx, nil)
	if err != nil {
		return registryport.Record{}, false, fmt.Errorf("begin capability registration: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	existing, key, err := scanCapability(tx.QueryRowContext(ctx, capabilitySelect+` WHERE capability_id = ? OR idempotency_key = ?`, normalized.ID, idempotencyKey))
	if err == nil {
		if key != idempotencyKey || !reflect.DeepEqual(existing, normalized) {
			return registryport.Record{}, false, registryport.ErrConflict
		}
		if err := tx.Commit(); err != nil {
			return registryport.Record{}, false, fmt.Errorf("commit repeated capability registration: %w", err)
		}
		return existing, false, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return registryport.Record{}, false, fmt.Errorf("read repeated capability registration: %w", err)
	}
	var collision string
	err = tx.QueryRowContext(ctx, `SELECT capability_id FROM capability_records WHERE canonical_name = ? AND kind = ? AND provenance_class = ?`, normalized.Name, normalized.Kind, normalized.Class).Scan(&collision)
	if err == nil {
		return registryport.Record{}, false, registryport.ErrConflict
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return registryport.Record{}, false, fmt.Errorf("check capability shadowing: %w", err)
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO capability_records(
		capability_id, idempotency_key, schema_version, canonical_name, kind, provenance_class,
		declared_version, fingerprint, source_json, interfaces_json, dependencies_json,
		risk_json, availability, observed_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		normalized.ID, idempotencyKey, normalized.SchemaVersion, normalized.Name, normalized.Kind,
		normalized.Class, nullableString(normalized.DeclaredVersion), normalized.Fingerprint, encoded.source,
		encoded.interfaces, encoded.dependencies, encoded.risk, normalized.Availability, formatTime(normalized.ObservedAt))
	if err != nil {
		return registryport.Record{}, false, fmt.Errorf("insert capability record: %w", err)
	}
	created, _, err := scanCapability(tx.QueryRowContext(ctx, capabilitySelect+` WHERE capability_id = ?`, normalized.ID))
	if err != nil {
		return registryport.Record{}, false, fmt.Errorf("read created capability record: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return registryport.Record{}, false, fmt.Errorf("commit capability registration: %w", err)
	}
	return created, true, nil
}

func (d *Database) Capability(ctx context.Context, id string) (registryport.Record, error) {
	record, _, err := scanCapability(d.sql.QueryRowContext(ctx, capabilitySelect+` WHERE capability_id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return registryport.Record{}, registryport.ErrNotFound
	}
	if err != nil {
		return registryport.Record{}, fmt.Errorf("read capability record: %w", err)
	}
	return record, nil
}

func (d *Database) Snapshot(ctx context.Context) ([]registryport.Record, error) {
	rows, err := d.sql.QueryContext(ctx, capabilitySelect+` ORDER BY canonical_name, kind, provenance_class, capability_id`)
	if err != nil {
		return nil, fmt.Errorf("read capability snapshot: %w", err)
	}
	defer func() { _ = rows.Close() }()
	records := make([]registryport.Record, 0)
	for rows.Next() {
		record, _, err := scanCapability(rows)
		if err != nil {
			return nil, fmt.Errorf("scan capability snapshot: %w", err)
		}
		records = append(records, record)
	}
	return records, rows.Err()
}

type capabilityJSON struct{ source, interfaces, dependencies, risk string }

func encodeCapability(record registryport.Record) (capabilityJSON, error) {
	values := []any{record.Source, record.Interfaces, record.Dependencies, record.Risk}
	encoded := make([]string, len(values))
	for index, value := range values {
		content, err := json.Marshal(value)
		if err != nil {
			return capabilityJSON{}, fmt.Errorf("encode capability record: %w", err)
		}
		encoded[index] = string(content)
	}
	return capabilityJSON{encoded[0], encoded[1], encoded[2], encoded[3]}, nil
}

func scanCapability(row rowScanner) (registryport.Record, string, error) {
	var record registryport.Record
	var key, source, interfaces, dependencies, risk, observedAt string
	var version sql.NullString
	if err := row.Scan(&record.ID, &key, &record.SchemaVersion, &record.Name, &record.Kind, &record.Class,
		&version, &record.Fingerprint, &source, &interfaces, &dependencies, &risk, &record.Availability, &observedAt); err != nil {
		return registryport.Record{}, "", err
	}
	record.DeclaredVersion = version.String
	for _, target := range []struct {
		content string
		value   any
	}{
		{source, &record.Source}, {interfaces, &record.Interfaces}, {dependencies, &record.Dependencies}, {risk, &record.Risk},
	} {
		if err := json.Unmarshal([]byte(target.content), target.value); err != nil {
			return registryport.Record{}, "", err
		}
	}
	parsed, err := parseTime(observedAt)
	if err != nil {
		return registryport.Record{}, "", err
	}
	record.ObservedAt = parsed
	return record, key, nil
}

func normalizeCapabilityRecord(record registryport.Record) (registryport.Record, error) {
	if record.SchemaVersion != 1 || strings.TrimSpace(record.ID) == "" || record.ID != strings.TrimSpace(record.ID) || !capabilityNamePattern.MatchString(record.Name) || record.ObservedAt.IsZero() {
		return record, errors.New("capability identity, schema version, canonical name, and observation time are required")
	}
	if record.DeclaredVersion != strings.TrimSpace(record.DeclaredVersion) {
		return record, errors.New("capability declared version must be trimmed")
	}
	if len(record.Fingerprint) != 64 {
		return record, errors.New("capability fingerprint must be lowercase SHA-256 hex")
	}
	if _, err := hex.DecodeString(record.Fingerprint); err != nil || record.Fingerprint != strings.ToLower(record.Fingerprint) {
		return record, errors.New("capability fingerprint must be lowercase SHA-256 hex")
	}
	if record.Kind != registryport.KindSkill && record.Kind != registryport.KindTool {
		return record, errors.New("capability kind is invalid")
	}
	switch record.Class {
	case registryport.ClassGuaranteed, registryport.ClassRegistered, registryport.ClassInherited, registryport.ClassUnsupportedDiscovery:
	default:
		return record, errors.New("capability provenance class is invalid")
	}
	switch record.Availability {
	case registryport.AvailabilityAvailable, registryport.AvailabilityUnavailable, registryport.AvailabilityUnhealthy:
	default:
		return record, errors.New("capability availability is invalid")
	}
	if strings.TrimSpace(record.Source.Type) == "" || strings.TrimSpace(record.Source.Locator) == "" {
		return record, errors.New("capability source is required")
	}
	record.Dependencies = append([]string(nil), record.Dependencies...)
	sort.Strings(record.Dependencies)
	for index, dependency := range record.Dependencies {
		if !capabilityNamePattern.MatchString(dependency) || (index > 0 && record.Dependencies[index-1] == dependency) {
			return record, errors.New("capability dependencies must be unique canonical names")
		}
	}
	if record.Dependencies == nil {
		record.Dependencies = []string{}
	}
	record.ObservedAt = record.ObservedAt.UTC()
	return record, nil
}

func nullableString(value string) any {
	if value == "" {
		return nil
	}
	return value
}
