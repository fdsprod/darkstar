package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"path"
	"regexp"
	"strings"

	"darkstar/src/core/investigation"
	"darkstar/src/ports/statestore"
)

// InvestigationService is the daemon-owned collection command boundary. No
// transport operation accepts provider results, tool calls, or unit scheduling.
type InvestigationService interface {
	Prepare(context.Context, investigation.PrepareRequest, string) (investigation.View, error)
	Get(context.Context, string) (investigation.View, error)
	Start(context.Context, string, uint64, string) (investigation.View, error)
	Retry(context.Context, string, uint64, string) (investigation.View, error)
	Cancel(context.Context, string, uint64, string) (investigation.View, error)
}

func (s *Server) SetInvestigations(service InvestigationService) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state != serverNew || service == nil {
		return errors.New("investigations must be configured before API start")
	}
	s.investigations = service
	return nil
}

var investigationIDPattern = regexp.MustCompile(`^investigation_[0-9A-HJKMNP-TV-Z]{26}$`)

func (s *Server) serveInvestigations(response http.ResponseWriter, request *http.Request, requestID string) {
	s.mu.RLock()
	service := s.investigations
	s.mu.RUnlock()
	if service == nil {
		writeAPIError(response, http.StatusServiceUnavailable, apiError{SchemaVersion: 1, Code: "INVESTIGATION_UNAVAILABLE", Message: "Investigation execution is not configured.", RequestID: requestID, Retryable: true})
		return
	}
	if request.URL.RawQuery != "" {
		writeInvestigationError(response, requestID, investigation.ErrInvalidRequest)
		return
	}
	resource := strings.TrimPrefix(path.Clean(request.URL.Path), "/api/v1/investigations")
	if resource == "" {
		if request.Method != http.MethodPost {
			writeWorkMethod(response, requestID, "POST")
			return
		}
		key, ok := requireIdempotencyKey(response, request, requestID)
		if !ok {
			return
		}
		var input investigation.PrepareRequest
		if err := decodeWorkJSON(request, &input); err != nil {
			writeWorkError(response, requestID, err)
			return
		}
		value, err := service.Prepare(request.Context(), input, key)
		if err != nil {
			writeInvestigationError(response, requestID, err)
			return
		}
		response.Header().Set("Location", "/api/v1/investigations/"+value.Collection.CollectionID)
		writeInvestigationView(response, http.StatusCreated, value)
		return
	}
	parts := strings.Split(strings.TrimPrefix(resource, "/"), "/")
	if len(parts) > 2 || !investigationIDPattern.MatchString(parts[0]) {
		writeWorkNotFound(response, requestID, "investigation")
		return
	}
	if len(parts) == 1 {
		if request.Method != http.MethodGet && request.Method != http.MethodHead {
			writeWorkMethod(response, requestID, "GET, HEAD")
			return
		}
		value, err := service.Get(request.Context(), parts[0])
		if err != nil {
			writeInvestigationError(response, requestID, err)
			return
		}
		writeInvestigationView(response, http.StatusOK, value)
		return
	}
	var command func(context.Context, string, uint64, string) (investigation.View, error)
	switch parts[1] {
	case "start":
		command = service.Start
	case "retry":
		command = service.Retry
	case "cancel":
		command = service.Cancel
	default:
		writeWorkNotFound(response, requestID, "investigation command")
		return
	}
	if request.Method != http.MethodPost {
		writeWorkMethod(response, requestID, "POST")
		return
	}
	key, ok := requireIdempotencyKey(response, request, requestID)
	if !ok {
		return
	}
	expected, err := parseIfMatch(request.Header.Get("If-Match"))
	if err != nil {
		writeWorkError(response, requestID, fmtWorkInvalid(err.Error()))
		return
	}
	var input map[string]json.RawMessage
	if err := decodeWorkJSON(request, &input); err != nil {
		writeWorkError(response, requestID, err)
		return
	}
	if input == nil || len(input) != 0 {
		writeInvestigationError(response, requestID, investigation.ErrInvalidRequest)
		return
	}
	value, err := command(request.Context(), parts[0], expected, key)
	if err != nil {
		writeInvestigationError(response, requestID, err)
		return
	}
	writeInvestigationView(response, http.StatusOK, value)
}

func writeInvestigationView(response http.ResponseWriter, status int, value investigation.View) {
	response.Header().Set("ETag", fmt.Sprintf(`"%d"`, value.Collection.Revision))
	writeJSON(response, status, value)
}

func writeInvestigationError(response http.ResponseWriter, requestID string, err error) {
	status, code := http.StatusConflict, "INVESTIGATION_FAILED"
	switch {
	case errors.Is(err, statestore.ErrNotFound):
		status, code = http.StatusNotFound, "NOT_FOUND"
	case errors.Is(err, investigation.ErrInvalidRequest):
		status, code = http.StatusBadRequest, "VALIDATION_FAILED"
	case errors.Is(err, investigation.ErrConflict):
		code = "INVESTIGATION_REVISION_CONFLICT"
	case errors.Is(err, investigation.ErrUnavailable):
		code = "INVESTIGATION_UNAVAILABLE"
	case errors.Is(err, investigation.ErrClosed):
		status, code = http.StatusServiceUnavailable, "INVESTIGATION_CLOSED"
	}
	writeAPIError(response, status, apiError{SchemaVersion: 1, Code: code, Message: err.Error(), RequestID: requestID})
}
