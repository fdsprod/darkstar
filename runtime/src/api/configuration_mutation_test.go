package api

import (
	"context"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"darkstar/src/core/config"
	"darkstar/src/core/configmutation"
	"darkstar/src/ports/configurationstore"
)

type recordingConfigurationMutations struct {
	scope    config.MutationScope
	apply    configmutation.ApplyRequest
	secret   configmutation.SecretWriteRequest
	applyErr error
}

func (s *recordingConfigurationMutations) Catalog(context.Context) (config.Catalog, error) {
	return config.SupportedCatalog(), nil
}
func (s *recordingConfigurationMutations) State(_ context.Context, scope config.MutationScope) (configmutation.State, error) {
	s.scope = scope
	return configurationState(scope), nil
}
func (s *recordingConfigurationMutations) Preview(_ context.Context, request configmutation.MutationRequest) (configmutation.Preview, error) {
	state := configurationState(request.Scope)
	return configmutation.Preview{SchemaVersion: 1, Valid: true, Before: state, After: state, Issues: []configmutation.ValidationIssue{}, Restart: config.RestartDaemon}, nil
}
func (s *recordingConfigurationMutations) Apply(_ context.Context, request configmutation.ApplyRequest) (configmutation.ApplyResult, error) {
	s.apply = request
	if s.applyErr != nil {
		return configmutation.ApplyResult{}, s.applyErr
	}
	return configmutation.ApplyResult{SchemaVersion: 1, State: configurationState(request.Scope), Restart: config.RestartDaemon}, nil
}
func (s *recordingConfigurationMutations) Restore(_ context.Context, request configmutation.RestoreRequest) (configmutation.ApplyResult, error) {
	return configmutation.ApplyResult{SchemaVersion: 1, State: configurationState(request.Scope), Restart: config.RestartDaemon}, nil
}
func (s *recordingConfigurationMutations) WriteSecret(_ context.Context, request configmutation.SecretWriteRequest) (configmutation.SecretReceipt, error) {
	s.secret = request
	return configmutation.SecretReceipt{SchemaVersion: 1, Name: request.Name, Revision: strings.Repeat("b", 64), Restart: config.RestartDaemon}, nil
}
func configurationState(scope config.MutationScope) configmutation.State {
	return configmutation.State{SchemaVersion: 1, Scope: scope, Revision: strings.Repeat("a", 64), SecretRevision: strings.Repeat("c", 64), Configured: []configmutation.ScopedSetting{}, Effective: []configmutation.EffectiveSetting{}}
}

func TestConfigurationMutationRoutesAndRedaction(t *testing.T) {
	t.Parallel()
	service := &recordingConfigurationMutations{}
	server, err := NewServer(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := server.SetConfigurationMutations(service); err != nil {
		t.Fatal(err)
	}
	if err := server.Start(context.Background(), os.Getpid(), time.Now()); err != nil {
		t.Fatal(err)
	}
	defer closeTestServer(t, server)
	endpoint, _ := server.Endpoint()
	catalog := configurationRequest(t, endpoint, http.MethodGet, "/api/v1/configuration/catalog", "", "")
	if catalog.StatusCode != http.StatusOK {
		t.Fatalf("catalog status=%d", catalog.StatusCode)
	}
	_, _ = io.Copy(io.Discard, catalog.Body)
	catalog.Body.Close()
	state := configurationRequest(t, endpoint, http.MethodGet, "/api/v1/configuration/state", "", "")
	if state.StatusCode != http.StatusOK || state.Header.Get("ETag") != `"`+strings.Repeat("a", 64)+`"` {
		t.Fatalf("state status=%d etag=%q", state.StatusCode, state.Header.Get("ETag"))
	}
	_, _ = io.Copy(io.Discard, state.Body)
	state.Body.Close()
	body := `{"scope":{"type":"user"},"key":"provider.codex.actionAvailability","change":{"operation":"set","value":{"type":"enum","value":"disabled"}},"expectedRevision":"` + strings.Repeat("a", 64) + `"}`
	applied := configurationRequest(t, endpoint, http.MethodPost, "/api/v1/configuration/apply", body, "apply-config-key")
	if applied.StatusCode != http.StatusOK || service.apply.IdempotencyKey != "apply-config-key" {
		t.Fatalf("apply status=%d request=%#v", applied.StatusCode, service.apply)
	}
	_, _ = io.Copy(io.Discard, applied.Body)
	applied.Body.Close()
	canary := "API-SECRET-CANARY"
	secret := configurationRequest(t, endpoint, http.MethodPost, "/api/v1/configuration/secrets", `{"name":"codex-api-key","value":"`+canary+`","expectedRevision":"`+strings.Repeat("c", 64)+`"}`, "secret-config-key")
	payload, _ := io.ReadAll(secret.Body)
	secret.Body.Close()
	if secret.StatusCode != http.StatusOK || service.secret.Value != canary || strings.Contains(string(payload), canary) {
		t.Fatalf("secret status=%d payload=%s recorded=%#v", secret.StatusCode, payload, service.secret)
	}
}

func TestConfigurationMutationTypedErrors(t *testing.T) {
	t.Parallel()
	service := &recordingConfigurationMutations{applyErr: configurationstore.ErrRevisionConflict}
	server, err := NewServer(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := server.SetConfigurationMutations(service); err != nil {
		t.Fatal(err)
	}
	if err := server.Start(context.Background(), os.Getpid(), time.Now()); err != nil {
		t.Fatal(err)
	}
	defer closeTestServer(t, server)
	endpoint, _ := server.Endpoint()
	body := `{"scope":{"type":"user"},"key":"provider.codex.actionAvailability","change":{"operation":"unset"},"expectedRevision":"` + strings.Repeat("a", 64) + `"}`
	response := configurationRequest(t, endpoint, http.MethodPost, "/api/v1/configuration/apply", body, "conflict-key")
	defer response.Body.Close()
	payload, _ := io.ReadAll(response.Body)
	if response.StatusCode != http.StatusConflict || !strings.Contains(string(payload), "REVISION_CONFLICT") {
		t.Fatalf("status=%d payload=%s", response.StatusCode, payload)
	}
}

func configurationRequest(t *testing.T, endpoint Endpoint, method, resource, body, key string) *http.Response {
	t.Helper()
	request, err := http.NewRequestWithContext(context.Background(), method, endpoint.BaseURL()+resource, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", endpoint.AuthorizationHeader())
	if key != "" {
		request.Header.Set("Idempotency-Key", key)
	}
	if body != "" {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	return response
}
