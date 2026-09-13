package api

import (
	"context"
	"errors"
	"net/http"
	"path"
	"strconv"
	"strings"

	"darkstar/src/core/backlog"
	"darkstar/src/ports"
	"darkstar/src/ports/statestore"
	"darkstar/src/ports/tracker"
)

// BacklogService only observes business sources and selects future intake
// configuration. This API has no work/run creation or provider write handle.
type BacklogService interface {
	Binding(context.Context, string) (statestore.BacklogBinding, error)
	History(context.Context, string) ([]statestore.BacklogBinding, error)
	SelectSource(context.Context, string, uint64, statestore.BacklogSource) (statestore.BacklogBinding, error)
	View(context.Context, string, backlog.ViewRequest) (backlog.View, error)
	Refresh(context.Context, string, uint64, tracker.Query) (backlog.RefreshResult, error)
	ReadRefresh(context.Context, string, uint64, tracker.TicketRef) (backlog.RefreshResult, error)
}

func (s *Server) SetBacklog(service BacklogService) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state != serverNew || service == nil {
		return errors.New("backlog service must be configured before server start")
	}
	s.backlog = service
	return nil
}

func (s *Server) serveBacklog(response http.ResponseWriter, request *http.Request, requestID string) {
	s.mu.RLock()
	service := s.backlog
	s.mu.RUnlock()
	if service == nil {
		writeBacklogError(response, requestID, &ports.Failure{Code: ports.FailureUnavailable, Message: "Backlog sources are not configured."})
		return
	}
	parts := strings.Split(strings.TrimPrefix(path.Clean(request.URL.Path), "/api/v1/projects/"), "/")
	if len(parts) < 2 || len(parts) > 3 || !projectIDPattern.MatchString(parts[0]) || parts[1] != "backlog" {
		writeWorkNotFound(response, requestID, "backlog")
		return
	}
	if len(parts) == 2 {
		serveBacklogView(service, response, request, requestID, parts[0])
		return
	}
	if request.URL.RawQuery != "" {
		writeBacklogError(response, requestID, backlogInvalid("Backlog source and refresh operations accept no query parameters."))
		return
	}
	switch parts[2] {
	case "source":
		serveBacklogSource(service, response, request, requestID, parts[0])
	case "refresh", "refresh-ticket":
		serveBacklogRefresh(service, response, request, requestID, parts[0], parts[2])
	default:
		writeWorkNotFound(response, requestID, "backlog operation")
	}
}

func serveBacklogView(service BacklogService, response http.ResponseWriter, request *http.Request, requestID, projectID string) {
	if request.Method != http.MethodGet && request.Method != http.MethodHead {
		writeWorkMethod(response, requestID, "GET, HEAD")
		return
	}
	values := request.URL.Query()
	for key, list := range values {
		if key != "limit" && key != "cursor" && key != "includePrevious" || len(list) != 1 {
			writeBacklogError(response, requestID, backlogInvalid("Unsupported or repeated backlog query parameter."))
			return
		}
	}
	input := backlog.ViewRequest{Limit: 100, Cursor: values.Get("cursor")}
	var err error
	if value := values.Get("limit"); value != "" {
		input.Limit, err = strconv.Atoi(value)
		if err != nil || input.Limit < 1 || input.Limit > 1000 {
			writeBacklogError(response, requestID, backlogInvalid("Backlog limit must be between 1 and 1000."))
			return
		}
	}
	if value := values.Get("includePrevious"); value != "" {
		if value != "true" && value != "false" {
			writeBacklogError(response, requestID, backlogInvalid("includePrevious must be true or false."))
			return
		}
		input.IncludePrevious = value == "true"
	}
	view, err := service.View(request.Context(), projectID, input)
	if err != nil {
		writeBacklogError(response, requestID, err)
		return
	}
	writeJSON(response, http.StatusOK, backlogView(view, input.IncludePrevious))
}

func serveBacklogSource(service BacklogService, response http.ResponseWriter, request *http.Request, requestID, projectID string) {
	var binding statestore.BacklogBinding
	var err error
	switch request.Method {
	case http.MethodGet, http.MethodHead:
		binding, err = service.Binding(request.Context(), projectID)
	case http.MethodPut:
		var input BacklogSourceRequest
		if decodeWorkJSON(request, &input) != nil || input.SchemaVersion != 1 || input.ExpectedRevision == 0 {
			writeBacklogError(response, requestID, backlogInvalid("Source selection requires schemaVersion 1 and the current expectedRevision."))
			return
		}
		source, sourceErr := input.Source.native(projectID)
		if sourceErr != nil {
			writeBacklogError(response, requestID, sourceErr)
			return
		}
		binding, err = service.SelectSource(request.Context(), projectID, input.ExpectedRevision, source)
	default:
		writeWorkMethod(response, requestID, "GET, HEAD, PUT")
		return
	}
	if err != nil {
		writeBacklogError(response, requestID, err)
		return
	}
	history, err := service.History(request.Context(), projectID)
	if err != nil {
		writeBacklogError(response, requestID, err)
		return
	}
	result := BacklogSourceResponse{SchemaVersion: 1, Binding: backlogBinding(binding), History: make([]BacklogBinding, 0, len(history))}
	for _, item := range history {
		result.History = append(result.History, backlogBinding(item))
	}
	writeJSON(response, http.StatusOK, result)
}

func serveBacklogRefresh(service BacklogService, response http.ResponseWriter, request *http.Request, requestID, projectID, operation string) {
	if request.Method != http.MethodPost {
		writeWorkMethod(response, requestID, "POST")
		return
	}
	var result backlog.RefreshResult
	var err error
	if operation == "refresh" {
		var input BacklogRefreshRequest
		if decodeWorkJSON(request, &input) != nil || input.SchemaVersion != 1 || input.ExpectedBindingRevision == 0 {
			writeBacklogError(response, requestID, backlogInvalid("Refresh requires schemaVersion 1 and an exact source binding revision."))
			return
		}
		result, err = service.Refresh(request.Context(), projectID, input.ExpectedBindingRevision, input.Query.native())
	} else {
		var input BacklogTicketRefreshRequest
		if decodeWorkJSON(request, &input) != nil || input.SchemaVersion != 1 || input.ExpectedBindingRevision == 0 || input.Ref.ID == "" {
			writeBacklogError(response, requestID, backlogInvalid("Exact refresh requires schemaVersion 1, a binding revision and ticket reference."))
			return
		}
		result, err = service.ReadRefresh(request.Context(), projectID, input.ExpectedBindingRevision, tracker.TicketRef{Namespace: input.Ref.Namespace.native(), ID: input.Ref.ID})
	}
	if err != nil {
		writeBacklogError(response, requestID, err)
		return
	}
	writeJSON(response, http.StatusOK, BacklogRefreshResponse{SchemaVersion: 1, Refresh: backlogRefresh(result.Refresh)})
}

func writeBacklogError(response http.ResponseWriter, requestID string, err error) {
	if errors.Is(err, statestore.ErrNotFound) {
		writeWorkNotFound(response, requestID, "backlog")
		return
	}
	var failure *ports.Failure
	if !errors.As(err, &failure) {
		failure = &ports.Failure{Code: ports.FailureUnavailable, Message: "Backlog state is unavailable.", Retryable: true}
	}
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
	case ports.FailureResourceExhausted:
		status = http.StatusTooManyRequests
	}
	writeAPIError(response, status, apiError{SchemaVersion: 1, Code: "BACKLOG_" + strings.ToUpper(string(failure.Code)), Message: failure.Message, RequestID: requestID, Retryable: failure.Retryable})
}
