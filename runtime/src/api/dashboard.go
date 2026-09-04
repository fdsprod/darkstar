package api

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io/fs"
	"net/http"
	"path"
	"strings"
	"time"
)

const dashboardBootstrapMarker = "</head>"

type dashboardBootstrap struct {
	APIVersion    Version `json:"apiVersion"`
	Authorization string  `json:"authorization"`
}

func serveDashboard(response http.ResponseWriter, request *http.Request, assets fs.FS, endpoint Endpoint) {
	nonce, err := newDashboardNonce()
	if err != nil {
		http.Error(response, "dashboard unavailable", http.StatusServiceUnavailable)
		return
	}
	setDashboardSecurityHeaders(response, nonce)
	if request.Method != http.MethodGet && request.Method != http.MethodHead {
		response.Header().Set("Allow", "GET, HEAD")
		http.Error(response, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	requested := strings.TrimPrefix(path.Clean("/"+request.URL.Path), "/")
	if requested == "." || requested == "" || requested == "index.html" {
		serveDashboardIndex(response, request, assets, endpoint, nonce)
		return
	}
	if info, err := fs.Stat(assets, requested); err == nil && info.Mode().IsRegular() {
		serveDashboardFile(response, request, assets, requested, strings.HasPrefix(requested, "assets/"))
		return
	}

	// File-like requests should remain real 404s. Extensionless locations are
	// client-side routes and receive the SPA entry document.
	if requested == "assets" || strings.HasPrefix(requested, "assets/") || path.Ext(requested) != "" {
		http.NotFound(response, request)
		return
	}
	serveDashboardIndex(response, request, assets, endpoint, nonce)
}

func serveDashboardIndex(response http.ResponseWriter, request *http.Request, assets fs.FS, endpoint Endpoint, nonce string) {
	content, err := fs.ReadFile(assets, "index.html")
	if err != nil {
		http.Error(response, "dashboard unavailable", http.StatusServiceUnavailable)
		return
	}
	bootstrap, err := json.Marshal(dashboardBootstrap{
		APIVersion:    endpoint.APIVersion,
		Authorization: endpoint.AuthorizationHeader(),
	})
	if err != nil {
		http.Error(response, "dashboard unavailable", http.StatusServiceUnavailable)
		return
	}
	marker := []byte(dashboardBootstrapMarker)
	position := bytes.Index(bytes.ToLower(content), marker)
	if position < 0 {
		http.Error(response, "dashboard unavailable", http.StatusServiceUnavailable)
		return
	}
	script := append([]byte(`<script nonce="`+nonce+`">window.__DARKSTAR_BOOTSTRAP__=Object.freeze(`), bootstrap...)
	script = append(script, []byte(`);</script>`)...)
	content = bytes.Join([][]byte{content[:position], script, content[position:]}, nil)

	response.Header().Set("Cache-Control", "no-store")
	response.Header().Set("Pragma", "no-cache")
	response.Header().Set("Content-Type", "text/html; charset=utf-8")
	http.ServeContent(response, request, "index.html", time.Time{}, bytes.NewReader(content))
}

func serveDashboardFile(response http.ResponseWriter, request *http.Request, assets fs.FS, name string, immutable bool) {
	content, err := fs.ReadFile(assets, name)
	if errors.Is(err, fs.ErrNotExist) {
		http.NotFound(response, request)
		return
	}
	if err != nil {
		http.Error(response, "dashboard unavailable", http.StatusServiceUnavailable)
		return
	}
	if immutable {
		response.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	}
	http.ServeContent(response, request, path.Base(name), time.Time{}, bytes.NewReader(content))
}

func setDashboardSecurityHeaders(response http.ResponseWriter, nonce string) {
	response.Header().Set("Content-Security-Policy", "default-src 'self'; base-uri 'none'; frame-ancestors 'none'; object-src 'none'; script-src 'self' 'nonce-"+nonce+"'; style-src 'self'; connect-src 'self'; img-src 'self' data:; font-src 'self'")
	response.Header().Set("Cross-Origin-Resource-Policy", "same-origin")
	response.Header().Set("Referrer-Policy", "no-referrer")
	response.Header().Set("X-Frame-Options", "DENY")
}

func newDashboardNonce() (string, error) {
	value := make([]byte, 18)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return base64.RawStdEncoding.EncodeToString(value), nil
}

func isAPIPath(requestPath string) bool {
	cleaned := path.Clean("/" + requestPath)
	return cleaned == "/api" || strings.HasPrefix(cleaned, "/api/")
}
