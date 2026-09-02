package projection

import (
	"errors"
	"testing"
	"time"

	"github.com/fdsprod/darkstar/runtime/src/ports/statestore"
)

func TestRunTransitionTable(t *testing.T) {
	t.Parallel()
	tests := []struct {
		from  statestore.RunStatus
		event string
		to    statestore.RunStatus
	}{
		{statestore.RunDraft, "run.route_frozen", statestore.RunReady},
		{statestore.RunReady, "run.started", statestore.RunQueued},
		{statestore.RunQueued, "run.visit_ready", statestore.RunRunning},
		{statestore.RunRunning, "run.waiting", statestore.RunWaiting},
		{statestore.RunWaiting, "run.resumed", statestore.RunQueued},
		{statestore.RunRunning, "run.blocked", statestore.RunBlocked},
		{statestore.RunBlocked, "run.resumed", statestore.RunQueued},
		{statestore.RunRunning, "run.completed", statestore.RunCompleted},
		{statestore.RunRunning, "run.failed", statestore.RunFailed},
		{statestore.RunFailed, "run.resumed", statestore.RunQueued},
	}
	for _, test := range tests {
		t.Run(string(test.from)+"/"+test.event, func(t *testing.T) {
			current := statestore.RunProjection{RunID: "run_A", Status: test.from, ResourceVersion: 1}
			next, applies, err := ReduceRun(&current, transitionEvent(statestore.AggregateRun, current.RunID, test.event))
			if err != nil || !applies || next.Status != test.to {
				t.Fatalf("transition = (%s, %v, %v), want (%s, true, nil)", next.Status, applies, err, test.to)
			}
		})
	}
	for _, state := range []statestore.RunStatus{statestore.RunDraft, statestore.RunReady, statestore.RunQueued, statestore.RunRunning, statestore.RunWaiting, statestore.RunBlocked, statestore.RunFailed} {
		current := statestore.RunProjection{RunID: "run_A", Status: state, ResourceVersion: 1}
		next, _, err := ReduceRun(&current, transitionEvent(statestore.AggregateRun, current.RunID, "run.cancelled"))
		if err != nil || next.Status != statestore.RunCancelled {
			t.Errorf("cancel from %s = (%s, %v)", state, next.Status, err)
		}
	}
	assertInvalidTransition(t, func() error {
		current := statestore.RunProjection{RunID: "run_A", Status: statestore.RunDraft, ResourceVersion: 1}
		_, _, err := ReduceRun(&current, transitionEvent(statestore.AggregateRun, current.RunID, "run.completed"))
		return err
	})
}

func TestNodeTransitionTable(t *testing.T) {
	t.Parallel()
	tests := []struct {
		from  statestore.NodeStatus
		event string
		to    statestore.NodeStatus
	}{
		{statestore.NodePending, "visit.ready", statestore.NodeReady},
		{statestore.NodeReady, "visit.started", statestore.NodeRunning},
		{statestore.NodeRunning, "visit.result_received", statestore.NodeValidating},
		{statestore.NodeValidating, "visit.succeeded", statestore.NodeSucceeded},
		{statestore.NodeValidating, "visit.waiting_checkpoint", statestore.NodeWaitingCheckpoint},
		{statestore.NodeWaitingCheckpoint, "visit.succeeded", statestore.NodeSucceeded},
		{statestore.NodeWaitingCheckpoint, "visit.changes_requested", statestore.NodeRunning},
		{statestore.NodeWaitingCheckpoint, "visit.rejected", statestore.NodeRejected},
		{statestore.NodeRunning, "visit.retrying", statestore.NodeReady},
		{statestore.NodeValidating, "visit.retrying", statestore.NodeReady},
		{statestore.NodeRunning, "visit.failed", statestore.NodeFailed},
		{statestore.NodeValidating, "visit.failed", statestore.NodeFailed},
	}
	for _, test := range tests {
		t.Run(string(test.from)+"/"+test.event, func(t *testing.T) {
			current := statestore.NodeProjection{VisitID: "visit_A", Status: test.from, ResourceVersion: 1}
			next, applies, err := ReduceNode(&current, transitionEvent(statestore.AggregateVisit, current.VisitID, test.event))
			if err != nil || !applies || next.Status != test.to {
				t.Fatalf("transition = (%s, %v, %v), want (%s, true, nil)", next.Status, applies, err, test.to)
			}
		})
	}
	assertInvalidTransition(t, func() error {
		current := statestore.NodeProjection{VisitID: "visit_A", Status: statestore.NodeRunning, ResourceVersion: 1}
		_, _, err := ReduceNode(&current, transitionEvent(statestore.AggregateVisit, current.VisitID, "visit.succeeded"))
		return err
	})
}

func TestAttemptTransitionTable(t *testing.T) {
	t.Parallel()
	tests := []struct {
		from  statestore.AttemptStatus
		event string
		to    statestore.AttemptStatus
	}{
		{statestore.AttemptCreated, "attempt.resources_acquired", statestore.AttemptStarting},
		{statestore.AttemptStarting, "attempt.started", statestore.AttemptRunning},
		{statestore.AttemptRunning, "attempt.result_received", statestore.AttemptValidating},
		{statestore.AttemptValidating, "attempt.succeeded", statestore.AttemptSucceeded},
	}
	for _, test := range tests {
		t.Run(string(test.from)+"/"+test.event, func(t *testing.T) {
			current := statestore.AttemptProjection{AttemptID: "attempt_A", Status: test.from, ResourceVersion: 1}
			event := transitionEvent(statestore.AggregateAttempt, current.AttemptID, test.event)
			if test.event == "attempt.started" {
				event.Data = []byte(`{"providerThreadId":"thread","providerTurnId":"turn","processOwnerId":"process"}`)
			}
			next, applies, err := ReduceAttempt(&current, event)
			if err != nil || !applies || next.Status != test.to {
				t.Fatalf("transition = (%s, %v, %v), want (%s, true, nil)", next.Status, applies, err, test.to)
			}
		})
	}
	for _, state := range []statestore.AttemptStatus{statestore.AttemptCreated, statestore.AttemptStarting, statestore.AttemptRunning, statestore.AttemptValidating} {
		current := statestore.AttemptProjection{AttemptID: "attempt_A", Status: state, ResourceVersion: 1}
		next, _, err := ReduceAttempt(&current, transitionEvent(statestore.AggregateAttempt, current.AttemptID, "attempt.failed"))
		if err != nil || next.Status != statestore.AttemptFailed {
			t.Errorf("failure from %s = (%s, %v)", state, next.Status, err)
		}
	}
	assertInvalidTransition(t, func() error {
		current := statestore.AttemptProjection{AttemptID: "attempt_A", Status: statestore.AttemptRunning, ResourceVersion: 1}
		_, _, err := ReduceAttempt(&current, transitionEvent(statestore.AggregateAttempt, current.AttemptID, "attempt.succeeded"))
		return err
	})
}

func TestAttemptResumeTransfersProcessOwnership(t *testing.T) {
	t.Parallel()
	current := statestore.AttemptProjection{
		AttemptID: "attempt_A", Status: statestore.AttemptRunning, ResourceVersion: 1,
		ProviderThreadID: "thread", ProviderTurnID: "turn", ProcessOwnerID: "process-old",
	}
	event := transitionEvent(statestore.AggregateAttempt, current.AttemptID, "attempt.resumed")
	event.Data = []byte(`{"providerThreadId":"thread","providerTurnId":"turn","processOwnerId":"process-new"}`)
	next, applies, err := ReduceAttempt(&current, event)
	if err != nil || !applies || next.ProcessOwnerID != "process-new" {
		t.Fatalf("resume = (%#v, %v, %v)", next, applies, err)
	}

	event.Data = []byte(`{"providerThreadId":"other-thread","providerTurnId":"turn","processOwnerId":"process-new"}`)
	if _, _, err := ReduceAttempt(&current, event); err == nil {
		t.Fatal("resume accepted a changed provider thread identity")
	}
}

func transitionEvent(kind statestore.AggregateType, id, eventKind string) statestore.Event {
	return statestore.Event{
		SchemaVersion: 1, ID: "event_A", AggregateType: kind, AggregateID: id,
		AggregateRevision: 2, Kind: eventKind, RecordedAt: time.Unix(1, 0).UTC(), Data: []byte(`{}`),
	}
}

func assertInvalidTransition(t *testing.T, operation func() error) {
	t.Helper()
	var invalid *InvalidTransitionError
	if err := operation(); !errors.As(err, &invalid) {
		t.Fatalf("error = %v, want InvalidTransitionError", err)
	}
}
