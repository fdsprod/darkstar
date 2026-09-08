package sqlite

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"darkstar/src/ports/workflowstore"
)

var _ workflowstore.NodeDefinitionStore = (*Database)(nil)

const nodeDefinitionSelect = `SELECT scope,owner,definition_name,definition_version,definition_digest,document_json,created_at,archived_at FROM node_definitions`

func (d *Database) InstallNodeDefinition(ctx context.Context, request workflowstore.NodeDefinitionRecord) (workflowstore.NodeDefinitionRecord, bool, error) {
	if request.Scope != workflowstore.DraftScopeUser && request.Scope != workflowstore.DraftScopeProject || request.Owner == "" || request.Name == "" || request.Version == "" || !lowercaseDigestPattern.MatchString(request.Digest) || !jsonObject(request.Document) || request.CreatedAt.IsZero() {
		return workflowstore.NodeDefinitionRecord{}, false, errors.New("complete user/project node definition is required")
	}
	result, err := d.sql.ExecContext(ctx, `INSERT OR IGNORE INTO node_definitions(scope,owner,definition_name,definition_version,definition_digest,document_json,created_at) VALUES(?,?,?,?,?,?,?)`, request.Scope, request.Owner, request.Name, request.Version, request.Digest, string(request.Document), formatTime(request.CreatedAt))
	if err != nil {
		return workflowstore.NodeDefinitionRecord{}, false, fmt.Errorf("install node definition: %w", err)
	}
	rows, _ := result.RowsAffected()
	stored, err := d.NodeDefinition(ctx, request.Scope, request.Owner, request.Name, request.Version)
	if err != nil {
		return workflowstore.NodeDefinitionRecord{}, false, err
	}
	if stored.Digest != request.Digest || !bytes.Equal(stored.Document, request.Document) {
		return workflowstore.NodeDefinitionRecord{}, false, fmt.Errorf("%w: %s %s", workflowstore.ErrNodeDefinitionConflict, request.Name, request.Version)
	}
	return stored, rows == 1, nil
}

func (d *Database) NodeDefinition(ctx context.Context, scope workflowstore.DraftScope, owner, name, version string) (workflowstore.NodeDefinitionRecord, error) {
	value, err := scanNodeDefinition(d.sql.QueryRowContext(ctx, nodeDefinitionSelect+` WHERE scope=? AND owner=? AND definition_name=? AND definition_version=?`, scope, owner, name, version))
	if errors.Is(err, sql.ErrNoRows) {
		return value, fmt.Errorf("%w: node definition %s %s", workflowstore.ErrNotFound, name, version)
	}
	return value, err
}
func (d *Database) NodeDefinitions(ctx context.Context) ([]workflowstore.NodeDefinitionRecord, error) {
	rows, err := d.sql.QueryContext(ctx, nodeDefinitionSelect+` ORDER BY scope,owner,definition_name,definition_version`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	values := []workflowstore.NodeDefinitionRecord{}
	for rows.Next() {
		value, err := scanNodeDefinition(rows)
		if err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	return values, rows.Err()
}
func (d *Database) ArchiveNodeDefinition(ctx context.Context, scope workflowstore.DraftScope, owner, name, version string, at time.Time) (workflowstore.NodeDefinitionRecord, bool, error) {
	if at.IsZero() {
		return workflowstore.NodeDefinitionRecord{}, false, errors.New("archive time required")
	}
	result, err := d.sql.ExecContext(ctx, `UPDATE node_definitions SET archived_at=COALESCE(archived_at,?) WHERE scope=? AND owner=? AND definition_name=? AND definition_version=?`, formatTime(at), scope, owner, name, version)
	if err != nil {
		return workflowstore.NodeDefinitionRecord{}, false, err
	}
	rows, _ := result.RowsAffected()
	value, err := d.NodeDefinition(ctx, scope, owner, name, version)
	return value, rows == 1 && value.ArchivedAt != nil, err
}

type nodeDefinitionScanner interface{ Scan(...any) error }

func scanNodeDefinition(row nodeDefinitionScanner) (workflowstore.NodeDefinitionRecord, error) {
	var value workflowstore.NodeDefinitionRecord
	var document, created string
	var archived sql.NullString
	if err := row.Scan(&value.Scope, &value.Owner, &value.Name, &value.Version, &value.Digest, &document, &created, &archived); err != nil {
		return value, err
	}
	value.Document = []byte(document)
	var err error
	value.CreatedAt, err = parseTime(created)
	if err != nil {
		return value, err
	}
	if archived.Valid {
		parsed, err := parseTime(archived.String)
		if err != nil {
			return value, err
		}
		value.ArchivedAt = &parsed
	}
	return value, nil
}
