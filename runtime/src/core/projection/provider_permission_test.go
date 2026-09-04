package projection

import (
	"bytes"
	"encoding/json"
	"testing"
	"time"

	"darkstar/src/ports/statestore"
)

func TestReduceProviderPermissionUsesClosedLifecycle(t *testing.T) {
	now := time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC)
	requested := statestore.Event{SchemaVersion: 1, AggregateType: statestore.AggregatePermission, AggregateID: "permission_00000000000000000000000000",
		AggregateRevision: 1, GlobalPosition: 1, CorrelationID: "run_00000000000000000000000000", RecordedAt: now,
		Actor: statestore.Actor{Type: statestore.ActorProvider, ID: "codex"}, Data: json.RawMessage(`{"runId":"run_00000000000000000000000000","attemptId":"attempt_00000000000000000000000000","nodeId":"design","providerThreadId":"thread-1","providerTurnId":"turn-1","providerRequestId":"opaque-1","interactionKind":"command","scope":{"target":"command","operation":"execute","subject":"go test"},"scopeDigest":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","policyDigest":"dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd","evidence":{"kind":"command","summary":"Provider requested a command interaction","payloadDigest":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","providerItemDigest":"cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"}}`), Kind: "permission.requested"}
	pending, applies, err := ReduceProviderPermission(nil, requested)
	if err != nil || !applies || pending.Status != statestore.ProviderPermissionPending || pending.Decision != nil || pending.Receipt != nil {
		t.Fatalf("pending = %#v, applies=%v, err=%v", pending, applies, err)
	}
	decision := requested
	decision.AggregateRevision, decision.GlobalPosition, decision.Kind, decision.CommandID = 2, 2, "permission.decision_recorded", "decision-key"
	decision.Actor = statestore.Actor{Type: statestore.ActorUser, ID: "local-user"}
	decision.Data = json.RawMessage(`{"scopeDigest":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","decision":"allow_once"}`)
	recorded, _, err := ReduceProviderPermission(&pending, decision)
	if err != nil || recorded.Status != statestore.ProviderPermissionDecisionRecorded || recorded.Decision == nil || recorded.Receipt != nil || recorded.Decision.ActionKey != "decision-key" {
		t.Fatalf("recorded = %#v, err=%v", recorded, err)
	}
	public, err := json.Marshal(recorded)
	if err != nil || bytes.Contains(public, []byte("decision-key")) || bytes.Contains(public, []byte("actionKey")) {
		t.Fatalf("public projection leaked raw idempotency material: %s, %v", public, err)
	}
	delivered := requested
	delivered.AggregateRevision, delivered.GlobalPosition, delivered.Kind = 3, 3, "permission.response_delivered"
	delivered.Actor = statestore.Actor{Type: statestore.ActorSystem, ID: "daemon"}
	delivered.Data = json.RawMessage(`{"providerRequestId":"opaque-1"}`)
	responded, _, err := ReduceProviderPermission(&recorded, delivered)
	if err != nil || responded.Status != statestore.ProviderPermissionResponded || responded.Decision == nil || responded.Receipt == nil {
		t.Fatalf("responded = %#v, err=%v", responded, err)
	}
	if _, _, err := ReduceProviderPermission(&pending, delivered); err == nil {
		t.Fatal("delivery before a recorded decision unexpectedly succeeded")
	}
	bad := decision
	bad.Data = json.RawMessage(`{"scopeDigest":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","decision":"allow_for_session"}`)
	if _, _, err := ReduceProviderPermission(&pending, bad); err == nil {
		t.Fatal("unbounded session grant unexpectedly succeeded")
	}
}
