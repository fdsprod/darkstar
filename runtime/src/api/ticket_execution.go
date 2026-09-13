package api

import (
	"context"
	"errors"
	"net/http"
	"path"
	"strings"

	"darkstar/src/core/ticketexecution"
	"darkstar/src/ports"
	"darkstar/src/ports/statestore"
	"darkstar/src/ports/tracker"
)

type TicketExecutionService interface {
	Admit(context.Context, ticketexecution.AdmissionRequest, string) (ticketexecution.Result, error)
	Approve(context.Context, string, string, string) (ticketexecution.Result, error)
	Rebind(context.Context, ticketexecution.RebindRequest, string) (ticketexecution.Result, error)
	Inspect(context.Context, string) (ticketexecution.WorkSourceView, error)
	ListWorkSourceViews(context.Context, string) ([]ticketexecution.WorkSourceView, error)
	TicketView(context.Context, string, string) ([]ticketexecution.WorkSourceView, error)
	RefreshSource(context.Context, string) (ticketexecution.WorkSourceView, error)
}

func (s *Server) SetTicketExecution(service TicketExecutionService) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state != serverNew || service == nil {
		return errors.New("ticket execution must be configured before server start")
	}
	s.ticketExecution = service
	return nil
}

func (s *Server) serveTicketExecution(response http.ResponseWriter, request *http.Request, requestID string) {
	s.mu.RLock()
	service := s.ticketExecution
	s.mu.RUnlock()
	if service == nil {
		writeBacklogError(response, requestID, &ports.Failure{Code: ports.FailureUnavailable, Message: "Ticket execution is not configured."})
		return
	}
	clean := path.Clean(request.URL.Path)
	if clean == "/api/v1/work-items/source-views" {
		if request.Method != http.MethodGet && request.Method != http.MethodHead {
			writeWorkMethod(response, requestID, "GET, HEAD")
			return
		}
		project := request.URL.Query().Get("projectId")
		if !sourceSingleQuery(request, "projectId", false) || project != "" && !projectIDPattern.MatchString(project) {
			writeBacklogError(response, requestID, backlogInvalid("Source views accept one optional projectId."))
			return
		}
		views, err := service.ListWorkSourceViews(request.Context(), project)
		writeSourceViews(response, requestID, views, err)
		return
	}
	if strings.HasPrefix(clean, "/api/v1/projects/") {
		parts := strings.Split(strings.TrimPrefix(clean, "/api/v1/projects/"), "/")
		if len(parts) != 3 || !projectIDPattern.MatchString(parts[0]) || parts[1] != "backlog" {
			writeWorkNotFound(response, requestID, "ticket execution")
			return
		}
		if parts[2] == "executions" {
			if request.Method != http.MethodGet && request.Method != http.MethodHead {
				writeWorkMethod(response, requestID, "GET, HEAD")
				return
			}
			if !sourceSingleQuery(request, "observationId", true) {
				writeBacklogError(response, requestID, backlogInvalid("Ticket execution views require one observationId."))
				return
			}
			views, err := service.TicketView(request.Context(), parts[0], request.URL.Query().Get("observationId"))
			writeSourceViews(response, requestID, views, err)
			return
		}
		if parts[2] == "admit" {
			serveTicketAdmission(service, response, request, requestID, parts[0])
			return
		}
		writeWorkNotFound(response, requestID, "ticket execution")
		return
	}
	parts := strings.Split(strings.TrimPrefix(clean, "/api/v1/work-items/"), "/")
	if len(parts) < 2 || len(parts) > 3 || !workIDPattern.MatchString(parts[0]) || parts[1] != "source" || request.URL.RawQuery != "" {
		writeWorkNotFound(response, requestID, "work source")
		return
	}
	if len(parts) == 2 {
		if request.Method != http.MethodGet && request.Method != http.MethodHead {
			writeWorkMethod(response, requestID, "GET, HEAD")
			return
		}
		view, err := service.Inspect(request.Context(), parts[0])
		writeSourceView(response, requestID, view, err)
		return
	}
	serveWorkSourceCommand(service, response, request, requestID, parts[0], parts[2])
}

func sourceSingleQuery(request *http.Request, key string, required bool) bool {
	values := request.URL.Query()
	for name, list := range values {
		if name != key || len(list) != 1 || list[0] == "" {
			return false
		}
	}
	return !required || values.Get(key) != ""
}

func sourceCommandKey(response http.ResponseWriter, request *http.Request, requestID string) (string, bool) {
	if request.Method != http.MethodPost {
		writeWorkMethod(response, requestID, "POST")
		return "", false
	}
	if request.URL.RawQuery != "" {
		writeBacklogError(response, requestID, backlogInvalid("Source commands accept no query parameters."))
		return "", false
	}
	return requireIdempotencyKey(response, request, requestID)
}

func serveTicketAdmission(service TicketExecutionService, response http.ResponseWriter, request *http.Request, requestID, project string) {
	key, ok := sourceCommandKey(response, request, requestID)
	if !ok {
		return
	}
	var input TicketAdmissionRequest
	if decodeWorkJSON(request, &input) != nil || input.SchemaVersion != 1 || input.ExpectedBindingRevision == 0 || input.ObservationID == "" {
		writeBacklogError(response, requestID, backlogInvalid("Admission requires schemaVersion 1, exact binding revision and observation ID."))
		return
	}
	result, err := service.Admit(request.Context(), ticketexecution.AdmissionRequest{ProjectID: project, BindingRevision: input.ExpectedBindingRevision, ObservationID: input.ObservationID, RoutingIntent: input.RoutingIntent}, key)
	writeAdmissionResult(response, requestID, result, err)
}

func serveWorkSourceCommand(service TicketExecutionService, response http.ResponseWriter, request *http.Request, requestID, workID, operation string) {
	if operation == "refresh" {
		if request.Method != http.MethodPost {
			writeWorkMethod(response, requestID, "POST")
			return
		}
		if request.ContentLength > 0 {
			writeBacklogError(response, requestID, backlogInvalid("Source refresh reads the retained work lineage and accepts no replacement source body."))
			return
		}
		view, err := service.RefreshSource(request.Context(), workID)
		writeSourceView(response, requestID, view, err)
		return
	}
	key, ok := sourceCommandKey(response, request, requestID)
	if !ok {
		return
	}
	var result ticketexecution.Result
	var err error
	switch operation {
	case "approve":
		var input SourceApprovalRequest
		if decodeWorkJSON(request, &input) != nil || input.SchemaVersion != 1 || input.ObservationID == "" {
			writeBacklogError(response, requestID, backlogInvalid("Source approval requires schemaVersion 1 and an exact observation ID."))
			return
		}
		result, err = service.Approve(request.Context(), workID, input.ObservationID, key)
	case "rebind":
		var input SourceRebindRequest
		if decodeWorkJSON(request, &input) != nil || input.SchemaVersion != 1 || input.ExpectedBindingRevision == 0 || input.ExpectedLineageRevision == 0 || input.ObservationID == "" {
			writeBacklogError(response, requestID, backlogInvalid("Rebind requires schemaVersion 1, exact lineage and binding revisions, and observation ID."))
			return
		}
		result, err = service.Rebind(request.Context(), ticketexecution.RebindRequest{WorkID: workID, ExpectedLineageRevision: input.ExpectedLineageRevision, BindingRevision: input.ExpectedBindingRevision, ObservationID: input.ObservationID}, key)
	default:
		writeWorkNotFound(response, requestID, "source operation")
		return
	}
	writeAdmissionResult(response, requestID, result, err)
}

func writeAdmissionResult(response http.ResponseWriter, requestID string, result ticketexecution.Result, err error) {
	if err != nil {
		writeBacklogError(response, requestID, err)
		return
	}
	writeJSON(response, http.StatusOK, TicketAdmissionResponse{SchemaVersion: 1, WorkItemID: result.Work.WorkItemID, SourceObservationID: result.Admission.ObservationID, Source: sourceView(result.Source)})
}

func writeSourceView(response http.ResponseWriter, requestID string, view ticketexecution.WorkSourceView, err error) {
	if err != nil {
		writeBacklogError(response, requestID, err)
		return
	}
	writeJSON(response, http.StatusOK, sourceView(view))
}

func writeSourceViews(response http.ResponseWriter, requestID string, views []ticketexecution.WorkSourceView, err error) {
	if err != nil {
		writeBacklogError(response, requestID, err)
		return
	}
	result := WorkSourceViews{SchemaVersion: 1, Items: make([]WorkSourceView, 0, len(views))}
	for _, view := range views {
		result.Items = append(result.Items, sourceView(view))
	}
	writeJSON(response, http.StatusOK, result)
}

func sourceView(value ticketexecution.WorkSourceView) WorkSourceView {
	result := WorkSourceView{SchemaVersion: 1, WorkItemID: value.Work.WorkItemID, ProjectID: value.Work.ProjectID, LocalActivity: value.LocalActivity, RunOutcome: value.LastRunOutcome, ApprovedTicket: sourceTicket(value.Approved), CurrentTicket: sourceTicket(value.Current), CurrentObservationID: value.CurrentObservationID, Assessment: SourceAssessment{State: SourceAssessmentState(value.Assessment.State), Reasons: append([]string{}, value.Assessment.Reasons...)}, Runs: append([]statestore.RunProjection{}, value.Runs...), Lineages: []SourceLineage{}, ExternalAcceptance: mapBacklogKnowledge(value.ExternalAcceptance, func(text string) string {
		return text
	})}
	for _, lineage := range value.Lineages {
		result.Lineages = append(result.Lineages, sourceLineage(lineage))
	}
	if value.Lineage != nil {
		lineage := sourceLineage(*value.Lineage)
		result.Lineage = &lineage
	}
	if value.Approval != nil {
		approval := value.Approval
		result.Approval = &TicketAdmission{ID: approval.ID, WorkItemID: approval.WorkID, ProjectID: approval.ProjectID, ObservationID: approval.ObservationID, LineageRevision: approval.LineageRevision, BindingRevision: approval.BindingRevision, ApprovedAt: backlogTime(approval.ApprovedAt), Actor: approval.Actor}
		if result.ApprovedTicket != nil {
			result.ApprovedTicket.ObservationID = approval.ObservationID
		}
	}
	if result.CurrentTicket != nil {
		result.CurrentTicket.ObservationID = value.CurrentObservationID
	}
	return result
}

func sourceLineage(value statestore.SourceLineage) SourceLineage {
	return SourceLineage{WorkItemID: value.WorkID, ProjectID: value.ProjectID, TicketKey: value.TicketKey, Revision: value.Revision, BindingRevision: value.BindingRevision, Ref: BacklogTicketRef{Namespace: backlogNamespace(value.Ref.Namespace), ID: value.Ref.ID}, Origin: value.Origin, CreatedAt: backlogTime(value.CreatedAt)}
}

func sourceTicket(value *tracker.Ticket) *SourceTicketObservation {
	if value == nil {
		return nil
	}
	result := &SourceTicketObservation{Ref: BacklogTicketRef{Namespace: backlogNamespace(value.Ref.Namespace), ID: value.Ref.ID}, Revision: value.Revision, Title: value.Title, Description: value.Description, Key: value.Key, URL: value.URL, BusinessState: mapBacklogKnowledge(value.BusinessState, backlogNamed), EvidenceRef: value.EvidenceRef}
	switch freshness := value.Freshness.(type) {
	case tracker.Fresh:
		result.ObservedAt = backlogTime(freshness.ObservedAt)
	case tracker.Stale:
		result.ObservedAt = backlogTime(freshness.LastObservedAt)
	}
	return result
}
