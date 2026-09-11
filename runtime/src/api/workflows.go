package api

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"path"
	"strings"

	"darkstar/src/core/workflow"
	"darkstar/src/ports/workflowstore"
)

const maxWorkflowRequestBytes = 2 << 20

type workflowCandidateRequest struct {
	Document        json.RawMessage     `json:"document"`
	SourceScope     workflowstore.Scope `json:"sourceScope"`
	SourceReference string              `json:"sourceReference"`
}

type workflowPreviewRequest struct {
	Range   workflow.RouteRequest `json:"range"`
	Context workflow.RouteContext `json:"context"`
}

type workflowDraftCreateRequest struct {
	Name           string                   `json:"name"`
	Scope          workflowstore.DraftScope `json:"scope"`
	ScopeReference string                   `json:"scopeReference"`
	Document       json.RawMessage          `json:"document"`
	Layout         json.RawMessage          `json:"layout,omitempty"`
}

type workflowDraftDuplicateRequest struct {
	Name           string                   `json:"name"`
	Version        string                   `json:"version,omitempty"`
	NewName        string                   `json:"newName"`
	Scope          workflowstore.DraftScope `json:"scope"`
	ScopeReference string                   `json:"scopeReference"`
}

type workflowDraftUpdateRequest struct {
	ID               string          `json:"id"`
	ExpectedRevision uint64          `json:"expectedRevision"`
	Document         json.RawMessage `json:"document,omitempty"`
	Layout           json.RawMessage `json:"layout,omitempty"`
}

type workflowDraftRevisionRequest struct {
	ID               string `json:"id"`
	ExpectedRevision uint64 `json:"expectedRevision"`
}

type workflowDraftRenameRequest struct {
	ID               string `json:"id"`
	Name             string `json:"name"`
	ExpectedRevision uint64 `json:"expectedRevision"`
}

type workflowDraftPublishRequest struct {
	ID               string `json:"id"`
	Version          string `json:"version"`
	ExpectedRevision uint64 `json:"expectedRevision"`
}

type workflowDraftPreviewRequest struct {
	ID               string                `json:"id"`
	ExpectedRevision uint64                `json:"expectedRevision"`
	Range            workflow.RouteRequest `json:"range"`
	Context          workflow.RouteContext `json:"context"`
}

type workflowArchiveRequest struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}
type nodeDefinitionDuplicateRequest struct {
	Source  workflow.ResolvedNodeDefinitionRef `json:"source"`
	Scope   workflow.NodeDefinitionScope       `json:"scope"`
	Owner   string                             `json:"owner"`
	Name    string                             `json:"name"`
	Version string                             `json:"version"`
}
type nodeDefinitionVersionRequest struct {
	Source  workflow.ResolvedNodeDefinitionRef `json:"source"`
	Version string                             `json:"version"`
}
type nodeDefinitionArchiveRequest struct {
	Ref workflow.ResolvedNodeDefinitionRef `json:"ref"`
}

func (s *Server) serveWorkflows(response http.ResponseWriter, request *http.Request, requestID string) {
	s.mu.RLock()
	service := s.workflows
	s.mu.RUnlock()
	if service == nil {
		writeAPIError(response, http.StatusServiceUnavailable, apiError{SchemaVersion: 1, Code: "WORKFLOW_SERVICE_UNAVAILABLE", Message: "Workflow operations are not configured.", RequestID: requestID, Retryable: true})
		return
	}
	clean := path.Clean(request.URL.Path)
	if clean == "/api/v1/workflows" {
		if request.Method != http.MethodGet && request.Method != http.MethodHead {
			writeWorkflowMethod(response, requestID, "GET, HEAD")
			return
		}
		if len(request.URL.Query()) > 1 || (len(request.URL.Query()) == 1 && !request.URL.Query().Has("name")) {
			writeWorkflowError(response, requestID, errors.New("only the optional name query is supported"))
			return
		}
		values, err := service.List(request.Context(), request.URL.Query().Get("name"))
		if err != nil {
			writeWorkflowError(response, requestID, err)
			return
		}
		writeJSON(response, http.StatusOK, values)
		return
	}
	action := strings.TrimPrefix(clean, "/api/v1/workflows/")
	switch action {
	case "content-usage":
		if request.Method != http.MethodGet && request.Method != http.MethodHead {
			writeWorkflowMethod(response, requestID, "GET, HEAD")
			return
		}
		contentID := request.URL.Query().Get("contentId")
		if contentID == "" {
			writeWorkflowError(response, requestID, errors.New("contentId is required"))
			return
		}
		usages, err := service.ContentUsages(request.Context(), contentID)
		if err != nil {
			writeWorkflowError(response, requestID, err)
			return
		}
		writeJSON(response, http.StatusOK, struct {
			Usages []workflow.ContentUsage `json:"usages"`
		}{usages})
	case "patterns/assessment-router":
		serveAssessmentRouterPattern(response, request, requestID)
	case "node-definitions":
		if request.Method != http.MethodGet && request.Method != http.MethodHead {
			writeWorkflowMethod(response, requestID, "GET, HEAD")
			return
		}
		query := request.URL.Query()
		filter := workflow.NodeDefinitionFilter{Query: query.Get("query")}
		if scope := query.Get("scope"); scope != "" {
			value := workflow.NodeDefinitionScope(scope)
			filter.Scope = &value
		}
		if lifecycle := query.Get("lifecycle"); lifecycle != "" {
			value := workflow.NodeDefinitionLifecycle(lifecycle)
			filter.Lifecycle = &value
		}
		values, err := service.NodeDefinitions(request.Context(), filter)
		if err != nil {
			writeWorkflowError(response, requestID, err)
			return
		}
		writeJSON(response, http.StatusOK, values)
	case "node-definitions/create":
		if request.Method != http.MethodPost {
			writeWorkflowMethod(response, requestID, "POST")
			return
		}
		var input workflow.NodeDefinitionCreateRequest
		if err := decodeWorkflowJSON(request, &input); err != nil {
			writeWorkflowError(response, requestID, err)
			return
		}
		value, err := service.CreateNodeDefinition(request.Context(), input)
		if err != nil {
			writeWorkflowError(response, requestID, err)
			return
		}
		writeJSON(response, http.StatusCreated, value)
	case "node-definitions/duplicate":
		if request.Method != http.MethodPost {
			writeWorkflowMethod(response, requestID, "POST")
			return
		}
		var input nodeDefinitionDuplicateRequest
		if err := decodeWorkflowJSON(request, &input); err != nil {
			writeWorkflowError(response, requestID, err)
			return
		}
		value, err := service.DuplicateNodeDefinition(request.Context(), input.Source, input.Scope, input.Owner, input.Name, input.Version)
		if err != nil {
			writeWorkflowError(response, requestID, err)
			return
		}
		writeJSON(response, http.StatusCreated, value)
	case "node-definitions/version":
		if request.Method != http.MethodPost {
			writeWorkflowMethod(response, requestID, "POST")
			return
		}
		var input nodeDefinitionVersionRequest
		if err := decodeWorkflowJSON(request, &input); err != nil {
			writeWorkflowError(response, requestID, err)
			return
		}
		value, err := service.VersionNodeDefinition(request.Context(), input.Source, input.Version)
		if err != nil {
			writeWorkflowError(response, requestID, err)
			return
		}
		writeJSON(response, http.StatusCreated, value)
	case "node-definitions/archive":
		if request.Method != http.MethodPost {
			writeWorkflowMethod(response, requestID, "POST")
			return
		}
		var input nodeDefinitionArchiveRequest
		if err := decodeWorkflowJSON(request, &input); err != nil {
			writeWorkflowError(response, requestID, err)
			return
		}
		value, err := service.ArchiveNodeDefinition(request.Context(), input.Ref)
		if err != nil {
			writeWorkflowError(response, requestID, err)
			return
		}
		writeJSON(response, http.StatusOK, value)
	case "archive":
		if request.Method != http.MethodPost {
			writeWorkflowMethod(response, requestID, "POST")
			return
		}
		var input workflowArchiveRequest
		if err := decodeWorkflowJSON(request, &input); err != nil {
			writeWorkflowError(response, requestID, err)
			return
		}
		value, err := service.ArchiveVersion(request.Context(), input.Name, input.Version)
		if err != nil {
			writeWorkflowError(response, requestID, err)
			return
		}
		writeJSON(response, http.StatusOK, value)
	case "library":
		if request.Method != http.MethodGet && request.Method != http.MethodHead {
			writeWorkflowMethod(response, requestID, "GET, HEAD")
			return
		}
		value, err := service.Library(request.Context())
		if err != nil {
			writeWorkflowError(response, requestID, err)
			return
		}
		writeJSON(response, http.StatusOK, value)
	case "authoring-catalog":
		if request.Method != http.MethodGet && request.Method != http.MethodHead {
			writeWorkflowMethod(response, requestID, "GET, HEAD")
			return
		}
		value, err := service.AuthoringCatalog(request.Context())
		if err != nil {
			writeWorkflowError(response, requestID, err)
			return
		}
		writeJSON(response, http.StatusOK, value)
	case "drafts/show":
		if request.Method != http.MethodGet && request.Method != http.MethodHead {
			writeWorkflowMethod(response, requestID, "GET, HEAD")
			return
		}
		id := request.URL.Query().Get("id")
		if id == "" || len(request.URL.Query()) != 1 {
			writeWorkflowError(response, requestID, errors.New("draft id is required"))
			return
		}
		value, err := service.Draft(request.Context(), id)
		if err != nil {
			writeWorkflowError(response, requestID, err)
			return
		}
		writeJSON(response, http.StatusOK, value)
	case "drafts/create":
		if request.Method != http.MethodPost {
			writeWorkflowMethod(response, requestID, "POST")
			return
		}
		var input workflowDraftCreateRequest
		if err := decodeWorkflowJSON(request, &input); err != nil {
			writeWorkflowError(response, requestID, err)
			return
		}
		value, err := service.CreateDraft(request.Context(), workflow.DraftCreateRequest{Name: input.Name, Scope: input.Scope,
			ScopeReference: input.ScopeReference, IdempotencyKey: request.Header.Get("Idempotency-Key"), Document: input.Document, Layout: input.Layout})
		if err != nil {
			writeWorkflowError(response, requestID, err)
			return
		}
		writeJSON(response, http.StatusCreated, value)
	case "drafts/duplicate":
		if request.Method != http.MethodPost {
			writeWorkflowMethod(response, requestID, "POST")
			return
		}
		var input workflowDraftDuplicateRequest
		if err := decodeWorkflowJSON(request, &input); err != nil {
			writeWorkflowError(response, requestID, err)
			return
		}
		value, err := service.DuplicateDraft(request.Context(), input.Name, input.Version, input.NewName, input.Scope,
			input.ScopeReference, request.Header.Get("Idempotency-Key"))
		if err != nil {
			writeWorkflowError(response, requestID, err)
			return
		}
		writeJSON(response, http.StatusCreated, value)
	case "drafts/update":
		if request.Method != http.MethodPost {
			writeWorkflowMethod(response, requestID, "POST")
			return
		}
		var input workflowDraftUpdateRequest
		if err := decodeWorkflowJSON(request, &input); err != nil {
			writeWorkflowError(response, requestID, err)
			return
		}
		value, err := service.UpdateDraft(request.Context(), workflow.DraftUpdateRequest{ID: input.ID, ExpectedRevision: input.ExpectedRevision, Document: input.Document, Layout: input.Layout})
		if err != nil {
			writeWorkflowError(response, requestID, err)
			return
		}
		writeJSON(response, http.StatusOK, value)
	case "drafts/rename":
		if request.Method != http.MethodPost {
			writeWorkflowMethod(response, requestID, "POST")
			return
		}
		var input workflowDraftRenameRequest
		if err := decodeWorkflowJSON(request, &input); err != nil {
			writeWorkflowError(response, requestID, err)
			return
		}
		value, err := service.RenameDraft(request.Context(), input.ID, input.Name, input.ExpectedRevision)
		if err != nil {
			writeWorkflowError(response, requestID, err)
			return
		}
		writeJSON(response, http.StatusOK, value)
	case "drafts/validate":
		if request.Method != http.MethodPost {
			writeWorkflowMethod(response, requestID, "POST")
			return
		}
		var input workflowDraftRevisionRequest
		if err := decodeWorkflowJSON(request, &input); err != nil {
			writeWorkflowError(response, requestID, err)
			return
		}
		value, err := service.ValidateDraft(request.Context(), input.ID, input.ExpectedRevision)
		if err != nil {
			writeWorkflowError(response, requestID, err)
			return
		}
		writeJSON(response, http.StatusOK, value)
	case "drafts/preview":
		if request.Method != http.MethodPost {
			writeWorkflowMethod(response, requestID, "POST")
			return
		}
		var input workflowDraftPreviewRequest
		if err := decodeWorkflowJSON(request, &input); err != nil {
			writeWorkflowError(response, requestID, err)
			return
		}
		value, issues, err := service.PreviewDraft(request.Context(), input.ID, input.ExpectedRevision, input.Range, input.Context)
		if err != nil {
			writeWorkflowError(response, requestID, err)
			return
		}
		if len(issues) != 0 {
			writeWorkflowValidation(response, requestID, issues)
			return
		}
		writeJSON(response, http.StatusOK, value)
	case "drafts/publish":
		if request.Method != http.MethodPost {
			writeWorkflowMethod(response, requestID, "POST")
			return
		}
		var input workflowDraftPublishRequest
		if err := decodeWorkflowJSON(request, &input); err != nil {
			writeWorkflowError(response, requestID, err)
			return
		}
		value, err := service.PublishDraft(request.Context(), workflow.DraftPublishRequest{ID: input.ID, Version: input.Version, ExpectedRevision: input.ExpectedRevision})
		if err != nil {
			writeWorkflowError(response, requestID, err)
			return
		}
		writeJSON(response, http.StatusCreated, value)
	case "drafts/discard":
		if request.Method != http.MethodPost {
			writeWorkflowMethod(response, requestID, "POST")
			return
		}
		var input workflowDraftRevisionRequest
		if err := decodeWorkflowJSON(request, &input); err != nil {
			writeWorkflowError(response, requestID, err)
			return
		}
		if err := service.DiscardDraft(request.Context(), input.ID, input.ExpectedRevision); err != nil {
			writeWorkflowError(response, requestID, err)
			return
		}
		writeJSON(response, http.StatusOK, map[string]any{"id": input.ID, "discarded": true})
	case "validate", "install":
		if request.Method != http.MethodPost {
			writeWorkflowMethod(response, requestID, "POST")
			return
		}
		var input workflowCandidateRequest
		if err := decodeWorkflowJSON(request, &input); err != nil {
			writeWorkflowError(response, requestID, err)
			return
		}
		candidate := workflowstore.Candidate{Scope: input.SourceScope, Reference: input.SourceReference, Content: input.Document}
		if action == "validate" {
			writeJSON(response, http.StatusOK, service.ValidateCandidate(request.Context(), candidate))
			return
		}
		result, err := service.Install(request.Context(), candidate)
		if err != nil {
			writeWorkflowError(response, requestID, err)
			return
		}
		writeJSON(response, http.StatusCreated, result)
	case "show", "graph", "preview":
		name, version, err := workflowIdentityQuery(request)
		if err != nil {
			writeWorkflowError(response, requestID, err)
			return
		}
		if action == "preview" {
			if request.Method != http.MethodPost {
				writeWorkflowMethod(response, requestID, "POST")
				return
			}
			var input workflowPreviewRequest
			if err := decodeWorkflowJSON(request, &input); err != nil {
				writeWorkflowError(response, requestID, err)
				return
			}
			value, issues, err := service.Preview(request.Context(), name, version, input.Range, input.Context)
			if err != nil {
				writeWorkflowError(response, requestID, err)
				return
			}
			if len(issues) != 0 {
				writeWorkflowValidation(response, requestID, issues)
				return
			}
			writeJSON(response, http.StatusOK, value)
			return
		}
		if request.Method != http.MethodGet && request.Method != http.MethodHead {
			writeWorkflowMethod(response, requestID, "GET, HEAD")
			return
		}
		if action == "show" {
			value, err := service.Definition(request.Context(), name, version)
			if err != nil {
				writeWorkflowError(response, requestID, err)
				return
			}
			writeJSON(response, http.StatusOK, value)
			return
		}
		value, err := service.Graph(request.Context(), name, version)
		if err != nil {
			writeWorkflowError(response, requestID, err)
			return
		}
		writeJSON(response, http.StatusOK, value)
	default:
		writeWorkflowError(response, requestID, workflowstore.ErrNotFound)
	}
}

func workflowIdentityQuery(request *http.Request) (string, string, error) {
	query := request.URL.Query()
	for key, values := range query {
		if (key != "name" && key != "version") || len(values) != 1 {
			return "", "", errors.New("workflow query supports one name and optional version")
		}
	}
	if strings.TrimSpace(query.Get("name")) == "" {
		return "", "", errors.New("workflow name is required")
	}
	return query.Get("name"), query.Get("version"), nil
}

func decodeWorkflowJSON(request *http.Request, destination any) error {
	decoder := json.NewDecoder(io.LimitReader(request.Body, maxWorkflowRequestBytes+1))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil || decoder.Decode(new(any)) != io.EOF {
		return errors.New("request must contain one valid workflow JSON object")
	}
	return nil
}

func writeWorkflowMethod(response http.ResponseWriter, requestID, allow string) {
	response.Header().Set("Allow", allow)
	writeAPIError(response, http.StatusMethodNotAllowed, apiError{SchemaVersion: 1, Code: "METHOD_NOT_ALLOWED", Message: "The HTTP method is not supported for this resource.", RequestID: requestID})
}

func writeWorkflowValidation(response http.ResponseWriter, requestID string, issues workflow.ValidationErrors) {
	details := make([]errorDetail, len(issues))
	for index, issue := range issues {
		details[index] = errorDetail{Field: issue.Location, Code: string(issue.Code), Message: issue.Message}
	}
	writeAPIError(response, http.StatusUnprocessableEntity, apiError{SchemaVersion: 1, Code: "VALIDATION_FAILED", Message: issues.Error(), RequestID: requestID, Details: details})
}

func writeWorkflowError(response http.ResponseWriter, requestID string, err error) {
	if errors.Is(err, workflowstore.ErrNotFound) {
		writeAPIError(response, http.StatusNotFound, apiError{SchemaVersion: 1, Code: "NOT_FOUND", Message: "The requested workflow was not found.", RequestID: requestID})
		return
	}
	if errors.Is(err, workflowstore.ErrVersionConflict) || errors.Is(err, workflowstore.ErrDraftConflict) || errors.Is(err, workflowstore.ErrBuiltInImmutable) || errors.Is(err, workflowstore.ErrNodeDefinitionConflict) || errors.Is(err, workflow.ErrNodeDefinitionImmutable) || errors.Is(err, workflow.ErrNodeDefinitionVersionConflict) {
		writeAPIError(response, http.StatusConflict, apiError{SchemaVersion: 1, Code: "CONFLICT", Message: err.Error(), RequestID: requestID})
		return
	}
	var issues workflow.ValidationErrors
	if errors.As(err, &issues) {
		writeWorkflowValidation(response, requestID, issues)
		return
	}
	writeAPIError(response, http.StatusBadRequest, apiError{SchemaVersion: 1, Code: "VALIDATION_FAILED", Message: err.Error(), RequestID: requestID})
}
