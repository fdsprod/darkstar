package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/fdsprod/darkstar/runtime/src/core/recovery"
)

// RecoveryConflictError reports a changed subject or decision within one
// startup pass. Recovery never overwrites its first durable classification.
type RecoveryConflictError struct {
	StartupID   string
	SubjectKind recovery.SubjectKind
	SubjectID   string
}

// CheckIntegrity performs SQLite's bounded structural check before startup
// recovery writes or projection rebuilding are allowed.
func (d *Database) CheckIntegrity(ctx context.Context) error {
	rows, err := d.sql.QueryContext(ctx, `PRAGMA quick_check`)
	if err != nil {
		return fmt.Errorf("run SQLite quick_check: %w", err)
	}
	defer func() {
		_ = rows.Close()
	}()
	ok := false
	for rows.Next() {
		var result string
		if err := rows.Scan(&result); err != nil {
			return fmt.Errorf("read SQLite quick_check: %w", err)
		}
		if result != "ok" {
			return fmt.Errorf("SQLite quick_check failed: %s", result)
		}
		ok = true
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate SQLite quick_check: %w", err)
	}
	if !ok {
		return errors.New("SQLite quick_check returned no result")
	}
	return nil
}

func (e *RecoveryConflictError) Error() string {
	return fmt.Sprintf("startup %s recovery decision for %s %s conflicts with durable state", e.StartupID, e.SubjectKind, e.SubjectID)
}

// PendingRecovery returns non-terminal leases before non-terminal operations.
// Reconciler also sorts defensively so adapter ordering is not a correctness
// dependency.
func (d *Database) PendingRecovery(ctx context.Context) ([]recovery.Subject, error) {
	leases, err := d.pendingLeaseRecovery(ctx)
	if err != nil {
		return nil, err
	}
	operations, err := d.pendingOperationRecovery(ctx)
	if err != nil {
		return nil, err
	}
	return append(leases, operations...), nil
}

func (d *Database) pendingLeaseRecovery(ctx context.Context) ([]recovery.Subject, error) {
	rows, err := d.sql.QueryContext(ctx, leaseSelect+` WHERE state IN ('held', 'releasing') ORDER BY lease_id`)
	if err != nil {
		return nil, fmt.Errorf("query recovery leases: %w", err)
	}
	defer func() {
		_ = rows.Close()
	}()
	var subjects []recovery.Subject
	for rows.Next() {
		lease, err := scanLease(rows)
		if err != nil {
			return nil, fmt.Errorf("scan recovery lease: %w", err)
		}
		authority := "lease:" + string(lease.ScopeKind)
		if len(lease.ProcessIdentity) != 0 {
			authority = "process"
		}
		payload, err := json.Marshal(struct {
			ScopeKind       string          `json:"scopeKind"`
			ScopeID         string          `json:"scopeId"`
			HolderAttemptID string          `json:"holderAttemptId"`
			FencingToken    uint64          `json:"fencingToken"`
			HostBootID      string          `json:"hostBootId"`
			HeartbeatAt     time.Time       `json:"heartbeatAt"`
			ExpiresAt       time.Time       `json:"expiresAt"`
			ProcessIdentity json.RawMessage `json:"processIdentity,omitempty"`
		}{
			ScopeKind: string(lease.ScopeKind), ScopeID: lease.ScopeID,
			HolderAttemptID: lease.HolderAttemptID, FencingToken: lease.FencingToken,
			HostBootID: lease.HostBootID, HeartbeatAt: lease.HeartbeatAt,
			ExpiresAt: lease.ExpiresAt, ProcessIdentity: lease.ProcessIdentity,
		})
		if err != nil {
			return nil, fmt.Errorf("encode recovery lease %s: %w", lease.LeaseID, err)
		}
		subjects = append(subjects, recovery.Subject{
			Kind: recovery.SubjectLease, ID: lease.LeaseID, Authority: authority,
			State: string(lease.State), Payload: payload,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate recovery leases: %w", err)
	}
	return subjects, nil
}

func (d *Database) pendingOperationRecovery(ctx context.Context) ([]recovery.Subject, error) {
	rows, err := d.sql.QueryContext(ctx, `SELECT operation_id, operation_kind, aggregate_id, request_json, state,
		available_at, lease_owner, lease_expires_at, attempt_count
		FROM outbox WHERE state IN ('prepared', 'leased') ORDER BY operation_id`)
	if err != nil {
		return nil, fmt.Errorf("query recovery operations: %w", err)
	}
	defer func() {
		_ = rows.Close()
	}()
	var subjects []recovery.Subject
	for rows.Next() {
		var id, kind, aggregateID, request, state, availableAt string
		var leaseOwner, leaseExpiresAt sql.NullString
		var attemptCount uint64
		if err := rows.Scan(&id, &kind, &aggregateID, &request, &state, &availableAt, &leaseOwner, &leaseExpiresAt, &attemptCount); err != nil {
			return nil, fmt.Errorf("scan recovery operation: %w", err)
		}
		payload, err := json.Marshal(struct {
			Kind           string          `json:"kind"`
			AggregateID    string          `json:"aggregateId"`
			Request        json.RawMessage `json:"request"`
			AvailableAt    string          `json:"availableAt"`
			LeaseOwner     *string         `json:"leaseOwner,omitempty"`
			LeaseExpiresAt *string         `json:"leaseExpiresAt,omitempty"`
			AttemptCount   uint64          `json:"attemptCount"`
		}{
			Kind: kind, AggregateID: aggregateID, Request: json.RawMessage(request),
			AvailableAt: availableAt, LeaseOwner: nullableStringPointer(leaseOwner),
			LeaseExpiresAt: nullableStringPointer(leaseExpiresAt), AttemptCount: attemptCount,
		})
		if err != nil {
			return nil, fmt.Errorf("encode recovery operation %s: %w", id, err)
		}
		subjects = append(subjects, recovery.Subject{
			Kind: recovery.SubjectOperation, ID: id, Authority: "operation:" + kind,
			State: state, Payload: payload,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate recovery operations: %w", err)
	}
	return subjects, nil
}

// ApplyRecovery atomically records the immutable decision and advances the
// subject projection. Repeating the exact decision within one startup is safe.
func (d *Database) ApplyRecovery(ctx context.Context, startupID string, subject recovery.Subject, decision recovery.Decision) (err error) {
	if startupID == "" || len(startupID) > 128 {
		return errors.New("startup ID must be between 1 and 128 bytes")
	}
	subject, err = recovery.NormalizeSubject(subject)
	if err != nil {
		return err
	}
	decision, err = recovery.NormalizeDecision(decision)
	if err != nil {
		return err
	}
	if err := recovery.ValidateDecisionForSubject(subject, decision); err != nil {
		return err
	}
	tx, err := d.sql.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin recovery decision: %w", err)
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()

	exact, found, err := readRecoveryDecision(ctx, tx, startupID, subject, decision)
	if err != nil {
		return err
	}
	if found {
		if exact {
			_ = tx.Rollback()
			return nil
		}
		return &RecoveryConflictError{StartupID: startupID, SubjectKind: subject.Kind, SubjectID: subject.ID}
	}

	now := d.now().UTC().Round(0)
	switch subject.Kind {
	case recovery.SubjectLease:
		err = applyLeaseRecovery(ctx, tx, startupID, subject, decision, now)
	case recovery.SubjectOperation:
		err = applyOperationRecovery(ctx, tx, startupID, subject, decision, now)
	default:
		err = fmt.Errorf("unsupported recovery subject kind %q", subject.Kind)
	}
	if err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO recovery_decisions(
		startup_id, subject_kind, subject_id, subject_authority, subject_state, subject_payload_json, outcome, evidence_json, recorded_at
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`, startupID, subject.Kind, subject.ID, subject.Authority,
		subject.State, string(subject.Payload), decision.Outcome, string(decision.Evidence), formatTime(now)); err != nil {
		return fmt.Errorf("record recovery decision: %w", err)
	}
	if err = tx.Commit(); err != nil {
		return fmt.Errorf("commit recovery decision: %w", err)
	}
	return nil
}

func readRecoveryDecision(ctx context.Context, tx *sql.Tx, startupID string, subject recovery.Subject, decision recovery.Decision) (bool, bool, error) {
	var authority, state, payload, outcome, evidence string
	err := tx.QueryRowContext(ctx, `SELECT subject_authority, subject_state, subject_payload_json, outcome, evidence_json
		FROM recovery_decisions WHERE startup_id = ? AND subject_kind = ? AND subject_id = ?`,
		startupID, subject.Kind, subject.ID).Scan(&authority, &state, &payload, &outcome, &evidence)
	if errors.Is(err, sql.ErrNoRows) {
		return false, false, nil
	}
	if err != nil {
		return false, false, fmt.Errorf("read recovery decision: %w", err)
	}
	exact := authority == subject.Authority && state == subject.State && payload == string(subject.Payload) &&
		outcome == string(decision.Outcome) && evidence == string(decision.Evidence)
	return exact, true, nil
}

func applyLeaseRecovery(ctx context.Context, tx *sql.Tx, startupID string, subject recovery.Subject, decision recovery.Decision, now time.Time) error {
	if subject.State != "held" && subject.State != "releasing" {
		return fmt.Errorf("lease %s cannot recover from %s", subject.ID, subject.State)
	}
	var result sql.Result
	var err error
	switch decision.Outcome {
	case recovery.OutcomeResume:
		if subject.State != "held" {
			return fmt.Errorf("lease %s cannot resume from %s", subject.ID, subject.State)
		}
		result, err = tx.ExecContext(ctx, `UPDATE leases SET daemon_instance_id = ?, heartbeat_at = ?, expires_at = ?
			WHERE lease_id = ? AND state = ?`, startupID, formatTime(now), formatTime(now.Add(30*time.Second)), subject.ID, subject.State)
	case recovery.OutcomeRetry, recovery.OutcomeInterrupt:
		result, err = tx.ExecContext(ctx, `UPDATE leases SET state = 'released', evidence_json = ?, released_at = ?
			WHERE lease_id = ? AND state = ?`, string(decision.Evidence), formatTime(now), subject.ID, subject.State)
	case recovery.OutcomeReconcileRequired:
		result, err = tx.ExecContext(ctx, `UPDATE leases SET state = 'reconcile_required', evidence_json = ?
			WHERE lease_id = ? AND state = ?`, string(decision.Evidence), subject.ID, subject.State)
	default:
		return fmt.Errorf("unsupported lease recovery outcome %q", decision.Outcome)
	}
	return requireRecoveryUpdate(result, err, subject)
}

func applyOperationRecovery(ctx context.Context, tx *sql.Tx, startupID string, subject recovery.Subject, decision recovery.Decision, now time.Time) error {
	if subject.State != "prepared" && subject.State != "leased" {
		return fmt.Errorf("operation %s cannot recover from %s", subject.ID, subject.State)
	}
	var result sql.Result
	var err error
	switch decision.Outcome {
	case recovery.OutcomeAdopt:
		result, err = tx.ExecContext(ctx, `UPDATE outbox SET state = 'committed', lease_owner = NULL,
			lease_expires_at = NULL, observation_json = ?, updated_at = ? WHERE operation_id = ? AND state = ?`,
			string(decision.Evidence), formatTime(now), subject.ID, subject.State)
	case recovery.OutcomeResume:
		result, err = tx.ExecContext(ctx, `UPDATE outbox SET state = 'leased', lease_owner = ?,
			lease_expires_at = ?, observation_json = NULL, updated_at = ? WHERE operation_id = ? AND state = ?`,
			startupID, formatTime(now.Add(30*time.Second)), formatTime(now), subject.ID, subject.State)
	case recovery.OutcomeRetry:
		result, err = tx.ExecContext(ctx, `UPDATE outbox SET state = 'prepared', available_at = ?, lease_owner = NULL,
			lease_expires_at = NULL, observation_json = NULL, updated_at = ? WHERE operation_id = ? AND state = ?`,
			formatTime(now), formatTime(now), subject.ID, subject.State)
	case recovery.OutcomeReconcileRequired:
		result, err = tx.ExecContext(ctx, `UPDATE outbox SET state = 'reconcile_required', lease_owner = NULL,
			lease_expires_at = NULL, observation_json = ?, updated_at = ? WHERE operation_id = ? AND state = ?`,
			string(decision.Evidence), formatTime(now), subject.ID, subject.State)
	default:
		return fmt.Errorf("unsupported operation recovery outcome %q", decision.Outcome)
	}
	return requireRecoveryUpdate(result, err, subject)
}

func requireRecoveryUpdate(result sql.Result, err error, subject recovery.Subject) error {
	if err != nil {
		return fmt.Errorf("transition %s %s: %w", subject.Kind, subject.ID, err)
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("inspect %s %s transition: %w", subject.Kind, subject.ID, err)
	}
	if changed != 1 {
		return fmt.Errorf("%s %s changed after it was observed", subject.Kind, subject.ID)
	}
	return nil
}

func nullableStringPointer(value sql.NullString) *string {
	if !value.Valid {
		return nil
	}
	copy := value.String
	return &copy
}
