package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"path"
	"strings"

	"darkstar/src/core/runexecution"
	"darkstar/src/ports/statestore"
)

// RunService is the command/query boundary published by the local API.
type RunService interface {
	Start(context.Context, runexecution.StartRequest, string) (runexecution.View, error)
	Get(context.Context, string) (runexecution.View, error)
}

func (s *Server) serveRuns(response http.ResponseWriter, request *http.Request, requestID string) {
	s.mu.RLock()
	runs := s.runs
	s.mu.RUnlock()
	if runs == nil {
		writeAPIError(response, http.StatusServiceUnavailable, apiError{SchemaVersion: 1, Code: "RUN_SERVICE_UNAVAILABLE", Message: "Run execution is not configured.", RequestID: requestID, Retryable: true})
		return
	}
	clean := path.Clean(request.URL.Path)
	if clean == "/api/v1/runs" {
		s.serveRunStart(response, request, requestID, runs)
		return
	}
	if request.Method != http.MethodGet && request.Method != http.MethodHead {
		response.Header().Set("Allow", "GET, HEAD")
		writeAPIError(response, http.StatusMethodNotAllowed, apiError{SchemaVersion: 1, Code: "METHOD_NOT_ALLOWED", Message: "The HTTP method is not supported for this resource.", RequestID: requestID})
		return
	}
	if request.URL.RawQuery != "" {
		writeAPIError(response, http.StatusBadRequest, apiError{SchemaVersion: 1, Code: "VALIDATION_FAILED", Message: "The run query does not accept query parameters.", RequestID: requestID})
		return
	}
	runID := strings.TrimPrefix(clean, "/api/v1/runs/")
	if !runIDPattern.MatchString(runID) {
		writeAPIError(response, http.StatusNotFound, apiError{SchemaVersion: 1, Code: "NOT_FOUND", Message: "The requested run was not found.", RequestID: requestID})
		return
	}
	view, err := runs.Get(request.Context(), runID)
	if errors.Is(err, statestore.ErrNotFound) {
		writeAPIError(response, http.StatusNotFound, apiError{SchemaVersion: 1, Code: "NOT_FOUND", Message: "The requested run was not found.", RequestID: requestID})
		return
	}
	if err != nil {
		writeAPIError(response, http.StatusInternalServerError, apiError{SchemaVersion: 1, Code: "RUN_QUERY_FAILED", Message: "The run projection could not be read.", RequestID: requestID, Retryable: true})
		return
	}
	writeJSON(response, http.StatusOK, view)
}

func (s *Server) serveRunStart(response http.ResponseWriter, request *http.Request, requestID string, runs RunService) {
	if request.Method != http.MethodPost {
		response.Header().Set("Allow", "POST")
		writeAPIError(response, http.StatusMethodNotAllowed, apiError{SchemaVersion: 1, Code: "METHOD_NOT_ALLOWED", Message: "The HTTP method is not supported for this resource.", RequestID: requestID})
		return
	}
	if request.URL.RawQuery != "" {
		writeAPIError(response, http.StatusBadRequest, apiError{SchemaVersion: 1, Code: "VALIDATION_FAILED", Message: "The run start request does not accept query parameters.", RequestID: requestID})
		return
	}
	key := request.Header.Get("Idempotency-Key")
	if strings.TrimSpace(key) != key || len(key) < 8 || len(key) > 128 {
		writeAPIError(response, http.StatusBadRequest, apiError{SchemaVersion: 1, Code: "VALIDATION_FAILED", Message: "Idempotency-Key must be between 8 and 128 bytes without surrounding whitespace.", RequestID: requestID})
		return
	}
	decoder := json.NewDecoder(io.LimitReader(request.Body, 4097))
	decoder.DisallowUnknownFields()
	var input runexecution.StartRequest
	if err := decoder.Decode(&input); err != nil || decoder.Decode(new(any)) != io.EOF {
		writeAPIError(response, http.StatusBadRequest, apiError{SchemaVersion: 1, Code: "VALIDATION_FAILED", Message: "The run start request must be one valid JSON object.", RequestID: requestID})
		return
	}
	view, err := runs.Start(request.Context(), input, key)
	if errors.Is(err, runexecution.ErrInvalidScenario) {
		writeAPIError(response, http.StatusBadRequest, apiError{SchemaVersion: 1, Code: "SCENARIO_UNSUPPORTED", Message: "The requested fake-provider scenario is not supported.", RequestID: requestID})
		return
	}
	if err != nil {
		writeAPIError(response, http.StatusConflict, apiError{SchemaVersion: 1, Code: "RUN_START_FAILED", Message: err.Error(), RequestID: requestID, Retryable: errors.Is(err, runexecution.ErrCommandInProgress)})
		return
	}
	response.Header().Set("Location", "/api/v1/runs/"+view.Run.RunID)
	writeJSON(response, http.StatusAccepted, view)
}
