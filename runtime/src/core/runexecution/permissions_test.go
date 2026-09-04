package runexecution

import (
	"bytes"
	"encoding/json"
	"testing"
	"time"

	"darkstar/src/ports/provider"
	"darkstar/src/ports/statestore"
)

func TestPermissionRequestedEventPersistsOnlyRedactedEvidence(t *testing.T) {
	secret := []byte(`{"command":"echo token-secret","path":"C:/private/secret.txt"}`)
	event := provider.Event{SchemaVersion: 1, AttemptID: "attempt_00000000000000000000000000", Sequence: 7, OccurredAt: time.Now().UTC(),
		Kind: provider.EventPermissionRequested, Provider: "codex", ProviderVersion: "1.2.3", ProviderItemID: "item-7", Payload: json.RawMessage(secret), RawEvidenceRef: "C:/private/raw.json"}
	pending := permissionRequestedEvent(statestore.AttemptProjection{AttemptID: event.AttemptID, RunID: "run_00000000000000000000000000", NodeID: "design"},
		provider.AttemptHandle{AttemptID: event.AttemptID, ProviderThreadID: "thread-1"}, event,
		provider.InteractionCheckpoint{Kind: provider.InteractionCommand, ProviderRequestID: "opaque-7", ScopeDigest: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"})
	if bytes.Contains(pending.Data, []byte("token-secret")) || bytes.Contains(pending.Data, []byte("private")) || bytes.Contains(pending.Data, []byte("raw.json")) {
		t.Fatalf("durable permission event leaked raw provider content: %s", pending.Data)
	}
	if !bytes.Contains(pending.Data, []byte(`"payloadDigest"`)) || !bytes.Contains(pending.Data, []byte(`"summary"`)) {
		t.Fatalf("redacted evidence lacks useful metadata: %s", pending.Data)
	}
}

func TestAllowedAgentActionsRequireAttemptAndRunLegality(t *testing.T) {
	if got := allowedAgentActions(statestore.AttemptRunning, statestore.RunRunning); len(got) != 1 || got[0] != AgentActionCancel {
		t.Fatalf("running actions = %#v", got)
	}
	for _, test := range []struct {
		attempt statestore.AttemptStatus
		run     statestore.RunStatus
	}{{statestore.AttemptSucceeded, statestore.RunRunning}, {statestore.AttemptRunning, statestore.RunCompleted}, {statestore.AttemptRunning, statestore.RunReconcileRequired}} {
		if got := allowedAgentActions(test.attempt, test.run); len(got) != 0 {
			t.Errorf("actions(%s,%s) = %#v, want empty", test.attempt, test.run, got)
		}
	}
}

func TestProviderPermissionActionsRequireActiveOwner(t *testing.T) {
	t.Parallel()
	pending := statestore.ProviderPermissionProjection{Status: statestore.ProviderPermissionPending}
	if actions := providerPermissionView(pending, false).AllowedActions; len(actions) != 0 {
		t.Fatalf("inactive pending permission actions = %#v, want none", actions)
	}
	if actions := providerPermissionView(pending, true).AllowedActions; len(actions) != 3 {
		t.Fatalf("active pending permission actions = %#v, want three decisions", actions)
	}
	recorded := statestore.ProviderPermissionProjection{Status: statestore.ProviderPermissionDecisionRecorded}
	if actions := providerPermissionView(recorded, false).AllowedActions; len(actions) != 0 {
		t.Fatalf("inactive recorded permission actions = %#v, want none", actions)
	}
	if actions := providerPermissionView(recorded, true).AllowedActions; len(actions) != 1 || actions[0] != ProviderPermissionRetryDelivery {
		t.Fatalf("active recorded permission actions = %#v, want retry", actions)
	}
}
