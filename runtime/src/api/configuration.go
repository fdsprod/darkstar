package api

import (
	"context"
	"errors"
	"net/http"

	"darkstar/src/core/config"
)

// ConfigurationReporter produces the authenticated, redacted effective
// configuration projection without exposing configuration mutation.
type ConfigurationReporter interface {
	EffectiveConfigurationForProject(context.Context, string) (config.EffectiveReport, error)
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
