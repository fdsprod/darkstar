package api

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"path"
	"strings"
	"sync"
	"time"
)

const requestIDHeader = "X-Request-Id"

type serverState uint8

const (
	serverNew serverState = iota
	serverRunning
	serverClosing
	serverClosed
)

// Server owns one IPv4 loopback listener and its protected endpoint snapshot.
// The bind address is intentionally not configurable in the MVP.
type Server struct {
	endpointPath string
	now          func() time.Time

	mu       sync.RWMutex
	state    serverState
	endpoint Endpoint
	http     *http.Server
}

// NewServer constructs an unstarted loopback API server.
func NewServer(runtimeDirectory string) (*Server, error) {
	statePath, err := endpointPath(runtimeDirectory)
	if err != nil {
		return nil, err
	}
	return &Server{
		endpointPath: statePath,
		now:          time.Now,
		state:        serverNew,
	}, nil
}

// Start binds an OS-assigned IPv4 loopback port, creates a fresh 256-bit token,
// publishes endpoint.json, and only then begins accepting requests.
func (s *Server) Start(ctx context.Context, pid int, processStartedAt time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state != serverNew {
		return fmt.Errorf("API server cannot start from state %d", s.state)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if pid <= 0 || processStartedAt.IsZero() {
		return errors.New("API server requires a complete process identity")
	}

	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return fmt.Errorf("bind API loopback listener: %w", err)
	}
	closeListener := true
	defer func() {
		if closeListener {
			_ = listener.Close()
		}
	}()

	token, err := newToken()
	if err != nil {
		return err
	}
	port := listener.Addr().(*net.TCPAddr).Port
	createdAt := s.now().UTC()
	endpoint := Endpoint{
		SchemaVersion:    EndpointSchemaVersion,
		APIVersion:       VersionV1,
		PID:              pid,
		ProcessStartedAt: processStartedAt.UTC(),
		Port:             port,
		Token:            token,
		CreatedAt:        createdAt,
	}
	if err := writeEndpoint(s.endpointPath, endpoint); err != nil {
		return err
	}

	httpServer := &http.Server{
		Handler:           s,
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    32 << 10,
	}
	s.endpoint = endpoint
	s.http = httpServer
	s.state = serverRunning
	closeListener = false
	go func() {
		_ = httpServer.Serve(listener)
	}()
	return nil
}

// Endpoint returns the current discovery snapshot. Its token remains redacted
// under ordinary formatting; clients obtain only the complete header value.
func (s *Server) Endpoint() (Endpoint, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.state != serverRunning {
		return Endpoint{}, false
	}
	return s.endpoint, true
}

// RotateToken atomically publishes a fresh credential and invalidates the old
// one before another request can pass authentication.
func (s *Server) RotateToken() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state != serverRunning {
		return errors.New("API server is not running")
	}
	token, err := newToken()
	if err != nil {
		return err
	}
	rotated := s.endpoint
	rotated.Token = token
	rotated.CreatedAt = s.now().UTC()
	if err := writeEndpoint(s.endpointPath, rotated); err != nil {
		return err
	}
	s.endpoint = rotated
	return nil
}

// Close stops accepting requests and removes endpoint state only when it still
// belongs to this server instance.
func (s *Server) Close() error {
	s.mu.Lock()
	if s.state == serverClosed || s.state == serverNew {
		s.state = serverClosed
		s.mu.Unlock()
		return nil
	}
	if s.state == serverClosing {
		s.mu.Unlock()
		return nil
	}
	s.state = serverClosing
	httpServer := s.http
	owned := s.endpoint
	s.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	shutdownErr := httpServer.Shutdown(ctx)
	removeErr := removeEndpointIfOwned(s.endpointPath, owned)

	s.mu.Lock()
	s.state = serverClosed
	s.http = nil
	s.mu.Unlock()
	return errors.Join(shutdownErr, removeErr)
}

func (s *Server) ServeHTTP(response http.ResponseWriter, request *http.Request) {
	requestID := newRequestID()
	response.Header().Set(requestIDHeader, requestID)
	response.Header().Set("X-Darkstar-API-Version", string(VersionV1))
	response.Header().Set("Cache-Control", "no-store")
	response.Header().Set("X-Content-Type-Options", "nosniff")

	if request.URL.Path == "/api/v1/health" && (request.Method == http.MethodGet || request.Method == http.MethodHead) {
		writeJSON(response, http.StatusOK, healthResponse{
			SchemaVersion: 1,
			Status:        "ok",
			APIVersions:   SupportedVersions(),
		})
		return
	}
	if !s.authenticate(request) {
		response.Header().Set("WWW-Authenticate", `Bearer realm="darkstar-local"`)
		writeAPIError(response, http.StatusUnauthorized, apiError{
			SchemaVersion: 1,
			Code:          "UNAUTHENTICATED",
			Message:       "A valid local API bearer token is required.",
			RequestID:     requestID,
			Retryable:     false,
		})
		return
	}

	if version, ok := requestedAPIVersion(request.URL.Path); ok && version != VersionV1 {
		writeAPIError(response, http.StatusUpgradeRequired, apiError{
			SchemaVersion: 1,
			Code:          "API_VERSION_UNSUPPORTED",
			Message:       "The requested API version is not supported.",
			RequestID:     requestID,
			Retryable:     false,
			Details: []errorDetail{{
				Field:   "apiVersion",
				Code:    "UNSUPPORTED",
				Message: "Supported versions: v1.",
			}},
		})
		return
	}

	if path.Clean(request.URL.Path) == "/api/v1" {
		if request.Method != http.MethodGet && request.Method != http.MethodHead {
			response.Header().Set("Allow", "GET, HEAD")
			writeAPIError(response, http.StatusMethodNotAllowed, apiError{
				SchemaVersion: 1,
				Code:          "METHOD_NOT_ALLOWED",
				Message:       "The HTTP method is not supported for this resource.",
				RequestID:     requestID,
				Retryable:     false,
			})
			return
		}
		writeJSON(response, http.StatusOK, apiRootResponse{SchemaVersion: 1, APIVersion: VersionV1})
		return
	}

	writeAPIError(response, http.StatusNotFound, apiError{
		SchemaVersion: 1,
		Code:          "NOT_FOUND",
		Message:       "The requested local API resource was not found.",
		RequestID:     requestID,
		Retryable:     false,
	})
}

func (s *Server) authenticate(request *http.Request) bool {
	values := request.Header.Values("Authorization")
	if len(values) != 1 {
		return false
	}
	scheme, credential, found := strings.Cut(values[0], " ")
	if !found || !strings.EqualFold(scheme, "Bearer") || credential == "" || strings.ContainsAny(credential, " \t\r\n") {
		return false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.state == serverRunning && s.endpoint.Token.matches(credential)
}

func requestedAPIVersion(requestPath string) (Version, bool) {
	segments := strings.Split(strings.TrimPrefix(requestPath, "/"), "/")
	if len(segments) < 2 || segments[0] != "api" || segments[1] == "" {
		return "", false
	}
	return Version(segments[1]), true
}

type healthResponse struct {
	SchemaVersion int       `json:"schemaVersion"`
	Status        string    `json:"status"`
	APIVersions   []Version `json:"apiVersions"`
}

type apiRootResponse struct {
	SchemaVersion int     `json:"schemaVersion"`
	APIVersion    Version `json:"apiVersion"`
}

type apiError struct {
	SchemaVersion   int           `json:"schemaVersion"`
	Code            string        `json:"code"`
	Message         string        `json:"message"`
	RequestID       string        `json:"requestId"`
	Retryable       bool          `json:"retryable"`
	ResourceVersion *int64        `json:"resourceVersion,omitempty"`
	Details         []errorDetail `json:"details,omitempty"`
}

type errorDetail struct {
	Field   string `json:"field"`
	Code    string `json:"code"`
	Message string `json:"message,omitempty"`
}

func writeAPIError(response http.ResponseWriter, status int, problem apiError) {
	writeJSON(response, status, problem)
}

func writeJSON(response http.ResponseWriter, status int, value any) {
	response.Header().Set("Content-Type", "application/json; charset=utf-8")
	response.WriteHeader(status)
	_ = json.NewEncoder(response).Encode(value)
}

func newRequestID() string {
	value := make([]byte, 16)
	if _, err := rand.Read(value); err != nil {
		return "request-id-unavailable"
	}
	return hex.EncodeToString(value)
}
