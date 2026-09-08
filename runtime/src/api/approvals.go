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
	"darkstar/src/core/attention"
	checkpointport "darkstar/src/ports/artifactcheckpoint"
	"darkstar/src/ports/statestore"
)

var approvalIDPattern = regexp.MustCompile(`^approval_[0-9A-HJKMNP-TV-Z]{26}$`)
var feedbackSetIDPattern = regexp.MustCompile(`^feedbackset_[0-9A-HJKMNP-TV-Z]{26}$`)

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
	if request.Method != http.MethodGet && request.Method != http.MethodHead {
		response.Header().Set("Allow", "GET, HEAD")
		writeAPIError(response, http.StatusMethodNotAllowed, apiError{SchemaVersion: 1, Code: "METHOD_NOT_ALLOWED", Message: "The HTTP method is not supported for this resource.", RequestID: requestID})
		return
	}
	clean := path.Clean(request.URL.Path)
	s.mu.RLock()
	service := s.approvals
	s.mu.RUnlock()
	if service == nil {
		writeAPIError(response, http.StatusServiceUnavailable, apiError{SchemaVersion: 1, Code: "APPROVAL_SERVICE_UNAVAILABLE", Message: "Artifact checkpoint queries are not configured.", RequestID: requestID, Retryable: true})
		return
	}
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

func (s *Server) serveAttentionCheckpoints(response http.ResponseWriter, request *http.Request, requestID string, service AttentionService) {
	query := request.URL.Query()
	for key, values := range query {
		if key != "kind" && key != "itemId" && key != "projectId" && key != "workItemId" && key != "runId" && key != "limit" && key != "cursor" {
			writeAPIError(response, http.StatusBadRequest, apiError{SchemaVersion: 1, Code: "VALIDATION_FAILED", Message: "Checkpoint filters contain an unknown field.", RequestID: requestID})
			return
		}
		if key != "kind" && len(values) > 1 {
			writeAPIError(response, http.StatusBadRequest, apiError{SchemaVersion: 1, Code: "VALIDATION_FAILED", Message: "Checkpoint context and pagination filters must be singular.", RequestID: requestID})
			return
		}
	}
	limit := 0
	var err error
	if raw := query.Get("limit"); raw != "" {
		limit, err = strconv.Atoi(raw)
		if err != nil {
			writeAPIError(response, http.StatusBadRequest, apiError{SchemaVersion: 1, Code: "VALIDATION_FAILED", Message: "Checkpoint limit must be an integer from 1 through 200.", RequestID: requestID})
			return
		}
	}
	kinds := make([]attention.Kind, 0, len(query["kind"]))
	for _, raw := range query["kind"] {
		if raw == "" {
			writeAPIError(response, http.StatusBadRequest, apiError{SchemaVersion: 1, Code: "VALIDATION_FAILED", Message: "Checkpoint kind cannot be empty.", RequestID: requestID})
			return
		}
		kinds = append(kinds, attention.Kind(raw))
	}
	page, err := service.List(request.Context(), attention.ListRequest{IncludePreparation: strings.HasSuffix(request.URL.Path, "/v2"), Kinds: kinds, ItemID: query.Get("itemId"), ProjectID: query.Get("projectId"), WorkItemID: query.Get("workItemId"), RunID: query.Get("runId"), Limit: limit, Cursor: query.Get("cursor")})
	if err != nil {
		if errors.Is(err, attention.ErrInvalidRequest) || errors.Is(err, attention.ErrInvalidCursor) {
			writeAPIError(response, http.StatusBadRequest, apiError{SchemaVersion: 1, Code: "VALIDATION_FAILED", Message: err.Error(), RequestID: requestID})
			return
		}
		writeAPIError(response, http.StatusInternalServerError, apiError{SchemaVersion: 1, Code: "CHECKPOINT_QUERY_FAILED", Message: "The Checkpoints projection could not be read.", RequestID: requestID, Retryable: true})
		return
	}
	if strings.HasSuffix(request.URL.Path, "/v2") {
		page.SchemaVersion = 2
	}
	writeJSON(response, http.StatusOK, page)
}

type attentionDecisionBody struct {
	Action       attention.DecisionAction `json:"action"`
	ScopeDigest  string                   `json:"scopeDigest"`
	PolicyDigest string                   `json:"policyDigest"`
	Comment      string                   `json:"comment,omitempty"`
}

func (s *Server) serveAttentionDecision(response http.ResponseWriter, request *http.Request, requestID string, service AttentionService) {
	if request.Method != http.MethodPost {
		response.Header().Set("Allow", "POST")
		writeAPIError(response, http.StatusMethodNotAllowed, apiError{SchemaVersion: 1, Code: "METHOD_NOT_ALLOWED", Message: "The HTTP method is not supported for this resource.", RequestID: requestID})
		return
	}
	if request.URL.RawQuery != "" {
		writeAPIError(response, http.StatusBadRequest, apiError{SchemaVersion: 1, Code: "VALIDATION_FAILED", Message: "Attention decisions do not accept query parameters.", RequestID: requestID})
		return
	}
	segments := strings.Split(strings.TrimPrefix(path.Clean(request.URL.Path), "/api/v1/attention/"), "/")
	if len(segments) != 3 || segments[2] != "decisions" ||
		(segments[0] != string(attention.KindWorkflowControl) && segments[0] != string(attention.KindExternalDelivery)) || !approvalIDPattern.MatchString(segments[1]) {
		writeAPIError(response, http.StatusNotFound, apiError{SchemaVersion: 1, Code: "NOT_FOUND", Message: "The requested attention command was not found.", RequestID: requestID})
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
	var body attentionDecisionBody
	if err := decoder.Decode(&body); err != nil || decoder.Decode(new(any)) != io.EOF {
		writeAPIError(response, http.StatusBadRequest, apiError{SchemaVersion: 1, Code: "VALIDATION_FAILED", Message: "The attention decision must be one valid JSON object.", RequestID: requestID})
		return
	}
	result, err := service.Decide(request.Context(), attention.DecisionRequest{
		Kind: attention.Kind(segments[0]), ID: segments[1], ExpectedResourceVersion: expected,
		Action: body.Action, ScopeDigest: body.ScopeDigest, PolicyDigest: body.PolicyDigest, Comment: body.Comment,
		IdempotencyKey: key, Actor: statestore.Actor{Type: statestore.ActorUser, ID: "local-user"},
	})
	if err != nil {
		status, code := http.StatusConflict, "ATTENTION_STALE"
		if errors.Is(err, attention.ErrInvalidRequest) {
			status, code = http.StatusBadRequest, "VALIDATION_FAILED"
		} else if errors.Is(err, statestore.ErrNotFound) {
			status, code = http.StatusNotFound, "NOT_FOUND"
		}
		writeAPIError(response, status, apiError{SchemaVersion: 1, Code: code, Message: err.Error(), RequestID: requestID})
		return
	}
	response.Header().Set("ETag", `"`+strconv.FormatUint(result.ResourceVersion, 10)+`"`)
	writeJSON(response, http.StatusOK, result)
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
	case errors.Is(err, checkpoint.ErrCheckpointConflict):
		code = "APPROVAL_VERSION_CONFLICT"
	case errors.Is(err, checkpointport.ErrRevisionLimit):
		code = "APPROVAL_REVISION_LIMIT"
	case errors.Is(err, checkpointport.ErrInvalidReviewState):
		code = "APPROVAL_INVALID_REVIEW_STATE"
	}
	writeAPIError(response, status, apiError{SchemaVersion: 1, Code: code, Message: message, RequestID: requestID})
}
