package cli

import (
	"bytes"
	"strings"
	"testing"

	"darkstar/src/core/runexecution"
	"darkstar/src/ports/provider"
	"darkstar/src/ports/statestore"
)

func TestWriteProviderPermissionShowsCompleteHumanReviewBinding(t *testing.T) {
	t.Parallel()
	view := runexecution.ProviderPermissionView{
		ID: "permission_01J00000000000000000000000", AttemptID: "attempt_01J00000000000000000000000",
		ProviderThreadID: "thread-1", ProviderTurnID: "turn-1", ProviderRequestID: "request-1", InteractionKind: "command",
		Scope:       provider.InteractionScope{Target: "command", Operation: "execute", Subject: "go test ./..."},
		ScopeDigest: strings.Repeat("a", 64), PolicyDigest: strings.Repeat("b", 64),
		Evidence:       statestore.JSONSnapshot(`{"kind":"command","summary":"Provider requested a command interaction","payloadDigest":"cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc","providerItemDigest":"dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd"}`),
		Status:         statestore.ProviderPermissionPending,
		AllowedActions: []runexecution.ProviderPermissionAction{runexecution.ProviderPermissionAllowOnce, runexecution.ProviderPermissionDeny},
	}
	var stdout, stderr bytes.Buffer
	if code := writeProviderPermission(view, false, &stdout, &stderr, "agent permissions show"); code != int(ExitSuccess) || stderr.Len() != 0 {
		t.Fatalf("writeProviderPermission() = %d, stderr=%q", code, stderr.String())
	}
	output := stdout.String()
	for _, expected := range []string{"Provider thread: thread-1", "Provider turn: turn-1", "Provider request: request-1", "Target: command", "Operation: execute", "Subject: go test ./...", "Scope digest: " + strings.Repeat("a", 64), "Policy digest: " + strings.Repeat("b", 64), "Allowed actions: allow_once, deny"} {
		if !strings.Contains(output, expected) {
			t.Errorf("human output omitted %q:\n%s", expected, output)
		}
	}
}
