package api

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"path"
	"regexp"
	"strconv"
	"strings"

	"darkstar/src/core/runexecution"
	"darkstar/src/ports/provider"
	"darkstar/src/ports/statestore"
)

var permissionRequestIDPattern = regexp.MustCompile(`^permission_[0-9A-HJKMNP-TV-Z]{26}$`)

type providerPermissionDecisionBody struct {
	Decision    provider.PermissionDecision `json:"decision"`
	ScopeDigest string                      `json:"scopeDigest"`
}

func (s *Server) serveProviderPermissions(response http.ResponseWriter, request *http.Request, requestID string, service AgentService) {
	clean := path.Clean(request.URL.Path)
	if clean == "/api/v1/agents/permissions" {
		if request.Method != http.MethodGet && request.Method != http.MethodHead {
			response.Header().Set("Allow", "GET, HEAD")
			writeAPIError(response, 405, apiError{SchemaVersion: 1, Code: "METHOD_NOT_ALLOWED", Message: "The HTTP method is not supported for this resource.", RequestID: requestID})
			return
		}
		query := request.URL.Query()
		attemptID := query.Get("attemptId")
		status := statestore.ProviderPermissionStatus(query.Get("status"))
		unknown := false
		for key := range query {
			if key != "attemptId" && key != "status" {
				unknown = true
			}
		}
		if unknown || len(query) > 2 || len(query["attemptId"]) > 1 || len(query["status"]) > 1 || (attemptID != "" && !attemptIDPattern.MatchString(attemptID)) {
			writeProviderPermissionError(response, requestID, runexecution.ErrPermissionInvalidRequest)
			return
		}
		var value runexecution.ProviderPermissionList
		var err error
		if attemptID == "" {
			value, err = service.ProviderPermissions(request.Context(), status)
		} else {
			value, err = service.ProviderPermissionsForAttempt(request.Context(), attemptID, status)
		}
		if err != nil {
			writeProviderPermissionError(response, requestID, err)
			return
		}
		writeJSON(response, 200, value)
		return
	}
	relative := strings.TrimPrefix(clean, "/api/v1/agents/permissions/")
	parts := strings.Split(relative, "/")
	if len(parts) == 0 || !permissionRequestIDPattern.MatchString(parts[0]) {
		writeProviderPermissionError(response, requestID, statestore.ErrNotFound)
		return
	}
	if request.URL.RawQuery != "" {
		writeProviderPermissionError(response, requestID, runexecution.ErrPermissionInvalidRequest)
		return
	}
	if len(parts) == 1 {
		if request.Method != http.MethodGet && request.Method != http.MethodHead {
			response.Header().Set("Allow", "GET, HEAD")
			writeAPIError(response, 405, apiError{SchemaVersion: 1, Code: "METHOD_NOT_ALLOWED", Message: "The HTTP method is not supported for this resource.", RequestID: requestID})
			return
		}
		value, err := service.ProviderPermission(request.Context(), parts[0])
		if err != nil {
			writeProviderPermissionError(response, requestID, err)
			return
		}
		response.Header().Set("ETag", `"`+strconv.FormatUint(value.ResourceVersion, 10)+`"`)
		writeJSON(response, 200, value)
		return
	}
	if len(parts) != 2 || (parts[1] != "decisions" && parts[1] != "delivery-retries") {
		writeProviderPermissionError(response, requestID, statestore.ErrNotFound)
		return
	}
	if request.Method != http.MethodPost {
		response.Header().Set("Allow", "POST")
		writeAPIError(response, 405, apiError{SchemaVersion: 1, Code: "METHOD_NOT_ALLOWED", Message: "The HTTP method is not supported for this resource.", RequestID: requestID})
		return
	}
	expected, err := parseIfMatch(request.Header.Get("If-Match"))
	if err != nil {
		writeProviderPermissionError(response, requestID, runexecution.ErrPermissionInvalidRequest)
		return
	}
	var value runexecution.ProviderPermissionView
	if parts[1] == "delivery-retries" {
		body, readErr := io.ReadAll(io.LimitReader(request.Body, 1))
		if readErr != nil || len(body) != 0 {
			writeProviderPermissionError(response, requestID, runexecution.ErrPermissionInvalidRequest)
			return
		}
		value, err = service.RetryProviderPermissionDelivery(request.Context(), parts[0], expected)
	} else {
		key, ok := requireIdempotencyKey(response, request, requestID)
		if !ok {
			return
		}
		decoder := json.NewDecoder(io.LimitReader(request.Body, 8193))
		decoder.DisallowUnknownFields()
		var body providerPermissionDecisionBody
		if decodeErr := decoder.Decode(&body); decodeErr != nil || decoder.Decode(new(any)) != io.EOF {
			writeProviderPermissionError(response, requestID, runexecution.ErrPermissionInvalidRequest)
			return
		}
		value, err = service.DecideProviderPermission(request.Context(), runexecution.DecideProviderPermissionRequest{PermissionRequestID: parts[0], ExpectedResourceVersion: expected, ScopeDigest: body.ScopeDigest, Decision: body.Decision, IdempotencyKey: key, Actor: statestore.Actor{Type: statestore.ActorUser, ID: "local-user"}})
	}
	if err != nil {
		writeProviderPermissionError(response, requestID, err)
		return
	}
	response.Header().Set("ETag", `"`+strconv.FormatUint(value.ResourceVersion, 10)+`"`)
	writeJSON(response, 200, value)
}

func writeProviderPermissionError(response http.ResponseWriter, requestID string, err error) {
	status, code, message, retryable := http.StatusConflict, "PROVIDER_PERMISSION_CONFLICT", err.Error(), false
	var versionConflict *runexecution.ProviderPermissionVersionConflictError
	switch {
	case errors.Is(err, statestore.ErrNotFound):
		status, code, message = 404, "NOT_FOUND", "The provider permission request was not found."
	case errors.Is(err, runexecution.ErrPermissionInvalidRequest):
		status, code = 400, "VALIDATION_FAILED"
	case errors.Is(err, runexecution.ErrPermissionScopeConflict):
		status, code = 412, "PROVIDER_PERMISSION_SCOPE_CONFLICT"
	case errors.As(err, &versionConflict):
		current := int64(versionConflict.Current)
		writeAPIError(response, 412, apiError{SchemaVersion: 1, Code: "PROVIDER_PERMISSION_VERSION_CONFLICT", Message: message, RequestID: requestID, ResourceVersion: &current})
		return
	case errors.Is(err, runexecution.ErrPermissionAlreadyDecided):
		code = "PROVIDER_PERMISSION_ALREADY_DECIDED"
	case errors.Is(err, runexecution.ErrPermissionDecisionInProgress):
		code = "PROVIDER_PERMISSION_DECISION_IN_PROGRESS"
	case errors.Is(err, runexecution.ErrPermissionDeliveryUnavailable):
		status, code, message, retryable = 503, "PROVIDER_PERMISSION_DELIVERY_UNAVAILABLE", "The decision is durable but provider delivery is unavailable.", true
	case errors.Is(err, runexecution.ErrPermissionInactiveAttempt):
		status, code, message = 409, "PROVIDER_PERMISSION_INACTIVE_ATTEMPT", "The owning attempt is no longer active, so this provider interaction cannot be authorized."
	}
	writeAPIError(response, status, apiError{SchemaVersion: 1, Code: code, Message: message, RequestID: requestID, Retryable: retryable})
}
