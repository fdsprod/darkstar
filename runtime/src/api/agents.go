package api

import (
	"context"
	"errors"
	"io"
	"net/http"
	"path"
	"regexp"
	"strings"

	"darkstar/src/core/runexecution"
	"darkstar/src/ports/statestore"
)

var attemptIDPattern = regexp.MustCompile(`^attempt_[0-9A-HJKMNP-TV-Z]{26}$`)

// AgentService is the provider-attempt operational boundary published by the
// local API. Attempt projections remain the authoritative lifecycle state.
type AgentService interface {
	ListAgents(context.Context) (runexecution.AgentList, error)
	Agent(context.Context, string) (runexecution.Agent, error)
	CancelAgent(context.Context, string, string) (runexecution.Agent, error)
}

func (s *Server) serveAgents(response http.ResponseWriter, request *http.Request, requestID string) {
	s.mu.RLock()
	agents := s.agents
	s.mu.RUnlock()
	if agents == nil {
		writeAPIError(response, http.StatusServiceUnavailable, apiError{SchemaVersion: 1, Code: "AGENT_SERVICE_UNAVAILABLE", Message: "Agent inspection is not configured.", RequestID: requestID, Retryable: true})
		return
	}
	clean := path.Clean(request.URL.Path)
	if clean == "/api/v1/agents" {
		if request.Method != http.MethodGet && request.Method != http.MethodHead {
			response.Header().Set("Allow", "GET, HEAD")
			writeAPIError(response, http.StatusMethodNotAllowed, apiError{SchemaVersion: 1, Code: "METHOD_NOT_ALLOWED", Message: "The HTTP method is not supported for this resource.", RequestID: requestID})
			return
		}
		if request.URL.RawQuery != "" {
			writeAPIError(response, http.StatusBadRequest, apiError{SchemaVersion: 1, Code: "VALIDATION_FAILED", Message: "The agent list does not accept query parameters.", RequestID: requestID})
			return
		}
		list, err := agents.ListAgents(request.Context())
		if err != nil {
			writeAPIError(response, http.StatusInternalServerError, apiError{SchemaVersion: 1, Code: "AGENT_QUERY_FAILED", Message: "Active agent state could not be read.", RequestID: requestID, Retryable: true})
			return
		}
		writeJSON(response, http.StatusOK, list)
		return
	}

	relative := strings.TrimPrefix(clean, "/api/v1/agents/")
	segments := strings.Split(relative, "/")
	if len(segments) < 1 || !attemptIDPattern.MatchString(segments[0]) {
		writeAPIError(response, http.StatusNotFound, apiError{SchemaVersion: 1, Code: "NOT_FOUND", Message: "The requested agent attempt was not found.", RequestID: requestID})
		return
	}
	attemptID := segments[0]
	if len(segments) == 1 {
		s.serveAgentStatus(response, request, requestID, agents, attemptID)
		return
	}
	if len(segments) == 2 && segments[1] == "logs" {
		s.serveAgentLog(response, request, requestID, agents, attemptID)
		return
	}
	if len(segments) == 2 && segments[1] == "cancel" {
		s.serveAgentCancel(response, request, requestID, agents, attemptID)
		return
	}
	writeAPIError(response, http.StatusNotFound, apiError{SchemaVersion: 1, Code: "NOT_FOUND", Message: "The requested agent resource was not found.", RequestID: requestID})
}

func (s *Server) serveAgentStatus(response http.ResponseWriter, request *http.Request, requestID string, agents AgentService, attemptID string) {
	if request.Method != http.MethodGet && request.Method != http.MethodHead {
		response.Header().Set("Allow", "GET, HEAD")
		writeAPIError(response, http.StatusMethodNotAllowed, apiError{SchemaVersion: 1, Code: "METHOD_NOT_ALLOWED", Message: "The HTTP method is not supported for this resource.", RequestID: requestID})
		return
	}
	if request.URL.RawQuery != "" {
		writeAPIError(response, http.StatusBadRequest, apiError{SchemaVersion: 1, Code: "VALIDATION_FAILED", Message: "Agent status does not accept query parameters.", RequestID: requestID})
		return
	}
	agent, err := agents.Agent(request.Context(), attemptID)
	if err != nil {
		writeAgentError(response, requestID, err)
		return
	}
	writeJSON(response, http.StatusOK, agent)
}

func (s *Server) serveAgentLog(response http.ResponseWriter, request *http.Request, requestID string, agents AgentService, attemptID string) {
	if request.Method != http.MethodGet {
		response.Header().Set("Allow", "GET")
		writeAPIError(response, http.StatusMethodNotAllowed, apiError{SchemaVersion: 1, Code: "METHOD_NOT_ALLOWED", Message: "The HTTP method is not supported for this resource.", RequestID: requestID})
		return
	}
	agent, err := agents.Agent(request.Context(), attemptID)
	if err != nil {
		writeAgentError(response, requestID, err)
		return
	}
	if agent.LogReference == "" {
		writeAPIError(response, http.StatusNotFound, apiError{SchemaVersion: 1, Code: "AGENT_LOG_NOT_FOUND", Message: "The attempt has no recorded log.", RequestID: requestID})
		return
	}
	s.serveLogReference(response, request, requestID, agent.LogReference)
}

func (s *Server) serveAgentCancel(response http.ResponseWriter, request *http.Request, requestID string, agents AgentService, attemptID string) {
	if request.Method != http.MethodPost {
		response.Header().Set("Allow", "POST")
		writeAPIError(response, http.StatusMethodNotAllowed, apiError{SchemaVersion: 1, Code: "METHOD_NOT_ALLOWED", Message: "The HTTP method is not supported for this resource.", RequestID: requestID})
		return
	}
	if request.URL.RawQuery != "" {
		writeAPIError(response, http.StatusBadRequest, apiError{SchemaVersion: 1, Code: "VALIDATION_FAILED", Message: "Agent cancellation does not accept query parameters.", RequestID: requestID})
		return
	}
	key, ok := requireIdempotencyKey(response, request, requestID)
	if !ok {
		return
	}
	body, err := io.ReadAll(io.LimitReader(request.Body, 2))
	if err != nil || len(strings.TrimSpace(string(body))) != 0 {
		writeAPIError(response, http.StatusBadRequest, apiError{SchemaVersion: 1, Code: "VALIDATION_FAILED", Message: "Agent cancellation does not accept a request body.", RequestID: requestID})
		return
	}
	agent, err := agents.CancelAgent(request.Context(), attemptID, key)
	if err != nil {
		writeAgentError(response, requestID, err)
		return
	}
	writeJSON(response, http.StatusOK, agent)
}

func writeAgentError(response http.ResponseWriter, requestID string, err error) {
	switch {
	case errors.Is(err, statestore.ErrNotFound):
		writeAPIError(response, http.StatusNotFound, apiError{SchemaVersion: 1, Code: "NOT_FOUND", Message: "The requested agent attempt was not found.", RequestID: requestID})
	case errors.Is(err, runexecution.ErrAgentInvalidTransition):
		writeAPIError(response, http.StatusConflict, apiError{SchemaVersion: 1, Code: "AGENT_CANCEL_INVALID_TRANSITION", Message: err.Error(), RequestID: requestID})
	default:
		writeRunControlError(response, requestID, err)
	}
}
