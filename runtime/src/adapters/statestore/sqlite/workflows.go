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
	"time"

	"darkstar/src/core/workflow"
	"darkstar/src/ports/workflowstore"
)

var lowercaseDigestPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

var _ workflowstore.Store = (*Database)(nil)

const installedVersionSelect = `SELECT workflow_name, workflow_version, workflow_digest, document_json,
	source_scope, source_reference, installed_at FROM workflow_versions`

const workflowDraftSelect = `SELECT draft_id, workflow_name, scope, scope_reference, base_version,
	revision, document_json, layout_json, document_digest, updated_at FROM workflow_drafts`

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

// CreateDraft creates one mutable authoring aggregate. The idempotency key and
// draft ID both fail closed when reused with different input.
func (d *Database) CreateDraft(ctx context.Context, request workflowstore.CreateDraftRequest) (workflowstore.Draft, bool, error) {
	if err := validateCreateDraftRequest(request); err != nil {
		return workflowstore.Draft{}, false, err
	}
	digest := sha256.Sum256(request.Document)
	result, err := d.sql.ExecContext(ctx, `INSERT OR IGNORE INTO workflow_drafts(
		draft_id, workflow_name, scope, scope_reference, base_version, revision,
		document_json, layout_json, document_digest, idempotency_key, updated_at)
		VALUES (?, ?, ?, ?, NULLIF(?, ''), 1, ?, ?, ?, ?, ?)`, request.ID, request.Name,
		request.Scope, request.ScopeReference, request.BaseVersion, string(request.Document), string(request.Layout),
		hex.EncodeToString(digest[:]), request.IdempotencyKey, formatTime(request.CreatedAt))
	if err != nil {
		return workflowstore.Draft{}, false, fmt.Errorf("create workflow draft: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return workflowstore.Draft{}, false, fmt.Errorf("inspect workflow draft creation: %w", err)
	}
	var draft workflowstore.Draft
	if rows == 1 {
		draft, err = d.Draft(ctx, request.ID)
	} else {
		draft, err = scanWorkflowDraft(d.sql.QueryRowContext(ctx, workflowDraftSelect+` WHERE draft_id = ? OR idempotency_key = ?`, request.ID, request.IdempotencyKey))
	}
	if err != nil {
		return workflowstore.Draft{}, false, fmt.Errorf("read workflow draft: %w", err)
	}
	if draft.ID != request.ID || draft.Name != request.Name || draft.Scope != request.Scope ||
		draft.ScopeReference != request.ScopeReference || draft.BaseVersion != request.BaseVersion ||
		!bytes.Equal(draft.Document, request.Document) || !bytes.Equal(draft.Layout, request.Layout) {
		return workflowstore.Draft{}, false, fmt.Errorf("%w: draft identity or idempotency key was reused", workflowstore.ErrDraftConflict)
	}
	return draft, rows == 1, nil
}

func (d *Database) Draft(ctx context.Context, id string) (workflowstore.Draft, error) {
	value, err := scanWorkflowDraft(d.sql.QueryRowContext(ctx, workflowDraftSelect+` WHERE draft_id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return workflowstore.Draft{}, fmt.Errorf("%w: draft %s", workflowstore.ErrNotFound, id)
	}
	if err != nil {
		return workflowstore.Draft{}, fmt.Errorf("read workflow draft: %w", err)
	}
	return value, nil
}

func (d *Database) Drafts(ctx context.Context) ([]workflowstore.Draft, error) {
	rows, err := d.sql.QueryContext(ctx, workflowDraftSelect+` ORDER BY workflow_name, draft_id`)
	if err != nil {
		return nil, fmt.Errorf("list workflow drafts: %w", err)
	}
	defer func() { _ = rows.Close() }()
	values := make([]workflowstore.Draft, 0)
	for rows.Next() {
		value, err := scanWorkflowDraft(rows)
		if err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	return values, rows.Err()
}

func (d *Database) UpdateDraft(ctx context.Context, request workflowstore.UpdateDraftRequest) (workflowstore.Draft, error) {
	if request.ID == "" || request.ExpectedRevision == 0 || request.Name == "" || request.UpdatedAt.IsZero() ||
		!jsonObject(request.Document) || !jsonObject(request.Layout) {
		return workflowstore.Draft{}, errors.New("complete workflow draft update and positive expected revision are required")
	}
	digest := sha256.Sum256(request.Document)
	result, err := d.sql.ExecContext(ctx, `UPDATE workflow_drafts SET workflow_name = ?, revision = revision + 1,
		document_json = ?, layout_json = ?, document_digest = ?, updated_at = ?
		WHERE draft_id = ? AND revision = ?`, request.Name, string(request.Document), string(request.Layout),
		hex.EncodeToString(digest[:]), formatTime(request.UpdatedAt), request.ID, request.ExpectedRevision)
	if err != nil {
		return workflowstore.Draft{}, fmt.Errorf("update workflow draft: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return workflowstore.Draft{}, fmt.Errorf("inspect workflow draft update: %w", err)
	}
	if rows == 0 {
		if _, err := d.Draft(ctx, request.ID); errors.Is(err, workflowstore.ErrNotFound) {
			return workflowstore.Draft{}, err
		}
		return workflowstore.Draft{}, fmt.Errorf("%w: draft %s expected revision %d", workflowstore.ErrDraftConflict, request.ID, request.ExpectedRevision)
	}
	return d.Draft(ctx, request.ID)
}

func (d *Database) DiscardDraft(ctx context.Context, id string, expectedRevision uint64) error {
	if id == "" || expectedRevision == 0 {
		return errors.New("draft ID and positive expected revision are required")
	}
	result, err := d.sql.ExecContext(ctx, `DELETE FROM workflow_drafts WHERE draft_id = ? AND revision = ?`, id, expectedRevision)
	if err != nil {
		return fmt.Errorf("discard workflow draft: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("inspect workflow draft discard: %w", err)
	}
	if rows == 0 {
		if _, err := d.Draft(ctx, id); errors.Is(err, workflowstore.ErrNotFound) {
			return err
		}
		return fmt.Errorf("%w: draft %s expected revision %d", workflowstore.ErrDraftConflict, id, expectedRevision)
	}
	return nil
}

func (d *Database) ArchiveVersion(ctx context.Context, name, version string, archivedAt time.Time) (workflowstore.Archive, bool, error) {
	if name == "" || version == "" || archivedAt.IsZero() {
		return workflowstore.Archive{}, false, errors.New("workflow archive identity and time are required")
	}
	if _, err := d.InstalledVersion(ctx, name, version); err != nil {
		return workflowstore.Archive{}, false, err
	}
	result, err := d.sql.ExecContext(ctx, `INSERT OR IGNORE INTO workflow_archives(workflow_name, workflow_version, archived_at) VALUES (?, ?, ?)`, name, version, formatTime(archivedAt))
	if err != nil {
		return workflowstore.Archive{}, false, fmt.Errorf("archive workflow version: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return workflowstore.Archive{}, false, err
	}
	var stored string
	if err := d.sql.QueryRowContext(ctx, `SELECT archived_at FROM workflow_archives WHERE workflow_name = ? AND workflow_version = ?`, name, version).Scan(&stored); err != nil {
		return workflowstore.Archive{}, false, err
	}
	parsed, err := parseTime(stored)
	if err != nil {
		return workflowstore.Archive{}, false, err
	}
	return workflowstore.Archive{Name: name, Version: version, ArchivedAt: parsed}, rows == 1, nil
}

func (d *Database) Archives(ctx context.Context) ([]workflowstore.Archive, error) {
	rows, err := d.sql.QueryContext(ctx, `SELECT workflow_name, workflow_version, archived_at FROM workflow_archives ORDER BY workflow_name, workflow_version`)
	if err != nil {
		return nil, fmt.Errorf("list workflow archives: %w", err)
	}
	defer func() { _ = rows.Close() }()
	values := make([]workflowstore.Archive, 0)
	for rows.Next() {
		var value workflowstore.Archive
		var stored string
		if err := rows.Scan(&value.Name, &value.Version, &stored); err != nil {
			return nil, err
		}
		value.ArchivedAt, err = parseTime(stored)
		if err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	return values, rows.Err()
}

func scanWorkflowDraft(row rowScanner) (workflowstore.Draft, error) {
	var value workflowstore.Draft
	var base sql.NullString
	var document, layout, updatedAt string
	if err := row.Scan(&value.ID, &value.Name, &value.Scope, &value.ScopeReference, &base,
		&value.Revision, &document, &layout, &value.DocumentDigest, &updatedAt); err != nil {
		return workflowstore.Draft{}, err
	}
	value.BaseVersion = base.String
	value.Document, value.Layout = json.RawMessage(document), json.RawMessage(layout)
	parsed, err := parseTime(updatedAt)
	if err != nil {
		return workflowstore.Draft{}, fmt.Errorf("workflow draft %s updated_at: %w", value.ID, err)
	}
	value.UpdatedAt = parsed
	return value, nil
}

func validateCreateDraftRequest(request workflowstore.CreateDraftRequest) error {
	if request.ID == "" || request.Name == "" || request.ScopeReference == "" || request.IdempotencyKey == "" || request.CreatedAt.IsZero() {
		return errors.New("workflow draft identity, scope reference, idempotency key, and creation time are required")
	}
	if request.Scope != workflowstore.DraftScopeUser && request.Scope != workflowstore.DraftScopeProject {
		return fmt.Errorf("invalid workflow draft scope %q", request.Scope)
	}
	if !jsonObject(request.Document) || !jsonObject(request.Layout) {
		return errors.New("workflow draft document and layout must be JSON objects")
	}
	return nil
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
