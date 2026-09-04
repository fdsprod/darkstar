package api

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
)

func TestDashboardServesIndexForRootAndClientRoutesWithMemoryOnlyBootstrap(t *testing.T) {
	t.Parallel()
	token, err := parseToken(strings.Repeat("a", tokenBytes*2))
	if err != nil {
		t.Fatal(err)
	}
	assets := fstest.MapFS{
		"index.html": {Data: []byte("<!doctype html><html><head><title>DARKSTAR</title></head><body></body></html>")},
	}
	endpoint := Endpoint{APIVersion: VersionV1, Token: token}

	for _, location := range []string{"/", "/index.html", "/work/work-01/run/run-02?tab=timeline", "/settings"} {
		location := location
		t.Run(location, func(t *testing.T) {
			t.Parallel()
			request := httptest.NewRequest(http.MethodGet, location, nil)
			response := httptest.NewRecorder()
			serveDashboard(response, request, assets, endpoint)

			result := response.Result()
			defer func() { _ = result.Body.Close() }()
			if result.StatusCode != http.StatusOK {
				t.Fatalf("status = %d, want 200", result.StatusCode)
			}
			body := readResponseBody(t, result)
			if !strings.Contains(body, `window.__DARKSTAR_BOOTSTRAP__=Object.freeze({"apiVersion":"v1","authorization":"Bearer `+strings.Repeat("a", tokenBytes*2)+`"})`) {
				t.Fatalf("dashboard bootstrap missing from %q", body)
			}
			if result.Header.Get("Cache-Control") != "no-store" || result.Header.Get("Pragma") != "no-cache" {
				t.Fatalf("index cache headers = %q, %q", result.Header.Get("Cache-Control"), result.Header.Get("Pragma"))
			}
			policy := result.Header.Get("Content-Security-Policy")
			if !strings.Contains(policy, "frame-ancestors 'none'") || !strings.Contains(policy, "'nonce-") || strings.Contains(policy, "unsafe-inline") {
				t.Fatalf("Content-Security-Policy = %q", policy)
			}
			if result.Header.Get("X-Frame-Options") != "DENY" || result.Header.Get("Cross-Origin-Resource-Policy") != "same-origin" {
				t.Fatalf("dashboard security headers = %#v", result.Header)
			}
		})
	}
}

func TestDashboardBootstrapUsesRotatedToken(t *testing.T) {
	t.Parallel()
	server, original := startTestServer(t)
	defer closeTestServer(t, server)
	if err := server.RotateToken(); err != nil {
		t.Fatal(err)
	}
	rotated, found := server.Endpoint()
	if !found {
		t.Fatal("rotated server endpoint unavailable")
	}

	response := get(t, rotated.BaseURL()+"/index.html?after=rotation", "")
	defer func() { _ = response.Body.Close() }()
	body := readResponseBody(t, response)
	if response.StatusCode != http.StatusOK || !strings.Contains(body, rotated.AuthorizationHeader()) {
		t.Fatalf("rotated dashboard response did not contain current authorization")
	}
	if strings.Contains(body, original.AuthorizationHeader()) {
		t.Fatal("rotated dashboard response contained stale authorization")
	}
}

func TestDashboardServesImmutableAssetsWithoutCredentialInjection(t *testing.T) {
	t.Parallel()
	token, err := parseToken(strings.Repeat("b", tokenBytes*2))
	if err != nil {
		t.Fatal(err)
	}
	assets := fstest.MapFS{
		"index.html":               {Data: []byte("<html><head></head></html>")},
		"assets/index-deadbeef.js": {Data: []byte("export const ready = true;\n")},
	}
	request := httptest.NewRequest(http.MethodGet, "/assets/index-deadbeef.js", nil)
	response := httptest.NewRecorder()
	serveDashboard(response, request, assets, Endpoint{APIVersion: VersionV1, Token: token})

	result := response.Result()
	defer func() { _ = result.Body.Close() }()
	if result.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", result.StatusCode)
	}
	body := readResponseBody(t, result)
	if strings.Contains(body, token.encoded()) || strings.Contains(body, "DARKSTAR_BOOTSTRAP") {
		t.Fatal("static asset contained dashboard credential bootstrap")
	}
	if got := result.Header.Get("Cache-Control"); got != "public, max-age=31536000, immutable" {
		t.Fatalf("Cache-Control = %q", got)
	}
	if contentType := result.Header.Get("Content-Type"); !strings.Contains(contentType, "javascript") {
		t.Fatalf("Content-Type = %q, want JavaScript", contentType)
	}
}

func TestDashboardDoesNotMaskMissingFilesOrAcceptMutations(t *testing.T) {
	t.Parallel()
	assets := fstest.MapFS{"index.html": {Data: []byte("<html><head></head></html>")}}
	endpoint := Endpoint{APIVersion: VersionV1}

	for _, location := range []string{"/assets", "/assets/", "/assets/missing.js", "/favicon.ico"} {
		missing := httptest.NewRecorder()
		serveDashboard(missing, httptest.NewRequest(http.MethodGet, location, nil), assets, endpoint)
		if missing.Code != http.StatusNotFound {
			t.Fatalf("missing asset %q status = %d, want 404", location, missing.Code)
		}
	}

	mutation := httptest.NewRecorder()
	serveDashboard(mutation, httptest.NewRequest(http.MethodPost, "/work/work-01", nil), assets, endpoint)
	if mutation.Code != http.StatusMethodNotAllowed || mutation.Header().Get("Allow") != "GET, HEAD" {
		t.Fatalf("dashboard POST = %d Allow %q", mutation.Code, mutation.Header().Get("Allow"))
	}
}

func TestAPIPathClassificationUsesCleanSegmentBoundary(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		path string
		want bool
	}{
		{path: "/api", want: true},
		{path: "/api/v1", want: true},
		{path: "/work/../api/v1/events", want: true},
		{path: "/apiary", want: false},
		{path: "/work/api/v1", want: false},
	} {
		if got := isAPIPath(test.path); got != test.want {
			t.Errorf("isAPIPath(%q) = %v, want %v", test.path, got, test.want)
		}
	}
}

func TestServerServesEmbeddedDashboardWithoutBearerAndKeepsAPIRoutesProtected(t *testing.T) {
	t.Parallel()
	server, endpoint := startTestServer(t)
	defer closeTestServer(t, server)

	dashboard := get(t, endpoint.BaseURL()+"/", "")
	defer func() { _ = dashboard.Body.Close() }()
	if dashboard.StatusCode != http.StatusOK {
		t.Fatalf("GET / status = %d", dashboard.StatusCode)
	}
	body := readResponseBody(t, dashboard)
	if !strings.Contains(body, endpoint.AuthorizationHeader()) || !strings.Contains(body, "DARKSTAR") {
		t.Fatal("embedded dashboard omitted shell or bearer bootstrap")
	}

	api := get(t, endpoint.BaseURL()+"/api/v1/work-items", "")
	defer func() { _ = api.Body.Close() }()
	assertAPIError(t, api, http.StatusUnauthorized, "UNAUTHENTICATED")
}

func readResponseBody(t *testing.T, response *http.Response) string {
	t.Helper()
	content, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	return string(content)
}
