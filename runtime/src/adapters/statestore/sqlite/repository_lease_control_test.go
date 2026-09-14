package sqlite

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"darkstar/src/ports/statestore"
)

func repositoryLeaseControlFixture(t *testing.T) (*Database, string, string, statestore.Lease) {
	t.Helper()
	database := openEventTestDatabase(t)
	ctx := context.Background()
	runID, visitID, attemptID := testID("run", 'S'), testID("visit", 'S'), testID("attempt", 'S')
	_, err := database.Append(ctx,
		pendingEvent(testID("event", '1'), statestore.AggregateRun, runID, 0, "run.created", `{"workItemId":"work_01K3Z1C1AAAAAAAAAAAAAAAAAA","workflowId":"delivery","workflowVersion":"1"}`),
		pendingEvent(testID("event", '2'), statestore.AggregateRun, runID, 1, "run.route_frozen", `{}`),
		pendingEvent(testID("event", '3'), statestore.AggregateRun, runID, 2, "run.started", `{}`),
		pendingEvent(testID("event", '4'), statestore.AggregateVisit, visitID, 0, "visit.created", `{"runId":"`+runID+`","nodeId":"design"}`),
		pendingEvent(testID("event", '5'), statestore.AggregateVisit, visitID, 1, "visit.ready", `{}`),
		pendingEvent(testID("event", '6'), statestore.AggregateVisit, visitID, 2, "visit.started", `{}`),
		pendingEvent(testID("event", '7'), statestore.AggregateAttempt, attemptID, 0, "attempt.created", `{"runId":"`+runID+`","visitId":"`+visitID+`","nodeId":"design","scenario":"fake-success","provider":"fake","logReference":"attempt.log"}`),
		pendingEvent(testID("event", '8'), statestore.AggregateAttempt, attemptID, 1, "attempt.resources_acquired", `{}`),
		pendingEvent(testID("event", '9'), statestore.AggregateAttempt, attemptID, 2, "attempt.started", `{"providerThreadId":"thread","providerTurnId":"turn","processOwnerId":"process"}`),
		pendingEvent(testID("event", 'A'), statestore.AggregateRun, runID, 3, "run.visit_ready", `{}`),
	)
	if err != nil {
		t.Fatal(err)
	}
	lease, err := database.AcquireLease(ctx, leaseRequest("lease_pause", "shared_repository", attemptID, 0))
	if err != nil {
		t.Fatal(err)
	}
	return database, runID, attemptID, lease
}

func TestRepositoryPauseRetainsOwnerAndRequiresExactResumeEvidence(t *testing.T) {
	ctx := context.Background()
	db, runID, attemptID, lease := repositoryLeaseControlFixture(t)
	guard := leaseGuard(lease)
	if err := db.PrepareRepositoryPause(ctx, runID, lease.DaemonInstanceID, "pause-command"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ResumeRepositoryLease(ctx, guard); err == nil {
		t.Fatal("incomplete pause was reactivated")
	}
	if _, err := db.AcquireLease(ctx, leaseRequest("lease_contender", "shared_repository", "other_attempt", 1)); err == nil {
		t.Fatal("paused reader released repository ownership")
	}
	pause := pendingEvent(testID("event", 'B'), statestore.AggregateRun, runID, 4, "run.paused", `{}`)
	pause.CommandID = "pause-command"
	if _, err := db.Append(ctx, pause); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ResumeRepositoryLease(ctx, guard); err == nil {
		t.Fatal("paused run resumed without explicit control")
	}
	if _, err := db.Append(ctx, pendingEvent(testID("event", 'C'), statestore.AggregateRun, runID, 5, "run.resumed", `{}`)); err != nil {
		t.Fatal(err)
	}
	for _, other := range []statestore.LeaseGuard{
		{LeaseID: guard.LeaseID, HolderAttemptID: "other_attempt", DaemonInstanceID: guard.DaemonInstanceID, FencingToken: guard.FencingToken},
		{LeaseID: guard.LeaseID, HolderAttemptID: attemptID, DaemonInstanceID: "new_daemon", FencingToken: guard.FencingToken},
	} {
		if _, err := db.ResumeRepositoryLease(ctx, other); err == nil {
			t.Fatal("pause evidence transferred writer authority")
		}
	}
	// Expiry is not the authorization: the retained same-owner pause/resume
	// evidence permits refreshing the fencing capability, without releasing it.
	db.now = func() time.Time {
		return eventTestTime.Add(time.Hour)
	}
	resumed, err := db.ResumeRepositoryLease(ctx, guard)
	if err != nil || resumed.State != statestore.LeaseHeld || resumed.FencingToken != lease.FencingToken {
		t.Fatalf("same-owner resume = %#v %v", resumed, err)
	}
	if _, err := db.HeartbeatLease(ctx, guard, statestore.DefaultLeaseDuration); err != nil {
		t.Fatal(err)
	}
	if _, err := db.AcquireLease(ctx, leaseRequest(lease.LeaseID, lease.ScopeID, attemptID, 0)); err != nil {
		t.Fatalf("renewed exact owner cannot adopt after heartbeat: %v", err)
	}
}

func TestRepositoryCancellationRequiresConfirmedAttemptEvidence(t *testing.T) {
	for _, disposition := range []string{"", "uncertain", "graceful", "forced", "already_terminal"} {
		t.Run("disposition_"+disposition, func(t *testing.T) {
			ctx := context.Background()
			db, runID, attemptID, lease := repositoryLeaseControlFixture(t)
			guard := leaseGuard(lease)
			if _, err := db.MarkLeaseReconcileRequired(ctx, guard, json.RawMessage(`{"settled":false}`)); err != nil {
				t.Fatal(err)
			}
			if _, err := db.Append(ctx, pendingEvent(testID("event", 'B'), statestore.AggregateRun, runID, 4, "run.cancelled", `{}`)); err != nil {
				t.Fatal(err)
			}
			if err := db.ReleaseCancelledRepositoryLeases(ctx, runID, "new_daemon"); err != nil {
				t.Fatal(err)
			}
			before, err := db.RepositoryLease(ctx, guard)
			if err != nil || before.State != statestore.LeaseReconcileRequired {
				t.Fatal("run cancellation alone freed uncertain writer")
			}
			data := `{"providerCancellation":{"disposition":"` + disposition + `"}}`
			if _, err := db.Append(ctx, pendingEvent(testID("event", 'C'), statestore.AggregateAttempt, attemptID, 3, "attempt.cancelled", data)); err != nil {
				t.Fatal(err)
			}
			// Replayed cancellation reconciliation must close the crash window
			// after cancellation committed but before lease release completed.
			for repeat := 0; repeat < 2; repeat++ {
				if err := db.ReleaseCancelledRepositoryLeases(ctx, runID, "new_daemon"); err != nil {
					t.Fatal(err)
				}
			}
			after, err := db.RepositoryLease(ctx, guard)
			if err != nil {
				t.Fatal(err)
			}
			confirmed := disposition == "graceful" || disposition == "forced" || disposition == "already_terminal"
			if (after.State == statestore.LeaseReleased) != confirmed {
				t.Fatalf("disposition %q produced lease %s", disposition, after.State)
			}
		})
	}
}
