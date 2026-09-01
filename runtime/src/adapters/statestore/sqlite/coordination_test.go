package sqlite

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/fdsprod/darkstar/runtime/src/ports/statestore"
)

var coordinationTestTime = time.Date(2026, time.September, 1, 1, 2, 3, 0, time.UTC)

func TestLeaseLifecycleHeartbeatsAndMonotonicFencing(t *testing.T) {
	t.Parallel()

	database, clock := openCoordinationTestDatabase(t)
	ctx := context.Background()
	request := leaseRequest("lease_01", "repo_01", "attempt_01", 0)
	request.ProcessIdentity = json.RawMessage(`{"pid":123,"ownerNonce":"nonce"}`)

	lease, err := database.AcquireLease(ctx, request)
	if err != nil {
		t.Fatalf("acquire first lease: %v", err)
	}
	if lease.FencingToken != 1 || lease.State != statestore.LeaseHeld || !lease.ExpiresAt.Equal(coordinationTestTime.Add(30*time.Second)) {
		t.Fatalf("first lease = %#v, want held token 1 for 30 seconds", lease)
	}
	adopted, err := database.AcquireLease(ctx, request)
	if err != nil {
		t.Fatalf("adopt acquisition after lost response: %v", err)
	}
	if adopted.LeaseID != lease.LeaseID || adopted.FencingToken != lease.FencingToken {
		t.Fatalf("adopted lease = %#v, want %#v", adopted, lease)
	}

	contender := leaseRequest("lease_02", "repo_01", "attempt_02", 1)
	_, err = database.AcquireLease(ctx, contender)
	var held *LeaseHeldError
	if !errors.As(err, &held) || held.LeaseID != lease.LeaseID {
		t.Fatalf("contending acquisition error = %v, want LeaseHeldError for %s", err, lease.LeaseID)
	}

	*clock = clock.Add(5 * time.Second)
	guard := leaseGuard(lease)
	heartbeat, err := database.HeartbeatLease(ctx, guard, statestore.DefaultLeaseDuration)
	if err != nil {
		t.Fatalf("heartbeat lease: %v", err)
	}
	if !heartbeat.HeartbeatAt.Equal(*clock) || !heartbeat.ExpiresAt.Equal(clock.Add(30*time.Second)) {
		t.Fatalf("heartbeat result = %#v", heartbeat)
	}
	stale := guard
	stale.FencingToken++
	if _, err := database.ValidateLease(ctx, statestore.LeaseScopeRepository, request.ScopeID, stale); err == nil {
		t.Fatal("stale fencing token unexpectedly validated")
	}

	releasing, err := database.BeginLeaseRelease(ctx, guard)
	if err != nil || releasing.State != statestore.LeaseReleasing {
		t.Fatalf("begin release = %#v, %v", releasing, err)
	}
	released, err := database.CompleteLeaseRelease(ctx, guard, json.RawMessage(`{"workspace":"clean","process":"absent"}`))
	if err != nil || released.State != statestore.LeaseReleased || released.ReleasedAt == nil {
		t.Fatalf("complete release = %#v, %v", released, err)
	}
	if _, err := database.ValidateLease(ctx, statestore.LeaseScopeRepository, request.ScopeID, guard); err == nil {
		t.Fatal("released lease unexpectedly validated")
	}

	next, err := database.AcquireLease(ctx, contender)
	if err != nil {
		t.Fatalf("acquire next lease: %v", err)
	}
	if next.FencingToken != 2 {
		t.Fatalf("next fencing token = %d, want 2", next.FencingToken)
	}
}

func TestExpiredLeaseBlocksReclaimUntilReconciled(t *testing.T) {
	t.Parallel()

	database, clock := openCoordinationTestDatabase(t)
	ctx := context.Background()
	request := leaseRequest("lease_expired", "repo_expired", "attempt_old", 0)
	lease, err := database.AcquireLease(ctx, request)
	if err != nil {
		t.Fatalf("acquire lease: %v", err)
	}
	*clock = clock.Add(31 * time.Second)
	guard := leaseGuard(lease)
	_, err = database.HeartbeatLease(ctx, guard, statestore.DefaultLeaseDuration)
	var expired *LeaseExpiredError
	if !errors.As(err, &expired) {
		t.Fatalf("late heartbeat error = %v, want LeaseExpiredError", err)
	}

	nextRequest := leaseRequest("lease_replacement", request.ScopeID, "attempt_new", 1)
	_, err = database.AcquireLease(ctx, nextRequest)
	var held *LeaseHeldError
	if !errors.As(err, &held) {
		t.Fatalf("expired-scope acquisition error = %v, want LeaseHeldError", err)
	}
	reconcile, err := database.MarkLeaseReconcileRequired(ctx, guard, json.RawMessage(`{"process":"proven_absent","workspace":"clean"}`))
	if err != nil || reconcile.State != statestore.LeaseReconcileRequired {
		t.Fatalf("mark reconciliation = %#v, %v", reconcile, err)
	}
	if _, err := database.AcquireLease(ctx, nextRequest); !errors.As(err, &held) {
		t.Fatalf("reconciliation scope acquisition error = %v, want LeaseHeldError", err)
	}
	if _, err := database.CompleteLeaseRelease(ctx, guard, json.RawMessage(`{"resolution":"release_after_proof"}`)); err != nil {
		t.Fatalf("release reconciled lease: %v", err)
	}
	replacement, err := database.AcquireLease(ctx, nextRequest)
	if err != nil {
		t.Fatalf("acquire after reconciliation: %v", err)
	}
	if replacement.FencingToken != 2 {
		t.Fatalf("replacement fencing token = %d, want 2", replacement.FencingToken)
	}
}

func TestQueueOrderingAvailabilityAndIdempotency(t *testing.T) {
	t.Parallel()

	database, clock := openCoordinationTestDatabase(t)
	ctx := context.Background()
	scope := "repo_queue"
	requests := []statestore.EnqueueRequest{
		queueRequest(scope, "item_low", 1, *clock),
		queueRequest(scope, "item_high_b", 5, *clock),
		queueRequest(scope, "item_high_a", 5, *clock),
		queueRequest(scope, "item_future", 9, clock.Add(time.Minute)),
	}
	for _, request := range requests {
		if _, err := database.Enqueue(ctx, request); err != nil {
			t.Fatalf("enqueue %s: %v", request.ItemID, err)
		}
	}
	head, err := database.QueueHead(ctx, statestore.QueueRepositoryWrite, scope)
	if err != nil {
		t.Fatalf("read queue head: %v", err)
	}
	if head.ItemID != "item_high_a" {
		t.Fatalf("queue head = %s, want item_high_a priority/tie-break order", head.ItemID)
	}

	adopted, err := database.Enqueue(ctx, requests[2])
	if err != nil {
		t.Fatalf("repeat exact enqueue: %v", err)
	}
	if !adopted.EnqueuedAt.Equal(coordinationTestTime) {
		t.Fatalf("repeat enqueue changed order time to %s", adopted.EnqueuedAt)
	}
	conflict := requests[2]
	conflict.Priority++
	_, err = database.Enqueue(ctx, conflict)
	var queueConflict *QueueConflictError
	if !errors.As(err, &queueConflict) {
		t.Fatalf("conflicting enqueue error = %v, want QueueConflictError", err)
	}

	if err := database.RemoveQueueEntry(ctx, statestore.QueueRepositoryWrite, scope, head.ItemID); err != nil {
		t.Fatalf("remove queue head: %v", err)
	}
	next, err := database.QueueHead(ctx, statestore.QueueRepositoryWrite, scope)
	if err != nil || next.ItemID != "item_high_b" {
		t.Fatalf("next queue head = %#v, %v; want item_high_b", next, err)
	}
}

func TestRepositoryLockAcquisitionIsFairAtomicAndIdempotent(t *testing.T) {
	t.Parallel()

	database, clock := openCoordinationTestDatabase(t)
	ctx := context.Background()
	scope := "repo_lock"
	for _, request := range []statestore.EnqueueRequest{
		queueRequest(scope, "attempt_second", 1, *clock),
		queueRequest(scope, "attempt_first", 2, *clock),
	} {
		if _, err := database.Enqueue(ctx, request); err != nil {
			t.Fatalf("enqueue repository writer: %v", err)
		}
	}

	firstRequest := leaseRequest("lease_first", scope, "attempt_first", 0)
	if _, err := database.AcquireRepositoryLock(ctx, "attempt_second", firstRequest); err == nil {
		t.Fatal("non-head repository writer unexpectedly acquired lock")
	} else {
		var headError *QueueHeadError
		if !errors.As(err, &headError) || headError.Actual != "attempt_first" {
			t.Fatalf("non-head error = %v, want QueueHeadError for attempt_first", err)
		}
	}

	first, err := database.AcquireRepositoryLock(ctx, "attempt_first", firstRequest)
	if err != nil {
		t.Fatalf("acquire repository lock: %v", err)
	}
	adopted, err := database.AcquireRepositoryLock(ctx, "attempt_first", firstRequest)
	if err != nil || adopted.LeaseID != first.LeaseID {
		t.Fatalf("adopt repository lock = %#v, %v", adopted, err)
	}
	head, err := database.QueueHead(ctx, statestore.QueueRepositoryWrite, scope)
	if err != nil || head.ItemID != "attempt_second" {
		t.Fatalf("queue after first lock = %#v, %v", head, err)
	}

	secondRequest := leaseRequest("lease_second", scope, "attempt_second", 1)
	_, err = database.AcquireRepositoryLock(ctx, "attempt_second", secondRequest)
	var held *LeaseHeldError
	if !errors.As(err, &held) {
		t.Fatalf("second writer error = %v, want LeaseHeldError", err)
	}
	head, err = database.QueueHead(ctx, statestore.QueueRepositoryWrite, scope)
	if err != nil || head.ItemID != "attempt_second" {
		t.Fatalf("failed acquisition removed queue item: %#v, %v", head, err)
	}

	guard := leaseGuard(first)
	if _, err := database.BeginLeaseRelease(ctx, guard); err != nil {
		t.Fatalf("begin first release: %v", err)
	}
	if _, err := database.CompleteLeaseRelease(ctx, guard, json.RawMessage(`{"workspace":"clean"}`)); err != nil {
		t.Fatalf("complete first release: %v", err)
	}
	second, err := database.AcquireRepositoryLock(ctx, "attempt_second", secondRequest)
	if err != nil {
		t.Fatalf("acquire second repository lock: %v", err)
	}
	if second.FencingToken != 2 {
		t.Fatalf("second repository fencing token = %d, want 2", second.FencingToken)
	}
	if _, err := database.QueueHead(ctx, statestore.QueueRepositoryWrite, scope); err == nil {
		t.Fatal("repository queue unexpectedly retained acquired item")
	}
}

func TestHeldLeaseSurvivesDatabaseRestart(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "durable-lease.db")
	first, err := Open(ctx, path, Options{})
	if err != nil {
		t.Fatalf("open first database: %v", err)
	}
	first.now = func() time.Time { return coordinationTestTime }
	request := leaseRequest("lease_restart", "repo_restart", "attempt_restart", 0)
	lease, err := first.AcquireLease(ctx, request)
	if err != nil {
		t.Fatalf("acquire durable lease: %v", err)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("close first database: %v", err)
	}

	second, err := Open(ctx, path, Options{})
	if err != nil {
		t.Fatalf("reopen database: %v", err)
	}
	t.Cleanup(func() { _ = second.Close() })
	second.now = func() time.Time { return coordinationTestTime.Add(time.Second) }
	resumed, err := second.ValidateLease(ctx, statestore.LeaseScopeRepository, request.ScopeID, leaseGuard(lease))
	if err != nil {
		t.Fatalf("validate lease after restart: %v", err)
	}
	if resumed.LeaseID != lease.LeaseID || resumed.FencingToken != lease.FencingToken {
		t.Fatalf("resumed lease = %#v, want %#v", resumed, lease)
	}
}

func TestConcurrentDatabaseConnectionsCreateOneScopeOwner(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "contended-lease.db")
	first, err := Open(ctx, path, Options{})
	if err != nil {
		t.Fatalf("open first database: %v", err)
	}
	t.Cleanup(func() { _ = first.Close() })
	second, err := Open(ctx, path, Options{})
	if err != nil {
		t.Fatalf("open second database: %v", err)
	}
	t.Cleanup(func() { _ = second.Close() })
	first.now = func() time.Time { return coordinationTestTime }
	second.now = func() time.Time { return coordinationTestTime }

	start := make(chan struct{})
	results := make(chan error, 2)
	for index, database := range []*Database{first, second} {
		request := leaseRequest("lease_contender_"+string(rune('a'+index)), "repo_contended", "attempt_"+string(rune('a'+index)), 0)
		go func() {
			<-start
			_, acquireErr := database.AcquireLease(ctx, request)
			results <- acquireErr
		}()
	}
	close(start)
	successes := 0
	for range 2 {
		if acquireErr := <-results; acquireErr == nil {
			successes++
		}
	}
	if successes != 1 {
		t.Fatalf("concurrent acquisition successes = %d, want exactly 1", successes)
	}
	var active, token uint64
	if err := first.SQL().QueryRowContext(ctx, `SELECT count(*) FROM leases WHERE scope_kind = 'repository' AND scope_id = 'repo_contended' AND state <> 'released'`).Scan(&active); err != nil {
		t.Fatalf("count active owners: %v", err)
	}
	if err := first.SQL().QueryRowContext(ctx, `SELECT last_fencing_token FROM lease_scopes WHERE scope_kind = 'repository' AND scope_id = 'repo_contended'`).Scan(&token); err != nil {
		t.Fatalf("read contended fencing token: %v", err)
	}
	if active != 1 || token != 1 {
		t.Fatalf("contended scope has active=%d token=%d, want 1/1", active, token)
	}
}

func openCoordinationTestDatabase(t *testing.T) (*Database, *time.Time) {
	t.Helper()
	database, err := Open(context.Background(), filepath.Join(t.TempDir(), "coordination.db"), Options{})
	if err != nil {
		t.Fatalf("open coordination database: %v", err)
	}
	t.Cleanup(func() { _ = database.Close() })
	clock := coordinationTestTime
	database.now = func() time.Time { return clock }
	return database, &clock
}

func leaseRequest(leaseID, scopeID, attemptID string, expectedToken uint64) statestore.AcquireLeaseRequest {
	return statestore.AcquireLeaseRequest{
		LeaseID:              leaseID,
		ScopeKind:            statestore.LeaseScopeRepository,
		ScopeID:              scopeID,
		HolderAttemptID:      attemptID,
		DaemonInstanceID:     "daemon_01",
		HostBootID:           "boot_01",
		ExpectedFencingToken: expectedToken,
		Duration:             statestore.DefaultLeaseDuration,
	}
}

func leaseGuard(lease statestore.Lease) statestore.LeaseGuard {
	return statestore.LeaseGuard{
		LeaseID:          lease.LeaseID,
		HolderAttemptID:  lease.HolderAttemptID,
		DaemonInstanceID: lease.DaemonInstanceID,
		FencingToken:     lease.FencingToken,
	}
}

func queueRequest(scopeID, itemID string, priority int, availableAt time.Time) statestore.EnqueueRequest {
	return statestore.EnqueueRequest{
		Kind:        statestore.QueueRepositoryWrite,
		ScopeID:     scopeID,
		ItemID:      itemID,
		Priority:    priority,
		AvailableAt: availableAt,
		Payload:     json.RawMessage(`{"writeCapable":true}`),
	}
}
