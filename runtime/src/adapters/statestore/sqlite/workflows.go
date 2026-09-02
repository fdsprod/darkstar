package sqlite

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"darkstar/src/core/workflow"
	"darkstar/src/ports/workflowstore"
)

var lowercaseDigestPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

var _ workflowstore.Store = (*Database)(nil)

const installedVersionSelect = `SELECT workflow_name, workflow_version, workflow_digest, document_json,
	source_scope, source_reference, installed_at FROM workflow_versions`

// Install creates one immutable workflow name/version. Repeating the same
// canonical digest is idempotent; different bytes return ErrVersionConflict.
func (d *Database) Install(ctx context.Context, request workflowstore.InstallRequest) (workflowstore.InstalledVersion, bool, error) {
	if err := validateInstallRequest(request); err != nil {
		return workflowstore.InstalledVersion{}, false, err
	}
	result, err := d.sql.ExecContext(ctx, `INSERT OR IGNORE INTO workflow_versions(
		workflow_name, workflow_version, workflow_digest, document_json, source_scope, source_reference, installed_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)`, request.Name, request.Version, request.Digest, string(request.Document),
		request.SourceScope, request.SourceRef, formatTime(request.InstalledAt))
	if err != nil {
		return workflowstore.InstalledVersion{}, false, fmt.Errorf("insert workflow version: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return workflowstore.InstalledVersion{}, false, fmt.Errorf("inspect workflow installation: %w", err)
	}
	installed, err := d.InstalledVersion(ctx, request.Name, request.Version)
	if err != nil {
		return workflowstore.InstalledVersion{}, false, err
	}
	if installed.Digest != request.Digest || !bytes.Equal(installed.Document, request.Document) {
		return workflowstore.InstalledVersion{}, false, fmt.Errorf("%w: %s %s is %s, attempted %s",
			workflowstore.ErrVersionConflict, request.Name, request.Version, installed.Digest, request.Digest)
	}
	return installed, rows == 1, nil
}

// InstalledVersion returns one exact immutable name/version.
func (d *Database) InstalledVersion(ctx context.Context, name, version string) (workflowstore.InstalledVersion, error) {
	value, err := scanInstalledVersion(d.sql.QueryRowContext(ctx,
		installedVersionSelect+` WHERE workflow_name = ? AND workflow_version = ?`, name, version))
	if errors.Is(err, sql.ErrNoRows) {
		return workflowstore.InstalledVersion{}, fmt.Errorf("%w: workflow %s %s", workflowstore.ErrNotFound, name, version)
	}
	if err != nil {
		return workflowstore.InstalledVersion{}, fmt.Errorf("read installed workflow: %w", err)
	}
	return value, nil
}

// InstalledVersions lists immutable versions, optionally restricted by name.
func (d *Database) InstalledVersions(ctx context.Context, name string) ([]workflowstore.InstalledVersion, error) {
	query := installedVersionSelect
	var args []any
	if name != "" {
		query += ` WHERE workflow_name = ?`
		args = append(args, name)
	}
	query += ` ORDER BY workflow_name, workflow_version`
	rows, err := d.sql.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list installed workflows: %w", err)
	}
	defer func() { _ = rows.Close() }()
	values := make([]workflowstore.InstalledVersion, 0)
	for rows.Next() {
		value, err := scanInstalledVersion(rows)
		if err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate installed workflows: %w", err)
	}
	return values, nil
}

// CreateRunSnapshot freezes the selected workflow and configuration once for an
// existing run aggregate. An identical repeat is idempotent.
func (d *Database) CreateRunSnapshot(ctx context.Context, request workflowstore.RunSnapshotRequest) (workflowstore.RunSnapshot, bool, error) {
	if err := validateRunSnapshotRequest(request); err != nil {
		return workflowstore.RunSnapshot{}, false, err
	}
	installed, err := d.InstalledVersion(ctx, request.WorkflowName, request.WorkflowVersion)
	if err != nil {
		return workflowstore.RunSnapshot{}, false, err
	}
	if installed.Digest != request.WorkflowDigest || !bytes.Equal(installed.Document, request.WorkflowDocument) {
		return workflowstore.RunSnapshot{}, false, fmt.Errorf("%w: selected workflow does not match installed version", workflowstore.ErrVersionConflict)
	}
	var aggregateType string
	if err := d.sql.QueryRowContext(ctx, `SELECT aggregate_type FROM aggregates WHERE aggregate_id = ?`, request.RunID).Scan(&aggregateType); errors.Is(err, sql.ErrNoRows) {
		return workflowstore.RunSnapshot{}, false, fmt.Errorf("%w: run %s", workflowstore.ErrNotFound, request.RunID)
	} else if err != nil {
		return workflowstore.RunSnapshot{}, false, fmt.Errorf("read run aggregate: %w", err)
	} else if aggregateType != "run" {
		return workflowstore.RunSnapshot{}, false, fmt.Errorf("aggregate %s is %s, want run", request.RunID, aggregateType)
	}

	result, err := d.sql.ExecContext(ctx, `INSERT OR IGNORE INTO run_workflow_snapshots(
		run_id, workflow_name, workflow_version, workflow_digest, workflow_json, config_digest, config_snapshot_json, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, request.RunID, request.WorkflowName, request.WorkflowVersion,
		request.WorkflowDigest, string(request.WorkflowDocument), request.ConfigDigest, string(request.ConfigSnapshot), formatTime(request.CreatedAt))
	if err != nil {
		return workflowstore.RunSnapshot{}, false, fmt.Errorf("insert run workflow snapshot: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return workflowstore.RunSnapshot{}, false, fmt.Errorf("inspect run workflow snapshot: %w", err)
	}
	snapshot, err := d.RunSnapshot(ctx, request.RunID)
	if err != nil {
		return workflowstore.RunSnapshot{}, false, err
	}
	if !sameSnapshot(snapshot, request) {
		return workflowstore.RunSnapshot{}, false, fmt.Errorf("%w: run %s already has workflow %s %s (%s)",
			workflowstore.ErrRunSnapshotConflict, request.RunID, snapshot.WorkflowName, snapshot.WorkflowVersion, snapshot.WorkflowDigest)
	}
	return snapshot, rows == 1, nil
}

// RunSnapshot returns the immutable workflow/config selection for one run.
func (d *Database) RunSnapshot(ctx context.Context, runID string) (workflowstore.RunSnapshot, error) {
	var value workflowstore.RunSnapshot
	var workflowJSON, configJSON, createdAt string
	err := d.sql.QueryRowContext(ctx, `SELECT run_id, workflow_name, workflow_version, workflow_digest,
		workflow_json, config_digest, config_snapshot_json, created_at FROM run_workflow_snapshots WHERE run_id = ?`, runID).
		Scan(&value.RunID, &value.WorkflowName, &value.WorkflowVersion, &value.WorkflowDigest,
			&workflowJSON, &value.ConfigDigest, &configJSON, &createdAt)
	if errors.Is(err, sql.ErrNoRows) {
		return workflowstore.RunSnapshot{}, fmt.Errorf("%w: snapshot for run %s", workflowstore.ErrNotFound, runID)
	}
	if err != nil {
		return workflowstore.RunSnapshot{}, fmt.Errorf("read run workflow snapshot: %w", err)
	}
	value.WorkflowDocument = json.RawMessage(workflowJSON)
	value.ConfigSnapshot = json.RawMessage(configJSON)
	value.CreatedAt, err = parseTime(createdAt)
	if err != nil {
		return workflowstore.RunSnapshot{}, fmt.Errorf("run snapshot %s created_at: %w", runID, err)
	}
	return value, nil
}

func scanInstalledVersion(row rowScanner) (workflowstore.InstalledVersion, error) {
	var value workflowstore.InstalledVersion
	var document, installedAt string
	if err := row.Scan(&value.Name, &value.Version, &value.Digest, &document, &value.SourceScope, &value.SourceRef, &installedAt); err != nil {
		return workflowstore.InstalledVersion{}, err
	}
	value.Document = json.RawMessage(document)
	parsed, err := parseTime(installedAt)
	if err != nil {
		return workflowstore.InstalledVersion{}, fmt.Errorf("workflow %s %s installed_at: %w", value.Name, value.Version, err)
	}
	value.InstalledAt = parsed
	return value, nil
}

func validateInstallRequest(request workflowstore.InstallRequest) error {
	if strings.TrimSpace(request.Name) == "" || strings.TrimSpace(request.Version) == "" || strings.TrimSpace(request.SourceRef) == "" {
		return errors.New("workflow name, version, and source reference are required")
	}
	if !lowercaseDigestPattern.MatchString(request.Digest) {
		return errors.New("workflow digest must be 64 lowercase hexadecimal characters")
	}
	if !jsonObject(request.Document) {
		return errors.New("workflow document must be a JSON object")
	}
	document, canonical, digest, err := workflow.Canonicalize(request.Document)
	if err != nil {
		return fmt.Errorf("validate workflow document: %w", err)
	}
	if document.Metadata.Name != request.Name || document.Metadata.Version != request.Version {
		return errors.New("workflow request identity does not match document metadata")
	}
	if !bytes.Equal(canonical, request.Document) || digest != request.Digest {
		return errors.New("workflow document or digest is not canonical")
	}
	if request.SourceScope != workflowstore.ScopeDefault && request.SourceScope != workflowstore.ScopeUser && request.SourceScope != workflowstore.ScopeProject {
		return fmt.Errorf("invalid workflow source scope %q", request.SourceScope)
	}
	if request.InstalledAt.IsZero() {
		return errors.New("workflow installation time is required")
	}
	return nil
}

func validateRunSnapshotRequest(request workflowstore.RunSnapshotRequest) error {
	if strings.TrimSpace(request.RunID) == "" || strings.TrimSpace(request.WorkflowName) == "" || strings.TrimSpace(request.WorkflowVersion) == "" {
		return errors.New("run ID, workflow name, and workflow version are required")
	}
	if !lowercaseDigestPattern.MatchString(request.WorkflowDigest) || !lowercaseDigestPattern.MatchString(request.ConfigDigest) {
		return errors.New("workflow and configuration digests must be 64 lowercase hexadecimal characters")
	}
	if !jsonObject(request.WorkflowDocument) || !jsonObject(request.ConfigSnapshot) {
		return errors.New("workflow and configuration snapshots must be JSON objects")
	}
	workflowHash := sha256.Sum256(request.WorkflowDocument)
	configHash := sha256.Sum256(request.ConfigSnapshot)
	if hex.EncodeToString(workflowHash[:]) != request.WorkflowDigest || hex.EncodeToString(configHash[:]) != request.ConfigDigest {
		return errors.New("workflow or configuration snapshot digest does not match its bytes")
	}
	if request.CreatedAt.IsZero() {
		return errors.New("run snapshot creation time is required")
	}
	return nil
}

func sameSnapshot(snapshot workflowstore.RunSnapshot, request workflowstore.RunSnapshotRequest) bool {
	return snapshot.RunID == request.RunID && snapshot.WorkflowName == request.WorkflowName &&
		snapshot.WorkflowVersion == request.WorkflowVersion && snapshot.WorkflowDigest == request.WorkflowDigest &&
		bytes.Equal(snapshot.WorkflowDocument, request.WorkflowDocument) && snapshot.ConfigDigest == request.ConfigDigest &&
		bytes.Equal(snapshot.ConfigSnapshot, request.ConfigSnapshot)
}
