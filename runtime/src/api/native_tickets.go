package api

import (
	"context"
	"errors"
	"net/http"
	"path"
	"strconv"
	"strings"

	"darkstar/src/core/ticketmanagement"
	"darkstar/src/ports"
	"darkstar/src/ports/statestore"
	"darkstar/src/ports/tracker"
)

type NativeTicketService interface {
	Browse(context.Context, string, tracker.Query) (ticketmanagement.Page, error)
	Detail(context.Context, string, string) (ticketmanagement.Detail, error)
	Edit(context.Context, string, string, ticketmanagement.EditRequest, string) (ticketmanagement.Detail, error)
	Transition(context.Context, string, string, ticketmanagement.TransitionRequest, string) (ticketmanagement.Detail, error)
}

func (s *Server) SetNativeTickets(service NativeTicketService) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state != serverNew || service == nil {
		return errors.New("native ticket service must be configured before start")
	}
	s.nativeTickets = service
	return nil
}

func (s *Server) serveNativeTickets(response http.ResponseWriter, request *http.Request, requestID string) {
	s.mu.RLock()
	service := s.nativeTickets
	s.mu.RUnlock()
	if service == nil {
		writeAPIError(response, http.StatusServiceUnavailable, apiError{SchemaVersion: 1, Code: "TICKET_SERVICE_UNAVAILABLE", Message: "Native ticket operations are not configured.", RequestID: requestID})
		return
	}
	parts := strings.Split(strings.TrimPrefix(path.Clean(request.URL.Path), "/api/v1/projects/"), "/")
	if len(parts) < 2 || len(parts) > 4 || !projectIDPattern.MatchString(parts[0]) || parts[1] != "tickets" {
		writeWorkNotFound(response, requestID, "native ticket")
		return
	}
	if len(parts) == 2 {
		if request.Method != http.MethodGet && request.Method != http.MethodHead {
			writeWorkMethod(response, requestID, "GET, HEAD")
			return
		}
		values := request.URL.Query()
		for key, list := range values {
			if (key != "q" && key != "state" && key != "cursor" && key != "pageSize") || len(list) != 1 {
				writeNativeTicketError(response, requestID, &ports.Failure{Code: ports.FailureInvalidRequest, Message: "Unsupported or repeated ticket query parameter."})
				return
			}
		}
		query := tracker.Query{Text: values.Get("q"), Cursor: values.Get("cursor"), PageSize: 50}
		if size := values.Get("pageSize"); size != "" {
			parsed, err := strconv.Atoi(size)
			if err != nil || parsed < 1 || parsed > 100 {
				writeNativeTicketError(response, requestID, &ports.Failure{Code: ports.FailureInvalidRequest, Message: "pageSize must be between 1 and 100."})
				return
			}
			query.PageSize = parsed
		}
		if state := values.Get("state"); state != "" {
			query.Predicates = []tracker.Predicate{{FieldID: "business_state", Operator: tracker.Equals, Values: []string{state}}}
		}
		result, err := service.Browse(request.Context(), parts[0], query)
		if err != nil {
			writeNativeTicketError(response, requestID, err)
			return
		}
		writeJSON(response, http.StatusOK, result)
		return
	}
	if request.URL.RawQuery != "" || parts[2] == "" {
		writeNativeTicketError(response, requestID, &ports.Failure{Code: ports.FailureInvalidRequest, Message: "Ticket detail does not accept query parameters."})
		return
	}
	if len(parts) == 3 {
		if request.Method != http.MethodGet && request.Method != http.MethodHead {
			writeWorkMethod(response, requestID, "GET, HEAD")
			return
		}
		result, err := service.Detail(request.Context(), parts[0], parts[2])
		if err != nil {
			writeNativeTicketError(response, requestID, err)
			return
		}
		writeJSON(response, http.StatusOK, result)
		return
	}
	if parts[3] != "edit" && parts[3] != "transition" {
		writeWorkNotFound(response, requestID, "native ticket command")
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
	var result ticketmanagement.Detail
	var err error
	if parts[3] == "edit" {
		var input ticketmanagement.EditRequest
		if err = decodeWorkJSON(request, &input); err == nil {
			result, err = service.Edit(request.Context(), parts[0], parts[2], input, key)
		}
	} else {
		var input ticketmanagement.TransitionRequest
		if err = decodeWorkJSON(request, &input); err == nil {
			result, err = service.Transition(request.Context(), parts[0], parts[2], input, key)
		}
	}
	if err != nil {
		writeNativeTicketError(response, requestID, err)
		return
	}
	writeJSON(response, http.StatusOK, result)
}

func writeNativeTicketError(response http.ResponseWriter, requestID string, err error) {
	if errors.Is(err, statestore.ErrNotFound) {
		writeWorkNotFound(response, requestID, "native ticket")
		return
	}
	var failure *ports.Failure
	if errors.As(err, &failure) {
		status := http.StatusConflict
		switch failure.Code {
		case ports.FailureInvalidRequest:
			status = http.StatusBadRequest
		case ports.FailureNotFound:
			status = http.StatusNotFound
		case ports.FailurePermissionDenied:
			status = http.StatusForbidden
		case ports.FailureUnauthenticated:
			status = http.StatusUnauthorized
		case ports.FailureUnavailable:
			status = http.StatusServiceUnavailable
		}
		writeAPIError(response, status, apiError{SchemaVersion: 1, Code: "TICKET_" + strings.ToUpper(string(failure.Code)), Message: failure.Message, RequestID: requestID, Retryable: failure.Retryable})
		return
	}
	writeWorkError(response, requestID, err)
}
