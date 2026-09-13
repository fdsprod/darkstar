package sqlite

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"darkstar/src/core/trackercontract"
	"darkstar/src/ports"
	"darkstar/src/ports/statestore"
	"darkstar/src/ports/tracker"
)

func sourceHash(value []byte) string {
	return fmt.Sprintf("%x", sha256.Sum256(value))
}

func currentSource(ctx context.Context, query rowQueryer, lineage statestore.SourceLineage) (statestore.BacklogCachedTicket, tracker.Pin, error) {
	baseline, baselineErr := scanBacklogCache(query.QueryRowContext(ctx, `SELECT `+backlogCacheColumns+` FROM backlog_cached_tickets c JOIN backlog_observations o ON o.observation_id=c.observation_id WHERE c.project_id=? AND c.binding_revision=? AND c.ticket_key=?`, lineage.ProjectID, lineage.BindingRevision, lineage.TicketKey))
	if baselineErr != nil && !sourceNotFound(baselineErr) {
		return baseline, tracker.Pin{}, baselineErr
	}
	var baselinePin tracker.Pin
	var refresh statestore.BacklogRefreshState
	if baselineErr == nil {
		var encoded string
		if err := query.QueryRowContext(ctx, `SELECT state_json FROM backlog_refreshes WHERE project_id=? AND binding_revision=?`, lineage.ProjectID, lineage.BindingRevision).Scan(&encoded); err != nil {
			return baseline, baselinePin, backlogNormalize(err)
		}
		var envelope backlogCheckpointJSON
		if strictBacklogJSON([]byte(encoded), &envelope) != nil || envelope.Version != "darkstar.backlog-checkpoint/v1" || !validBacklogCheckpoint(envelope.State) {
			return baseline, baselinePin, backlogFailure(ports.FailureProtocolDrift, "source checkpoint is invalid")
		}
		refresh = envelope.State
		baselinePin = refresh.Pin
	}
	checked, checkErr := scanBacklogCache(query.QueryRowContext(ctx, `SELECT ?,?,o.ticket_key,o.observation_id,c.state,c.checked_at,0,c.reason,c.evidence_ref,`+backlogObservationColumns+` FROM source_work_checks c JOIN backlog_observations o ON o.observation_id=c.observation_id WHERE c.work_id=? AND c.lineage_revision=?`, lineage.ProjectID, lineage.BindingRevision, lineage.WorkID, lineage.Revision))
	if checkErr != nil && !sourceNotFound(checkErr) {
		return checked, tracker.Pin{}, checkErr
	}
	if checkErr == nil && (baselineErr != nil || !checked.CheckedAt.Before(baseline.CheckedAt)) {
		var pinJSON string
		if err := query.QueryRowContext(ctx, `SELECT pin_json FROM source_work_checks WHERE work_id=? AND lineage_revision=?`, lineage.WorkID, lineage.Revision).Scan(&pinJSON); err != nil {
			return checked, tracker.Pin{}, err
		}
		if err := strictBacklogJSON([]byte(pinJSON), &baselinePin); err != nil {
			return checked, tracker.Pin{}, err
		}
		baseline, baselineErr = checked, nil
	}
	if baselineErr != nil {
		return baseline, tracker.Pin{}, baselineErr
	}
	if refresh.Failure != nil && (refresh.Failure.Code == ports.FailurePermissionDenied || refresh.Failure.Code == ports.FailureUnauthenticated) && !refresh.UpdatedAt.Before(baseline.CheckedAt) {
		baseline.State = statestore.BacklogInaccessible
		baseline.Reason = "source access is unavailable; retained content remains readable"
	}
	return baseline, baselinePin, nil
}

func (d *Database) CurrentSourceObservation(ctx context.Context, work string) (statestore.BacklogCachedTicket, error) {
	lineage, err := workLineage(ctx, d.sql, work)
	if err != nil {
		return statestore.BacklogCachedTicket{}, err
	}
	value, _, err := currentSource(ctx, d.sql, lineage)
	return value, err
}

func insertSourceObservation(ctx context.Context, tx *sql.Tx, observation statestore.BacklogObservation) error {
	if err := validateBacklogObservation(observation); err != nil {
		return err
	}
	ref, _ := json.Marshal(observation.Ref)
	if _, err := tx.ExecContext(ctx, `INSERT INTO backlog_observations VALUES (?,?,?,?,?,?,?,?) ON CONFLICT(observation_id) DO NOTHING`, observation.ID, observation.TicketKey, observation.NativeRevision, observation.ContentDigest, string(ref), string(observation.Ticket), formatTime(observation.ObservedAt), observation.EvidenceRef); err != nil {
		return err
	}
	var digest string
	if err := tx.QueryRowContext(ctx, `SELECT content_digest FROM backlog_observations WHERE observation_id=?`, observation.ID).Scan(&digest); err != nil {
		return err
	}
	if digest != observation.ContentDigest {
		return backlogFailure(ports.FailureProtocolDrift, "source reused an immutable revision with different content")
	}
	return nil
}

func (d *Database) RecordSourceCheck(ctx context.Context, request statestore.SourceCheckMutation) error {
	if request.WorkID == "" || request.ExpectedLineageRevision == 0 || request.CheckedAt.IsZero() || request.CheckedAt.After(d.now().UTC().Add(time.Minute)) {
		return backlogFailure(ports.FailureInvalidRequest, "source check requires exact lineage, pin and observation time")
	}
	tx, err := d.sql.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() {
		_ = tx.Rollback()
	}()
	if _, err := tx.ExecContext(ctx, `UPDATE global_positions SET last_position=last_position WHERE singleton=1`); err != nil {
		return err
	}
	lineage, err := workLineage(ctx, tx, request.WorkID)
	if err != nil {
		return err
	}
	previous, previousPin, previousErr := currentSource(ctx, tx, lineage)
	if _, inaccessible := request.Outcome.(statestore.SourceInaccessible); inaccessible && trackercontract.ValidatePin(request.Pin, request.Pin) != nil && previousErr == nil {
		request.Pin = previousPin
	}
	if lineage.Revision != request.ExpectedLineageRevision || trackercontract.ValidatePin(request.Pin, request.Pin) != nil || request.Pin.BindingRevision != fmt.Sprint(lineage.BindingRevision) || request.Pin.AdapterID != lineage.Ref.Namespace.Provider {
		return backlogFailure(ports.FailureConflict, "source check lineage or adapter changed")
	}
	var sourceJSON string
	if err := tx.QueryRowContext(ctx, `SELECT source_json FROM backlog_bindings WHERE project_id=? AND revision=?`, lineage.ProjectID, lineage.BindingRevision).Scan(&sourceJSON); err != nil {
		return err
	}
	if namespace, err := backlogSourceNamespace(lineage.ProjectID, sourceJSON, request.Pin); err != nil || namespace != lineage.Ref.Namespace {
		return backlogFailure(ports.FailureConflict, "source check differs from its immutable connection")
	}
	if _, err := physicalLineage(ctx, tx, request.WorkID); sourceNotFound(err) {
		if err := insertLineage(ctx, tx, lineage); err != nil {
			return err
		}
	} else if err != nil {
		return err
	}
	if previousErr != nil && !sourceNotFound(previousErr) {
		return previousErr
	}
	if previousErr == nil && request.CheckedAt.Before(previous.CheckedAt) {
		return backlogFailure(ports.FailureConflict, "a newer source check is already retained")
	}
	observationID, state, reason, evidence := previous.ObservationID, statestore.BacklogAvailable, "", previous.Observation.EvidenceRef
	switch outcome := request.Outcome.(type) {
	case statestore.SourceObserved:
		if outcome.Observation.Ref != lineage.Ref || !outcome.Observation.ObservedAt.Equal(request.CheckedAt) {
			return backlogFailure(ports.FailureInvalidRequest, "exact source check returned another ticket")
		}
		if err := insertSourceObservation(ctx, tx, outcome.Observation); err != nil {
			return err
		}
		observationID, evidence = outcome.Observation.ID, outcome.Observation.EvidenceRef
	case statestore.SourceUnchanged:
		if previousErr != nil || outcome.ObservationID != previous.ObservationID {
			return backlogFailure(ports.FailureConflict, "unchanged check does not match retained observation")
		}
	case statestore.SourceMissing:
		if previousErr != nil || outcome.EvidenceRef == "" {
			return backlogFailure(ports.FailureInvalidRequest, "missing check requires retained content and exact evidence")
		}
		state, evidence, reason = statestore.BacklogMissing, outcome.EvidenceRef, "exact source lookup reports missing; history retained"
	case statestore.SourceInaccessible:
		if previousErr != nil {
			return previousErr
		}
		state, reason = statestore.BacklogInaccessible, "source access unavailable; history retained"
	default:
		return backlogFailure(ports.FailureInvalidRequest, "unknown source check outcome")
	}
	pin, err := json.Marshal(request.Pin)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO source_work_checks VALUES (?,?,?,?,?,?,?,?) ON CONFLICT(work_id,lineage_revision) DO UPDATE SET observation_id=excluded.observation_id,state=excluded.state,checked_at=excluded.checked_at,reason=excluded.reason,evidence_ref=excluded.evidence_ref,pin_json=excluded.pin_json`, request.WorkID, lineage.Revision, observationID, state, formatTime(request.CheckedAt), reason, evidence, string(pin))
	if err != nil {
		return err
	}
	return tx.Commit()
}
