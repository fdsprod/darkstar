package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path"
	"strconv"
	"strings"

	"darkstar/src/core/runexecution"
	"darkstar/src/core/workflow"
	"darkstar/src/ports/statestore"
	"darkstar/src/ports/workflowstore"
)

// RunService is the command/query boundary published by the local API.
type RunService interface {
	Create(context.Context, runexecution.CreateRequest, string) (statestore.RunProjection, error)
	Start(context.Context, runexecution.StartRequest, string) (runexecution.View, error)
	List(context.Context, int, string) (runexecution.Page, error)
	Get(context.Context, string) (runexecution.View, error)
	Pause(context.Context, runexecution.ControlRequest) (statestore.RunProjection, error)
	Resume(context.Context, runexecution.ControlRequest) (statestore.RunProjection, error)
	Retry(context.Context, runexecution.RetryRequest) (statestore.RunProjection, error)
	Continue(context.Context, runexecution.ContinueRequest) (statestore.RunProjection, error)
	Cancel(context.Context, runexecution.ControlRequest) (statestore.RunProjection, error)
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
		switch request.Method {
		case http.MethodGet, http.MethodHead:
			s.serveRunList(response, request, requestID, runs)
		case http.MethodPost:
			s.serveRunStart(response, request, requestID, runs)
		default:
			response.Header().Set("Allow", "GET, HEAD, POST")
			writeAPIError(response, http.StatusMethodNotAllowed, apiError{SchemaVersion: 1, Code: "METHOD_NOT_ALLOWED", Message: "The HTTP method is not supported for this resource.", RequestID: requestID})
		}
		return
	}
	relative := strings.TrimPrefix(clean, "/api/v1/runs/")
	segments := strings.Split(relative, "/")
	if len(segments) == 2 {
		s.serveRunControl(response, request, requestID, runs, segments[0], segments[1])
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

type retryRunBody struct {
	NodeID string `json:"nodeId,omitempty"`
}

type continueRunBody struct {
	Until string `json:"until"`
}

func (s *Server) serveRunControl(response http.ResponseWriter, request *http.Request, requestID string, runs RunService, runID, action string) {
	if !runIDPattern.MatchString(runID) || (action != "pause" && action != "resume" && action != "retry" && action != "continue" && action != "cancel") {
		writeAPIError(response, http.StatusNotFound, apiError{SchemaVersion: 1, Code: "NOT_FOUND", Message: "The requested run control was not found.", RequestID: requestID})
		return
	}
	if request.Method != http.MethodPost {
		response.Header().Set("Allow", "POST")
		writeAPIError(response, http.StatusMethodNotAllowed, apiError{SchemaVersion: 1, Code: "METHOD_NOT_ALLOWED", Message: "The HTTP method is not supported for this resource.", RequestID: requestID})
		return
	}
	if request.URL.RawQuery != "" {
		writeAPIError(response, http.StatusBadRequest, apiError{SchemaVersion: 1, Code: "VALIDATION_FAILED", Message: "Run controls do not accept query parameters.", RequestID: requestID})
		return
	}
	key, ok := requireIdempotencyKey(response, request, requestID)
	if !ok {
		return
	}
	expected, err := parseIfMatch(request.Header.Get("If-Match"))
	if err != nil {
		writeAPIError(response, http.StatusBadRequest, apiError{SchemaVersion: 1, Code: "VALIDATION_FAILED", Message: err.Error(), RequestID: requestID})
		return
	}
	body, err := io.ReadAll(io.LimitReader(request.Body, 4097))
	if err != nil || len(body) > 4096 {
		writeAPIError(response, http.StatusBadRequest, apiError{SchemaVersion: 1, Code: "VALIDATION_FAILED", Message: "The run control body is invalid.", RequestID: requestID})
		return
	}
	common := runexecution.ControlRequest{
		RunID: runID, ExpectedResourceVersion: expected, IdempotencyKey: key,
		Actor: statestore.Actor{Type: statestore.ActorUser, ID: "local-user"},
	}
	var value statestore.RunProjection
	switch action {
	case "pause", "resume", "cancel":
		if len(strings.TrimSpace(string(body))) != 0 {
			writeAPIError(response, http.StatusBadRequest, apiError{SchemaVersion: 1, Code: "VALIDATION_FAILED", Message: "This run control does not accept a request body.", RequestID: requestID})
			return
		}
		switch action {
		case "pause":
			value, err = runs.Pause(request.Context(), common)
		case "resume":
			value, err = runs.Resume(request.Context(), common)
		case "cancel":
			value, err = runs.Cancel(request.Context(), common)
		}
	case "retry":
		var input retryRunBody
		if len(strings.TrimSpace(string(body))) != 0 && decodeRunVariant(body, &input) != nil {
			writeAPIError(response, http.StatusBadRequest, apiError{SchemaVersion: 1, Code: "VALIDATION_FAILED", Message: "Retry accepts only an optional nodeId.", RequestID: requestID})
			return
		}
		value, err = runs.Retry(request.Context(), runexecution.RetryRequest{ControlRequest: common, NodeID: input.NodeID})
	case "continue":
		var input continueRunBody
		if len(strings.TrimSpace(string(body))) == 0 || decodeRunVariant(body, &input) != nil || strings.TrimSpace(input.Until) == "" {
			writeAPIError(response, http.StatusBadRequest, apiError{SchemaVersion: 1, Code: "VALIDATION_FAILED", Message: "Continue requires exactly one non-empty until boundary.", RequestID: requestID})
			return
		}
		value, err = runs.Continue(request.Context(), runexecution.ContinueRequest{ControlRequest: common, UntilNodeID: input.Until})
	}
	if err != nil {
		writeRunControlError(response, requestID, err)
		return
	}
	response.Header().Set("ETag", fmt.Sprintf(`"%d"`, value.ResourceVersion))
	writeJSON(response, http.StatusOK, value)
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
	body, err := io.ReadAll(io.LimitReader(request.Body, 4097))
	if err != nil || len(body) == 0 || len(body) > 4096 {
		writeAPIError(response, http.StatusBadRequest, apiError{SchemaVersion: 1, Code: "VALIDATION_FAILED", Message: "The run start request must be one valid JSON object.", RequestID: requestID})
		return
	}
	var fields map[string]json.RawMessage
	if err := decodeRunVariant(body, &fields); err != nil {
		writeAPIError(response, http.StatusBadRequest, apiError{SchemaVersion: 1, Code: "VALIDATION_FAILED", Message: "The run start request must match exactly one supported request shape.", RequestID: requestID})
		return
	}
	if _, fake := fields["scenario"]; !fake {
		var input runexecution.CreateRequest
		if err := decodeRunVariant(body, &input); err != nil {
			writeAPIError(response, http.StatusBadRequest, apiError{SchemaVersion: 1, Code: "VALIDATION_FAILED", Message: "The work-backed run request must contain only workItemId, workflowId, and workflowVersion.", RequestID: requestID})
			return
		}
		value, err := runs.Create(request.Context(), input, key)
		if err != nil {
			writeRunCommandError(response, requestID, err)
			return
		}
		response.Header().Set("Location", "/api/v1/runs/"+value.RunID)
		response.Header().Set("ETag", fmt.Sprintf(`"%d"`, value.ResourceVersion))
		writeJSON(response, http.StatusCreated, value)
		return
	}
	var input runexecution.StartRequest
	if len(fields) != 1 || decodeRunVariant(body, &input) != nil {
		writeAPIError(response, http.StatusBadRequest, apiError{SchemaVersion: 1, Code: "VALIDATION_FAILED", Message: "The fake run request must contain only scenario.", RequestID: requestID})
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

func (s *Server) serveRunList(response http.ResponseWriter, request *http.Request, requestID string, runs RunService) {
	query := request.URL.Query()
	for key, values := range query {
		if (key != "limit" && key != "after") || len(values) != 1 {
			writeAPIError(response, http.StatusBadRequest, apiError{SchemaVersion: 1, Code: "VALIDATION_FAILED", Message: "The run list supports one limit and one after cursor.", RequestID: requestID})
			return
		}
	}
	limit := 50
	if raw := query.Get("limit"); raw != "" {
		var err error
		limit, err = strconv.Atoi(raw)
		if err != nil || limit < 1 || limit > 200 {
			writeAPIError(response, http.StatusBadRequest, apiError{SchemaVersion: 1, Code: "VALIDATION_FAILED", Message: "limit must be between 1 and 200.", RequestID: requestID})
			return
		}
	}
	after := query.Get("after")
	if after != "" && !runIDPattern.MatchString(after) {
		writeAPIError(response, http.StatusBadRequest, apiError{SchemaVersion: 1, Code: "VALIDATION_FAILED", Message: "after must be a canonical run cursor.", RequestID: requestID})
		return
	}
	page, err := runs.List(request.Context(), limit, after)
	if err != nil {
		writeRunCommandError(response, requestID, err)
		return
	}
	writeJSON(response, http.StatusOK, page)
}

func decodeRunVariant(body []byte, destination any) error {
	decoder := json.NewDecoder(strings.NewReader(string(body)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil || decoder.Decode(new(any)) != io.EOF {
		return errors.New("invalid run request")
	}
	return nil
}

func writeRunCommandError(response http.ResponseWriter, requestID string, err error) {
	if errors.Is(err, statestore.ErrNotFound) || errors.Is(err, workflowstore.ErrNotFound) {
		writeAPIError(response, http.StatusNotFound, apiError{SchemaVersion: 1, Code: "NOT_FOUND", Message: "The requested work item or workflow was not found.", RequestID: requestID})
		return
	}
	if errors.Is(err, runexecution.ErrWorkflowUnavailable) {
		writeAPIError(response, http.StatusServiceUnavailable, apiError{SchemaVersion: 1, Code: "WORKFLOW_SERVICE_UNAVAILABLE", Message: err.Error(), RequestID: requestID, Retryable: true})
		return
	}
	if errors.Is(err, runexecution.ErrInvalidRequest) || errors.Is(err, runexecution.ErrPageCursor) {
		writeAPIError(response, http.StatusBadRequest, apiError{SchemaVersion: 1, Code: "VALIDATION_FAILED", Message: err.Error(), RequestID: requestID})
		return
	}
	var issues workflow.ValidationErrors
	if errors.As(err, &issues) {
		writeWorkflowValidation(response, requestID, issues)
		return
	}
	writeAPIError(response, http.StatusConflict, apiError{SchemaVersion: 1, Code: "RUN_START_FAILED", Message: err.Error(), RequestID: requestID, Retryable: errors.Is(err, runexecution.ErrCommandInProgress)})
}

func writeRunControlError(response http.ResponseWriter, requestID string, err error) {
	var conflict *runexecution.ControlConflictError
	var transition *runexecution.InvalidTransitionError
	switch {
	case errors.Is(err, statestore.ErrNotFound):
		writeAPIError(response, http.StatusNotFound, apiError{SchemaVersion: 1, Code: "NOT_FOUND", Message: "The requested run was not found.", RequestID: requestID})
	case errors.Is(err, runexecution.ErrInvalidControl), errors.Is(err, runexecution.ErrInvalidRequest):
		writeAPIError(response, http.StatusBadRequest, apiError{SchemaVersion: 1, Code: "VALIDATION_FAILED", Message: err.Error(), RequestID: requestID})
	case errors.As(err, &conflict):
		current := int64(conflict.Current)
		writeAPIError(response, http.StatusPreconditionFailed, apiError{SchemaVersion: 1, Code: "RUN_VERSION_CONFLICT", Message: err.Error(), RequestID: requestID, ResourceVersion: &current})
	case errors.As(err, &transition):
		writeAPIError(response, http.StatusConflict, apiError{SchemaVersion: 1, Code: "RUN_CONTROL_INVALID_TRANSITION", Message: err.Error(), RequestID: requestID})
	case errors.Is(err, runexecution.ErrCommandInProgress):
		writeAPIError(response, http.StatusConflict, apiError{SchemaVersion: 1, Code: "COMMAND_IN_PROGRESS", Message: err.Error(), RequestID: requestID, Retryable: true})
	default:
		var issues workflow.ValidationErrors
		if errors.As(err, &issues) {
			writeWorkflowValidation(response, requestID, issues)
			return
		}
		writeAPIError(response, http.StatusConflict, apiError{SchemaVersion: 1, Code: "RUN_CONTROL_FAILED", Message: err.Error(), RequestID: requestID})
	}
}
