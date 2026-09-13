package api

import (
	"context"
	"errors"
	"net/http"
	"path"
	"strconv"
	"strings"

	"darkstar/src/ports"
	"darkstar/src/ports/trackerconnection"
)

type TrackerConnectionService interface {
	StoreCredential(context.Context, string, string) error
	CreateLinear(context.Context, trackerconnection.LinearSetupRequest) (trackerconnection.Record, error)
	CreateGitHubToken(context.Context, trackerconnection.GitHubTokenSetupRequest) (trackerconnection.Record, error)
	CreateGitHubCLI(context.Context, trackerconnection.GitHubCLISetupRequest) (trackerconnection.Record, error)
	List(context.Context) ([]trackerconnection.Record, error)
	Get(context.Context, string, string) (trackerconnection.Record, error)
	Health(context.Context, string, string) (trackerconnection.Health, error)
	Destinations(context.Context, string, string, string, int) (trackerconnection.Destinations, error)
}

func (s *Server) SetTrackerConnections(service TrackerConnectionService) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state != serverNew || service == nil {
		return errors.New("tracker connections must be configured before start")
	}
	s.trackerConnections = service
	return nil
}

func (s *Server) serveTrackerConnections(response http.ResponseWriter, request *http.Request, requestID string) {
	s.mu.RLock()
	service := s.trackerConnections
	s.mu.RUnlock()
	if service == nil {
		writeNativeTicketError(response, requestID, &ports.Failure{Code: ports.FailureUnavailable, Message: "Tracker connection setup is unavailable."})
		return
	}
	parts := strings.Split(strings.TrimPrefix(path.Clean(request.URL.Path), "/api/v1/tracker/"), "/")
	if len(parts) == 2 && parts[0] == "credentials" {
		if request.Method != http.MethodPost {
			writeWorkMethod(response, requestID, "POST")
			return
		}
		var input struct {
			SchemaVersion int    `json:"schemaVersion"`
			Secret        string `json:"secret"`
		}
		if request.URL.RawQuery != "" || decodeWorkJSON(request, &input) != nil || input.SchemaVersion != 1 {
			writeNativeTicketError(response, requestID, &ports.Failure{Code: ports.FailureInvalidRequest, Message: "A schemaVersion 1 credential body is required."})
			return
		}
		// Secret bytes are handed directly to protected storage. Do not create
		// command/idempotency records, log bodies, or echo the supplied value.
		if err := service.StoreCredential(request.Context(), parts[1], input.Secret); err != nil {
			writeNativeTicketError(response, requestID, err)
			return
		}
		writeJSON(response, http.StatusOK, struct {
			SchemaVersion int  `json:"schemaVersion"`
			Stored        bool `json:"stored"`
		}{SchemaVersion: 1, Stored: true})
		return
	}
	if len(parts) == 0 || parts[0] != "connections" {
		writeWorkNotFound(response, requestID, "tracker connection")
		return
	}
	if len(parts) == 2 && request.Method == http.MethodPost {
		if request.URL.RawQuery != "" {
			writeNativeTicketError(response, requestID, &ports.Failure{Code: ports.FailureInvalidRequest, Message: "Connection setup accepts no query parameters."})
			return
		}
		var record trackerconnection.Record
		var err error
		switch parts[1] {
		case "linear":
			var input trackerconnection.LinearSetupRequest
			if err = decodeWorkJSON(request, &input); err == nil {
				record, err = service.CreateLinear(request.Context(), input)
			}
		case "github-token":
			var input trackerconnection.GitHubTokenSetupRequest
			if err = decodeWorkJSON(request, &input); err == nil {
				record, err = service.CreateGitHubToken(request.Context(), input)
			}
		case "github-cli":
			var input trackerconnection.GitHubCLISetupRequest
			if err = decodeWorkJSON(request, &input); err == nil {
				record, err = service.CreateGitHubCLI(request.Context(), input)
			}
		default:
			writeWorkNotFound(response, requestID, "tracker setup method")
			return
		}
		if err != nil {
			writeNativeTicketError(response, requestID, err)
			return
		}
		writeJSON(response, http.StatusCreated, record)
		return
	}
	if request.Method != http.MethodGet && request.Method != http.MethodHead {
		writeWorkMethod(response, requestID, "GET, HEAD")
		return
	}
	if len(parts) == 1 {
		if request.URL.RawQuery != "" {
			writeNativeTicketError(response, requestID, &ports.Failure{Code: ports.FailureInvalidRequest, Message: "Connection list accepts no query parameters."})
			return
		}
		values, err := service.List(request.Context())
		if err != nil {
			writeNativeTicketError(response, requestID, err)
			return
		}
		writeJSON(response, http.StatusOK, struct {
			SchemaVersion int                        `json:"schemaVersion"`
			Connections   []trackerconnection.Record `json:"connections"`
		}{SchemaVersion: 1, Connections: values})
		return
	}
	if len(parts) < 4 || len(parts) > 5 || parts[2] != "revisions" {
		writeWorkNotFound(response, requestID, "tracker connection revision")
		return
	}
	var result any
	var err error
	if len(parts) == 5 && parts[4] == "destinations" {
		query := request.URL.Query()
		for name, values := range query {
			if (name != "cursor" && name != "pageSize") || len(values) != 1 {
				writeNativeTicketError(response, requestID, &ports.Failure{Code: ports.FailureInvalidRequest, Message: "Unsupported destination discovery parameter."})
				return
			}
		}
		size := 50
		if raw := query.Get("pageSize"); raw != "" {
			size, err = strconv.Atoi(raw)
			if err != nil || size < 1 || size > 100 {
				writeNativeTicketError(response, requestID, &ports.Failure{Code: ports.FailureInvalidRequest, Message: "pageSize must be 1 to 100."})
				return
			}
		}
		result, err = service.Destinations(request.Context(), parts[1], parts[3], query.Get("cursor"), size)
	} else if request.URL.RawQuery != "" {
		err = &ports.Failure{Code: ports.FailureInvalidRequest, Message: "Connection detail and health accept no query parameters."}
	} else if len(parts) == 4 {
		result, err = service.Get(request.Context(), parts[1], parts[3])
	} else if parts[4] == "health" {
		result, err = service.Health(request.Context(), parts[1], parts[3])
	} else {
		writeWorkNotFound(response, requestID, "tracker connection operation")
		return
	}
	if err != nil {
		writeNativeTicketError(response, requestID, err)
		return
	}
	writeJSON(response, http.StatusOK, result)
}
