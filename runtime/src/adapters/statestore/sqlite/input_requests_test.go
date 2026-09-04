package sqlite

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"darkstar/src/ports/statestore"
)

func TestInputRequestProjectionQueriesAndRebuild(t *testing.T) {
	ctx := context.Background()
	database, err := Open(ctx, filepath.Join(t.TempDir(), "input.db"), Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	now := time.Date(2026, 9, 3, 12, 0, 0, 0, time.UTC)
	id := "input_00000000000000000000000000"
	runID := "run_00000000000000000000000000"
	attemptID := "attempt_00000000000000000000000000"
	appendInputEvent := func(revision uint64, kind, command string, actor statestore.Actor, data string) {
		t.Helper()
		_, appendErr := database.Append(ctx, statestore.PendingEvent{SchemaVersion: 1, ID: eventID(revision), AggregateType: statestore.AggregateInput,
			AggregateID: id, ExpectedRevision: revision - 1, Kind: kind, OccurredAt: now.Add(time.Duration(revision) * time.Second),
			CorrelationID: runID, CommandID: command, Actor: actor, Data: json.RawMessage(data), Metadata: json.RawMessage(`{}`)})
		if appendErr != nil {
			t.Fatal(appendErr)
		}
	}
	appendInputEvent(1, "input.requested", "request", statestore.Actor{Type: statestore.ActorProvider, ID: "codex"},
		`{"runId":"`+runID+`","attemptId":"`+attemptID+`","nodeId":"design","providerThreadId":"thread-1","providerRequestId":"opaque-1","scopeDigest":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","request":{"questions":[{"id":"confirm","prompt":"Proceed?","options":["Yes","No"],"schema":{"type":"string","allowedValues":["Yes","No"]}}]}}`)
	pending, err := database.InputRequests(ctx, statestore.InputRequestPending)
	if err != nil || len(pending) != 1 || pending[0].InputRequestID != id {
		t.Fatalf("pending = %#v, %v", pending, err)
	}
	appendInputEvent(2, "input.answer_recorded", "answer-key", statestore.Actor{Type: statestore.ActorUser, ID: "local-user"},
		`{"scopeDigest":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","answer":"yes"}`)
	appendInputEvent(3, "input.answer_delivered", "deliver:answer-key", statestore.Actor{Type: statestore.ActorSystem, ID: "daemon"}, `{"providerRequestId":"opaque-1"}`)
	byRun, _ := database.InputRequestsForRun(ctx, runID)
	byAttempt, _ := database.InputRequestsForAttempt(ctx, attemptID)
	if len(byRun) != 1 || len(byAttempt) != 1 || byRun[0].Status != statestore.InputRequestAnswered {
		t.Fatalf("queries = %#v %#v", byRun, byAttempt)
	}
	if err := database.RebuildProjections(ctx); err != nil {
		t.Fatal(err)
	}
	rebuilt, err := database.InputRequest(ctx, id)
	if err != nil || rebuilt.Status != statestore.InputRequestAnswered || rebuilt.Answer == nil || rebuilt.Receipt == nil {
		t.Fatalf("rebuilt = %#v, %v", rebuilt, err)
	}
}

func eventID(revision uint64) string {
	if revision == 1 {
		return "event_00000000000000000000000001"
	}
	if revision == 2 {
		return "event_00000000000000000000000002"
	}
	return "event_00000000000000000000000003"
}
