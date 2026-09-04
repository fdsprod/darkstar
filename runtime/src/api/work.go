package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"

	"darkstar/src/core/workmanagement"
	"darkstar/src/ports/statestore"
)

var (
	projectIDPattern = regexp.MustCompile(`^project_[0-9A-HJKMNP-TV-Z]{26}$`)
	workIDPattern    = regexp.MustCompile(`^work_[0-9A-HJKMNP-TV-Z]{26}$`)
)

// WorkService is the project and work-item command/query boundary.
type WorkService interface {
	RegisterProject(context.Context, workmanagement.ProjectRegistration, string) (statestore.ProjectProjection, error)
	Projects(context.Context) ([]statestore.ProjectProjection, error)
	Project(context.Context, string) (workmanagement.ProjectView, error)
	CreateWork(context.Context, workmanagement.CreateWorkRequest, string) (statestore.WorkItemProjection, error)
	ImportWork(context.Context, workmanagement.ImportWorkRequest, string) (statestore.WorkItemProjection, error)
	WorkItems(context.Context, string) ([]statestore.WorkItemProjection, error)
	WorkItem(context.Context, string) (workmanagement.WorkView, error)
}

func (s *Server) serveProjects(response http.ResponseWriter, request *http.Request, requestID string) {
	service := s.workService()
	if service == nil {
		writeAPIError(response, http.StatusServiceUnavailable, apiError{SchemaVersion: 1, Code: "WORK_SERVICE_UNAVAILABLE", Message: "Project and work operations are not configured.", RequestID: requestID, Retryable: true})
		return
	}
	clean := path.Clean(request.URL.Path)
	if clean == "/api/v1/projects" {
		switch request.Method {
		case http.MethodGet, http.MethodHead:
			if request.URL.RawQuery != "" {
				writeWorkError(response, requestID, workmanagement.ErrInvalidRequest)
				return
			}
			values, err := service.Projects(request.Context())
			if err != nil {
				writeWorkError(response, requestID, err)
				return
			}
			writeJSON(response, http.StatusOK, values)
		case http.MethodPost:
			key, ok := requireIdempotencyKey(response, request, requestID)
			if !ok {
				return
			}
			var input workmanagement.ProjectRegistration
			if err := decodeWorkJSON(request, &input); err != nil {
				writeWorkError(response, requestID, err)
				return
			}
			canonicalSource, err := existingAbsoluteDirectory(input.Source)
			if err != nil {
				writeWorkError(response, requestID, err)
				return
			}
			input.Source = canonicalSource
			value, err := service.RegisterProject(request.Context(), input, key)
			if err != nil {
				writeWorkError(response, requestID, err)
				return
			}
			response.Header().Set("Location", "/api/v1/projects/"+value.ProjectID)
			writeJSON(response, http.StatusCreated, value)
		default:
			writeWorkMethod(response, requestID, "GET, HEAD, POST")
		}
		return
	}

	if request.Method != http.MethodGet && request.Method != http.MethodHead {
		writeWorkMethod(response, requestID, "GET, HEAD")
		return
	}
	if request.URL.RawQuery != "" {
		writeWorkError(response, requestID, workmanagement.ErrInvalidRequest)
		return
	}
	projectID := strings.TrimPrefix(clean, "/api/v1/projects/")
	if !projectIDPattern.MatchString(projectID) {
		writeWorkNotFound(response, requestID, "project")
		return
	}
	value, err := service.Project(request.Context(), projectID)
	if err != nil {
		writeWorkError(response, requestID, err)
		return
	}
	writeJSON(response, http.StatusOK, value)
}

func existingAbsoluteDirectory(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" || !filepath.IsAbs(value) {
		return "", fmtWorkInvalid("project source must be an existing absolute directory")
	}
	canonical := filepath.Clean(value)
	info, err := os.Stat(canonical)
	if err != nil || !info.IsDir() {
		return "", fmtWorkInvalid("project source must be an existing absolute directory")
	}
	return canonical, nil
}

func (s *Server) serveWorkItems(response http.ResponseWriter, request *http.Request, requestID string) {
	service := s.workService()
	if service == nil {
		writeAPIError(response, http.StatusServiceUnavailable, apiError{SchemaVersion: 1, Code: "WORK_SERVICE_UNAVAILABLE", Message: "Project and work operations are not configured.", RequestID: requestID, Retryable: true})
		return
	}
	clean := path.Clean(request.URL.Path)
	if clean == "/api/v1/work-items/import" {
		if request.Method != http.MethodPost {
			writeWorkMethod(response, requestID, "POST")
			return
		}
		key, ok := requireIdempotencyKey(response, request, requestID)
		if !ok {
			return
		}
		var input workmanagement.ImportWorkRequest
		if err := decodeWorkJSON(request, &input); err != nil {
			writeWorkError(response, requestID, err)
			return
		}
		value, err := service.ImportWork(request.Context(), input, key)
		if err != nil {
			writeWorkError(response, requestID, err)
			return
		}
		response.Header().Set("Location", "/api/v1/work-items/"+value.WorkItemID)
		writeJSON(response, http.StatusCreated, value)
		return
	}
	if clean == "/api/v1/work-items" {
		switch request.Method {
		case http.MethodGet, http.MethodHead:
			query := request.URL.Query()
			if len(query) > 1 || (len(query) == 1 && (!query.Has("projectId") || len(query["projectId"]) != 1)) {
				writeWorkError(response, requestID, workmanagement.ErrInvalidRequest)
				return
			}
			projectID := query.Get("projectId")
			if projectID != "" && !projectIDPattern.MatchString(projectID) {
				writeWorkError(response, requestID, workmanagement.ErrInvalidRequest)
				return
			}
			values, err := service.WorkItems(request.Context(), projectID)
			if err != nil {
				writeWorkError(response, requestID, err)
				return
			}
			writeJSON(response, http.StatusOK, values)
		case http.MethodPost:
			key, ok := requireIdempotencyKey(response, request, requestID)
			if !ok {
				return
			}
			var input workmanagement.CreateWorkRequest
			if err := decodeWorkJSON(request, &input); err != nil {
				writeWorkError(response, requestID, err)
				return
			}
			value, err := service.CreateWork(request.Context(), input, key)
			if err != nil {
				writeWorkError(response, requestID, err)
				return
			}
			response.Header().Set("Location", "/api/v1/work-items/"+value.WorkItemID)
			writeJSON(response, http.StatusCreated, value)
		default:
			writeWorkMethod(response, requestID, "GET, HEAD, POST")
		}
		return
	}

	if request.Method != http.MethodGet && request.Method != http.MethodHead {
		writeWorkMethod(response, requestID, "GET, HEAD")
		return
	}
	if request.URL.RawQuery != "" {
		writeWorkError(response, requestID, workmanagement.ErrInvalidRequest)
		return
	}
	workID := strings.TrimPrefix(clean, "/api/v1/work-items/")
	if !workIDPattern.MatchString(workID) {
		writeWorkNotFound(response, requestID, "work item")
		return
	}
	value, err := service.WorkItem(request.Context(), workID)
	if err != nil {
		writeWorkError(response, requestID, err)
		return
	}
	writeJSON(response, http.StatusOK, value)
}

func (s *Server) workService() WorkService {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.work
}

func decodeWorkJSON(request *http.Request, destination any) error {
	if request.URL.RawQuery != "" {
		return fmtWorkInvalid("mutation requests do not accept query parameters")
	}
	decoder := json.NewDecoder(io.LimitReader(request.Body, 64<<10))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil || decoder.Decode(new(any)) != io.EOF {
		return fmtWorkInvalid("request must contain one valid JSON object")
	}
	return nil
}

func requireIdempotencyKey(response http.ResponseWriter, request *http.Request, requestID string) (string, bool) {
	key := request.Header.Get("Idempotency-Key")
	if strings.TrimSpace(key) != key || len(key) < 8 || len(key) > 128 {
		writeAPIError(response, http.StatusBadRequest, apiError{SchemaVersion: 1, Code: "VALIDATION_FAILED", Message: "Idempotency-Key must be between 8 and 128 bytes without surrounding whitespace.", RequestID: requestID})
		return "", false
	}
	return key, true
}

func fmtWorkInvalid(message string) error {
	return errors.Join(workmanagement.ErrInvalidRequest, errors.New(message))
}

func writeWorkMethod(response http.ResponseWriter, requestID, allow string) {
	response.Header().Set("Allow", allow)
	writeAPIError(response, http.StatusMethodNotAllowed, apiError{SchemaVersion: 1, Code: "METHOD_NOT_ALLOWED", Message: "The HTTP method is not supported for this resource.", RequestID: requestID})
}

func writeWorkNotFound(response http.ResponseWriter, requestID, kind string) {
	writeAPIError(response, http.StatusNotFound, apiError{SchemaVersion: 1, Code: "NOT_FOUND", Message: "The requested " + kind + " was not found.", RequestID: requestID})
}

func writeWorkError(response http.ResponseWriter, requestID string, err error) {
	if errors.Is(err, statestore.ErrNotFound) {
		writeWorkNotFound(response, requestID, "resource")
		return
	}
	if errors.Is(err, workmanagement.ErrInvalidRequest) {
		writeAPIError(response, http.StatusBadRequest, apiError{SchemaVersion: 1, Code: "VALIDATION_FAILED", Message: err.Error(), RequestID: requestID})
		return
	}
	if errors.Is(err, workmanagement.ErrCommandInProgress) {
		writeAPIError(response, http.StatusConflict, apiError{SchemaVersion: 1, Code: "COMMAND_IN_PROGRESS", Message: err.Error(), RequestID: requestID, Retryable: true})
		return
	}
	writeAPIError(response, http.StatusConflict, apiError{SchemaVersion: 1, Code: "WORK_COMMAND_FAILED", Message: err.Error(), RequestID: requestID})
}
