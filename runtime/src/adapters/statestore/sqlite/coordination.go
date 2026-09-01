package sqlite

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/fdsprod/darkstar/runtime/src/ports/statestore"
)

// FencingConflictError reports a compare-and-swap mismatch for a lease scope.
type FencingConflictError struct {
	ScopeKind statestore.LeaseScopeKind
	ScopeID   string
	Expected  uint64
	Actual    uint64
}

func (e *FencingConflictError) Error() string {
	return fmt.Sprintf("%s %s fencing conflict: expected %d, actual %d", e.ScopeKind, e.ScopeID, e.Expected, e.Actual)
}

// LeaseHeldError reports that a scope still has an unreleased owner. Expiry is
// included as evidence, but never treated as permission to reclaim the scope.
type LeaseHeldError struct {
	ScopeKind statestore.LeaseScopeKind
	ScopeID   string
	LeaseID   string
	State     statestore.LeaseState
	ExpiresAt time.Time
}

func (e *LeaseHeldError) Error() string {
	return fmt.Sprintf("%s %s is owned by lease %s in state %s until %s", e.ScopeKind, e.ScopeID, e.LeaseID, e.State, e.ExpiresAt.Format(time.RFC3339Nano))
}

// LeaseGuardConflictError reports a stale or mismatched mutation capability.
type LeaseGuardConflictError struct {
	LeaseID string
}

func (e *LeaseGuardConflictError) Error() string {
	return fmt.Sprintf("lease guard for %s does not match the durable owner", e.LeaseID)
}

// LeaseExpiredError reports that a holder must be reconciled before it may
// mutate or heartbeat again.
type LeaseExpiredError struct {
	LeaseID   string
	ExpiresAt time.Time
}

func (e *LeaseExpiredError) Error() string {
	return fmt.Sprintf("lease %s expired at %s and requires reconciliation", e.LeaseID, e.ExpiresAt.Format(time.RFC3339Nano))
}

// QueueConflictError reports a reused item identity with different immutable
// queue attributes.
type QueueConflictError struct {
	Kind    statestore.QueueKind
	ScopeID string
	ItemID  string
}

func (e *QueueConflictError) Error() string {
	return fmt.Sprintf("queue item %s/%s/%s conflicts with its durable request", e.Kind, e.ScopeID, e.ItemID)
}

// QueueHeadError reports that a repository writer attempted to bypass the
// deterministic head of its queue.
type QueueHeadError struct {
	ScopeID string
	Want    string
	Actual  string
}

func (e *QueueHeadError) Error() string {
	return fmt.Sprintf("repository %s queue head is %s, not %s", e.ScopeID, e.Actual, e.Want)
}

// AcquireLease atomically allocates the next fencing token when the scope has
// no unreleased lease. An expired lease still blocks until explicit recovery.
func (d *Database) AcquireLease(ctx context.Context, request statestore.AcquireLeaseRequest) (lease statestore.Lease, err error) {
	if err := validateAcquireLeaseRequest(request); err != nil {
		return statestore.Lease{}, err
	}
	tx, err := d.sql.BeginTx(ctx, nil)
	if err != nil {
		return statestore.Lease{}, fmt.Errorf("begin lease acquisition: %w", err)
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()

	lease, err = d.acquireLeaseTx(ctx, tx, request, d.now().UTC().Round(0))
	if err != nil {
		return statestore.Lease{}, err
	}
	if err = tx.Commit(); err != nil {
		return statestore.Lease{}, fmt.Errorf("commit lease acquisition: %w", err)
	}
	return lease, nil
}

func (d *Database) acquireLeaseTx(ctx context.Context, tx *sql.Tx, request statestore.AcquireLeaseRequest, now time.Time) (statestore.Lease, error) {
	existing, err := readLeaseByID(ctx, tx, request.LeaseID)
	if err == nil {
		if leaseMatchesRequest(existing, request) {
			return existing, nil
		}
		return statestore.Lease{}, &LeaseGuardConflictError{LeaseID: request.LeaseID}
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return statestore.Lease{}, fmt.Errorf("read lease %s: %w", request.LeaseID, err)
	}

	if _, err := tx.ExecContext(ctx, `INSERT INTO lease_scopes(scope_kind, scope_id, last_fencing_token, updated_at)
		VALUES (?, ?, 0, ?) ON CONFLICT(scope_kind, scope_id) DO NOTHING`, request.ScopeKind, request.ScopeID, formatTime(now)); err != nil {
		return statestore.Lease{}, fmt.Errorf("initialize lease scope: %w", err)
	}
	var actual uint64
	if err := tx.QueryRowContext(ctx, `SELECT last_fencing_token FROM lease_scopes WHERE scope_kind = ? AND scope_id = ?`,
		request.ScopeKind, request.ScopeID).Scan(&actual); err != nil {
		return statestore.Lease{}, fmt.Errorf("read lease scope: %w", err)
	}
	if actual != request.ExpectedFencingToken {
		return statestore.Lease{}, &FencingConflictError{ScopeKind: request.ScopeKind, ScopeID: request.ScopeID, Expected: request.ExpectedFencingToken, Actual: actual}
	}

	active, err := readActiveLease(ctx, tx, request.ScopeKind, request.ScopeID)
	if err == nil {
		return statestore.Lease{}, &LeaseHeldError{ScopeKind: request.ScopeKind, ScopeID: request.ScopeID, LeaseID: active.LeaseID, State: active.State, ExpiresAt: active.ExpiresAt}
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return statestore.Lease{}, fmt.Errorf("read active lease: %w", err)
	}

	token := actual + 1
	expiresAt := now.Add(normalizeLeaseDuration(request.Duration))
	processIdentity, err := nullableJSONObject(request.ProcessIdentity)
	if err != nil {
		return statestore.Lease{}, fmt.Errorf("process identity: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO leases(
		lease_id, scope_kind, scope_id, holder_attempt_id, daemon_instance_id, fencing_token,
		acquired_at, heartbeat_at, expires_at, host_boot_id, process_identity_json, state
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 'held')`,
		request.LeaseID, request.ScopeKind, request.ScopeID, request.HolderAttemptID, request.DaemonInstanceID,
		token, formatTime(now), formatTime(now), formatTime(expiresAt), request.HostBootID, processIdentity); err != nil {
		return statestore.Lease{}, fmt.Errorf("insert lease: %w", err)
	}
	result, err := tx.ExecContext(ctx, `UPDATE lease_scopes SET last_fencing_token = ?, updated_at = ?
		WHERE scope_kind = ? AND scope_id = ? AND last_fencing_token = ?`,
		token, formatTime(now), request.ScopeKind, request.ScopeID, actual)
	if err != nil {
		return statestore.Lease{}, fmt.Errorf("advance fencing token: %w", err)
	}
	if changed, err := result.RowsAffected(); err != nil || changed != 1 {
		return statestore.Lease{}, &FencingConflictError{ScopeKind: request.ScopeKind, ScopeID: request.ScopeID, Expected: actual, Actual: actual + 1}
	}
	return readLeaseByID(ctx, tx, request.LeaseID)
}

// HeartbeatLease extends only an exact, unexpired held lease.
func (d *Database) HeartbeatLease(ctx context.Context, guard statestore.LeaseGuard, duration time.Duration) (statestore.Lease, error) {
	if err := validateLeaseGuard(guard); err != nil {
		return statestore.Lease{}, err
	}
	if err := validateLeaseDuration(duration); err != nil {
		return statestore.Lease{}, err
	}
	now := d.now().UTC().Round(0)
	tx, err := d.sql.BeginTx(ctx, nil)
	if err != nil {
		return statestore.Lease{}, fmt.Errorf("begin lease heartbeat: %w", err)
	}
	defer tx.Rollback()
	lease, err := readGuardedLease(ctx, tx, guard)
	if err != nil {
		return statestore.Lease{}, err
	}
	if lease.State != statestore.LeaseHeld {
		return statestore.Lease{}, fmt.Errorf("lease %s is %s, not held", lease.LeaseID, lease.State)
	}
	if now.Before(lease.HeartbeatAt) {
		return statestore.Lease{}, fmt.Errorf("lease %s clock regressed from %s to %s", lease.LeaseID, lease.HeartbeatAt.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano))
	}
	if now.After(lease.ExpiresAt) {
		return statestore.Lease{}, &LeaseExpiredError{LeaseID: lease.LeaseID, ExpiresAt: lease.ExpiresAt}
	}
	if _, err := tx.ExecContext(ctx, `UPDATE leases SET heartbeat_at = ?, expires_at = ? WHERE lease_id = ?`,
		formatTime(now), formatTime(now.Add(duration)), lease.LeaseID); err != nil {
		return statestore.Lease{}, fmt.Errorf("update lease heartbeat: %w", err)
	}
	lease, err = readLeaseByID(ctx, tx, lease.LeaseID)
	if err != nil {
		return statestore.Lease{}, fmt.Errorf("read heartbeat result: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return statestore.Lease{}, fmt.Errorf("commit lease heartbeat: %w", err)
	}
	return lease, nil
}

// BeginLeaseRelease records that external ownership is being inventoried and
// detached. It is idempotent for an already-releasing matching lease.
func (d *Database) BeginLeaseRelease(ctx context.Context, guard statestore.LeaseGuard) (statestore.Lease, error) {
	return d.transitionGuardedLease(ctx, guard, func(ctx context.Context, tx *sql.Tx, lease statestore.Lease, now time.Time) error {
		if lease.State == statestore.LeaseReleasing {
			return nil
		}
		if lease.State != statestore.LeaseHeld {
			return fmt.Errorf("lease %s cannot begin release from %s", lease.LeaseID, lease.State)
		}
		_, err := tx.ExecContext(ctx, `UPDATE leases SET state = 'releasing' WHERE lease_id = ?`, lease.LeaseID)
		return err
	})
}

// CompleteLeaseRelease records the disposition evidence and releases the scope.
// Reconciliation can also finish here after explicit operator proof.
func (d *Database) CompleteLeaseRelease(ctx context.Context, guard statestore.LeaseGuard, evidence json.RawMessage) (statestore.Lease, error) {
	normalized, err := requiredJSONObject(evidence)
	if err != nil {
		return statestore.Lease{}, fmt.Errorf("release evidence: %w", err)
	}
	return d.transitionGuardedLease(ctx, guard, func(ctx context.Context, tx *sql.Tx, lease statestore.Lease, now time.Time) error {
		if lease.State == statestore.LeaseReleased {
			if bytes.Equal(lease.Evidence, normalized) {
				return nil
			}
			return fmt.Errorf("lease %s was released with different evidence", lease.LeaseID)
		}
		if lease.State != statestore.LeaseReleasing && lease.State != statestore.LeaseReconcileRequired {
			return fmt.Errorf("lease %s cannot complete release from %s", lease.LeaseID, lease.State)
		}
		_, err := tx.ExecContext(ctx, `UPDATE leases SET state = 'released', evidence_json = ?, released_at = ? WHERE lease_id = ?`,
			string(normalized), formatTime(now), lease.LeaseID)
		return err
	})
}

// MarkLeaseReconcileRequired fences the scope while preserving uncertainty.
func (d *Database) MarkLeaseReconcileRequired(ctx context.Context, guard statestore.LeaseGuard, evidence json.RawMessage) (statestore.Lease, error) {
	normalized, err := requiredJSONObject(evidence)
	if err != nil {
		return statestore.Lease{}, fmt.Errorf("reconciliation evidence: %w", err)
	}
	return d.transitionGuardedLease(ctx, guard, func(ctx context.Context, tx *sql.Tx, lease statestore.Lease, now time.Time) error {
		if lease.State == statestore.LeaseReconcileRequired {
			if bytes.Equal(lease.Evidence, normalized) {
				return nil
			}
			return fmt.Errorf("lease %s already requires reconciliation with different evidence", lease.LeaseID)
		}
		if lease.State != statestore.LeaseHeld && lease.State != statestore.LeaseReleasing {
			return fmt.Errorf("lease %s cannot require reconciliation from %s", lease.LeaseID, lease.State)
		}
		_, err := tx.ExecContext(ctx, `UPDATE leases SET state = 'reconcile_required', evidence_json = ? WHERE lease_id = ?`, string(normalized), lease.LeaseID)
		return err
	})
}

func (d *Database) transitionGuardedLease(ctx context.Context, guard statestore.LeaseGuard, transition func(context.Context, *sql.Tx, statestore.Lease, time.Time) error) (lease statestore.Lease, err error) {
	if err := validateLeaseGuard(guard); err != nil {
		return statestore.Lease{}, err
	}
	tx, err := d.sql.BeginTx(ctx, nil)
	if err != nil {
		return statestore.Lease{}, fmt.Errorf("begin lease transition: %w", err)
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()
	lease, err = readGuardedLease(ctx, tx, guard)
	if err != nil {
		return statestore.Lease{}, err
	}
	if err = transition(ctx, tx, lease, d.now().UTC().Round(0)); err != nil {
		return statestore.Lease{}, err
	}
	lease, err = readLeaseByID(ctx, tx, guard.LeaseID)
	if err != nil {
		return statestore.Lease{}, fmt.Errorf("read lease transition: %w", err)
	}
	if err = tx.Commit(); err != nil {
		return statestore.Lease{}, fmt.Errorf("commit lease transition: %w", err)
	}
	return lease, nil
}

// ValidateLease rejects stale, non-held, or expired capabilities before a
// daemon-owned mutation is attempted.
func (d *Database) ValidateLease(ctx context.Context, kind statestore.LeaseScopeKind, scopeID string, guard statestore.LeaseGuard) (statestore.Lease, error) {
	if err := validateLeaseGuard(guard); err != nil {
		return statestore.Lease{}, err
	}
	lease, err := readGuardedLease(ctx, d.sql, guard)
	if err != nil {
		return statestore.Lease{}, err
	}
	if lease.ScopeKind != kind || lease.ScopeID != scopeID {
		return statestore.Lease{}, &LeaseGuardConflictError{LeaseID: guard.LeaseID}
	}
	if lease.State != statestore.LeaseHeld {
		return statestore.Lease{}, fmt.Errorf("lease %s is %s, not held", lease.LeaseID, lease.State)
	}
	if now := d.now().UTC().Round(0); now.After(lease.ExpiresAt) {
		return statestore.Lease{}, &LeaseExpiredError{LeaseID: lease.LeaseID, ExpiresAt: lease.ExpiresAt}
	}
	return lease, nil
}

// Enqueue inserts an immutable scheduling request. Repeating the exact request
// returns the original row and preserves its first-enqueued ordering time.
func (d *Database) Enqueue(ctx context.Context, request statestore.EnqueueRequest) (statestore.QueueEntry, error) {
	normalized, err := validateEnqueueRequest(request)
	if err != nil {
		return statestore.QueueEntry{}, err
	}
	now := d.now().UTC().Round(0)
	_, err = d.sql.ExecContext(ctx, `INSERT INTO queue_entries(queue_kind, scope_id, item_id, priority, available_at, enqueued_at, payload_json)
		VALUES (?, ?, ?, ?, ?, ?, ?) ON CONFLICT(queue_kind, scope_id, item_id) DO NOTHING`,
		request.Kind, request.ScopeID, request.ItemID, request.Priority, formatTime(request.AvailableAt), formatTime(now), string(normalized))
	if err != nil {
		return statestore.QueueEntry{}, fmt.Errorf("enqueue item: %w", err)
	}
	entry, err := readQueueEntry(ctx, d.sql, request.Kind, request.ScopeID, request.ItemID)
	if err != nil {
		return statestore.QueueEntry{}, fmt.Errorf("read enqueued item: %w", err)
	}
	if entry.Priority != request.Priority || !entry.AvailableAt.Equal(request.AvailableAt.UTC()) || !bytes.Equal(entry.Payload, normalized) {
		return statestore.QueueEntry{}, &QueueConflictError{Kind: request.Kind, ScopeID: request.ScopeID, ItemID: request.ItemID}
	}
	return entry, nil
}

// QueueHead returns the highest-priority available item, then the oldest item,
// with the immutable item ID as the final deterministic tie-breaker.
func (d *Database) QueueHead(ctx context.Context, kind statestore.QueueKind, scopeID string) (statestore.QueueEntry, error) {
	if err := validateQueueCoordinates(kind, scopeID, "head"); err != nil {
		return statestore.QueueEntry{}, err
	}
	entry, err := scanQueueEntry(d.sql.QueryRowContext(ctx, `SELECT queue_kind, scope_id, item_id, priority, available_at, enqueued_at, payload_json
		FROM queue_entries WHERE queue_kind = ? AND scope_id = ? AND available_at <= ?
		ORDER BY priority DESC, enqueued_at, item_id LIMIT 1`, kind, scopeID, formatTime(d.now().UTC().Round(0))))
	if errors.Is(err, sql.ErrNoRows) {
		return statestore.QueueEntry{}, &NotFoundError{Kind: "queue head", ID: string(kind) + "/" + scopeID}
	}
	if err != nil {
		return statestore.QueueEntry{}, fmt.Errorf("read queue head: %w", err)
	}
	return entry, nil
}

// RemoveQueueEntry idempotently removes a cancelled or otherwise completed item.
func (d *Database) RemoveQueueEntry(ctx context.Context, kind statestore.QueueKind, scopeID, itemID string) error {
	if err := validateQueueCoordinates(kind, scopeID, itemID); err != nil {
		return err
	}
	if _, err := d.sql.ExecContext(ctx, `DELETE FROM queue_entries WHERE queue_kind = ? AND scope_id = ? AND item_id = ?`, kind, scopeID, itemID); err != nil {
		return fmt.Errorf("remove queue item: %w", err)
	}
	return nil
}

// AcquireRepositoryLock atomically proves that queueItemID is the fair queue
// head, removes it, and allocates the repository's next fencing token.
func (d *Database) AcquireRepositoryLock(ctx context.Context, queueItemID string, request statestore.AcquireLeaseRequest) (lease statestore.Lease, err error) {
	if request.ScopeKind != statestore.LeaseScopeRepository {
		return statestore.Lease{}, fmt.Errorf("repository lock requires repository scope, got %s", request.ScopeKind)
	}
	if err := validateAcquireLeaseRequest(request); err != nil {
		return statestore.Lease{}, err
	}
	if queueItemID == "" {
		return statestore.Lease{}, errors.New("repository queue item ID is required")
	}
	tx, err := d.sql.BeginTx(ctx, nil)
	if err != nil {
		return statestore.Lease{}, fmt.Errorf("begin repository lock acquisition: %w", err)
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()
	now := d.now().UTC().Round(0)
	existing, readErr := readLeaseByID(ctx, tx, request.LeaseID)
	if readErr == nil {
		if !leaseMatchesRequest(existing, request) {
			return statestore.Lease{}, &LeaseGuardConflictError{LeaseID: request.LeaseID}
		}
		if err = tx.Commit(); err != nil {
			return statestore.Lease{}, fmt.Errorf("commit repository lock adoption: %w", err)
		}
		return existing, nil
	}
	if !errors.Is(readErr, sql.ErrNoRows) {
		return statestore.Lease{}, fmt.Errorf("read repository lease: %w", readErr)
	}
	head, err := scanQueueEntry(tx.QueryRowContext(ctx, `SELECT queue_kind, scope_id, item_id, priority, available_at, enqueued_at, payload_json
		FROM queue_entries WHERE queue_kind = 'repository_write' AND scope_id = ? AND available_at <= ?
		ORDER BY priority DESC, enqueued_at, item_id LIMIT 1`, request.ScopeID, formatTime(now)))
	if errors.Is(err, sql.ErrNoRows) {
		return statestore.Lease{}, &NotFoundError{Kind: "repository queue head", ID: request.ScopeID}
	}
	if err != nil {
		return statestore.Lease{}, fmt.Errorf("read repository queue head: %w", err)
	}
	if head.ItemID != queueItemID {
		return statestore.Lease{}, &QueueHeadError{ScopeID: request.ScopeID, Want: queueItemID, Actual: head.ItemID}
	}
	lease, err = d.acquireLeaseTx(ctx, tx, request, now)
	if err != nil {
		return statestore.Lease{}, err
	}
	result, err := tx.ExecContext(ctx, `DELETE FROM queue_entries WHERE queue_kind = 'repository_write' AND scope_id = ? AND item_id = ?`, request.ScopeID, queueItemID)
	if err != nil {
		return statestore.Lease{}, fmt.Errorf("dequeue repository writer: %w", err)
	}
	if changed, err := result.RowsAffected(); err != nil || changed != 1 {
		return statestore.Lease{}, fmt.Errorf("repository queue item %s disappeared during lock acquisition", queueItemID)
	}
	if err = tx.Commit(); err != nil {
		return statestore.Lease{}, fmt.Errorf("commit repository lock acquisition: %w", err)
	}
	return lease, nil
}

func validateAcquireLeaseRequest(request statestore.AcquireLeaseRequest) error {
	if request.LeaseID == "" || request.ScopeID == "" || request.HolderAttemptID == "" || request.DaemonInstanceID == "" || request.HostBootID == "" {
		return errors.New("lease ID, scope ID, holder attempt ID, daemon instance ID, and host boot ID are required")
	}
	switch request.ScopeKind {
	case statestore.LeaseScopeAttempt, statestore.LeaseScopeRepository, statestore.LeaseScopeWorktree:
	default:
		return fmt.Errorf("invalid lease scope kind %q", request.ScopeKind)
	}
	if err := validateLeaseDuration(normalizeLeaseDuration(request.Duration)); err != nil {
		return err
	}
	if _, err := nullableJSONObject(request.ProcessIdentity); err != nil {
		return fmt.Errorf("process identity: %w", err)
	}
	return nil
}

func validateLeaseDuration(duration time.Duration) error {
	if duration < statestore.MaximumHeartbeatInterval {
		return fmt.Errorf("lease duration must be at least %s: %s", statestore.MaximumHeartbeatInterval, duration)
	}
	return nil
}

func normalizeLeaseDuration(duration time.Duration) time.Duration {
	if duration == 0 {
		return statestore.DefaultLeaseDuration
	}
	return duration
}

func validateLeaseGuard(guard statestore.LeaseGuard) error {
	if guard.LeaseID == "" || guard.HolderAttemptID == "" || guard.DaemonInstanceID == "" || guard.FencingToken == 0 {
		return errors.New("lease ID, holder attempt ID, daemon instance ID, and fencing token are required")
	}
	return nil
}

func validateEnqueueRequest(request statestore.EnqueueRequest) (json.RawMessage, error) {
	if err := validateQueueCoordinates(request.Kind, request.ScopeID, request.ItemID); err != nil {
		return nil, err
	}
	if request.Priority < 0 {
		return nil, fmt.Errorf("queue priority must be non-negative: %d", request.Priority)
	}
	if request.AvailableAt.IsZero() {
		return nil, errors.New("queue availability time is required")
	}
	return requiredJSONObject(request.Payload)
}

func validateQueueCoordinates(kind statestore.QueueKind, scopeID, itemID string) error {
	switch kind {
	case statestore.QueueAttempt, statestore.QueueRepositoryWrite:
	default:
		return fmt.Errorf("invalid queue kind %q", kind)
	}
	if scopeID == "" || itemID == "" {
		return errors.New("queue scope ID and item ID are required")
	}
	return nil
}

func readGuardedLease(ctx context.Context, query leaseRowQueryer, guard statestore.LeaseGuard) (statestore.Lease, error) {
	lease, err := readLeaseByID(ctx, query, guard.LeaseID)
	if errors.Is(err, sql.ErrNoRows) {
		return statestore.Lease{}, &NotFoundError{Kind: "lease", ID: guard.LeaseID}
	}
	if err != nil {
		return statestore.Lease{}, fmt.Errorf("read guarded lease: %w", err)
	}
	if lease.HolderAttemptID != guard.HolderAttemptID || lease.DaemonInstanceID != guard.DaemonInstanceID || lease.FencingToken != guard.FencingToken {
		return statestore.Lease{}, &LeaseGuardConflictError{LeaseID: guard.LeaseID}
	}
	return lease, nil
}

func leaseMatchesRequest(lease statestore.Lease, request statestore.AcquireLeaseRequest) bool {
	processIdentity, err := nullableJSONObject(request.ProcessIdentity)
	if err != nil {
		return false
	}
	return lease.ScopeKind == request.ScopeKind && lease.ScopeID == request.ScopeID &&
		lease.HolderAttemptID == request.HolderAttemptID && lease.DaemonInstanceID == request.DaemonInstanceID &&
		lease.HostBootID == request.HostBootID && lease.FencingToken == request.ExpectedFencingToken+1 &&
		lease.ExpiresAt.Sub(lease.AcquiredAt) == normalizeLeaseDuration(request.Duration) &&
		bytes.Equal(lease.ProcessIdentity, rawNullableJSON(processIdentity))
}

type leaseRowQueryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func readLeaseByID(ctx context.Context, query leaseRowQueryer, leaseID string) (statestore.Lease, error) {
	return scanLease(query.QueryRowContext(ctx, leaseSelect+` WHERE lease_id = ?`, leaseID))
}

func readActiveLease(ctx context.Context, query leaseRowQueryer, kind statestore.LeaseScopeKind, scopeID string) (statestore.Lease, error) {
	return scanLease(query.QueryRowContext(ctx, leaseSelect+` WHERE scope_kind = ? AND scope_id = ? AND state <> 'released'`, kind, scopeID))
}

func scanLease(row *sql.Row) (statestore.Lease, error) {
	var lease statestore.Lease
	var acquiredAt, heartbeatAt, expiresAt string
	var processIdentity, evidence, releasedAt sql.NullString
	err := row.Scan(&lease.LeaseID, &lease.ScopeKind, &lease.ScopeID, &lease.HolderAttemptID, &lease.DaemonInstanceID,
		&lease.FencingToken, &acquiredAt, &heartbeatAt, &expiresAt, &lease.HostBootID, &processIdentity,
		&lease.State, &evidence, &releasedAt)
	if err != nil {
		return statestore.Lease{}, err
	}
	lease.AcquiredAt, err = parseTime(acquiredAt)
	if err != nil {
		return statestore.Lease{}, err
	}
	lease.HeartbeatAt, err = parseTime(heartbeatAt)
	if err != nil {
		return statestore.Lease{}, err
	}
	lease.ExpiresAt, err = parseTime(expiresAt)
	if err != nil {
		return statestore.Lease{}, err
	}
	if processIdentity.Valid {
		lease.ProcessIdentity = json.RawMessage(processIdentity.String)
	}
	if evidence.Valid {
		lease.Evidence = json.RawMessage(evidence.String)
	}
	if releasedAt.Valid {
		parsed, parseErr := parseTime(releasedAt.String)
		if parseErr != nil {
			return statestore.Lease{}, parseErr
		}
		lease.ReleasedAt = &parsed
	}
	return lease, nil
}

const leaseSelect = `SELECT lease_id, scope_kind, scope_id, holder_attempt_id, daemon_instance_id, fencing_token,
	acquired_at, heartbeat_at, expires_at, host_boot_id, process_identity_json, state, evidence_json, released_at FROM leases`

func readQueueEntry(ctx context.Context, query leaseRowQueryer, kind statestore.QueueKind, scopeID, itemID string) (statestore.QueueEntry, error) {
	return scanQueueEntry(query.QueryRowContext(ctx, `SELECT queue_kind, scope_id, item_id, priority, available_at, enqueued_at, payload_json
		FROM queue_entries WHERE queue_kind = ? AND scope_id = ? AND item_id = ?`, kind, scopeID, itemID))
}

func scanQueueEntry(row *sql.Row) (statestore.QueueEntry, error) {
	var entry statestore.QueueEntry
	var availableAt, enqueuedAt, payload string
	if err := row.Scan(&entry.Kind, &entry.ScopeID, &entry.ItemID, &entry.Priority, &availableAt, &enqueuedAt, &payload); err != nil {
		return statestore.QueueEntry{}, err
	}
	var err error
	entry.AvailableAt, err = parseTime(availableAt)
	if err != nil {
		return statestore.QueueEntry{}, err
	}
	entry.EnqueuedAt, err = parseTime(enqueuedAt)
	if err != nil {
		return statestore.QueueEntry{}, err
	}
	entry.Payload = json.RawMessage(payload)
	return entry, nil
}

func requiredJSONObject(value json.RawMessage) (json.RawMessage, error) {
	if len(value) == 0 {
		return nil, errors.New("JSON object is required")
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(value, &object); err != nil || object == nil {
		return nil, errors.New("value must be a JSON object")
	}
	var buffer bytes.Buffer
	if err := json.Compact(&buffer, value); err != nil {
		return nil, errors.New("value must be valid JSON")
	}
	return buffer.Bytes(), nil
}

func nullableJSONObject(value json.RawMessage) (any, error) {
	if len(value) == 0 {
		return nil, nil
	}
	normalized, err := requiredJSONObject(value)
	if err != nil {
		return nil, err
	}
	return string(normalized), nil
}

func rawNullableJSON(value any) json.RawMessage {
	if value == nil {
		return nil
	}
	return json.RawMessage(value.(string))
}
