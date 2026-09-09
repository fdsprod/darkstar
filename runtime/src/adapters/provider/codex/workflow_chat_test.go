package codex

import (
	"context"
	"darkstar/src/core/workflowchat"
	"encoding/json"
	"testing"
	"time"
)

func TestAuthoringDisablesInheritedExecutionAndExternalTools(t *testing.T) {
	config := authoringOverrides(map[string]any{"mcp_servers": map[string]any{"external": map[string]any{}}, "plugins": map[string]any{"example": map[string]any{}}})
	for _, key := range []string{"features.shell_tool", "features.unified_exec", "features.apply_patch_freeform", "features.apps", "features.plugins", "features.multi_agent", "mcp_servers.external.enabled", "plugins.example.enabled"} {
		if config[key] != false {
			t.Fatalf("%s not disabled", key)
		}
	}
	if config["web_search"] != "disabled" {
		t.Fatal("web search enabled")
	}
}

func TestWorkflowChatProtocolStreamsQuestionsAndRejectsPublishing(t *testing.T) {
	client, server := newTestClient(t, "0.151.0-alpha.7.2")
	initializeClient(t, client, server)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	kinds := []string{}
	emit := func(kind string, _ any) error { kinds = append(kinds, kind); return nil }
	session := &workflowchat.Session{Target: workflowchat.Target{Kind: "new"}, Emit: emit, Generation: &workflowchat.Generation{Model: "test-model", Effort: "high"}}
	done := make(chan error, 1)
	go func() {
		done <- runWorkflowChat(ctx, client, t.TempDir(), session, []workflowchat.Message{{Role: "user", Text: "Clarify this workflow"}}, emit)
	}()
	reply := func(result any) {
		request := server.receive(t)
		server.send(t, map[string]any{"id": request.ID, "result": result})
	}
	reply(map[string]any{"config": map[string]any{}})
	reply(map[string]any{"data": []any{map[string]any{"model": "test-model", "displayName": "Test Model", "defaultReasoningEffort": "low", "supportedReasoningEfforts": []any{map[string]string{"reasoningEffort": "low"}, map[string]string{"reasoningEffort": "high"}}}}})
	request := server.receive(t)
	var params map[string]any
	if err := json.Unmarshal(request.Params, &params); err != nil {
		t.Fatal(err)
	}
	if params["sandbox"] != "read-only" || params["ephemeral"] != true || params["model"] != "test-model" || len(params["dynamicTools"].([]any)) != 5 {
		t.Fatalf("thread params: %#v", params)
	}
	server.send(t, map[string]any{"id": request.ID, "result": map[string]any{"thread": map[string]string{"id": "chat-thread"}}})
	turnRequest := server.receive(t)
	var turnParams map[string]any
	if err := json.Unmarshal(turnRequest.Params, &turnParams); err != nil {
		t.Fatal(err)
	}
	if turnParams["model"] != "test-model" || turnParams["effort"] != "high" {
		t.Fatalf("generation not forwarded: %#v", turnParams)
	}
	server.send(t, map[string]any{"id": turnRequest.ID, "result": map[string]any{"turn": map[string]string{"id": "chat-turn"}}})
	tool := func(name string, args any) wireMessage {
		server.send(t, map[string]any{"id": "tool-call", "method": "item/tool/call", "params": map[string]any{"threadId": "chat-thread", "turnId": "chat-turn", "callId": "call", "tool": name, "arguments": args}})
		return server.receive(t)
	}
	denied := tool("publish_workflow", map[string]any{})
	var result struct {
		Success bool `json:"success"`
	}
	if err := json.Unmarshal(denied.Result, &result); err != nil || result.Success {
		t.Fatal("publish tool was not denied")
	}
	accepted := tool("ask_question", map[string]any{"question": "Which review policy?", "options": []string{"Human approval"}})
	if err := json.Unmarshal(accepted.Result, &result); err != nil || !result.Success {
		t.Fatalf("question failed: %s", accepted.Result)
	}
	server.send(t, map[string]any{"method": "item/agentMessage/delta", "params": map[string]string{"threadId": "chat-thread", "turnId": "chat-turn", "delta": "Waiting for your answer."}})
	server.send(t, map[string]any{"method": "turn/completed", "params": map[string]any{"threadId": "chat-thread", "turn": map[string]string{"id": "chat-turn", "status": "completed"}}})
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if len(kinds) != 2 || kinds[0] != "question" || kinds[1] != "text" {
		t.Fatalf("events: %v", kinds)
	}
}

func TestWorkflowChatRejectsUnsupportedModelEffortPairs(t *testing.T) {
	models := []workflowchat.Model{{ID: "test", Efforts: []string{"low", "high"}, DefaultEffort: "low"}}
	for _, choice := range []workflowchat.Generation{{Model: "missing", Effort: "low"}, {Model: "test", Effort: "unsupported"}} {
		if validateChatGeneration(models, choice) == nil {
			t.Fatalf("accepted %#v", choice)
		}
	}
	if err := validateChatGeneration(models, workflowchat.Generation{Model: "test", Effort: "high"}); err != nil {
		t.Fatal(err)
	}
}
