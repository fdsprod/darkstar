package api

import (
	"context"
	"errors"
	"net/http"
	"path"
	"regexp"
	"strings"

	"darkstar/src/core/repositoryscope"
	"darkstar/src/ports/statestore"
)

type RepositoryScopeService interface {
	Prepare(context.Context, repositoryscope.PrepareRequest, string) (repositoryscope.View, error)
	Get(context.Context, string) (repositoryscope.View, error)
}

func (s *Server) SetRepositoryScopes(service RepositoryScopeService) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state != serverNew || service == nil {
		return errors.New("repository scope service must be configured before API start")
	}
	s.repositoryScopes = service
	return nil
}

var repositoryScopeIDPattern = regexp.MustCompile(`^scope_[0-9A-HJKMNP-TV-Z]{26}$`)

func (s *Server) serveRepositoryScopes(response http.ResponseWriter, request *http.Request, requestID string) {
	s.mu.RLock()
	service := s.repositoryScopes
	s.mu.RUnlock()
	if service == nil {
		writeAPIError(response, http.StatusServiceUnavailable, apiError{SchemaVersion: 1, Code: "REPOSITORY_SCOPE_UNAVAILABLE", Message: "Repository investigation scopes are not configured.", RequestID: requestID, Retryable: true})
		return
	}
	resource := strings.TrimPrefix(path.Clean(request.URL.Path), "/api/v1/investigation-scopes")
	if request.URL.RawQuery != "" {
		writeRepositoryScopeError(response, requestID, repositoryscope.ErrInvalidRequest)
		return
	}
	if resource == "" {
		if request.Method != http.MethodPost {
			writeWorkMethod(response, requestID, "POST")
			return
		}
		key, ok := requireIdempotencyKey(response, request, requestID)
		if !ok {
			return
		}
		var input repositoryscope.PrepareRequest
		if err := decodeWorkJSON(request, &input); err != nil {
			writeWorkError(response, requestID, err)
			return
		}
		value, err := service.Prepare(request.Context(), input, key)
		if err != nil {
			writeRepositoryScopeError(response, requestID, err)
			return
		}
		response.Header().Set("Location", "/api/v1/investigation-scopes/"+value.Scope.ScopeID)
		writeJSON(response, http.StatusCreated, value)
		return
	}
	id := strings.TrimPrefix(resource, "/")
	if !repositoryScopeIDPattern.MatchString(id) {
		writeWorkNotFound(response, requestID, "investigation scope")
		return
	}
	if request.Method != http.MethodGet && request.Method != http.MethodHead {
		writeWorkMethod(response, requestID, "GET, HEAD")
		return
	}
	value, err := service.Get(request.Context(), id)
	if err != nil {
		writeRepositoryScopeError(response, requestID, err)
		return
	}
	writeJSON(response, http.StatusOK, value)
}

func writeRepositoryScopeError(response http.ResponseWriter, requestID string, err error) {
	status, code := http.StatusConflict, "REPOSITORY_SCOPE_FAILED"
	switch {
	case errors.Is(err, statestore.ErrNotFound):
		status, code = http.StatusNotFound, "NOT_FOUND"
	case errors.Is(err, repositoryscope.ErrInvalidRequest):
		status, code = http.StatusBadRequest, "VALIDATION_FAILED"
	case errors.Is(err, repositoryscope.ErrScopeConflict), errors.Is(err, statestore.ErrRepositoryScopeConflict):
		code = "REPOSITORY_SCOPE_CONFLICT"
	case errors.Is(err, repositoryscope.ErrScopeUnavailable):
		code = "REPOSITORY_SCOPE_UNAVAILABLE"
	}
	writeAPIError(response, status, apiError{SchemaVersion: 1, Code: code, Message: err.Error(), RequestID: requestID})
}
