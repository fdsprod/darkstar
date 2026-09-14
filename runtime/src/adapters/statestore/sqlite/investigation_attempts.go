package sqlite

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"time"

	"darkstar/src/core/identity"
	"darkstar/src/ports/statestore"
)

func readInvestigationAttempt(ctx context.Context, q rowQueryer, id string) (statestore.InvestigationAttempt, error) {
	var value statestore.InvestigationAttempt
	err := readScopeJSON(ctx, q, `SELECT record_json FROM investigation_attempts WHERE attempt_id=?`, id, &value)
	return value, err
}

func (d *Database) ClaimInvestigation(ctx context.Context, owner string, global int, ttl time.Duration) (statestore.InvestigationClaim, bool, error) {
	if owner == "" || global < 0 || ttl < time.Second {
		return statestore.InvestigationClaim{}, false, errors.New("invalid investigation claim")
	}
	tx, err := d.sql.BeginTx(ctx, nil)
	if err != nil {
		return statestore.InvestigationClaim{}, false, err
	}
	defer func() {
		_ = tx.Rollback()
	}()
	attempts, err := investigationRows[statestore.InvestigationAttempt](ctx, tx, `SELECT record_json FROM investigation_attempts WHERE json_extract(record_json,'$.state') IN ('running','uncertain') ORDER BY json_extract(record_json,'$.leaseExpiresAt'),attempt_id`)
	if err != nil {
		return statestore.InvestigationClaim{}, false, err
	}
	now := d.now().UTC()
	for _, attempt := range attempts {
		if attempt.LeaseExpiresAt.After(now) {
			continue
		}
		// Expiration authorizes observation/cancellation only, never redispatch.
		attempt.OwnerID = owner
		attempt.State = "uncertain"
		attempt.LeaseExpiresAt = now.Add(ttl)
		attempt.UpdatedAt = now
		if err = updateInvestigationJSON(ctx, tx, `UPDATE investigation_attempts SET record_json=? WHERE attempt_id=?`, attempt, attempt.AttemptID); err != nil {
			return statestore.InvestigationClaim{}, false, err
		}
		if err = d.scopeEvent(ctx, tx, "investigation.reconciliation_claimed", attempt.AttemptID+"/"+fmt.Sprint(now.UnixNano()), attempt); err != nil {
			return statestore.InvestigationClaim{}, false, err
		}
		if err = tx.Commit(); err != nil {
			return statestore.InvestigationClaim{}, false, err
		}
		return statestore.InvestigationClaim{Attempt: attempt, Reconcile: true}, true, nil
	}
	if len(attempts) >= global {
		return statestore.InvestigationClaim{}, false, nil
	}
	collections, err := investigationRows[statestore.InvestigationCollection](ctx, tx, `SELECT record_json FROM investigations WHERE json_extract(record_json,'$.status')='running' ORDER BY json_extract(record_json,'$.createdAt'),investigation_id`)
	if err != nil {
		return statestore.InvestigationClaim{}, false, err
	}
	for _, collection := range collections {
		active := 0
		for _, attempt := range attempts {
			if attempt.CollectionID == collection.CollectionID {
				active++
			}
		}
		if active >= collection.Concurrency {
			continue
		}
		units, readErr := readInvestigationUnits(ctx, tx, collection.CollectionID)
		if readErr != nil {
			return statestore.InvestigationClaim{}, false, readErr
		}
		reposPending := false
		for _, unit := range units {
			if unit.Kind == "repository" && (unit.Status == "pending" || unit.Status == "running" || unit.Status == "uncertain") {
				reposPending = true
			}
		}
		for _, unit := range units {
			if unit.Status != "pending" || (unit.Kind == "synthesis" && reposPending) {
				continue
			}
			var number uint64
			if err = tx.QueryRowContext(ctx, `SELECT count(*)+1 FROM investigation_attempts WHERE unit_id=?`, unit.UnitID).Scan(&number); err != nil {
				return statestore.InvestigationClaim{}, false, err
			}
			attempt := statestore.InvestigationAttempt{AttemptID: identity.Deterministic("attempt_", unit.UnitID+"/"+fmt.Sprint(number)), CollectionID: collection.CollectionID, UnitID: unit.UnitID, Number: number, State: "running", OwnerID: owner, LeaseExpiresAt: now.Add(ttl), CreatedAt: now, UpdatedAt: now}
			raw, marshalErr := json.Marshal(attempt)
			if marshalErr != nil {
				return statestore.InvestigationClaim{}, false, marshalErr
			}
			if _, err = tx.ExecContext(ctx, `INSERT INTO investigation_attempts VALUES(?,?,?,?)`, attempt.AttemptID, collection.CollectionID, unit.UnitID, string(raw)); err != nil {
				return statestore.InvestigationClaim{}, false, err
			}
			unit.CurrentAttemptID = attempt.AttemptID
			unit.Status = "running"
			if err = updateInvestigationJSON(ctx, tx, `UPDATE investigation_units SET record_json=? WHERE unit_id=?`, unit, unit.UnitID); err != nil {
				return statestore.InvestigationClaim{}, false, err
			}
			if err = d.writeInvestigation(ctx, tx, collection); err != nil {
				return statestore.InvestigationClaim{}, false, err
			}
			if err = d.scopeEvent(ctx, tx, "investigation.attempt_claimed", attempt.AttemptID, attempt); err != nil {
				return statestore.InvestigationClaim{}, false, err
			}
			if err = tx.Commit(); err != nil {
				return statestore.InvestigationClaim{}, false, err
			}
			return statestore.InvestigationClaim{Attempt: attempt}, true, nil
		}
	}
	return statestore.InvestigationClaim{}, false, nil
}

func (d *Database) RenewInvestigationAttempt(ctx context.Context, id, owner string, ttl time.Duration) error {
	tx, err := d.sql.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() {
		_ = tx.Rollback()
	}()
	attempt, err := readInvestigationAttempt(ctx, tx, id)
	if err != nil {
		return err
	}
	if attempt.OwnerID != owner || (attempt.State != "running" && attempt.State != "uncertain") {
		return statestore.ErrRepositoryScopeConflict
	}
	attempt.LeaseExpiresAt = d.now().UTC().Add(ttl)
	if err = updateInvestigationJSON(ctx, tx, `UPDATE investigation_attempts SET record_json=? WHERE attempt_id=?`, attempt, id); err != nil {
		return err
	}
	return tx.Commit()
}

func (d *Database) ObserveInvestigationAttempt(ctx context.Context, id, owner string, observation statestore.InvestigationObservation) error {
	tx, err := d.sql.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() {
		_ = tx.Rollback()
	}()
	attempt, err := readInvestigationAttempt(ctx, tx, id)
	if err != nil {
		return err
	}
	if attempt.OwnerID != owner || (attempt.State != "running" && attempt.State != "uncertain") {
		return statestore.ErrRepositoryScopeConflict
	}
	key := observation.Kind
	switch observation.Kind {
	case "prepared":
		if observation.CapabilityFingerprint == "" || !scopeSHA.MatchString(observation.ContextDigest) || !json.Valid(observation.Request) || len(observation.Request) > 4*1024*1024 || fmt.Sprintf("%x", sha256.Sum256(observation.Request)) != observation.ContextDigest {
			return errors.New("invalid prepared investigation inputs")
		}
		if attempt.ContextDigest != "" && (attempt.ContextDigest != observation.ContextDigest || attempt.CapabilityFingerprint != observation.CapabilityFingerprint) {
			return statestore.ErrRepositoryScopeConflict
		}
		attempt.ContextDigest = observation.ContextDigest
		attempt.CapabilityFingerprint = observation.CapabilityFingerprint
		attempt.Request = observation.Request
	case "handle":
		if observation.Handle == nil || observation.Handle.AttemptID != id || attempt.ContextDigest == "" || attempt.ContextDigest != observation.ContextDigest || attempt.CapabilityFingerprint != observation.CapabilityFingerprint {
			return errors.New("provider handle lacks matching prepared inputs")
		}
		if attempt.Handle != nil && !reflect.DeepEqual(attempt.Handle, observation.Handle) {
			return statestore.ErrRepositoryScopeConflict
		}
		attempt.Handle = observation.Handle
	case "event":
		if observation.Event == nil || observation.Event.AttemptID != id || observation.Event.Sequence == 0 {
			return errors.New("invalid investigation provider event")
		}
		key = fmt.Sprintf("event/%d", observation.Event.Sequence)
		if observation.Event.Sequence > attempt.LastSequence {
			attempt.LastSequence = observation.Event.Sequence
		}
	case "submission":
		if !json.Valid(observation.Submission) || len(observation.Submission) > 1024*1024 {
			return errors.New("invalid investigation submission")
		}
		if attempt.Submission != nil && statestore.RepositoryScopeContentDigest(attempt.Submission) != statestore.RepositoryScopeContentDigest(observation.Submission) {
			return statestore.ErrRepositoryScopeConflict
		}
		attempt.Submission = observation.Submission
	case "result":
		if attempt.ContextDigest == "" || len(attempt.Request) == 0 {
			return errors.New("investigation result requires retained prepared inputs")
		}
		if err = validInvestigationResult(observation.Result); err != nil {
			return err
		}
		var unit statestore.InvestigationUnit
		if err = readScopeJSON(ctx, tx, `SELECT record_json FROM investigation_units WHERE unit_id=?`, attempt.UnitID, &unit); err != nil {
			return err
		}
		if observation.Result.Kind != unit.Kind || observation.Result.RepositoryID != unit.RepositoryID {
			return errors.New("investigation result belongs to another work unit")
		}
		if attempt.Result != nil && statestore.RepositoryScopeContentDigest(attempt.Result) != statestore.RepositoryScopeContentDigest(observation.Result) {
			return statestore.ErrRepositoryScopeConflict
		}
		attempt.Result = observation.Result
	default:
		return errors.New("unknown investigation observation")
	}
	raw, err := json.Marshal(observation)
	if err != nil {
		return err
	}
	var previous string
	err = tx.QueryRowContext(ctx, `SELECT record_json FROM investigation_observations WHERE attempt_id=? AND observation_id=?`, id, key).Scan(&previous)
	if err == nil {
		if previous != string(raw) {
			return statestore.ErrRepositoryScopeConflict
		}
		return nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO investigation_observations VALUES(?,?,?,?)`, id, key, observation.Kind, string(raw)); err != nil {
		return err
	}
	attempt.UpdatedAt = d.now().UTC()
	if err = updateInvestigationJSON(ctx, tx, `UPDATE investigation_attempts SET record_json=? WHERE attempt_id=?`, attempt, id); err != nil {
		return err
	}
	if err = d.scopeEvent(ctx, tx, "investigation.attempt_observed", id+"/"+key, observation); err != nil {
		return err
	}
	return tx.Commit()
}

func validInvestigationResult(result *statestore.InvestigationUnitResult) error {
	if result == nil || (result.Kind != "repository" && result.Kind != "synthesis") || (result.Quality != "complete" && result.Quality != "partial" && result.Quality != "missing") || !json.Valid(result.Findings) || len(result.Findings) > 1024*1024 || result.Artifact.ArtifactID == "" || result.Artifact.Version == 0 || !scopeSHA.MatchString(result.Digest) {
		return errors.New("invalid validated investigation result")
	}
	return nil
}

func (d *Database) CompleteInvestigationAttempt(ctx context.Context, id, owner, state, reason string) (statestore.InvestigationAttempt, error) {
	if state != "succeeded" && state != "failed" && state != "cancelled" && state != "uncertain" {
		return statestore.InvestigationAttempt{}, errors.New("invalid investigation outcome")
	}
	tx, err := d.sql.BeginTx(ctx, nil)
	if err != nil {
		return statestore.InvestigationAttempt{}, err
	}
	defer func() {
		_ = tx.Rollback()
	}()
	attempt, err := readInvestigationAttempt(ctx, tx, id)
	if err != nil {
		return attempt, err
	}
	if attempt.OwnerID != owner {
		return attempt, statestore.ErrRepositoryScopeConflict
	}
	if attempt.State != "running" && attempt.State != "uncertain" {
		if attempt.State == state {
			return attempt, nil
		}
		return attempt, statestore.ErrRepositoryScopeConflict
	}
	if state == "succeeded" && attempt.Result == nil {
		return attempt, errors.New("success requires a retained validated artifact")
	}
	if state != "succeeded" && reason == "" {
		return attempt, errors.New("non-success requires an explicit reason")
	}
	collection, err := readInvestigation(ctx, tx, attempt.CollectionID)
	if err != nil {
		return attempt, err
	}
	var unit statestore.InvestigationUnit
	if err = readScopeJSON(ctx, tx, `SELECT record_json FROM investigation_units WHERE unit_id=?`, attempt.UnitID, &unit); err != nil {
		return attempt, err
	}
	if unit.CurrentAttemptID != id {
		return attempt, statestore.ErrRepositoryScopeConflict
	}
	attempt.State = state
	attempt.Reason = reason
	attempt.UpdatedAt = d.now().UTC()
	unit.Status = state
	unit.Reason = reason
	if state == "succeeded" {
		unit.Result = attempt.Result
	}
	if err = updateInvestigationJSON(ctx, tx, `UPDATE investigation_attempts SET record_json=? WHERE attempt_id=?`, attempt, id); err != nil {
		return attempt, err
	}
	if err = updateInvestigationJSON(ctx, tx, `UPDATE investigation_units SET record_json=? WHERE unit_id=?`, unit, unit.UnitID); err != nil {
		return attempt, err
	}
	units, err := readInvestigationUnits(ctx, tx, collection.CollectionID)
	if err != nil {
		return attempt, err
	}
	pending := false
	partial := false
	synthesis := ""
	for _, current := range units {
		pending = pending || current.Status == "pending" || current.Status == "running" || current.Status == "uncertain"
		partial = partial || current.Status != "succeeded" || (current.Result != nil && current.Result.Quality != "complete")
		if current.Kind == "synthesis" {
			synthesis = current.Status
		}
	}
	if !pending {
		switch {
		case collection.Status == "cancelling":
			collection.Status = "cancelled"
		case synthesis != "succeeded":
			collection.Status = "failed"
		case partial:
			collection.Status = "partial"
		default:
			collection.Status = "succeeded"
		}
	}
	if err = d.writeInvestigation(ctx, tx, collection); err != nil {
		return attempt, err
	}
	if err = d.scopeEvent(ctx, tx, "investigation.attempt_completed", id+"/"+fmt.Sprint(attempt.UpdatedAt.UnixNano()), attempt); err != nil {
		return attempt, err
	}
	return attempt, tx.Commit()
}
