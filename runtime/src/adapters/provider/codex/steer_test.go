package codex

import (
	"darkstar/src/ports/provider"
	"encoding/json"
	"testing"
)

func TestSteerBindsToActiveTurnAndRequiresAcknowledgement(t *testing.T) {
	client, server := newTestClient(t)
	initializeClient(t, client, server)
	handle := provider.AttemptHandle{AttemptID: "attempt", ProviderThreadID: "thread", ProviderTurnID: "turn"}
	ready := make(chan struct{})
	close(ready)
	adapter := &Adapter{attempts: map[string]*codexAttempt{"attempt": {handle: handle, client: client, ready: ready}}}
	done := make(chan error, 1)
	go func() { done <- adapter.Steer(t.Context(), handle, "message-1", "Document Windows.") }()
	request := server.receive(t)
	var params map[string]json.RawMessage
	if err := json.Unmarshal(request.Params, &params); err != nil {
		t.Fatal(err)
	}
	if request.Method != "turn/steer" || string(params["expectedTurnId"]) != `"turn"` || string(params["clientUserMessageId"]) != `"message-1"` {
		t.Fatalf("steer request: %s %s", request.Method, request.Params)
	}
	server.send(t, map[string]any{"id": request.ID, "result": map[string]string{"turnId": "turn"}})
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	adapter.attempts["attempt"].terminal = true
	if err := adapter.Steer(t.Context(), handle, "message-2", "Too late"); err == nil {
		t.Fatal("steered a completed attempt")
	}
}
