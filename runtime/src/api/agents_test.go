package api

import (
	"context"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
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

	cancelRequest, _ := http.NewRequest(http.MethodPost, endpoint.BaseURL()+"/api/v1/agents/"+agent.AttemptID+"/cancel", nil)
	cancelRequest.Header.Set("Authorization", endpoint.AuthorizationHeader())
	cancelRequest.Header.Set("Idempotency-Key", "cancel-agent-test")
	cancelRequest.Header.Set("If-Match", `"4"`)
	cancelled, err := http.DefaultClient.Do(cancelRequest)
	if err != nil {
		t.Fatal(err)
	}
	decodeJSON(t, cancelled, &detail)
	_ = cancelled.Body.Close()
	if cancelled.StatusCode != http.StatusOK || agents.cancelKey != "cancel-agent-test" || agents.cancelVersion != 4 || detail.Status != statestore.AttemptCancelled {
		t.Fatalf("agent cancel status=%d key=%q version=%d value=%#v", cancelled.StatusCode, agents.cancelKey, agents.cancelVersion, detail)
	}
	// The helper clients share the process transport; retire their idle
	// connections before graceful server shutdown so repeated runs cannot leave
	// a connection racing the listener teardown on Windows.
	http.DefaultTransport.(*http.Transport).CloseIdleConnections()
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

	cancelRequest, _ := http.NewRequest(http.MethodPost, endpoint.BaseURL()+"/api/v1/agents/"+agents.agent.AttemptID+"/cancel", nil)
	cancelRequest.Header.Set("Authorization", endpoint.AuthorizationHeader())
	cancelRequest.Header.Set("Idempotency-Key", "terminal-agent")
	cancelRequest.Header.Set("If-Match", `"4"`)
	conflict, err := http.DefaultClient.Do(cancelRequest)
	if err != nil {
		t.Fatal(err)
	}
	assertAPIError(t, conflict, http.StatusConflict, "AGENT_CANCEL_INVALID_TRANSITION")
	_ = conflict.Body.Close()
}

func TestProviderPermissionAPIListsAndRecordsScopedDecision(t *testing.T) {
	permission := apiTestPermission()
	agents := &recordingAgentService{agent: apiTestAgent(), permission: permission}
	server, err := NewServer(t.TempDir())
	if err != nil {
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

	listed := get(t, endpoint.BaseURL()+"/api/v1/agents/permissions", endpoint.AuthorizationHeader())
	var list runexecution.ProviderPermissionList
	decodeJSON(t, listed, &list)
	_ = listed.Body.Close()
	if listed.StatusCode != http.StatusOK || len(list.Items) != 1 || list.Items[0].ID != permission.ID {
		t.Fatalf("permission list status=%d value=%#v", listed.StatusCode, list)
	}

	request, _ := http.NewRequest(http.MethodPost, endpoint.BaseURL()+"/api/v1/agents/permissions/"+permission.ID+"/decisions", strings.NewReader(`{"decision":"deny","scopeDigest":"`+permission.ScopeDigest+`"}`))
	request.Header.Set("Authorization", endpoint.AuthorizationHeader())
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Idempotency-Key", "permission-decision")
	request.Header.Set("If-Match", `"1"`)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	var decided runexecution.ProviderPermissionView
	decodeJSON(t, response, &decided)
	_ = response.Body.Close()
	if response.StatusCode != http.StatusOK || response.Header.Get("ETag") != `"2"` || agents.permissionRequest.Decision != "deny" || agents.permissionRequest.ScopeDigest != permission.ScopeDigest || agents.permissionRequest.ExpectedResourceVersion != 1 || decided.Status != statestore.ProviderPermissionDecisionRecorded {
		t.Fatalf("permission decision status=%d etag=%q request=%#v value=%#v", response.StatusCode, response.Header.Get("ETag"), agents.permissionRequest, decided)
	}
}

type recordingAgentService struct {
	agent             runexecution.Agent
	cancelKey         string
	cancelVersion     uint64
	cancelErr         error
	permission        runexecution.ProviderPermissionView
	permissionRequest runexecution.DecideProviderPermissionRequest
}

func (s *recordingAgentService) ListAgents(context.Context) (runexecution.AgentList, error) {
	return runexecution.AgentList{SchemaVersion: 1, Items: []runexecution.Agent{s.agent}}, nil
}

func (s *recordingAgentService) Agent(context.Context, string) (runexecution.Agent, error) {
	return s.agent, nil
}

func (s *recordingAgentService) CancelAgent(_ context.Context, _ string, expected uint64, key string) (runexecution.Agent, error) {
	s.cancelKey, s.cancelVersion = key, expected
	if s.cancelErr != nil {
		return runexecution.Agent{}, s.cancelErr
	}
	s.agent.Status = statestore.AttemptCancelled
	return s.agent, nil
}

func (s *recordingAgentService) ProviderPermission(context.Context, string) (runexecution.ProviderPermissionView, error) {
	if s.permission.ID == "" {
		return runexecution.ProviderPermissionView{}, statestore.ErrNotFound
	}
	return s.permission, nil
}

func (s *recordingAgentService) ProviderPermissions(context.Context, statestore.ProviderPermissionStatus) (runexecution.ProviderPermissionList, error) {
	items := []runexecution.ProviderPermissionView{}
	if s.permission.ID != "" {
		items = append(items, s.permission)
	}
	return runexecution.ProviderPermissionList{SchemaVersion: 1, Items: items}, nil
}

func (s *recordingAgentService) ProviderPermissionsForAttempt(context.Context, string, statestore.ProviderPermissionStatus) (runexecution.ProviderPermissionList, error) {
	items := []runexecution.ProviderPermissionView{}
	if s.permission.ID != "" {
		items = append(items, s.permission)
	}
	return runexecution.ProviderPermissionList{SchemaVersion: 1, Items: items}, nil
}

func (s *recordingAgentService) DecideProviderPermission(_ context.Context, request runexecution.DecideProviderPermissionRequest) (runexecution.ProviderPermissionView, error) {
	s.permissionRequest = request
	s.permission.Status = statestore.ProviderPermissionDecisionRecorded
	s.permission.ResourceVersion = 2
	s.permission.AllowedActions = []runexecution.ProviderPermissionAction{runexecution.ProviderPermissionRetryDelivery}
	return s.permission, nil
}

func apiTestPermission() runexecution.ProviderPermissionView {
	now := time.Now().UTC()
	return runexecution.ProviderPermissionView{
		ID: "permission_00000000000000000000000000", RunID: "run_00000000000000000000000000", AttemptID: "attempt_00000000000000000000000000", NodeID: "technical_design",
		ProviderThreadID: "thread-1", ProviderRequestID: "opaque-1", InteractionKind: "command", ScopeDigest: strings.Repeat("a", 64),
		Evidence: statestore.JSONSnapshot(`{"provider":"codex","providerVersion":"1","providerItemId":"item-1","summary":"command request","payloadDigest":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"}`),
		Status:   statestore.ProviderPermissionPending, AllowedActions: []runexecution.ProviderPermissionAction{runexecution.ProviderPermissionAllowOnce, runexecution.ProviderPermissionDeny, runexecution.ProviderPermissionCancel},
		ResourceVersion: 1, LastGlobalPosition: 1, CreatedAt: now, UpdatedAt: now,
	}
}

func (s *recordingAgentService) RetryProviderPermissionDelivery(context.Context, string, uint64) (runexecution.ProviderPermissionView, error) {
	return runexecution.ProviderPermissionView{}, statestore.ErrNotFound
}

func apiTestAgent() runexecution.Agent {
	now := time.Now().UTC()
	return runexecution.Agent{
		AttemptID: "attempt_00000000000000000000000000", RunID: "run_00000000000000000000000000", NodeID: "technical_design",
		Provider: "codex", Status: statestore.AttemptRunning, LogReference: "agent-test.log", CreatedAt: now, UpdatedAt: now,
		ResourceVersion: 4, AllowedActions: []runexecution.AgentAction{runexecution.AgentActionCancel},
		Execution: runexecution.AgentExecution{
			Source: "context_manifest", Workspace: runexecution.AgentWorkspace{ID: "C:/worktree", Access: "workspace_write"},
			Permissions: []string{"repository.write", "workspace:workspace_write"},
		},
	}
}
