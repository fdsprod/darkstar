package api

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"path"
	"strconv"
	"strings"

	checkpoint "darkstar/src/core/artifactcheckpoint"
	checkpointport "darkstar/src/ports/artifactcheckpoint"
	"darkstar/src/ports/artifactregistry"
	"darkstar/src/ports/statestore"
)

// ReviewSessionService is intentionally separate from provider permission
// handling: checkpoint feedback never grants tool or execution authority.
type ReviewSessionService interface {
	ReviewSession(context.Context, string) (checkpointport.ReviewSession, error)
	ReviewHistory(context.Context, string) (checkpointport.ReviewHistory, error)
	SubmitFeedback(context.Context, checkpoint.FeedbackRequest) (checkpointport.ReviewSession, error)
	ResumeRevision(context.Context, checkpoint.ResumeRequest) (checkpointport.ReviewSession, error)
	RecordAgentResponse(context.Context, checkpoint.AgentResponseRequest) (checkpointport.ReviewSession, error)
	Decide(context.Context, checkpoint.DecisionRequest) (checkpointport.Round, error)
}

type reviewCommandBody struct {
	CandidateDigest string                      `json:"candidateDigest"`
	ScopeDigest     string                      `json:"scopeDigest"`
	PolicyDigest    string                      `json:"policyDigest,omitempty"`
	Message         string                      `json:"message,omitempty"`
	Comment         string                      `json:"comment,omitempty"`
	AttemptID       string                      `json:"attemptId,omitempty"`
	Outcome         checkpointport.AgentOutcome `json:"outcome,omitempty"`
	Candidate       artifactregistry.VersionRef `json:"candidate,omitempty"`
	NextApprovalID  string                      `json:"nextApprovalId,omitempty"`
	Action          checkpointport.Action       `json:"action,omitempty"`
}

func (s *Server) serveReviewSessions(response http.ResponseWriter, request *http.Request, requestID string) {
	s.mu.RLock()
	service, ok := s.approvals.(ReviewSessionService)
	s.mu.RUnlock()
	if !ok {
		writeAPIError(response, http.StatusServiceUnavailable, apiError{SchemaVersion: 1, Code: "REVIEW_SESSION_SERVICE_UNAVAILABLE", Message: "Checkpoint review sessions are not configured.", RequestID: requestID, Retryable: true})
		return
	}
	clean := path.Clean(request.URL.Path)
	if clean == "/api/v1/review-sessions" {
		if request.Method != http.MethodGet && request.Method != http.MethodHead {
			response.Header().Set("Allow", "GET, HEAD")
			writeAPIError(response, http.StatusMethodNotAllowed, apiError{SchemaVersion: 1, Code: "METHOD_NOT_ALLOWED", Message: "The HTTP method is not supported for this resource.", RequestID: requestID})
			return
		}
		query := request.URL.Query()
		if len(query) != 1 || len(query["checkpointId"]) != 1 || !strings.HasPrefix(query.Get("checkpointId"), "checkpoint_") {
			writeAPIError(response, http.StatusBadRequest, apiError{SchemaVersion: 1, Code: "VALIDATION_FAILED", Message: "checkpointId is required and must be singular.", RequestID: requestID})
			return
		}
		history, err := service.ReviewHistory(request.Context(), query.Get("checkpointId"))
		if err != nil {
			writeApprovalError(response, requestID, err)
			return
		}
		writeJSON(response, http.StatusOK, history)
		return
	}
	if request.URL.RawQuery != "" {
		writeAPIError(response, http.StatusBadRequest, apiError{SchemaVersion: 1, Code: "VALIDATION_FAILED", Message: "Review-session resources do not accept query parameters.", RequestID: requestID})
		return
	}
	segments := strings.Split(strings.TrimPrefix(clean, "/api/v1/review-sessions/"), "/")
	if len(segments) == 0 || !approvalIDPattern.MatchString(segments[0]) || len(segments) > 2 {
		writeAPIError(response, http.StatusNotFound, apiError{SchemaVersion: 1, Code: "NOT_FOUND", Message: "The requested review session was not found.", RequestID: requestID})
		return
	}
	if len(segments) == 1 {
		if request.Method != http.MethodGet && request.Method != http.MethodHead {
			response.Header().Set("Allow", "GET, HEAD")
			writeAPIError(response, http.StatusMethodNotAllowed, apiError{SchemaVersion: 1, Code: "METHOD_NOT_ALLOWED", Message: "The HTTP method is not supported for this resource.", RequestID: requestID})
			return
		}
		session, err := service.ReviewSession(request.Context(), segments[0])
		if err != nil {
			writeApprovalError(response, requestID, err)
			return
		}
		response.Header().Set("ETag", `"`+strconv.FormatUint(session.ResourceVersion, 10)+`"`)
		writeJSON(response, http.StatusOK, session)
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
	var body reviewCommandBody
	decoder := json.NewDecoder(io.LimitReader(request.Body, 32769))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&body); err != nil || decoder.Decode(new(any)) != io.EOF {
		writeAPIError(response, http.StatusBadRequest, apiError{SchemaVersion: 1, Code: "VALIDATION_FAILED", Message: "The review command must be one valid JSON object.", RequestID: requestID})
		return
	}
	actor := statestore.Actor{Type: statestore.ActorUser, ID: "local-user"}
	var result checkpointport.ReviewSession
	switch segments[1] {
	case "feedback":
		result, err = service.SubmitFeedback(request.Context(), checkpoint.FeedbackRequest{ApprovalID: segments[0], ExpectedResourceVersion: expected,
			CandidateDigest: body.CandidateDigest, ScopeDigest: body.ScopeDigest, Message: body.Message, IdempotencyKey: key, Actor: actor})
	case "resume":
		result, err = service.ResumeRevision(request.Context(), checkpoint.ResumeRequest{ApprovalID: segments[0], ExpectedResourceVersion: expected,
			CandidateDigest: body.CandidateDigest, ScopeDigest: body.ScopeDigest, AttemptID: body.AttemptID, IdempotencyKey: key,
			Actor: statestore.Actor{Type: statestore.ActorSystem, ID: "local-daemon"}})
	case "agent-responses":
		result, err = service.RecordAgentResponse(request.Context(), checkpoint.AgentResponseRequest{ApprovalID: segments[0], ExpectedResourceVersion: expected,
			CandidateDigest: body.CandidateDigest, ScopeDigest: body.ScopeDigest, AttemptID: body.AttemptID, Outcome: body.Outcome,
			Message: body.Message, Candidate: body.Candidate, NextApprovalID: body.NextApprovalID, IdempotencyKey: key,
			Actor: statestore.Actor{Type: statestore.ActorSystem, ID: "local-daemon"}})
	case "decisions":
		if body.Action != checkpointport.ActionApprove && body.Action != checkpointport.ActionReject {
			writeAPIError(response, http.StatusBadRequest, apiError{SchemaVersion: 1, Code: "VALIDATION_FAILED", Message: "Review decisions must be approve or reject.", RequestID: requestID})
			return
		}
		_, err = service.Decide(request.Context(), checkpoint.DecisionRequest{ApprovalID: segments[0], ExpectedResourceVersion: expected,
			Action: body.Action, CandidateDigest: body.CandidateDigest, ScopeDigest: body.ScopeDigest, PolicyDigest: body.PolicyDigest,
			Comment: body.Comment, IdempotencyKey: key, Actor: actor})
		if err == nil {
			result, err = service.ReviewSession(request.Context(), segments[0])
		}
	default:
		writeAPIError(response, http.StatusNotFound, apiError{SchemaVersion: 1, Code: "NOT_FOUND", Message: "The requested review action was not found.", RequestID: requestID})
		return
	}
	if err != nil {
		writeApprovalError(response, requestID, err)
		return
	}
	response.Header().Set("ETag", `"`+strconv.FormatUint(result.ResourceVersion, 10)+`"`)
	writeJSON(response, http.StatusOK, result)
}
