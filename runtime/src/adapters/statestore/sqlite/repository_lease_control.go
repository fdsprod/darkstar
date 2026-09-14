package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"darkstar/src/ports/statestore"
)

type repositoryPauseEvidence struct {
	Kind             string `json:"kind"`
	RunID            string `json:"runId"`
	AttemptID        string `json:"attemptId"`
	CommandID        string `json:"commandId"`
	AfterPosition    uint64 `json:"afterPosition"`
	ProviderThreadID string `json:"providerThreadId"`
	ProviderTurnID   string `json:"providerTurnId"`
	ProcessOwnerID   string `json:"processOwnerId"`
}

// PrepareRepositoryPause records orchestration intent before the reader is
// stopped. It never claims the provider terminated or releases writer ownership.
func (d *Database) PrepareRepositoryPause(ctx context.Context, runID, daemonID, commandID string) error {
	tx, err := d.sql.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() {
		_ = tx.Rollback()
	}()
	leases, err := repositoryLeasesForRun(ctx, tx, runID)
	if err != nil {
		return err
	}
	var position uint64
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(global_position),0) FROM events`).Scan(&position); err != nil {
		return err
	}
	for _, lease := range leases {
		if lease.DaemonInstanceID != daemonID || lease.State != statestore.LeaseHeld {
			continue
		}
		attempt, err := readAttemptProjection(ctx, tx, lease.HolderAttemptID)
		if err != nil {
			return err
		}
		evidence, err := json.Marshal(repositoryPauseEvidence{Kind: "repository_pause_intent", RunID: runID, AttemptID: attempt.AttemptID, CommandID: commandID, AfterPosition: position, ProviderThreadID: attempt.ProviderThreadID, ProviderTurnID: attempt.ProviderTurnID, ProcessOwnerID: attempt.ProcessOwnerID})
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE leases SET state='reconcile_required',evidence_json=? WHERE lease_id=? AND state='held'`, string(evidence), lease.LeaseID); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// RepositoryLease reads the exact fenced owner without treating expiry or a
// paused observer as authorization for another writer.
func (d *Database) RepositoryLease(ctx context.Context, guard statestore.LeaseGuard) (statestore.Lease, error) {
	lease, err := readGuardedLease(ctx, d.sql, guard)
	if err == nil && lease.ScopeKind != statestore.LeaseScopeRepository {
		err = errors.New("lease is not a repository writer")
	}
	return lease, err
}

func (d *Database) OwnedRepositoryLease(ctx context.Context, leaseID, attemptID, daemonID string) (statestore.Lease, error) {
	lease, err := readLeaseByID(ctx, d.sql, leaseID)
	if errors.Is(err, sql.ErrNoRows) {
		return lease, statestore.ErrNotFound
	}
	if err == nil && (lease.ScopeKind != statestore.LeaseScopeRepository || lease.HolderAttemptID != attemptID || lease.DaemonInstanceID != daemonID) {
		err = &LeaseGuardConflictError{LeaseID: leaseID}
	}
	return lease, err
}

// ResumeRepositoryLease reactivates the same daemon/attempt after a completed
// pause and explicit resume. Uncertain exits and cross-daemon recovery need
// independent process reconciliation and cannot use this transition.
func (d *Database) ResumeRepositoryLease(ctx context.Context, guard statestore.LeaseGuard) (statestore.Lease, error) {
	return d.transitionGuardedLease(ctx, guard, func(ctx context.Context, tx *sql.Tx, lease statestore.Lease, now time.Time) error {
		var evidence repositoryPauseEvidence
		if lease.ScopeKind != statestore.LeaseScopeRepository || lease.State != statestore.LeaseReconcileRequired || json.Unmarshal(lease.Evidence, &evidence) != nil || evidence.Kind != "repository_pause_intent" || evidence.AttemptID != guard.HolderAttemptID {
			return errors.New("repository writer has no resumable pause evidence")
		}
		attempt, err := readAttemptProjection(ctx, tx, guard.HolderAttemptID)
		if err != nil {
			return err
		}
		if attempt.RunID != evidence.RunID || attempt.Status != statestore.AttemptRunning || evidence.ProviderThreadID == "" || evidence.ProviderTurnID == "" || evidence.ProcessOwnerID == "" || attempt.ProviderThreadID != evidence.ProviderThreadID || attempt.ProviderTurnID != evidence.ProviderTurnID || attempt.ProcessOwnerID != evidence.ProcessOwnerID {
			return errors.New("paused writer provider identity no longer matches")
		}
		var pausePosition, resumePosition uint64
		if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(global_position),0) FROM events WHERE aggregate_id=? AND kind='run.paused' AND command_id=? AND global_position>?`, evidence.RunID, evidence.CommandID, evidence.AfterPosition).Scan(&pausePosition); err != nil {
			return err
		}
		if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(global_position),0) FROM events WHERE aggregate_id=? AND kind='run.resumed' AND global_position>?`, evidence.RunID, pausePosition).Scan(&resumePosition); err != nil {
			return err
		}
		run, err := readRunProjection(ctx, tx, evidence.RunID)
		if err != nil {
			return err
		}
		if pausePosition == 0 || resumePosition == 0 || (run.Status != statestore.RunQueued && run.Status != statestore.RunRunning) {
			return errors.New("paused repository writer requires a completed pause and explicit resume")
		}
		_, err = tx.ExecContext(ctx, `UPDATE leases SET state='held',evidence_json=NULL,heartbeat_at=?,expires_at=? WHERE lease_id=?`, formatTime(now), formatTime(now.Add(statestore.DefaultLeaseDuration)), lease.LeaseID)
		return err
	})
}

// ReleaseCancelledRepositoryLeases requires exact durable provider cancellation
// evidence for each attempt. A cancelled run or elapsed lease alone grants no
// permission to free a possibly active writer.
func (d *Database) ReleaseCancelledRepositoryLeases(ctx context.Context, runID, daemonID string) error {
	tx, err := d.sql.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() {
		_ = tx.Rollback()
	}()
	leases, err := repositoryLeasesForRun(ctx, tx, runID)
	if err != nil {
		return err
	}
	for _, lease := range leases {
		var position uint64
		var disposition string
		err := tx.QueryRowContext(ctx, `SELECT global_position,json_extract(data_json,'$.providerCancellation.disposition') FROM events
		 WHERE aggregate_id=? AND kind IN ('attempt.cancelled','attempt.cancellation_reconciled')
		 AND json_extract(data_json,'$.providerCancellation.disposition') IN ('graceful','forced','already_terminal')
		 ORDER BY global_position DESC LIMIT 1`, lease.HolderAttemptID).Scan(&position, &disposition)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return err
		}
		attempt, err := readAttemptProjection(ctx, tx, lease.HolderAttemptID)
		if err != nil {
			return err
		}
		if attempt.Status != statestore.AttemptCancelled {
			return errors.New("provider cancellation evidence does not match current attempt")
		}
		evidence, _ := json.Marshal(map[string]any{"kind": "confirmed_provider_cancellation", "attemptId": lease.HolderAttemptID, "eventPosition": position, "disposition": disposition, "reconciledBy": daemonID})
		if _, err := tx.ExecContext(ctx, `UPDATE leases SET state='released',evidence_json=?,released_at=? WHERE lease_id=? AND state<>'released'`, string(evidence), formatTime(d.now().UTC()), lease.LeaseID); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func repositoryLeasesForRun(ctx context.Context, tx *sql.Tx, runID string) ([]statestore.Lease, error) {
	rows, err := tx.QueryContext(ctx, leaseSelect+` WHERE scope_kind='repository' AND state<>'released' AND holder_attempt_id IN (SELECT attempt_id FROM attempt_projection WHERE run_id=?)`, runID)
	if err != nil {
		return nil, err
	}
	defer func() {
		_ = rows.Close()
	}()
	var values []statestore.Lease
	for rows.Next() {
		lease, err := scanLease(rows)
		if err != nil {
			return nil, fmt.Errorf("read run repository lease: %w", err)
		}
		values = append(values, lease)
	}
	return values, rows.Err()
}
