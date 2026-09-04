package projection

import (
	"encoding/json"
	"testing"
	"time"

	"darkstar/src/ports/statestore"
)

func TestReduceInputRequestUsesClosedSiblingLifecycle(t *testing.T) {
	now := time.Date(2026, 9, 3, 12, 0, 0, 0, time.UTC)
	requested := statestore.Event{SchemaVersion: 1, AggregateType: statestore.AggregateInput, AggregateID: "input_00000000000000000000000000",
		AggregateRevision: 1, GlobalPosition: 1, CorrelationID: "run_00000000000000000000000000", RecordedAt: now,
		Actor: statestore.Actor{Type: statestore.ActorProvider, ID: "codex"}, Data: json.RawMessage(`{"runId":"run_00000000000000000000000000","attemptId":"attempt_00000000000000000000000000","nodeId":"design","providerThreadId":"thread-1","providerRequestId":"opaque-1","scopeDigest":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","request":{"questions":[{"id":"confirm","prompt":"Proceed?","options":["Yes","No"],"schema":{"type":"string","allowedValues":["Yes","No"]}}]}}`), Kind: "input.requested"}
	pending, applies, err := ReduceInputRequest(nil, requested)
	if err != nil || !applies || pending.Status != statestore.InputRequestPending || pending.Answer != nil || pending.Receipt != nil {
		t.Fatalf("pending = %#v, applies=%v, err=%v", pending, applies, err)
	}
	recordedEvent := requested
	recordedEvent.AggregateRevision, recordedEvent.GlobalPosition, recordedEvent.Kind, recordedEvent.CommandID = 2, 2, "input.answer_recorded", "answer-key"
	recordedEvent.Actor = statestore.Actor{Type: statestore.ActorUser, ID: "local-user"}
	recordedEvent.Data = json.RawMessage(`{"scopeDigest":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","answer":{"choice":"yes"}}`)
	recorded, _, err := ReduceInputRequest(&pending, recordedEvent)
	if err != nil || recorded.Status != statestore.InputRequestAnswerRecorded || recorded.Answer == nil || recorded.Receipt != nil {
		t.Fatalf("recorded = %#v, err=%v", recorded, err)
	}
	deliveredEvent := requested
	deliveredEvent.AggregateRevision, deliveredEvent.GlobalPosition, deliveredEvent.Kind = 3, 3, "input.answer_delivered"
	deliveredEvent.Actor = statestore.Actor{Type: statestore.ActorSystem, ID: "daemon"}
	deliveredEvent.Data = json.RawMessage(`{"providerRequestId":"opaque-1"}`)
	answered, _, err := ReduceInputRequest(&recorded, deliveredEvent)
	if err != nil || answered.Status != statestore.InputRequestAnswered || answered.Answer == nil || answered.Receipt == nil {
		t.Fatalf("answered = %#v, err=%v", answered, err)
	}
	if _, _, err := ReduceInputRequest(&pending, deliveredEvent); err == nil {
		t.Fatal("delivery before a recorded answer unexpectedly succeeded")
	}
}
