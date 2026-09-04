package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"darkstar/src/core/identity"
	"darkstar/src/core/readinesscontrol"
	"darkstar/src/core/routeassessment"
	"darkstar/src/ports/statestore"
)

type readinessDecisionBody struct {
	Action           routeassessment.Choice `json:"action"`
	AssessmentDigest string                 `json:"assessmentDigest"`
	Reason           string                 `json:"reason"`
	RemedyCode       string                 `json:"remedyCode,omitempty"`
}

func (s *Server) serveRunReadiness(response http.ResponseWriter, request *http.Request, requestID, runID string) {
	service, ok := s.readinessService(response, requestID)
	if !ok {
		return
	}
	if !runIDPattern.MatchString(runID) {
		writeAPIError(response, http.StatusNotFound, apiError{SchemaVersion: 1, Code: "NOT_FOUND", Message: "The requested run was not found.", RequestID: requestID})
		return
	}
	if request.Method != http.MethodGet && request.Method != http.MethodHead {
		response.Header().Set("Allow", "GET, HEAD")
		writeAPIError(response, http.StatusMethodNotAllowed, apiError{SchemaVersion: 1, Code: "METHOD_NOT_ALLOWED", Message: "The HTTP method is not supported for this resource.", RequestID: requestID})
		return
	}
	if request.URL.RawQuery != "" {
		writeAPIError(response, http.StatusBadRequest, apiError{SchemaVersion: 1, Code: "VALIDATION_FAILED", Message: "The readiness query does not accept query parameters.", RequestID: requestID})
		return
	}
	view, err := service.LatestForRun(request.Context(), runID)
	if err != nil {
		writeReadinessError(response, requestID, err)
		return
	}
	response.Header().Set("ETag", fmt.Sprintf(`"%d"`, view.ResourceVersion))
	writeJSON(response, http.StatusOK, view)
}

func (s *Server) serveRunReadinessDecision(response http.ResponseWriter, request *http.Request, requestID, runID string) {
	service, ok := s.readinessService(response, requestID)
	if !ok {
		return
	}
	if !runIDPattern.MatchString(runID) {
		writeAPIError(response, http.StatusNotFound, apiError{SchemaVersion: 1, Code: "NOT_FOUND", Message: "The requested run was not found.", RequestID: requestID})
		return
	}
	if request.Method != http.MethodPost {
		response.Header().Set("Allow", "POST")
		writeAPIError(response, http.StatusMethodNotAllowed, apiError{SchemaVersion: 1, Code: "METHOD_NOT_ALLOWED", Message: "The HTTP method is not supported for this resource.", RequestID: requestID})
		return
	}
	if request.URL.RawQuery != "" {
		writeAPIError(response, http.StatusBadRequest, apiError{SchemaVersion: 1, Code: "VALIDATION_FAILED", Message: "Readiness decisions do not accept query parameters.", RequestID: requestID})
		return
	}
	key, ok := requireIdempotencyKey(response, request, requestID)
	if !ok {
		return
	}
	expectedVersion, err := parseIfMatch(request.Header.Get("If-Match"))
	if err != nil {
		writeAPIError(response, http.StatusBadRequest, apiError{SchemaVersion: 1, Code: "VALIDATION_FAILED", Message: err.Error(), RequestID: requestID})
		return
	}
	body, err := io.ReadAll(io.LimitReader(request.Body, 8193))
	if err != nil || len(body) == 0 || len(body) > 8192 {
		writeAPIError(response, http.StatusBadRequest, apiError{SchemaVersion: 1, Code: "VALIDATION_FAILED", Message: "The readiness decision must be one valid JSON object.", RequestID: requestID})
		return
	}
	var input readinessDecisionBody
	if decodeReadinessDecision(body, &input) != nil || strings.TrimSpace(input.Reason) == "" || input.Reason != strings.TrimSpace(input.Reason) || len(input.Reason) > 4096 {
		writeAPIError(response, http.StatusUnprocessableEntity, apiError{SchemaVersion: 1, Code: "READINESS_DECISION_INVALID", Message: "The readiness decision is invalid.", RequestID: requestID})
		return
	}
	current, err := service.LatestForRun(request.Context(), runID)
	if err != nil {
		writeReadinessError(response, requestID, err)
		return
	}
	if current.Assessment.Digest != input.AssessmentDigest {
		writeAPIError(response, http.StatusPreconditionFailed, apiError{SchemaVersion: 1, Code: "READINESS_ASSESSMENT_CHANGED", Message: "The readiness assessment changed. Review the latest assessment before deciding.", RequestID: requestID})
		return
	}
	if current.Status == statestore.ReadinessAssessmentPending && current.ResourceVersion != expectedVersion {
		writeAPIError(response, http.StatusPreconditionFailed, apiError{SchemaVersion: 1, Code: "READINESS_VERSION_CONFLICT", Message: "The readiness assessment version changed. Review the latest assessment before deciding.", RequestID: requestID})
		return
	}
	view, err := service.Decide(request.Context(), readinesscontrol.DecisionRequest{
		AssessmentID: current.Assessment.AssessmentID, ExpectedResourceVersion: expectedVersion,
		ExpectedDigest: input.AssessmentDigest, DecisionID: identity.Deterministic("decision_", current.Assessment.AssessmentID+"\x00"+key), Choice: input.Action,
		RemedyCode: input.RemedyCode, Reason: input.Reason, IdempotencyKey: key,
	})
	if err != nil {
		writeReadinessError(response, requestID, err)
		return
	}
	response.Header().Set("ETag", fmt.Sprintf(`"%d"`, view.ResourceVersion))
	writeJSON(response, http.StatusOK, view)
}

func (s *Server) readinessService(response http.ResponseWriter, requestID string) (ReadinessService, bool) {
	s.mu.RLock()
	service := s.readiness
	s.mu.RUnlock()
	if service == nil {
		writeAPIError(response, http.StatusServiceUnavailable, apiError{SchemaVersion: 1, Code: "READINESS_SERVICE_UNAVAILABLE", Message: "Run readiness is not configured.", RequestID: requestID, Retryable: true})
		return nil, false
	}
	return service, true
}

func decodeReadinessDecision(body []byte, target *readinessDecisionBody) error {
	decoder := json.NewDecoder(strings.NewReader(string(body)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return errors.New("readiness decision must contain one object")
	}
	switch target.Action {
	case routeassessment.ChoiceContinue, routeassessment.ChoiceAcceptRouteChange, routeassessment.ChoiceCancel:
		if target.RemedyCode != "" {
			return errors.New("this readiness action does not accept a remedy")
		}
	case routeassessment.ChoiceSupplyInput:
		if strings.TrimSpace(target.RemedyCode) == "" || target.RemedyCode != strings.TrimSpace(target.RemedyCode) {
			return errors.New("supply_input requires a remedy")
		}
	default:
		return errors.New("unsupported readiness action")
	}
	return nil
}

func writeReadinessError(response http.ResponseWriter, requestID string, err error) {
	var assessmentError *routeassessment.Error
	switch {
	case errors.Is(err, statestore.ErrNotFound):
		writeAPIError(response, http.StatusNotFound, apiError{SchemaVersion: 1, Code: "READINESS_NOT_FOUND", Message: "No readiness assessment is recorded for this run.", RequestID: requestID})
	case errors.Is(err, readinesscontrol.ErrAssessmentConflict):
		writeAPIError(response, http.StatusPreconditionFailed, apiError{SchemaVersion: 1, Code: "READINESS_ASSESSMENT_CHANGED", Message: "The readiness assessment changed. Review the latest assessment before deciding.", RequestID: requestID})
	case errors.Is(err, readinesscontrol.ErrAlreadyDecided):
		writeAPIError(response, http.StatusConflict, apiError{SchemaVersion: 1, Code: "READINESS_ALREADY_DECIDED", Message: "This readiness assessment already has a recorded decision.", RequestID: requestID})
	case errors.Is(err, readinesscontrol.ErrIdempotencyConflict):
		writeAPIError(response, http.StatusConflict, apiError{SchemaVersion: 1, Code: "IDEMPOTENCY_CONFLICT", Message: "The idempotency key was already used for a different readiness decision.", RequestID: requestID})
	case errors.Is(err, readinesscontrol.ErrInvalidRequest), errors.As(err, &assessmentError):
		writeAPIError(response, http.StatusUnprocessableEntity, apiError{SchemaVersion: 1, Code: "READINESS_DECISION_INVALID", Message: "The readiness decision is invalid.", RequestID: requestID})
	default:
		writeAPIError(response, http.StatusInternalServerError, apiError{SchemaVersion: 1, Code: "READINESS_QUERY_FAILED", Message: "The readiness projection could not be read.", RequestID: requestID, Retryable: true})
	}
}
