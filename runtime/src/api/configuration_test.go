package api

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"darkstar/src/core/config"
)

type staticConfigurationReporter struct {
	report      config.EffectiveReport
	projectRoot string
}

func (reporter *staticConfigurationReporter) EffectiveConfigurationForProject(_ context.Context, projectRoot string) (config.EffectiveReport, error) {
	reporter.projectRoot = projectRoot
	return reporter.report, nil
}

func TestEffectiveConfigurationRequiresAuthenticationAndReturnsStrictReport(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	projectRoot := filepath.Join(root, "project")
	userPath := filepath.Join(root, "config", "config.yaml")
	projectPath := filepath.Join(projectRoot, ".darkstar", "config.yaml")
	defaults, err := config.Defaults(map[string]any{"provider": "fake"})
	if err != nil {
		t.Fatal(err)
	}
	effective, err := config.Resolve(defaults)
	if err != nil {
		t.Fatal(err)
	}
	report, err := config.NewEffectiveReport(projectRoot, []config.File{
		{Scope: config.FileScopeUser, Path: userPath},
		{Scope: config.FileScopeProject, Path: projectPath},
	}, effective)
	if err != nil {
		t.Fatal(err)
	}
	reporter := &staticConfigurationReporter{report: report}
	server, err := NewServer(filepath.Join(root, "runtime"))
	if err != nil {
		t.Fatal(err)
	}
	if err := server.SetConfiguration(reporter); err != nil {
		t.Fatal(err)
	}
	if err := server.Start(context.Background(), os.Getpid(), time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	defer closeTestServer(t, server)
	endpoint, found := server.Endpoint()
	if !found {
		t.Fatal("started server has no endpoint")
	}

	unauthorized := get(t, endpoint.BaseURL()+"/api/v1/configuration/effective", "")
	defer func() { _ = unauthorized.Body.Close() }()
	assertAPIError(t, unauthorized, http.StatusUnauthorized, "UNAUTHENTICATED")

	invalid := get(t, endpoint.BaseURL()+"/api/v1/configuration/effective?projectRoot=relative", endpoint.AuthorizationHeader())
	defer func() { _ = invalid.Body.Close() }()
	assertAPIError(t, invalid, http.StatusBadRequest, "VALIDATION_FAILED")
	mutation, err := http.NewRequestWithContext(context.Background(), http.MethodPost, endpoint.BaseURL()+"/api/v1/configuration/effective", nil)
	if err != nil {
		t.Fatal(err)
	}
	mutation.Header.Set("Authorization", endpoint.AuthorizationHeader())
	mutationResponse, err := (&http.Client{Timeout: 5 * time.Second}).Do(mutation)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = mutationResponse.Body.Close() }()
	assertAPIError(t, mutationResponse, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED")
	if allow := mutationResponse.Header.Get("Allow"); allow != "GET, HEAD" {
		t.Fatalf("Allow = %q", allow)
	}

	response := get(t, endpoint.BaseURL()+"/api/v1/configuration/effective", endpoint.AuthorizationHeader())
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("GET effective configuration status = %d", response.StatusCode)
	}
	payload, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(payload), `"source":{"scope":"default","reference":"shipped defaults"}`) {
		t.Fatalf("configuration report omitted public winning source: %s", payload)
	}
	var received config.EffectiveReport
	if err := json.Unmarshal(payload, &received); err != nil {
		t.Fatal(err)
	}
	if received.SchemaVersion != 1 || received.ProjectRoot != projectRoot || len(received.Files) != 2 || len(received.Entries) != 1 {
		t.Fatalf("configuration report = %#v", received)
	}
	if reporter.projectRoot != "" {
		t.Fatalf("reporter project root = %q, want empty startup-root selector", reporter.projectRoot)
	}
	if err := server.SetConfiguration(reporter); err == nil {
		t.Fatal("SetConfiguration succeeded after server start")
	}
}

func TestEffectiveConfigurationForwardsAbsoluteProjectRoot(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	reporter := &staticConfigurationReporter{report: config.EffectiveReport{
		SchemaVersion: 1, ProjectRoot: root,
		Files: []config.File{
			{Scope: config.FileScopeUser, Path: filepath.Join(root, "config", "config.yaml")},
			{Scope: config.FileScopeProject, Path: filepath.Join(root, ".darkstar", "config.yaml")},
		},
		Entries: []config.Entry{},
	}}
	server, err := NewServer(filepath.Join(root, "runtime"))
	if err != nil {
		t.Fatal(err)
	}
	if err := server.SetConfiguration(reporter); err != nil {
		t.Fatal(err)
	}
	if err := server.Start(context.Background(), os.Getpid(), time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	defer closeTestServer(t, server)
	endpoint, _ := server.Endpoint()
	requestRoot := filepath.Join(root, "another project")
	response := get(t, endpoint.BaseURL()+"/api/v1/configuration/effective?projectRoot="+url.QueryEscape(requestRoot), endpoint.AuthorizationHeader())
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK || reporter.projectRoot != requestRoot {
		t.Fatalf("status = %d, reporter project root = %q", response.StatusCode, reporter.projectRoot)
	}
}
