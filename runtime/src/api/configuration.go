package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"path"

	"darkstar/src/core/config"
	"darkstar/src/core/configmutation"
	"darkstar/src/ports/configurationstore"
)

// ConfigurationReporter produces the authenticated, redacted effective
// configuration projection without exposing configuration mutation.
type ConfigurationReporter interface {
	EffectiveConfigurationForProject(context.Context, string) (config.EffectiveReport, error)
}

// ConfigurationMutationService is the complete typed settings command/query boundary.
type ConfigurationMutationService interface {
	Catalog(context.Context) (config.Catalog, error)
	State(context.Context, config.MutationScope) (configmutation.State, error)
	Preview(context.Context, configmutation.MutationRequest) (configmutation.Preview, error)
	Apply(context.Context, configmutation.ApplyRequest) (configmutation.ApplyResult, error)
	Restore(context.Context, configmutation.RestoreRequest) (configmutation.ApplyResult, error)
	WriteSecret(context.Context, configmutation.SecretWriteRequest) (configmutation.SecretReceipt, error)
}

func (s *Server) SetConfigurationMutations(service ConfigurationMutationService) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state != serverNew {
		return errors.New("API configuration mutation service can only be set before start")
	}
	if service == nil {
		return errors.New("API configuration mutation service is required")
	}
	s.configMutation = service
	return nil
}

func (s *Server) serveConfiguration(response http.ResponseWriter, request *http.Request, requestID string) {
	clean := path.Clean(request.URL.Path)
	if clean == "/api/v1/configuration/effective" {
		s.serveEffectiveConfiguration(response, request, requestID)
		return
	}
	s.mu.RLock()
	service := s.configMutation
	s.mu.RUnlock()
	if service == nil {
		writeAPIError(response, http.StatusServiceUnavailable, apiError{SchemaVersion: 1, Code: "CONFIGURATION_UNAVAILABLE", Message: "Configuration mutation is not configured.", RequestID: requestID, Retryable: true})
		return
	}
	switch clean {
	case "/api/v1/configuration/catalog":
		if request.Method != http.MethodGet && request.Method != http.MethodHead {
			writeConfigurationMethod(response, requestID, "GET, HEAD")
			return
		}
		if request.URL.RawQuery != "" {
			writeConfigurationError(response, requestID, configmutation.ErrInvalidRequest)
			return
		}
		value, err := service.Catalog(request.Context())
		if err != nil {
			writeConfigurationError(response, requestID, err)
			return
		}
		writeJSON(response, http.StatusOK, value)
	case "/api/v1/configuration/state":
		if request.Method != http.MethodGet && request.Method != http.MethodHead {
			writeConfigurationMethod(response, requestID, "GET, HEAD")
			return
		}
		scope, err := configurationScopeFromQuery(request)
		if err != nil {
			writeConfigurationError(response, requestID, err)
			return
		}
		value, err := service.State(request.Context(), scope)
		if err != nil {
			writeConfigurationError(response, requestID, err)
			return
		}
		response.Header().Set("ETag", `"`+value.Revision+`"`)
		writeJSON(response, http.StatusOK, value)
	case "/api/v1/configuration/preview":
		if request.Method != http.MethodPost {
			writeConfigurationMethod(response, requestID, "POST")
			return
		}
		var input configmutation.MutationRequest
		if err := decodeConfigurationJSON(request, &input); err != nil {
			writeConfigurationError(response, requestID, err)
			return
		}
		value, err := service.Preview(request.Context(), input)
		if err != nil {
			writeConfigurationError(response, requestID, err)
			return
		}
		writeJSON(response, http.StatusOK, value)
	case "/api/v1/configuration/apply":
		if request.Method != http.MethodPost {
			writeConfigurationMethod(response, requestID, "POST")
			return
		}
		key, ok := requireIdempotencyKey(response, request, requestID)
		if !ok {
			return
		}
		var input configmutation.MutationRequest
		if err := decodeConfigurationJSON(request, &input); err != nil {
			writeConfigurationError(response, requestID, err)
			return
		}
		value, err := service.Apply(request.Context(), configmutation.ApplyRequest{MutationRequest: input, IdempotencyKey: key})
		if err != nil {
			writeConfigurationError(response, requestID, err)
			return
		}
		writeJSON(response, http.StatusOK, value)
	case "/api/v1/configuration/restore":
		if request.Method != http.MethodPost {
			writeConfigurationMethod(response, requestID, "POST")
			return
		}
		key, ok := requireIdempotencyKey(response, request, requestID)
		if !ok {
			return
		}
		var input struct {
			Scope            config.MutationScope `json:"scope"`
			ExpectedRevision string               `json:"expectedRevision"`
		}
		if err := decodeConfigurationJSON(request, &input); err != nil {
			writeConfigurationError(response, requestID, err)
			return
		}
		value, err := service.Restore(request.Context(), configmutation.RestoreRequest{Scope: input.Scope, ExpectedRevision: input.ExpectedRevision, IdempotencyKey: key})
		if err != nil {
			writeConfigurationError(response, requestID, err)
			return
		}
		writeJSON(response, http.StatusOK, value)
	case "/api/v1/configuration/secrets":
		if request.Method != http.MethodPost {
			writeConfigurationMethod(response, requestID, "POST")
			return
		}
		key, ok := requireIdempotencyKey(response, request, requestID)
		if !ok {
			return
		}
		var input struct {
			Name             string `json:"name"`
			Value            string `json:"value"`
			ExpectedRevision string `json:"expectedRevision"`
		}
		if err := decodeConfigurationJSON(request, &input); err != nil {
			writeConfigurationError(response, requestID, err)
			return
		}
		value, err := service.WriteSecret(request.Context(), configmutation.SecretWriteRequest{Name: input.Name, Value: input.Value, ExpectedRevision: input.ExpectedRevision, IdempotencyKey: key})
		if err != nil {
			writeConfigurationError(response, requestID, err)
			return
		}
		writeJSON(response, http.StatusOK, value)
	default:
		writeAPIError(response, http.StatusNotFound, apiError{SchemaVersion: 1, Code: "NOT_FOUND", Message: "The API resource was not found.", RequestID: requestID})
	}
}

func configurationScopeFromQuery(request *http.Request) (config.MutationScope, error) {
	query := request.URL.Query()
	if len(query) == 0 {
		return config.UserMutationScope(), nil
	}
	if len(query) != 1 || !query.Has("projectId") || len(query["projectId"]) != 1 {
		return config.MutationScope{}, errors.Join(configmutation.ErrInvalidRequest, errors.New("state accepts only one projectId query"))
	}
	return config.ProjectMutationScope(query.Get("projectId"))
}
func decodeConfigurationJSON(request *http.Request, destination any) error {
	if request.URL.RawQuery != "" {
		return errors.Join(configmutation.ErrInvalidRequest, errors.New("mutation requests do not accept query parameters"))
	}
	decoder := json.NewDecoder(io.LimitReader(request.Body, 64<<10))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil || decoder.Decode(new(any)) != io.EOF {
		return errors.Join(configmutation.ErrInvalidRequest, errors.New("request must contain one valid JSON object"))
	}
	return nil
}
func writeConfigurationMethod(response http.ResponseWriter, requestID, allow string) {
	response.Header().Set("Allow", allow)
	writeAPIError(response, http.StatusMethodNotAllowed, apiError{SchemaVersion: 1, Code: "METHOD_NOT_ALLOWED", Message: "The HTTP method is not supported for this resource.", RequestID: requestID})
}
func writeConfigurationError(response http.ResponseWriter, requestID string, err error) {
	status, code, message, retryable := http.StatusServiceUnavailable, "CONFIGURATION_FAILED", "Configuration operation failed.", true
	switch {
	case errors.Is(err, configmutation.ErrInvalidRequest):
		status, code, message, retryable = http.StatusUnprocessableEntity, "VALIDATION_FAILED", err.Error(), false
	case errors.Is(err, configmutation.ErrProjectNotFound), errors.Is(err, configmutation.ErrProjectMismatch), errors.Is(err, configurationstore.ErrNoPrevious):
		status, code, message, retryable = http.StatusNotFound, "CONFIGURATION_NOT_FOUND", err.Error(), false
	case errors.Is(err, configurationstore.ErrRevisionConflict):
		status, code, message, retryable = http.StatusConflict, "REVISION_CONFLICT", err.Error(), false
	case errors.Is(err, configmutation.ErrCommandInProgress):
		status, code, message, retryable = http.StatusServiceUnavailable, "COMMAND_RECOVERY_PENDING", err.Error(), true
	case errors.Is(err, configurationstore.ErrPathBoundary):
		status, code, message, retryable = http.StatusUnprocessableEntity, "PATH_BOUNDARY_VIOLATION", err.Error(), false
	}
	writeAPIError(response, status, apiError{SchemaVersion: 1, Code: code, Message: message, RequestID: requestID, Retryable: retryable})
}

// SetConfiguration installs read-only effective configuration reporting before
// Start publishes the API.
func (s *Server) SetConfiguration(reporter ConfigurationReporter) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state != serverNew {
		return errors.New("API configuration reporter can only be set before start")
	}
	if reporter == nil {
		return errors.New("API configuration reporter is required")
	}
	s.configuration = reporter
	return nil
}

func (s *Server) serveEffectiveConfiguration(response http.ResponseWriter, request *http.Request, requestID string) {
	if request.Method != http.MethodGet && request.Method != http.MethodHead {
		response.Header().Set("Allow", "GET, HEAD")
		writeAPIError(response, http.StatusMethodNotAllowed, apiError{
			SchemaVersion: 1,
			Code:          "METHOD_NOT_ALLOWED",
			Message:       "The HTTP method is not supported for this resource.",
			RequestID:     requestID,
			Retryable:     false,
		})
		return
	}
	s.mu.RLock()
	reporter := s.configuration
	s.mu.RUnlock()
	if reporter == nil {
		writeAPIError(response, http.StatusServiceUnavailable, apiError{
			SchemaVersion: 1,
			Code:          "CONFIGURATION_UNAVAILABLE",
			Message:       "Effective configuration reporting is not configured.",
			RequestID:     requestID,
			Retryable:     true,
		})
		return
	}
	query := request.URL.Query()
	if len(query) > 1 || (len(query) == 1 && !query.Has("projectRoot")) || len(query["projectRoot"]) > 1 {
		writeAPIError(response, http.StatusBadRequest, apiError{
			SchemaVersion: 1,
			Code:          "VALIDATION_FAILED",
			Message:       "The effective configuration request query is invalid.",
			RequestID:     requestID,
			Retryable:     false,
		})
		return
	}
	projectRoot := query.Get("projectRoot")
	if projectRoot != "" && !pathIsAbsolute(projectRoot) {
		writeAPIError(response, http.StatusBadRequest, apiError{
			SchemaVersion: 1,
			Code:          "VALIDATION_FAILED",
			Message:       "The configuration project root must be absolute.",
			RequestID:     requestID,
			Retryable:     false,
		})
		return
	}
	report, err := reporter.EffectiveConfigurationForProject(request.Context(), projectRoot)
	if err != nil {
		writeAPIError(response, http.StatusServiceUnavailable, apiError{
			SchemaVersion: 1,
			Code:          "CONFIGURATION_FAILED",
			Message:       "Effective configuration reporting failed.",
			RequestID:     requestID,
			Retryable:     true,
		})
		return
	}
	writeJSON(response, http.StatusOK, report)
}
