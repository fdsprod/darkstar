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

	"darkstar/src/core/runexecution"
	"darkstar/src/ports/statestore"
)

var inputIDPattern = regexp.MustCompile(`^input_[0-9A-HJKMNP-TV-Z]{26}$`)

// InputRequestService keeps provider questions separate from authority-bearing approvals.
type InputRequestService interface {
	InputRequest(context.Context, string) (runexecution.InputRequestView, error)
	InputRequests(context.Context, statestore.InputRequestStatus) (runexecution.InputRequestList, error)
	InputRequestsForRun(context.Context, string) (runexecution.InputRequestList, error)
	InputRequestsForAttempt(context.Context, string) (runexecution.InputRequestList, error)
	AnswerInput(context.Context, runexecution.AnswerInputRequest) (runexecution.InputRequestView, error)
	RetryInputDelivery(context.Context, string, uint64) (runexecution.InputRequestView, error)
}

type inputAnswerBody struct {
	ScopeDigest string          `json:"scopeDigest"`
	Answer      json.RawMessage `json:"answer"`
}

func (s *Server) serveInputRequests(response http.ResponseWriter, request *http.Request, requestID string) {
	s.mu.RLock()
	service := s.inputs
	s.mu.RUnlock()
	if service == nil {
		writeAPIError(response, http.StatusServiceUnavailable, apiError{SchemaVersion: 1, Code: "INPUT_REQUEST_SERVICE_UNAVAILABLE", Message: "User-input requests are not configured.", RequestID: requestID, Retryable: true})
		return
	}
	clean := path.Clean(request.URL.Path)
	if clean == "/api/v1/input-requests" {
		s.serveInputRequestCollection(response, request, requestID, service)
		return
	}
	relative := strings.TrimPrefix(clean, "/api/v1/input-requests/")
	segments := strings.Split(relative, "/")
	if len(segments) == 0 || !inputIDPattern.MatchString(segments[0]) {
		writeInputRequestError(response, requestID, statestore.ErrNotFound)
		return
	}
	if len(segments) == 1 {
		if request.Method != http.MethodGet && request.Method != http.MethodHead {
			response.Header().Set("Allow", "GET, HEAD")
			writeAPIError(response, http.StatusMethodNotAllowed, apiError{SchemaVersion: 1, Code: "METHOD_NOT_ALLOWED", Message: "The HTTP method is not supported for this resource.", RequestID: requestID})
			return
		}
		if request.URL.RawQuery != "" {
			writeInputRequestError(response, requestID, runexecution.ErrInputInvalidRequest)
			return
		}
		value, err := service.InputRequest(request.Context(), segments[0])
		if err != nil {
			writeInputRequestError(response, requestID, err)
			return
		}
		response.Header().Set("ETag", `"`+strconv.FormatUint(value.ResourceVersion, 10)+`"`)
		writeJSON(response, http.StatusOK, value)
		return
	}
	if len(segments) != 2 || (segments[1] != "answer" && segments[1] != "delivery-retries") {
		writeInputRequestError(response, requestID, statestore.ErrNotFound)
		return
	}
	if request.Method != http.MethodPost {
		response.Header().Set("Allow", "POST")
		writeAPIError(response, http.StatusMethodNotAllowed, apiError{SchemaVersion: 1, Code: "METHOD_NOT_ALLOWED", Message: "The HTTP method is not supported for this resource.", RequestID: requestID})
		return
	}
	expected, err := parseIfMatch(request.Header.Get("If-Match"))
	if err != nil {
		writeInputRequestError(response, requestID, runexecution.ErrInputInvalidRequest)
		return
	}
	var value runexecution.InputRequestView
	if segments[1] == "delivery-retries" {
		if request.URL.RawQuery != "" || request.ContentLength > 0 {
			writeInputRequestError(response, requestID, runexecution.ErrInputInvalidRequest)
			return
		}
		value, err = service.RetryInputDelivery(request.Context(), segments[0], expected)
	} else {
		key, ok := requireIdempotencyKey(response, request, requestID)
		if !ok {
			return
		}
		decoder := json.NewDecoder(io.LimitReader(request.Body, (1<<20)+1))
		decoder.DisallowUnknownFields()
		var body inputAnswerBody
		if decodeErr := decoder.Decode(&body); decodeErr != nil || decoder.Decode(new(any)) != io.EOF {
			writeInputRequestError(response, requestID, runexecution.ErrInputInvalidRequest)
			return
		}
		value, err = service.AnswerInput(request.Context(), runexecution.AnswerInputRequest{InputRequestID: segments[0], ExpectedResourceVersion: expected,
			ScopeDigest: body.ScopeDigest, Answer: body.Answer, IdempotencyKey: key, Actor: statestore.Actor{Type: statestore.ActorUser, ID: "local-user"}})
	}
	if err != nil {
		writeInputRequestError(response, requestID, err)
		return
	}
	response.Header().Set("ETag", `"`+strconv.FormatUint(value.ResourceVersion, 10)+`"`)
	writeJSON(response, http.StatusOK, value)
}

func (s *Server) serveInputRequestCollection(response http.ResponseWriter, request *http.Request, requestID string, service InputRequestService) {
	if request.Method != http.MethodGet && request.Method != http.MethodHead {
		response.Header().Set("Allow", "GET, HEAD")
		writeAPIError(response, http.StatusMethodNotAllowed, apiError{SchemaVersion: 1, Code: "METHOD_NOT_ALLOWED", Message: "The HTTP method is not supported for this resource.", RequestID: requestID})
		return
	}
	query := request.URL.Query()
	runID, attemptID := query.Get("runId"), query.Get("attemptId")
	status := statestore.InputRequestStatus(query.Get("status"))
	unknown := false
	for key := range query {
		if key != "runId" && key != "attemptId" && key != "status" {
			unknown = true
		}
	}
	if unknown || len(query) > 2 || len(query["runId"]) > 1 || len(query["attemptId"]) > 1 || len(query["status"]) > 1 ||
		(runID != "" && attemptID != "") || (runID != "" && !runIDPattern.MatchString(runID)) || (attemptID != "" && !attemptIDPattern.MatchString(attemptID)) ||
		(status != "" && status != statestore.InputRequestPending && status != statestore.InputRequestAnswerRecorded && status != statestore.InputRequestAnswered) {
		writeInputRequestError(response, requestID, runexecution.ErrInputInvalidRequest)
		return
	}
	effectiveStatus := status
	if effectiveStatus == "" {
		effectiveStatus = statestore.InputRequestPending
	}
	var value runexecution.InputRequestList
	var err error
	if runID != "" {
		value, err = service.InputRequestsForRun(request.Context(), runID)
	} else if attemptID != "" {
		value, err = service.InputRequestsForAttempt(request.Context(), attemptID)
	} else {
		value, err = service.InputRequests(request.Context(), effectiveStatus)
	}
	if err != nil {
		writeInputRequestError(response, requestID, err)
		return
	}
	if runID != "" || attemptID != "" {
		filtered := make([]runexecution.InputRequestView, 0, len(value.Items))
		for _, item := range value.Items {
			if item.Status == effectiveStatus {
				filtered = append(filtered, item)
			}
		}
		value.Items = filtered
	}
	writeJSON(response, http.StatusOK, value)
}

func writeInputRequestError(response http.ResponseWriter, requestID string, err error) {
	status, code, message, retryable := http.StatusConflict, "INPUT_REQUEST_CONFLICT", err.Error(), false
	switch {
	case errors.Is(err, statestore.ErrNotFound):
		status, code, message = http.StatusNotFound, "NOT_FOUND", "The requested input request was not found."
	case errors.Is(err, runexecution.ErrInputInvalidRequest):
		status, code, message = http.StatusBadRequest, "VALIDATION_FAILED", "The input-request operation is invalid."
	case errors.Is(err, runexecution.ErrInputScopeConflict):
		code = "INPUT_SCOPE_CONFLICT"
	case errors.Is(err, runexecution.ErrInputAlreadyAnswered):
		code = "INPUT_ALREADY_ANSWERED"
	case errors.Is(err, runexecution.ErrInputAnswerInProgress):
		code = "INPUT_ANSWER_IN_PROGRESS"
	case errors.Is(err, runexecution.ErrInputDeliveryUnavailable):
		status, code, message, retryable = http.StatusServiceUnavailable, "INPUT_DELIVERY_UNAVAILABLE", "The answer is durable but provider delivery is currently unavailable.", true
	}
	writeAPIError(response, status, apiError{SchemaVersion: 1, Code: code, Message: message, RequestID: requestID, Retryable: retryable})
}
