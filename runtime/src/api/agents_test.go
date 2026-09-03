package api

import (
	"context"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"darkstar/src/core/runexecution"
	"darkstar/src/ports/statestore"
)

func TestAgentAPIListsInspectsReadsLogsAndCancelsAttempts(t *testing.T) {
	root := t.TempDir()
	logs, err := NewDirectoryLogs(filepath.Join(root, "logs"))
	if err != nil {
		t.Fatal(err)
	}
	if err := logs.AppendLog(context.Background(), "agent-test.log", []byte("first line\nsecond line\n")); err != nil {
		t.Fatal(err)
	}
	agent := apiTestAgent()
	agents := &recordingAgentService{agent: agent}
	server, err := NewServer(filepath.Join(root, "runtime"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "runtime"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := server.SetStreams(StreamServices{Events: &memoryEventSource{}, Logs: logs}); err != nil {
		t.Fatal(err)
	}
	if err := server.SetAgents(agents); err != nil {
		t.Fatal(err)
	}
	if err := server.Start(context.Background(), os.Getpid(), time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	defer closeTestServer(t, server)
	endpoint, _ := server.Endpoint()

	listed := get(t, endpoint.BaseURL()+"/api/v1/agents", endpoint.AuthorizationHeader())
	var list runexecution.AgentList
	decodeJSON(t, listed, &list)
	_ = listed.Body.Close()
	if listed.StatusCode != http.StatusOK || len(list.Items) != 1 || list.Items[0].AttemptID != agent.AttemptID {
		t.Fatalf("agent list status=%d value=%#v", listed.StatusCode, list)
	}

	shown := get(t, endpoint.BaseURL()+"/api/v1/agents/"+agent.AttemptID, endpoint.AuthorizationHeader())
	var detail runexecution.Agent
	decodeJSON(t, shown, &detail)
	_ = shown.Body.Close()
	if shown.StatusCode != http.StatusOK || detail.Execution.Workspace.ID != "C:/worktree" || detail.Provider != "codex" {
		t.Fatalf("agent status=%d value=%#v", shown.StatusCode, detail)
	}

	logResponse := get(t, endpoint.BaseURL()+"/api/v1/agents/"+agent.AttemptID+"/logs?after=6&limit=4", endpoint.AuthorizationHeader())
	content, _ := io.ReadAll(logResponse.Body)
	_ = logResponse.Body.Close()
	if logResponse.StatusCode != http.StatusOK || string(content) != "line" || logResponse.Header.Get("X-Darkstar-Log-Next-Offset") != "10" {
		t.Fatalf("agent log status=%d next=%q content=%q", logResponse.StatusCode, logResponse.Header.Get("X-Darkstar-Log-Next-Offset"), content)
	}

	cancelled := workRequest(t, endpoint, http.MethodPost, "/api/v1/agents/"+agent.AttemptID+"/cancel", "", "cancel-agent-test")
	decodeJSON(t, cancelled, &detail)
	_ = cancelled.Body.Close()
	if cancelled.StatusCode != http.StatusOK || agents.cancelKey != "cancel-agent-test" || detail.Status != statestore.AttemptCancelled {
		t.Fatalf("agent cancel status=%d key=%q value=%#v", cancelled.StatusCode, agents.cancelKey, detail)
	}
}

func TestAgentAPIRejectsInvalidIdentityAndTerminalCancellation(t *testing.T) {
	server, err := NewServer(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	agents := &recordingAgentService{agent: apiTestAgent(), cancelErr: &runexecution.AgentTransitionError{AttemptID: "attempt_00000000000000000000000000", Status: statestore.AttemptSucceeded}}
	if err := server.SetAgents(agents); err != nil {
		t.Fatal(err)
	}
	if err := server.Start(context.Background(), os.Getpid(), time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	defer closeTestServer(t, server)
	endpoint, _ := server.Endpoint()

	missing := get(t, endpoint.BaseURL()+"/api/v1/agents/not-an-attempt", endpoint.AuthorizationHeader())
	assertAPIError(t, missing, http.StatusNotFound, "NOT_FOUND")
	_ = missing.Body.Close()

	conflict := workRequest(t, endpoint, http.MethodPost, "/api/v1/agents/"+agents.agent.AttemptID+"/cancel", "", "terminal-agent")
	assertAPIError(t, conflict, http.StatusConflict, "AGENT_CANCEL_INVALID_TRANSITION")
	_ = conflict.Body.Close()
}

type recordingAgentService struct {
	agent     runexecution.Agent
	cancelKey string
	cancelErr error
}

func (s *recordingAgentService) ListAgents(context.Context) (runexecution.AgentList, error) {
	return runexecution.AgentList{SchemaVersion: 1, Items: []runexecution.Agent{s.agent}}, nil
}

func (s *recordingAgentService) Agent(context.Context, string) (runexecution.Agent, error) {
	return s.agent, nil
}

func (s *recordingAgentService) CancelAgent(_ context.Context, _ string, key string) (runexecution.Agent, error) {
	s.cancelKey = key
	if s.cancelErr != nil {
		return runexecution.Agent{}, s.cancelErr
	}
	s.agent.Status = statestore.AttemptCancelled
	return s.agent, nil
}

func apiTestAgent() runexecution.Agent {
	now := time.Now().UTC()
	return runexecution.Agent{
		AttemptID: "attempt_00000000000000000000000000", RunID: "run_00000000000000000000000000", NodeID: "technical_design",
		Provider: "codex", Status: statestore.AttemptRunning, LogReference: "agent-test.log", CreatedAt: now, UpdatedAt: now,
		Execution: runexecution.AgentExecution{
			Source: "context_manifest", Workspace: runexecution.AgentWorkspace{ID: "C:/worktree", Access: "workspace_write"},
			Permissions: []string{"repository.write", "workspace:workspace_write"},
		},
	}
}
