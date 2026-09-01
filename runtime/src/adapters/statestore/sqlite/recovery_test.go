package sqlite

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/fdsprod/darkstar/runtime/src/core/recovery"
	"github.com/fdsprod/darkstar/runtime/src/ports/statestore"
)

func TestPendingRecoveryAndAtomicDecisions(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	database, err := Open(ctx, filepath.Join(t.TempDir(), "recovery.db"), Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = database.Close()
	}()
	now := time.Date(2026, 9, 1, 2, 0, 0, 0, time.UTC)
	database.now = func() time.Time { return now }

	lease, err := database.AcquireLease(ctx, statestore.AcquireLeaseRequest{
		LeaseID: "lease_process", ScopeKind: statestore.LeaseScopeAttempt, ScopeID: "attempt_scope",
		HolderAttemptID: "attempt_01", DaemonInstanceID: "daemon_old", HostBootID: "boot_01",
		Duration: 30 * time.Second, ProcessIdentity: json.RawMessage(`{"pid":42}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	const runID = "run_01ARZ3NDEKTSV4RRFFQ69G5FAV"
	if _, err := database.SQL().ExecContext(ctx, `INSERT INTO aggregates(aggregate_id, aggregate_type, revision, created_at, updated_at)
		VALUES (?, 'run', 0, ?, ?)`, runID, formatTime(now), formatTime(now)); err != nil {
		t.Fatal(err)
	}
	if _, err := database.SQL().ExecContext(ctx, `INSERT INTO outbox(operation_id, operation_kind, aggregate_id, request_json,
		state, available_at, attempt_count, created_at, updated_at) VALUES ('operation_push', 'push', ?, '{}', 'prepared', ?, 0, ?, ?)`,
		runID, formatTime(now), formatTime(now), formatTime(now)); err != nil {
		t.Fatal(err)
	}

	subjects, err := database.PendingRecovery(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(subjects) != 2 || subjects[0].ID != lease.LeaseID || subjects[0].Authority != "process" ||
		subjects[1].ID != "operation_push" || subjects[1].Authority != "operation:push" {
		t.Fatalf("PendingRecovery() = %#v", subjects)
	}

	resume := recovery.Decision{Outcome: recovery.OutcomeResume, Evidence: json.RawMessage(`{"identity":"exact"}`)}
	if err := database.ApplyRecovery(ctx, "daemon_new", subjects[0], resume); err != nil {
		t.Fatal(err)
	}
	var daemonID, expiresAt string
	var fencingToken uint64
	if err := database.SQL().QueryRowContext(ctx, `SELECT daemon_instance_id, fencing_token, expires_at FROM leases WHERE lease_id = ?`, lease.LeaseID).
		Scan(&daemonID, &fencingToken, &expiresAt); err != nil {
		t.Fatal(err)
	}
	if daemonID != "daemon_new" || fencingToken != lease.FencingToken || expiresAt != formatTime(now.Add(30*time.Second)) {
		t.Fatalf("resumed lease = daemon %s token %d expiry %s", daemonID, fencingToken, expiresAt)
	}
	if err := database.ApplyRecovery(ctx, "daemon_new", subjects[0], resume); err != nil {
		t.Fatalf("repeat exact resume: %v", err)
	}
	changedLease := subjects[0]
	changedLease.Payload = json.RawMessage(`{"changed":true}`)
	err = database.ApplyRecovery(ctx, "daemon_new", changedLease, resume)
	var changedConflict *RecoveryConflictError
	if !errors.As(err, &changedConflict) {
		t.Fatalf("changed subject payload error = %v", err)
	}

	retry := recovery.Decision{Outcome: recovery.OutcomeRetry, Evidence: json.RawMessage(`{"remote":"absent"}`)}
	if err := database.ApplyRecovery(ctx, "daemon_new", subjects[1], retry); err != nil {
		t.Fatal(err)
	}
	var state, availableAt string
	var owner any
	if err := database.SQL().QueryRowContext(ctx, `SELECT state, available_at, lease_owner FROM outbox WHERE operation_id = 'operation_push'`).
		Scan(&state, &availableAt, &owner); err != nil {
		t.Fatal(err)
	}
	if state != "prepared" || availableAt != formatTime(now) || owner != nil {
		t.Fatalf("retried operation = state %s available %s owner %#v", state, availableAt, owner)
	}

	conflict := recovery.Decision{Outcome: recovery.OutcomeAdopt, Evidence: json.RawMessage(`{"remote":"exact"}`)}
	err = database.ApplyRecovery(ctx, "daemon_new", subjects[1], conflict)
	var recoveryConflict *RecoveryConflictError
	if !errors.As(err, &recoveryConflict) {
		t.Fatalf("conflicting decision error = %v", err)
	}
	var decisionCount int
	if err := database.SQL().QueryRowContext(ctx, `SELECT count(*) FROM recovery_decisions`).Scan(&decisionCount); err != nil {
		t.Fatal(err)
	}
	if decisionCount != 2 {
		t.Fatalf("decision count = %d, want 2", decisionCount)
	}
}

func TestReconcileRequiredFencesAmbiguousSubjects(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	database, err := Open(ctx, filepath.Join(t.TempDir(), "ambiguous.db"), Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = database.Close()
	}()
	now := time.Date(2026, 9, 1, 2, 0, 0, 0, time.UTC)
	database.now = func() time.Time { return now }
	_, err = database.AcquireLease(ctx, statestore.AcquireLeaseRequest{
		LeaseID: "lease_repo", ScopeKind: statestore.LeaseScopeRepository, ScopeID: "repo_01",
		HolderAttemptID: "attempt_01", DaemonInstanceID: "daemon_old", HostBootID: "boot_01", Duration: 30 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	subjects, err := database.PendingRecovery(ctx)
	if err != nil || len(subjects) != 1 {
		t.Fatalf("PendingRecovery() = %#v, %v", subjects, err)
	}
	decision := recovery.Decision{Outcome: recovery.OutcomeReconcileRequired, Evidence: json.RawMessage(`{"reason":"workspace_unknown"}`)}
	if err := database.ApplyRecovery(ctx, "daemon_new", subjects[0], decision); err != nil {
		t.Fatal(err)
	}
	var state, evidence string
	if err := database.SQL().QueryRowContext(ctx, `SELECT state, evidence_json FROM leases WHERE lease_id = 'lease_repo'`).Scan(&state, &evidence); err != nil {
		t.Fatal(err)
	}
	if state != "reconcile_required" || evidence != `{"reason":"workspace_unknown"}` {
		t.Fatalf("ambiguous lease = %s %s", state, evidence)
	}
	remaining, err := database.PendingRecovery(ctx)
	if err != nil || len(remaining) != 0 {
		t.Fatalf("PendingRecovery after fence = %#v, %v", remaining, err)
	}
}
