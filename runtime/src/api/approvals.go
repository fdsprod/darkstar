package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"path"
	"regexp"
	"strconv"
	"strings"

	checkpoint "darkstar/src/core/artifactcheckpoint"
	checkpointport "darkstar/src/ports/artifactcheckpoint"
	"darkstar/src/ports/statestore"
)

var approvalIDPattern = regexp.MustCompile(`^approval_[0-9A-HJKMNP-TV-Z]{26}$`)

// ApprovalService is the artifact checkpoint decision boundary published by the API.
type ApprovalService interface {
	Decide(context.Context, checkpoint.DecisionRequest) (checkpointport.Round, error)
	Round(context.Context, string) (checkpointport.Round, error)
	History(context.Context, string) (checkpointport.History, error)
	List(context.Context, checkpoint.ListRequest) (checkpointport.Queue, error)
}

type approvalDecisionBody struct {
	Action       checkpointport.Action `json:"action"`
	ScopeDigest  string                `json:"scopeDigest"`
	PolicyDigest string                `json:"policyDigest"`
	Comment      string                `json:"comment,omitempty"`
}

func (s *Server) serveApprovals(response http.ResponseWriter, request *http.Request, requestID string) {
	s.mu.RLock()
	service := s.approvals
	s.mu.RUnlock()
	if service == nil {
		writeAPIError(response, http.StatusServiceUnavailable, apiError{SchemaVersion: 1, Code: "APPROVAL_SERVICE_UNAVAILABLE", Message: "Artifact checkpoint decisions are not configured.", RequestID: requestID, Retryable: true})
		return
	}
	if request.URL.RawQuery != "" {
		writeAPIError(response, http.StatusBadRequest, apiError{SchemaVersion: 1, Code: "VALIDATION_FAILED", Message: "Approval decisions do not accept query parameters.", RequestID: requestID})
		return
	}
	clean := path.Clean(request.URL.Path)
	prefix := "/api/v1/approvals/"
	if !strings.HasPrefix(clean, prefix) {
		writeAPIError(response, http.StatusNotFound, apiError{SchemaVersion: 1, Code: "NOT_FOUND", Message: "The requested approval was not found.", RequestID: requestID})
		return
	}
	relative := strings.TrimPrefix(clean, prefix)
	segments := strings.Split(relative, "/")
	approvalID := segments[0]
	if !approvalIDPattern.MatchString(approvalID) {
		writeAPIError(response, http.StatusNotFound, apiError{SchemaVersion: 1, Code: "NOT_FOUND", Message: "The requested approval was not found.", RequestID: requestID})
		return
	}
	if len(segments) == 1 {
		if request.Method != http.MethodGet && request.Method != http.MethodHead {
			response.Header().Set("Allow", "GET, HEAD")
			writeAPIError(response, http.StatusMethodNotAllowed, apiError{SchemaVersion: 1, Code: "METHOD_NOT_ALLOWED", Message: "The HTTP method is not supported for this resource.", RequestID: requestID})
			return
		}
		round, err := service.Round(request.Context(), approvalID)
		if err != nil {
			writeApprovalError(response, requestID, err)
			return
		}
		response.Header().Set("ETag", `"`+strconv.FormatUint(round.ResourceVersion, 10)+`"`)
		writeJSON(response, http.StatusOK, round)
		return
	}
	if len(segments) != 2 || segments[1] != "decisions" {
		writeAPIError(response, http.StatusNotFound, apiError{SchemaVersion: 1, Code: "NOT_FOUND", Message: "The requested approval was not found.", RequestID: requestID})
		return
	}
	if request.Method != http.MethodPost {
		response.Header().Set("Allow", "POST")
		writeAPIError(response, http.StatusMethodNotAllowed, apiError{SchemaVersion: 1, Code: "METHOD_NOT_ALLOWED", Message: "The HTTP method is not supported for this resource.", RequestID: requestID})
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
	decoder := json.NewDecoder(io.LimitReader(request.Body, 8193))
	decoder.DisallowUnknownFields()
	var body approvalDecisionBody
	if err := decoder.Decode(&body); err != nil || decoder.Decode(new(any)) != io.EOF {
		writeAPIError(response, http.StatusBadRequest, apiError{SchemaVersion: 1, Code: "VALIDATION_FAILED", Message: "The approval decision must be one valid JSON object.", RequestID: requestID})
		return
	}
	result, err := service.Decide(request.Context(), checkpoint.DecisionRequest{
		ApprovalID: approvalID, ExpectedResourceVersion: expected, Action: body.Action,
		ScopeDigest: body.ScopeDigest, PolicyDigest: body.PolicyDigest, Comment: body.Comment,
		IdempotencyKey: key, Actor: statestore.Actor{Type: statestore.ActorUser, ID: "local-user"},
	})
	if err != nil {
		writeApprovalError(response, requestID, err)
		return
	}
	response.Header().Set("ETag", `"`+strconv.FormatUint(result.ResourceVersion, 10)+`"`)
	writeJSON(response, http.StatusOK, result)
}

func (s *Server) serveCheckpoints(response http.ResponseWriter, request *http.Request, requestID string) {
	s.mu.RLock()
	service := s.approvals
	s.mu.RUnlock()
	if service == nil {
		writeAPIError(response, http.StatusServiceUnavailable, apiError{SchemaVersion: 1, Code: "APPROVAL_SERVICE_UNAVAILABLE", Message: "Artifact checkpoint queries are not configured.", RequestID: requestID, Retryable: true})
		return
	}
	if request.Method != http.MethodGet && request.Method != http.MethodHead {
		response.Header().Set("Allow", "GET, HEAD")
		writeAPIError(response, http.StatusMethodNotAllowed, apiError{SchemaVersion: 1, Code: "METHOD_NOT_ALLOWED", Message: "The HTTP method is not supported for this resource.", RequestID: requestID})
		return
	}
	clean := path.Clean(request.URL.Path)
	if clean == "/api/v1/checkpoints" {
		query := request.URL.Query()
		unknown := false
		for key := range query {
			if key != "class" && key != "runId" && key != "status" {
				unknown = true
			}
		}
		if unknown || len(query) > 3 || len(query["class"]) > 1 || len(query["runId"]) > 1 || len(query["status"]) > 1 {
			writeAPIError(response, http.StatusBadRequest, apiError{SchemaVersion: 1, Code: "VALIDATION_FAILED", Message: "Checkpoint filters must be singular class, runId, and status values.", RequestID: requestID})
			return
		}
		class := query.Get("class")
		if class != "" && class != "workflow_checkpoint" {
			writeAPIError(response, http.StatusBadRequest, apiError{SchemaVersion: 1, Code: "VALIDATION_FAILED", Message: "Checkpoint class must be workflow_checkpoint.", RequestID: requestID})
			return
		}
		queue, err := service.List(request.Context(), checkpoint.ListRequest{RunID: query.Get("runId"), Status: statestore.ApprovalStatus(query.Get("status"))})
		if err != nil {
			writeApprovalError(response, requestID, err)
			return
		}
		writeJSON(response, http.StatusOK, queue)
		return
	}
	if request.URL.RawQuery != "" {
		writeAPIError(response, http.StatusBadRequest, apiError{SchemaVersion: 1, Code: "VALIDATION_FAILED", Message: "Checkpoint history does not accept query parameters.", RequestID: requestID})
		return
	}
	checkpointID := strings.TrimPrefix(clean, "/api/v1/checkpoints/")
	if !strings.HasPrefix(checkpointID, "checkpoint_") || strings.Contains(checkpointID, "/") {
		writeAPIError(response, http.StatusNotFound, apiError{SchemaVersion: 1, Code: "NOT_FOUND", Message: "The requested checkpoint was not found.", RequestID: requestID})
		return
	}
	history, err := service.History(request.Context(), checkpointID)
	if err != nil {
		writeApprovalError(response, requestID, err)
		return
	}
	writeJSON(response, http.StatusOK, history)
}

func parseIfMatch(value string) (uint64, error) {
	if len(value) < 3 || value[0] != '"' || value[len(value)-1] != '"' || strings.Contains(value[1:len(value)-1], `"`) {
		return 0, errors.New("If-Match must be one quoted positive resource version")
	}
	parsed, err := strconv.ParseUint(value[1:len(value)-1], 10, 64)
	if err != nil || parsed == 0 {
		return 0, errors.New("If-Match must be one quoted positive resource version")
	}
	return parsed, nil
}

func writeApprovalError(response http.ResponseWriter, requestID string, err error) {
	status, code, message := http.StatusConflict, "APPROVAL_CONFLICT", err.Error()
	switch {
	case errors.Is(err, statestore.ErrNotFound):
		status, code, message = http.StatusNotFound, "NOT_FOUND", "The requested approval was not found."
	case errors.Is(err, checkpoint.ErrInvalidRequest):
		status, code = http.StatusBadRequest, "VALIDATION_FAILED"
	case errors.Is(err, checkpointport.ErrIdempotencyConflict):
		code = "APPROVAL_IDEMPOTENCY_CONFLICT"
	case errors.Is(err, checkpointport.ErrAlreadyResolved):
		code = "APPROVAL_ALREADY_RESOLVED"
	case errors.Is(err, checkpoint.ErrCandidateConflict):
		code = "APPROVAL_STALE_CANDIDATE"
	case errors.Is(err, checkpointport.ErrRevisionLimit):
		code = "APPROVAL_REVISION_LIMIT"
	case errors.Is(err, checkpointport.ErrInvalidReviewState):
		code = "APPROVAL_INVALID_REVIEW_STATE"
	}
	writeAPIError(response, status, apiError{SchemaVersion: 1, Code: code, Message: message, RequestID: requestID})
}
