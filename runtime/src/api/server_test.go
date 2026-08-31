package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestServerPublishesProtectedLoopbackEndpointAndNegotiatesVersion(t *testing.T) {
	t.Parallel()

	server, endpoint := startTestServer(t)
	defer closeTestServer(t, server)

	address, err := net.ResolveTCPAddr("tcp4", strings.TrimPrefix(endpoint.BaseURL(), "http://"))
	if err != nil {
		t.Fatal(err)
	}
	if !address.IP.IsLoopback() || address.IP.String() != "127.0.0.1" {
		t.Fatalf("endpoint address = %s, want IPv4 loopback", address)
	}
	if got, want := fmt.Sprint(endpoint.Token), "[redacted]"; got != want {
		t.Fatalf("formatted token = %q, want %q", got, want)
	}
	if got, want := fmt.Sprintf("%#v", endpoint.Token), "api.Token([redacted])"; got != want {
		t.Fatalf("Go-syntax token formatting = %q, want %q", got, want)
	}
	if got, err := NegotiateVersion(endpoint, []Version{VersionV1}); err != nil || got != VersionV1 {
		t.Fatalf("NegotiateVersion() = %q, %v", got, err)
	}
	if _, err := NegotiateVersion(endpoint, []Version{"v2"}); !errors.Is(err, ErrNoCompatibleVersion) {
		t.Fatalf("NegotiateVersion(v2) error = %v, want ErrNoCompatibleVersion", err)
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(server.endpointPath)
		if err != nil {
			t.Fatal(err)
		}
		if permissions := info.Mode().Perm(); permissions != 0o600 {
			t.Fatalf("endpoint permissions = %o, want 600", permissions)
		}
	}
	if err := verifyProtectedFile(server.endpointPath); err != nil {
		t.Fatalf("endpoint protection: %v", err)
	}
}

func TestHealthIsUnauthenticatedButAPIRootRequiresBearerToken(t *testing.T) {
	t.Parallel()

	server, endpoint := startTestServer(t)
	defer closeTestServer(t, server)

	health := get(t, endpoint.BaseURL()+"/api/v1/health", "")
	defer health.Body.Close()
	if health.StatusCode != http.StatusOK {
		t.Fatalf("GET /api/v1/health status = %d", health.StatusCode)
	}
	var healthBody healthResponse
	decodeJSON(t, health, &healthBody)
	if healthBody.Status != "ok" || len(healthBody.APIVersions) != 1 || healthBody.APIVersions[0] != VersionV1 {
		t.Fatalf("health response = %#v", healthBody)
	}

	missing := get(t, endpoint.BaseURL()+"/api/v1", "")
	defer missing.Body.Close()
	assertAPIError(t, missing, http.StatusUnauthorized, "UNAUTHENTICATED")
	if missing.Header.Get("WWW-Authenticate") == "" {
		t.Fatal("unauthenticated response omitted WWW-Authenticate")
	}

	authorized := get(t, endpoint.BaseURL()+"/api/v1", endpoint.AuthorizationHeader())
	defer authorized.Body.Close()
	if authorized.StatusCode != http.StatusOK {
		t.Fatalf("authorized GET /api/v1 status = %d", authorized.StatusCode)
	}
	var root apiRootResponse
	decodeJSON(t, authorized, &root)
	if root.SchemaVersion != 1 || root.APIVersion != VersionV1 {
		t.Fatalf("API root = %#v", root)
	}
}

func TestAuthenticatedFailuresUseStableErrorEnvelope(t *testing.T) {
	t.Parallel()

	server, endpoint := startTestServer(t)
	defer closeTestServer(t, server)

	unsupported := get(t, endpoint.BaseURL()+"/api/v2/runs", endpoint.AuthorizationHeader())
	defer unsupported.Body.Close()
	problem := assertAPIError(t, unsupported, http.StatusUpgradeRequired, "API_VERSION_UNSUPPORTED")
	if len(problem.Details) != 1 || problem.Details[0].Field != "apiVersion" {
		t.Fatalf("unsupported version details = %#v", problem.Details)
	}

	notFound := get(t, endpoint.BaseURL()+"/api/v1/does-not-exist", endpoint.AuthorizationHeader())
	defer notFound.Body.Close()
	assertAPIError(t, notFound, http.StatusNotFound, "NOT_FOUND")
}

func TestRotationAtomicallyInvalidatesPreviousToken(t *testing.T) {
	t.Parallel()

	server, original := startTestServer(t)
	defer closeTestServer(t, server)

	if err := server.RotateToken(); err != nil {
		t.Fatal(err)
	}
	rotated, err := ReadEndpoint(filepath.Dir(server.endpointPath))
	if err != nil {
		t.Fatal(err)
	}
	if original.Token.equal(rotated.Token) {
		t.Fatal("rotation preserved the previous token")
	}

	stale := get(t, original.BaseURL()+"/api/v1", original.AuthorizationHeader())
	defer stale.Body.Close()
	assertAPIError(t, stale, http.StatusUnauthorized, "UNAUTHENTICATED")

	current := get(t, rotated.BaseURL()+"/api/v1", rotated.AuthorizationHeader())
	defer current.Body.Close()
	if current.StatusCode != http.StatusOK {
		t.Fatalf("request with rotated token status = %d", current.StatusCode)
	}
}

func TestCloseRemovesOnlyOwnedEndpoint(t *testing.T) {
	t.Parallel()

	server, _ := startTestServer(t)
	if err := server.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(server.endpointPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("endpoint after Close = %v, want absent", err)
	}
}

func startTestServer(t *testing.T) (*Server, Endpoint) {
	t.Helper()
	server, err := NewServer(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	startedAt := time.Date(2026, 8, 31, 20, 0, 0, 123400000, time.UTC)
	if err := server.Start(context.Background(), 1234, startedAt); err != nil {
		t.Fatal(err)
	}
	endpoint, err := ReadEndpoint(filepath.Dir(server.endpointPath))
	if err != nil {
		t.Fatal(err)
	}
	return server, endpoint
}

func closeTestServer(t *testing.T, server *Server) {
	t.Helper()
	if err := server.Close(); err != nil {
		t.Error(err)
	}
}

func get(t *testing.T, url, authorization string) *http.Response {
	t.Helper()
	request, err := http.NewRequestWithContext(context.Background(), http.MethodGet, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	if authorization != "" {
		request.Header.Set("Authorization", authorization)
	}
	client := &http.Client{Timeout: 5 * time.Second}
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	return response
}

func decodeJSON(t *testing.T, response *http.Response, destination any) {
	t.Helper()
	if contentType := response.Header.Get("Content-Type"); !strings.HasPrefix(contentType, "application/json") {
		t.Fatalf("Content-Type = %q, want application/json", contentType)
	}
	if err := json.NewDecoder(response.Body).Decode(destination); err != nil {
		t.Fatal(err)
	}
}

func assertAPIError(t *testing.T, response *http.Response, status int, code string) apiError {
	t.Helper()
	if response.StatusCode != status {
		t.Fatalf("status = %d, want %d", response.StatusCode, status)
	}
	var problem apiError
	decodeJSON(t, response, &problem)
	if problem.SchemaVersion != 1 || problem.Code != code || problem.Message == "" || problem.RequestID == "" {
		t.Fatalf("API error = %#v", problem)
	}
	if header := response.Header.Get(requestIDHeader); header != problem.RequestID {
		t.Fatalf("request ID header = %q, body = %q", header, problem.RequestID)
	}
	return problem
}
