package codex

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	registryport "darkstar/src/ports/capabilityregistry"
)

func TestObserveCapabilitiesProjectsScopedSkillsAndPaginatedMCPTools(t *testing.T) {
	t.Parallel()
	client, server := newTestClient(t, "0.151.0-alpha.7.2")
	initializeClient(t, client, server)
	observedAt := time.Date(2026, 9, 3, 4, 5, 6, 0, time.UTC)
	testRoot := t.TempDir()
	projectRoot := filepath.Join(testRoot, "repo")
	repoSkill := createInventorySkill(t, filepath.Join(projectRoot, ".agents", "skills", "review"), "repo")
	userSkill := createInventorySkill(t, filepath.Join(testRoot, "user", "skills", "personal"), "user")
	systemSkill := createInventorySkill(t, filepath.Join(testRoot, "system", "skills", "core"), "system")
	adapter := &Adapter{projectRoot: projectRoot, clock: func() time.Time { return observedAt }}

	completed := make(chan struct {
		snapshot registryport.ObservationSnapshot
		err      error
	}, 1)
	go func() {
		snapshot, err := adapter.observeCapabilities(context.Background(), client)
		completed <- struct {
			snapshot registryport.ObservationSnapshot
			err      error
		}{snapshot: snapshot, err: err}
	}()

	skillsRequest := server.receive(t)
	if skillsRequest.Method != "skills/list" || !strings.Contains(string(skillsRequest.Params), `"forceReload":true`) {
		t.Fatalf("skills request = %#v", skillsRequest)
	}
	server.send(t, map[string]any{
		"id": json.RawMessage(skillsRequest.ID),
		"result": map[string]any{"data": []any{map[string]any{
			"cwd": projectRoot, "errors": []any{},
			"skills": []any{
				map[string]any{"name": "Repo Review", "description": "review", "enabled": true, "path": repoSkill, "scope": "repo"},
				map[string]any{"name": "Personal", "description": "personal", "enabled": false, "path": userSkill, "scope": "user"},
				map[string]any{"name": "Core", "description": "core", "enabled": true, "path": systemSkill, "scope": "system"},
			},
		}}},
	})

	firstMCPRequest := server.receive(t)
	if firstMCPRequest.Method != "mcpServerStatus/list" || !strings.Contains(string(firstMCPRequest.Params), `"detail":"toolsAndAuthOnly"`) {
		t.Fatalf("first MCP request = %#v", firstMCPRequest)
	}
	connected := "connected"
	server.send(t, map[string]any{
		"id": json.RawMessage(firstMCPRequest.ID),
		"result": map[string]any{
			"data": []any{map[string]any{
				"name": "Docs", "authStatus": "oAuth", "runtimeStatus": connected,
				"resourceTemplates": []any{}, "resources": []any{},
				"tools": map[string]any{"search": map[string]any{"name": "Search Web", "description": "search", "inputSchema": map[string]any{"type": "object"}}},
			}},
			"nextCursor": "page-2",
		},
	})

	secondMCPRequest := server.receive(t)
	if !strings.Contains(string(secondMCPRequest.Params), `"cursor":"page-2"`) {
		t.Fatalf("second MCP request = %#v", secondMCPRequest)
	}
	server.send(t, map[string]any{
		"id": json.RawMessage(secondMCPRequest.ID),
		"result": map[string]any{
			"data": []any{map[string]any{
				"name": "Deploy", "authStatus": "notLoggedIn", "runtimeStatus": connected,
				"resourceTemplates": []any{}, "resources": []any{},
				"tools": map[string]any{"release": map[string]any{"name": "Release", "inputSchema": map[string]any{"type": "object"}}},
			}},
			"nextCursor": nil,
		},
	})

	result := <-completed
	if result.err != nil {
		t.Fatal(result.err)
	}
	if result.snapshot.Provider != providerName || len(result.snapshot.HostFingerprint) != 64 || len(result.snapshot.Capabilities) != 5 {
		t.Fatalf("snapshot = %#v", result.snapshot)
	}
	want := []struct {
		scope        registryport.ObservationScope
		name         string
		kind         registryport.Kind
		availability registryport.Availability
	}{
		{registryport.ObservationCodex, "deploy/release", registryport.KindTool, registryport.AvailabilityUnavailable},
		{registryport.ObservationCodex, "docs/search-web", registryport.KindTool, registryport.AvailabilityAvailable},
		{registryport.ObservationCodex, "system/core", registryport.KindSkill, registryport.AvailabilityAvailable},
		{registryport.ObservationProject, "repo-review", registryport.KindSkill, registryport.AvailabilityAvailable},
		{registryport.ObservationUser, "personal", registryport.KindSkill, registryport.AvailabilityUnavailable},
	}
	for index, expected := range want {
		actual := result.snapshot.Capabilities[index]
		if actual.Scope != expected.scope || actual.Name != expected.name || actual.Kind != expected.kind || actual.Availability != expected.availability || len(actual.Fingerprint) != 64 || actual.DeclaredVersion != "" || !actual.ObservedAt.Equal(observedAt) {
			t.Fatalf("capability[%d] = %#v, want %#v", index, actual, expected)
		}
	}
}

func createInventorySkill(t *testing.T, directory, content string) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(directory, "agents"), 0o700); err != nil {
		t.Fatal(err)
	}
	skillPath := filepath.Join(directory, "SKILL.md")
	if err := os.WriteFile(skillPath, []byte("---\nname: "+content+"\n---\n"+content+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "agents", "openai.yaml"), []byte("interface:\n  display_name: "+content+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return skillPath
}

func TestObserveCapabilitiesFailsClosedOnSkillDiscoveryErrors(t *testing.T) {
	t.Parallel()
	client, server := newTestClient(t, "0.151.0-alpha.7.2")
	initializeClient(t, client, server)
	adapter := &Adapter{projectRoot: `C:\repo`, clock: time.Now}

	completed := make(chan error, 1)
	go func() {
		_, err := adapter.observeCapabilities(context.Background(), client)
		completed <- err
	}()
	request := server.receive(t)
	server.send(t, map[string]any{
		"id": json.RawMessage(request.ID),
		"result": map[string]any{"data": []any{map[string]any{
			"cwd": `C:\repo`, "skills": []any{},
			"errors": []any{map[string]string{"message": "invalid metadata", "path": `C:\repo\.agents\skills\broken\SKILL.md`}},
		}}},
	})
	if err := <-completed; err == nil || !strings.Contains(err.Error(), "discovery errors") {
		t.Fatalf("error = %v", err)
	}
}
