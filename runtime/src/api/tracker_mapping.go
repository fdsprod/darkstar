package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"path"
	"strings"

	"darkstar/src/core/trackerrules"
	"darkstar/src/ports"
)

type TrackerMappingRevision struct {
	Revision        uint64          `json:"revision"`
	BindingRevision uint64          `json:"bindingRevision"`
	Rules           json.RawMessage `json:"rules"`
	CreatedAt       string          `json:"createdAt"`
}

type TrackerMappingHistory struct {
	SchemaVersion  int                      `json:"schemaVersion"`
	ActiveRevision uint64                   `json:"activeRevision"`
	Revisions      []TrackerMappingRevision `json:"revisions"`
}

type TrackerMappingRequest struct {
	SchemaVersion int                  `json:"schemaVersion"`
	Rules         json.RawMessage      `json:"rules"`
	ObservationID string               `json:"observationId,omitempty"`
	Event         *TrackerMappingEvent `json:"event,omitempty"`
}

type TrackerMappingEvent struct {
	Workflow    trackerrules.WorkflowPin `json:"workflow"`
	MilestoneID string                   `json:"milestoneId"`
}

type TrackerMappingActivation struct {
	SchemaVersion          int    `json:"schemaVersion"`
	Revision               uint64 `json:"revision"`
	ExpectedActiveRevision uint64 `json:"expectedActiveRevision"`
	ObservationID          string `json:"observationId,omitempty"`
}

type TrackerBoardAction struct {
	ID             string   `json:"id"`
	Name           string   `json:"name"`
	TargetStateID  string   `json:"targetStateId"`
	Availability   string   `json:"availability"`
	Reason         string   `json:"reason,omitempty"`
	Automation     []string `json:"automation"`
	RequiredFields []string `json:"requiredFields"`
}

type TrackerBoardColumn struct {
	ID        string   `json:"id"`
	Name      string   `json:"name"`
	StatusIDs []string `json:"statusIds"`
}

type TrackerBoard struct {
	SchemaVersion  int                             `json:"schemaVersion"`
	ActiveRevision uint64                          `json:"activeRevision"`
	Columns        []TrackerBoardColumn            `json:"columns"`
	UnknownGroup   TrackerBoardColumn              `json:"unknownGroup"`
	Actions        map[string][]TrackerBoardAction `json:"actions"`
	Reason         string                          `json:"reason,omitempty"`
}

type TrackerBoardTransition struct {
	SchemaVersion           int    `json:"schemaVersion"`
	ObservationID           string `json:"observationId"`
	ExpectedBindingRevision uint64 `json:"expectedBindingRevision"`
	ExpectedMappingRevision uint64 `json:"expectedMappingRevision"`
	TransitionID            string `json:"transitionId"`
}

type TrackerMappingService interface {
	History(context.Context, string) (TrackerMappingHistory, error)
	Save(context.Context, string, TrackerMappingRequest) (TrackerMappingRevision, error)
	Activate(context.Context, string, TrackerMappingActivation) (TrackerMappingHistory, error)
	Discovery(context.Context, string, string) (any, error)
	Preview(context.Context, string, TrackerMappingRequest) (any, error)
	Board(context.Context, string) (TrackerBoard, error)
	Transition(context.Context, string, TrackerBoardTransition, string) (any, error)
}

func (s *Server) SetTrackerMapping(service TrackerMappingService) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state != serverNew || service == nil {
		return errors.New("tracker mappings must be configured before server start")
	}
	s.trackerMapping = service
	return nil
}

func (s *Server) serveTrackerMapping(response http.ResponseWriter, request *http.Request, requestID string) {
	s.mu.RLock()
	service := s.trackerMapping
	s.mu.RUnlock()
	if service == nil {
		writeBacklogError(response, requestID, &ports.Failure{Code: ports.FailureUnavailable, Message: "Tracker mappings are unavailable."})
		return
	}
	parts := strings.Split(strings.TrimPrefix(path.Clean(request.URL.Path), "/api/v1/projects/"), "/")
	if len(parts) < 2 || len(parts) > 3 || !projectIDPattern.MatchString(parts[0]) {
		writeWorkNotFound(response, requestID, "tracker mapping")
		return
	}
	operation := parts[1]
	if len(parts) == 3 {
		operation += "/" + parts[2]
	}
	var result any
	var err error
	switch request.Method {
	case http.MethodGet, http.MethodHead:
		if !sourceSingleQuery(request, "observationId", false) || request.URL.RawQuery != "" && operation != "tracker-mapping/discovery" {
			writeBacklogError(response, requestID, backlogInvalid("Only discovery accepts one observationId query."))
			return
		}
		switch operation {
		case "tracker-mapping":
			result, err = service.History(request.Context(), parts[0])
		case "tracker-mapping/discovery":
			result, err = service.Discovery(request.Context(), parts[0], request.URL.Query().Get("observationId"))
		case "tracker-board":
			result, err = service.Board(request.Context(), parts[0])
		default:
			writeWorkMethod(response, requestID, "POST")
			return
		}
	case http.MethodPost:
		if request.URL.RawQuery != "" {
			writeBacklogError(response, requestID, backlogInvalid("Mapping commands accept no query parameters."))
			return
		}
		switch operation {
		case "tracker-mapping", "tracker-mapping/preview":
			var input TrackerMappingRequest
			if decodeWorkJSON(request, &input) != nil || input.SchemaVersion != 1 || len(input.Rules) == 0 {
				err = backlogInvalid("Mapping requires schemaVersion 1 and rules.")
			} else if operation == "tracker-mapping" {
				result, err = service.Save(request.Context(), parts[0], input)
			} else {
				result, err = service.Preview(request.Context(), parts[0], input)
			}
		case "tracker-mapping/activate":
			var input TrackerMappingActivation
			if decodeWorkJSON(request, &input) != nil || input.SchemaVersion != 1 || input.Revision == 0 {
				err = backlogInvalid("Activation requires schemaVersion 1 and exact revision.")
			} else {
				result, err = service.Activate(request.Context(), parts[0], input)
			}
		case "tracker-board/transition":
			key, ok := requireIdempotencyKey(response, request, requestID)
			if !ok {
				return
			}
			var input TrackerBoardTransition
			if decodeWorkJSON(request, &input) != nil || input.SchemaVersion != 1 || input.ObservationID == "" || input.ExpectedBindingRevision == 0 || input.TransitionID == "" {
				err = backlogInvalid("Transition requires exact source, observation and action IDs.")
			} else {
				result, err = service.Transition(request.Context(), parts[0], input, key)
			}
		default:
			writeWorkMethod(response, requestID, "GET, HEAD")
			return
		}
	default:
		writeWorkMethod(response, requestID, "GET, HEAD, POST")
		return
	}
	if err != nil {
		writeBacklogError(response, requestID, err)
		return
	}
	writeJSON(response, http.StatusOK, result)
}
