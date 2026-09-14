package sqlite

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"darkstar/src/ports"
	"darkstar/src/ports/statestore"
)

var _ statestore.TrackerMappingStore = (*Database)(nil)

func (d *Database) SaveTrackerMapping(ctx context.Context, value statestore.TrackerMappingRevision) error {
	if value.ProjectID == "" || value.Revision == 0 || value.BindingRevision == 0 || value.CreatedAt.IsZero() || !json.Valid(value.RulesJSON) {
		return backlogFailure(ports.FailureInvalidRequest, "Mapping requires a project, positive revisions, valid rules and creation time.")
	}
	prior, err := d.TrackerMapping(ctx, value.ProjectID, value.Revision)
	if err == nil {
		if prior.BindingRevision == value.BindingRevision && bytes.Equal(prior.RulesJSON, value.RulesJSON) {
			return nil
		}
		return backlogFailure(ports.FailureConflict, "Mapping revision already exists; save a new revision.")
	}
	if !mappingNotFound(err) {
		return err
	}
	_, err = d.sql.ExecContext(ctx, `INSERT INTO tracker_mapping_revisions(project_id,revision,binding_revision,rules_json,created_at) VALUES(?,?,?,?,?)`, value.ProjectID, value.Revision, value.BindingRevision, string(value.RulesJSON), value.CreatedAt.UTC().Format(time.RFC3339Nano))
	return backlogNormalize(err)
}

func mappingNotFound(err error) bool {
	var problem *ports.Failure
	return errors.As(err, &problem) && problem.Code == ports.FailureNotFound
}

func scanTrackerMapping(row interface{ Scan(...any) error }) (statestore.TrackerMappingRevision, error) {
	var result statestore.TrackerMappingRevision
	var encoded, created string
	if err := row.Scan(&result.ProjectID, &result.Revision, &result.BindingRevision, &encoded, &created); err != nil {
		return result, backlogNormalize(err)
	}
	result.RulesJSON = json.RawMessage(encoded)
	var err error
	result.CreatedAt, err = parseTime(created)
	return result, err
}

func (d *Database) TrackerMapping(ctx context.Context, project string, revision uint64) (statestore.TrackerMappingRevision, error) {
	return scanTrackerMapping(d.sql.QueryRowContext(ctx, `SELECT project_id,revision,binding_revision,rules_json,created_at FROM tracker_mapping_revisions WHERE project_id=? AND revision=?`, project, revision))
}

func (d *Database) TrackerMappingHistory(ctx context.Context, project string) ([]statestore.TrackerMappingRevision, error) {
	rows, err := d.sql.QueryContext(ctx, `SELECT project_id,revision,binding_revision,rules_json,created_at FROM tracker_mapping_revisions WHERE project_id=? ORDER BY revision DESC`, project)
	if err != nil {
		return nil, err
	}
	defer func() {
		_ = rows.Close()
	}()
	result := []statestore.TrackerMappingRevision{}
	for rows.Next() {
		value, err := scanTrackerMapping(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, value)
	}
	return result, rows.Err()
}

func (d *Database) ActiveTrackerMapping(ctx context.Context, project string, binding uint64) (statestore.TrackerMappingRevision, error) {
	return scanTrackerMapping(d.sql.QueryRowContext(ctx, `SELECT r.project_id,r.revision,r.binding_revision,r.rules_json,r.created_at FROM tracker_mapping_revisions r JOIN tracker_mapping_active a ON a.project_id=r.project_id AND a.mapping_revision=r.revision WHERE a.project_id=? AND a.binding_revision=?`, project, binding))
}

func (d *Database) ActivateTrackerMapping(ctx context.Context, project string, revision, expected uint64, at time.Time) error {
	if project == "" || revision == 0 || at.IsZero() {
		return backlogFailure(ports.FailureInvalidRequest, "Activation requires an exact mapping revision.")
	}
	tx, err := d.sql.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() {
		_ = tx.Rollback()
	}()
	var binding uint64
	err = tx.QueryRowContext(ctx, `SELECT r.binding_revision FROM tracker_mapping_revisions r JOIN backlog_selected_sources s ON s.project_id=r.project_id AND s.binding_revision=r.binding_revision WHERE r.project_id=? AND r.revision=?`, project, revision).Scan(&binding)
	if errors.Is(err, sql.ErrNoRows) {
		return backlogFailure(ports.FailureConflict, "Mapping is not for the currently selected source.")
	}
	if err != nil {
		return err
	}
	var current uint64
	err = tx.QueryRowContext(ctx, `SELECT mapping_revision FROM tracker_mapping_active WHERE project_id=? AND binding_revision=?`, project, binding).Scan(&current)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if current != expected {
		return backlogFailure(ports.FailureConflict, "Active mapping changed; reload before activation.")
	}
	stamp := at.UTC().Format(time.RFC3339Nano)
	_, err = tx.ExecContext(ctx, `INSERT INTO tracker_mapping_active(project_id,binding_revision,mapping_revision,activated_at) VALUES(?,?,?,?) ON CONFLICT(project_id,binding_revision) DO UPDATE SET mapping_revision=excluded.mapping_revision,activated_at=excluded.activated_at`, project, binding, revision, stamp)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO tracker_mapping_activations(project_id,binding_revision,mapping_revision,previous_revision,activated_at) VALUES(?,?,?,?,?)`, project, binding, revision, expected, stamp)
	if err != nil {
		return err
	}
	return tx.Commit()
}
