package sqlite

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"darkstar/src/ports/statestore"
)

func TestProviderPermissionProjectionQueriesAndRebuild(t *testing.T) {
	ctx := context.Background()
	database, err := Open(ctx, filepath.Join(t.TempDir(), "permissions.db"), Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	now := time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC)
	id, runID, attemptID := "permission_00000000000000000000000000", "run_00000000000000000000000000", "attempt_00000000000000000000000000"
	appendPermission := func(revision uint64, kind, command string, actor statestore.Actor, data string) {
		t.Helper()
		_, appendErr := database.Append(ctx, statestore.PendingEvent{SchemaVersion: 1, ID: eventID(revision), AggregateType: statestore.AggregatePermission,
			AggregateID: id, ExpectedRevision: revision - 1, Kind: kind, OccurredAt: now.Add(time.Duration(revision) * time.Second), CorrelationID: runID,
			CommandID: command, Actor: actor, Data: json.RawMessage(data), Metadata: json.RawMessage(`{}`)})
		if appendErr != nil {
			t.Fatal(appendErr)
		}
	}
	appendPermission(1, "permission.requested", "request", statestore.Actor{Type: statestore.ActorProvider, ID: "codex"},
		`{"runId":"`+runID+`","attemptId":"`+attemptID+`","nodeId":"design","providerThreadId":"thread-1","providerTurnId":"turn-1","providerRequestId":"opaque-1","interactionKind":"command","scope":{"target":"command","operation":"execute","subject":"go test"},"scopeDigest":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","policyDigest":"dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd","evidence":{"kind":"command","summary":"Provider requested a command interaction","payloadDigest":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","providerItemDigest":"cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"}}`)
	pending, err := database.ProviderPermissions(ctx, statestore.ProviderPermissionPending)
	if err != nil || len(pending) != 1 || pending[0].PermissionRequestID != id {
		t.Fatalf("pending = %#v, %v", pending, err)
	}
	appendPermission(2, "permission.decision_recorded", "decision-key", statestore.Actor{Type: statestore.ActorUser, ID: "local-user"}, `{"scopeDigest":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","decision":"deny"}`)
	appendPermission(3, "permission.response_delivered", "deliver:decision-key", statestore.Actor{Type: statestore.ActorSystem, ID: "daemon"}, `{"providerRequestId":"opaque-1"}`)
	byAttempt, err := database.ProviderPermissionsForAttempt(ctx, attemptID)
	if err != nil || len(byAttempt) != 1 || byAttempt[0].Status != statestore.ProviderPermissionResponded {
		t.Fatalf("by attempt = %#v, %v", byAttempt, err)
	}
	if err := database.RebuildProjections(ctx); err != nil {
		t.Fatal(err)
	}
	rebuilt, err := database.ProviderPermission(ctx, id)
	if err != nil || rebuilt.Status != statestore.ProviderPermissionResponded || rebuilt.Decision == nil || rebuilt.Receipt == nil || rebuilt.Decision.ActionKey != "decision-key" {
		t.Fatalf("rebuilt = %#v, %v", rebuilt, err)
	}
}
