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
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/fdsprod/darkstar/runtime/src/core/health"
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
	recovery RecoveryStatus
	doctor   DoctorReporter
	streams  *StreamServices
	exporter RunExporter

	streamPollInterval      time.Duration
	streamKeepaliveInterval time.Duration
}

// DoctorReporter produces the authenticated, detailed subsystem health report.
type DoctorReporter interface {
	ReportForProject(context.Context, string) (health.Report, error)
}

// NewServer constructs an unstarted loopback API server.
func NewServer(runtimeDirectory string) (*Server, error) {
	statePath, err := endpointPath(runtimeDirectory)
	if err != nil {
		return nil, err
	}
	return &Server{
		endpointPath:            statePath,
		now:                     time.Now,
		state:                   serverNew,
		recovery:                RecoveryStatus{},
		streamPollInterval:      100 * time.Millisecond,
		streamKeepaliveInterval: 15 * time.Second,
	}, nil
}

// RecoveryStatus is the safe startup-reconciliation summary exposed by the
// public API. It contains no authority evidence or credentials.
type RecoveryStatus struct {
	Reconciled        int `json:"reconciled"`
	ReconcileRequired int `json:"reconcileRequired"`
}

// SchedulingAllowed derives scheduler admission from the unresolved count.
func (status RecoveryStatus) SchedulingAllowed() bool { return status.ReconcileRequired == 0 }

// SetRecoveryStatus configures startup state before Start publishes the API.
func (s *Server) SetRecoveryStatus(status RecoveryStatus) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state != serverNew {
		return errors.New("API recovery status can only be set before start")
	}
	if status.Reconciled < 0 || status.ReconcileRequired < 0 || status.ReconcileRequired > status.Reconciled {
		return errors.New("API recovery status is contradictory")
	}
	s.recovery = status
	return nil
}

// SetDoctor configures detailed health reporting before Start publishes the API.
func (s *Server) SetDoctor(reporter DoctorReporter) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state != serverNew {
		return errors.New("API doctor can only be set before start")
	}
	if reporter == nil {
		return errors.New("API doctor is required")
	}
	s.doctor = reporter
	return nil
}

// SetStreams installs the complete event and log streaming capability before
// the endpoint is published. Configuring the pair atomically prevents a daemon
// from advertising only half of the public streaming contract.
func (s *Server) SetStreams(services StreamServices) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state != serverNew {
		return errors.New("API streams can only be set before start")
	}
	if services.Events == nil || services.Logs == nil {
		return errors.New("API event and log sources are required")
	}
	s.streams = &services
	return nil
}

// SetRunExporter installs the finite run-export capability before Start.
func (s *Server) SetRunExporter(exporter RunExporter) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state != serverNew {
		return errors.New("API run exporter can only be set before start")
	}
	if exporter == nil {
		return errors.New("API run exporter is required")
	}
	s.exporter = exporter
	return nil
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
			Recovery:      s.recovery,
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

	if path.Clean(request.URL.Path) == "/api/v1/events" {
		s.serveEvents(response, request, requestID)
		return
	}
	if strings.HasPrefix(path.Clean(request.URL.Path), "/api/v1/logs/") {
		s.serveLog(response, request, requestID)
		return
	}
	if strings.HasPrefix(path.Clean(request.URL.Path), "/api/v1/runs/") && strings.HasSuffix(path.Clean(request.URL.Path), "/export") {
		s.serveRunExport(response, request, requestID)
		return
	}

	if path.Clean(request.URL.Path) == "/api/v1/doctor" {
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
		s.mu.RLock()
		reporter := s.doctor
		s.mu.RUnlock()
		if reporter == nil {
			writeAPIError(response, http.StatusServiceUnavailable, apiError{
				SchemaVersion: 1,
				Code:          "DOCTOR_UNAVAILABLE",
				Message:       "Detailed subsystem health reporting is not configured.",
				RequestID:     requestID,
				Retryable:     true,
			})
			return
		}
		query := request.URL.Query()
		if len(query) > 1 || (len(query) == 1 && !query.Has("projectRoot")) || len(query["projectRoot"]) > 1 {
			writeAPIError(response, http.StatusBadRequest, apiError{
				SchemaVersion: 1,
				Code:          "VALIDATION_FAILED",
				Message:       "The doctor request query is invalid.",
				RequestID:     requestID,
				Retryable:     false,
			})
			return
		}
		projectRoot := query.Get("projectRoot")
		if projectRoot != "" && !pathIsAbsolute(projectRoot) {
			writeAPIError(response, http.StatusBadRequest, apiError{
				SchemaVersion: 1,
				Code:          "VALIDATION_FAILED",
				Message:       "The doctor project root must be absolute.",
				RequestID:     requestID,
				Retryable:     false,
			})
			return
		}
		report, err := reporter.ReportForProject(request.Context(), projectRoot)
		if err != nil {
			writeAPIError(response, http.StatusServiceUnavailable, apiError{
				SchemaVersion: 1,
				Code:          "DOCTOR_FAILED",
				Message:       "Detailed subsystem health reporting failed.",
				RequestID:     requestID,
				Retryable:     true,
			})
			return
		}
		writeJSON(response, http.StatusOK, report)
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
		writeJSON(response, http.StatusOK, apiRootResponse{SchemaVersion: 1, APIVersion: VersionV1, Recovery: s.recovery})
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

func pathIsAbsolute(value string) bool {
	// API and daemon run on the same operating system, so filepath semantics are
	// the authoritative validation for the local project path.
	return filepath.IsAbs(value)
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
	SchemaVersion int            `json:"schemaVersion"`
	Status        string         `json:"status"`
	APIVersions   []Version      `json:"apiVersions"`
	Recovery      RecoveryStatus `json:"recovery"`
}

type apiRootResponse struct {
	SchemaVersion int            `json:"schemaVersion"`
	APIVersion    Version        `json:"apiVersion"`
	Recovery      RecoveryStatus `json:"recovery"`
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
