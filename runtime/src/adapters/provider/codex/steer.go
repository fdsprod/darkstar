package codex

import (
	"context"
	"darkstar/src/ports/provider"
	"errors"
)

// Steer uses the active-turn precondition in the locally generated App Server
// protocol. A successful response means accepted by that turn, not completed work.
func (adapter *Adapter) Steer(ctx context.Context, handle provider.AttemptHandle, id, text string) error {
	state, err := adapter.attempt(handle.AttemptID)
	if err != nil {
		return err
	}
	state.mu.Lock()
	terminal := state.terminal
	client := state.client
	current := state.handle
	state.mu.Unlock()
	if terminal || client == nil || current.ProviderTurnID == "" {
		return errors.New("the agent no longer has an active turn")
	}
	var response struct {
		TurnID string `json:"turnId"`
	}
	err = client.Call(ctx, "turn/steer", map[string]any{"threadId": current.ProviderThreadID, "expectedTurnId": current.ProviderTurnID, "clientUserMessageId": id, "input": []any{map[string]any{"type": "text", "text": text}}}, &response)
	if err != nil {
		return err
	}
	if response.TurnID != current.ProviderTurnID {
		return errors.New("provider did not confirm the expected turn")
	}
	return nil
}
