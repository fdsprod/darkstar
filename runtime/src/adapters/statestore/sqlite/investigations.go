package sqlite

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"

	"darkstar/src/core/identity"
	"darkstar/src/ports/statestore"
)

var _ statestore.InvestigationStore = (*Database)(nil)

func (d *Database) Investigation(ctx context.Context, id string) (statestore.InvestigationCollection, error) {
	return readInvestigation(ctx, d.sql, id)
}

func readInvestigation(ctx context.Context, q rowQueryer, id string) (statestore.InvestigationCollection, error) {
	var value statestore.InvestigationCollection
	err := readScopeJSON(ctx, q, `SELECT record_json FROM investigations WHERE investigation_id=?`, id, &value)
	return value, err
}

func (d *Database) Investigations(ctx context.Context) ([]statestore.InvestigationCollection, error) {
	return investigationRows[statestore.InvestigationCollection](ctx, d.sql, `SELECT record_json FROM investigations ORDER BY investigation_id`)
}

type investigationQueryer interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

func investigationRows[T any](ctx context.Context, q investigationQueryer, query string, args ...any) ([]T, error) {
	rows, err := q.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer func() {
		_ = rows.Close()
	}()
	values := make([]T, 0)
	for rows.Next() {
		var raw string
		var value T
		if err = rows.Scan(&raw); err != nil {
			return nil, err
		}
		if err = json.Unmarshal([]byte(raw), &value); err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	return values, rows.Err()
}

func (d *Database) InvestigationUnits(ctx context.Context, id string) ([]statestore.InvestigationUnit, error) {
	return readInvestigationUnits(ctx, d.sql, id)
}

func readInvestigationUnits(ctx context.Context, q investigationQueryer, id string) ([]statestore.InvestigationUnit, error) {
	return investigationRows[statestore.InvestigationUnit](ctx, q, `SELECT record_json FROM investigation_units WHERE investigation_id=? ORDER BY unit_id`, id)
}

func (d *Database) InvestigationAttempts(ctx context.Context, id string) ([]statestore.InvestigationAttempt, error) {
	return investigationRows[statestore.InvestigationAttempt](ctx, d.sql, `SELECT record_json FROM investigation_attempts WHERE investigation_id=? ORDER BY json_extract(record_json,'$.createdAt'),attempt_id`, id)
}

func updateInvestigationJSON(ctx context.Context, tx *sql.Tx, query string, value any, id string) error {
	encoded, err := json.Marshal(value)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, query, string(encoded), id)
	return err
}

func (d *Database) writeInvestigation(ctx context.Context, tx *sql.Tx, value statestore.InvestigationCollection) error {
	value.Revision++
	value.UpdatedAt = d.now().UTC()
	return updateInvestigationJSON(ctx, tx, `UPDATE investigations SET record_json=? WHERE investigation_id=?`, value, value.CollectionID)
}

func (d *Database) CreateInvestigation(ctx context.Context, value statestore.InvestigationCollection) (statestore.InvestigationCollection, error) {
	if value.SchemaVersion != 1 || value.Status != "prepared" || value.Revision != 1 || value.Concurrency < 1 || value.Concurrency > 8 || value.RepositoryIDs == nil || len(value.RepositoryIDs) > 32 || !scopeSHA.MatchString(value.RequestDigest) || !scopeSHA.MatchString(value.Task.Digest) || !json.Valid(value.Task.Content) || value.Provider.Provider == "" {
		return value, errors.New("invalid investigation specification")
	}
	if fmt.Sprintf("%x", sha256.Sum256(value.Task.Content)) != value.Task.Digest {
		return value, errors.New("investigation task digest mismatch")
	}
	if value.Provider.Provider == "daemon" {
		if value.Provider.Extension != nil || len(value.RepositoryIDs) != 0 {
			return value, errors.New("daemon synthesis requires an empty repository scope and no provider extension")
		}
	} else if value.Provider.Extension == nil || value.Provider.Extension.Validate() != nil {
		return value, errors.New("investigation requires an exact provider extension pin")
	}
	if value.Provider.CapabilityFingerprint == "" {
		return value, errors.New("investigation capability fingerprint is required")
	}
	tx, err := d.sql.BeginTx(ctx, nil)
	if err != nil {
		return value, err
	}
	defer func() {
		_ = tx.Rollback()
	}()
	prior, err := readInvestigation(ctx, tx, value.CollectionID)
	if err == nil {
		if prior.RequestDigest != value.RequestDigest {
			return prior, statestore.ErrRepositoryScopeConflict
		}
		return prior, nil
	}
	if !errors.Is(err, statestore.ErrNotFound) {
		return value, err
	}
	var scope statestore.RepositoryScope
	if err = readScopeJSON(ctx, tx, `SELECT record_json FROM repository_scopes WHERE scope_id=?`, value.ScopeID, &scope); err != nil {
		return value, err
	}
	if scope.ProjectID != value.ProjectID || scope.Digest != value.ScopeDigest || len(scope.Repositories) != len(value.RepositoryIDs) {
		return value, errors.New("investigation scope mismatch")
	}
	ids := make([]string, 0, len(scope.Repositories))
	for _, entry := range scope.Repositories {
		ids = append(ids, entry.Repository.RepositoryID)
	}
	sort.Strings(ids)
	for i, id := range ids {
		if value.RepositoryIDs[i] != id {
			return value, errors.New("investigation repository selection mismatch")
		}
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return value, err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO investigations VALUES(?,?)`, value.CollectionID, string(encoded)); err != nil {
		return value, err
	}
	for _, id := range append(append([]string{}, ids...), "") {
		kind := "repository"
		if id == "" {
			kind = "synthesis"
		}
		unit := statestore.InvestigationUnit{UnitID: identity.Deterministic("investigation_unit_", value.CollectionID+"/"+id), CollectionID: value.CollectionID, Kind: kind, RepositoryID: id, Status: "pending"}
		raw, marshalErr := json.Marshal(unit)
		if marshalErr != nil {
			return value, marshalErr
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO investigation_units VALUES(?,?,?)`, unit.UnitID, value.CollectionID, string(raw)); err != nil {
			return value, err
		}
	}
	if err = d.scopeEvent(ctx, tx, "investigation.prepared", value.CollectionID, value); err != nil {
		return value, err
	}
	return value, tx.Commit()
}

func (d *Database) TransitionInvestigation(ctx context.Context, id string, expected uint64, command, key string) (statestore.InvestigationCollection, error) {
	tx, err := d.sql.BeginTx(ctx, nil)
	if err != nil {
		return statestore.InvestigationCollection{}, err
	}
	defer func() {
		_ = tx.Rollback()
	}()
	value, err := readInvestigation(ctx, tx, id)
	if err != nil {
		return value, err
	}
	var priorCommand string
	var priorExpected uint64
	err = tx.QueryRowContext(ctx, `SELECT command,expected_revision FROM investigation_commands WHERE investigation_id=? AND command_key=?`, id, key).Scan(&priorCommand, &priorExpected)
	if err == nil {
		if priorCommand != command || priorExpected != expected {
			return value, statestore.ErrRepositoryScopeConflict
		}
		return value, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return value, err
	}
	if value.Revision != expected {
		return value, statestore.ErrRepositoryScopeConflict
	}
	units, err := readInvestigationUnits(ctx, tx, id)
	if err != nil {
		return value, err
	}
	switch command {
	case "start":
		if value.Status != "prepared" {
			return value, statestore.ErrRepositoryScopeConflict
		}
		value.Status = "running"
	case "retry":
		if value.Status != "failed" && value.Status != "partial" && value.Status != "cancelled" {
			return value, statestore.ErrRepositoryScopeConflict
		}
		retry := false
		for i := range units {
			if units[i].Status == "uncertain" || units[i].Status == "running" {
				return value, statestore.ErrRepositoryScopeConflict
			}
			if units[i].Kind == "repository" && units[i].Status != "succeeded" {
				units[i].Status = "pending"
				units[i].Reason = ""
				units[i].Result = nil
				retry = true
			}
		}
		for i := range units {
			if units[i].Kind == "synthesis" && (retry || units[i].Status != "succeeded") {
				units[i].Status = "pending"
				units[i].Reason = ""
				units[i].Result = nil
				retry = true
			}
		}
		if !retry {
			return value, statestore.ErrRepositoryScopeConflict
		}
		value.Status = "running"
	case "cancel":
		if value.Status == "succeeded" || value.Status == "failed" || value.Status == "partial" || value.Status == "cancelled" {
			return value, statestore.ErrRepositoryScopeConflict
		}
		value.Status = "cancelling"
		active := false
		for i := range units {
			if units[i].Status == "pending" {
				units[i].Status = "cancelled"
				units[i].Reason = "cancelled before dispatch"
			}
			active = active || units[i].Status == "running" || units[i].Status == "uncertain"
		}
		if !active {
			value.Status = "cancelled"
		}
	default:
		return value, errors.New("unknown investigation command")
	}
	for _, unit := range units {
		if err = updateInvestigationJSON(ctx, tx, `UPDATE investigation_units SET record_json=? WHERE unit_id=?`, unit, unit.UnitID); err != nil {
			return value, err
		}
	}
	if err = d.writeInvestigation(ctx, tx, value); err != nil {
		return value, err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO investigation_commands VALUES(?,?,?,?)`, id, key, command, expected); err != nil {
		return value, err
	}
	if err = d.scopeEvent(ctx, tx, "investigation."+command, id+"/"+fmt.Sprint(expected), struct {
		CollectionID string `json:"collectionId"`
		Command      string `json:"command"`
		Key          string `json:"key"`
	}{id, command, key}); err != nil {
		return value, err
	}
	if err = tx.Commit(); err != nil {
		return value, err
	}
	return d.Investigation(ctx, id)
}
