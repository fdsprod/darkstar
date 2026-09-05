package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"path/filepath"
	"testing"
	"time"

	"darkstar/src/adapters/statestore/sqlite"
	"darkstar/src/core/workflow"
	"darkstar/src/ports/workflowstore"
)

func TestWorkflowAPICoversInstallListShowGraphAndPreview(t *testing.T) {
	ctx := context.Background()
	database, err := sqlite.Open(ctx, filepath.Join(t.TempDir(), "workflow-api.db"), sqlite.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	catalog, err := workflow.NewCatalog(emptyWorkflowSource{}, database)
	if err != nil {
		t.Fatal(err)
	}
	server, err := NewServer(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := server.SetWorkflows(catalog); err != nil {
		t.Fatal(err)
	}
	if err := server.Start(ctx, 1234, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	defer closeTestServer(t, server)
	endpoint, _ := server.Endpoint()

	document := json.RawMessage(apiWorkflowDocument())
	input, _ := json.Marshal(workflowCandidateRequest{Document: document, SourceScope: workflowstore.ScopeProject, SourceReference: "delivery.json"})
	install := workflowRequest(t, endpoint, http.MethodPost, "/api/v1/workflows/install", input)
	if install.StatusCode != http.StatusCreated {
		t.Fatalf("install status = %d", install.StatusCode)
	}
	drainWorkflowResponse(t, install)

	list := workflowRequest(t, endpoint, http.MethodGet, "/api/v1/workflows", nil)
	var summaries []workflow.VersionSummary
	decodeJSON(t, list, &summaries)
	_ = list.Body.Close()
	if len(summaries) != 1 || summaries[0].Name != "api-workflow" {
		t.Fatalf("list = %#v", summaries)
	}

	query := "?name=" + url.QueryEscape("api-workflow") + "&version=1.0.0"
	for _, action := range []string{"show", "graph"} {
		response := workflowRequest(t, endpoint, http.MethodGet, "/api/v1/workflows/"+action+query, nil)
		if response.StatusCode != http.StatusOK {
			t.Fatalf("%s status = %d", action, response.StatusCode)
		}
		drainWorkflowResponse(t, response)
	}
	previewBody := []byte(`{"range":{},"context":{}}`)
	preview := workflowRequest(t, endpoint, http.MethodPost, "/api/v1/workflows/preview"+query, previewBody)
	var route workflow.RoutePreview
	decodeJSON(t, preview, &route)
	_ = preview.Body.Close()
	if route.Route.Entry != "finish" || len(route.Route.Nodes) != 1 {
		t.Fatalf("preview = %#v", route)
	}
}

func TestWorkflowDraftAuthoringUsesCASAndPublishesImmutableVersion(t *testing.T) {
	ctx := context.Background()
	database, err := sqlite.Open(ctx, filepath.Join(t.TempDir(), "workflow-drafts.db"), sqlite.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	catalog, err := workflow.NewCatalog(emptyWorkflowSource{}, database)
	if err != nil {
		t.Fatal(err)
	}
	server, err := NewServer(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := server.SetWorkflows(catalog); err != nil {
		t.Fatal(err)
	}
	if err := server.Start(ctx, 1234, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	defer closeTestServer(t, server)
	endpoint, _ := server.Endpoint()

	createBody, _ := json.Marshal(workflowDraftCreateRequest{Name: "api-workflow", Scope: workflowstore.DraftScopeProject,
		ScopeReference: "project-test", Document: json.RawMessage(apiWorkflowDocument()), Layout: json.RawMessage(`{"finish":{"x":10,"y":20}}`)})
	createdResponse := workflowRequestWithKey(t, endpoint, "/api/v1/workflows/drafts/create", createBody, "draft-create-one")
	if createdResponse.StatusCode != http.StatusCreated {
		t.Fatalf("create status = %d", createdResponse.StatusCode)
	}
	var created workflowstore.Draft
	decodeJSON(t, createdResponse, &created)
	_ = createdResponse.Body.Close()
	if created.Revision != 1 || created.ID == "" {
		t.Fatalf("created draft = %#v", created)
	}

	updateBody, _ := json.Marshal(workflowDraftUpdateRequest{ID: created.ID, ExpectedRevision: 1, Layout: json.RawMessage(`{"finish":{"x":30,"y":40}}`)})
	updatedResponse := workflowRequest(t, endpoint, http.MethodPost, "/api/v1/workflows/drafts/update", updateBody)
	var updated workflowstore.Draft
	decodeJSON(t, updatedResponse, &updated)
	_ = updatedResponse.Body.Close()
	if updated.Revision != 2 || updated.DocumentDigest != created.DocumentDigest {
		t.Fatalf("layout update changed semantics: %#v", updated)
	}

	stale := workflowRequest(t, endpoint, http.MethodPost, "/api/v1/workflows/drafts/update", updateBody)
	if stale.StatusCode != http.StatusConflict {
		t.Fatalf("stale update status = %d", stale.StatusCode)
	}
	drainWorkflowResponse(t, stale)

	validateBody, _ := json.Marshal(workflowDraftRevisionRequest{ID: created.ID, ExpectedRevision: 2})
	validated := workflowRequest(t, endpoint, http.MethodPost, "/api/v1/workflows/drafts/validate", validateBody)
	var report workflow.DraftValidationReport
	decodeJSON(t, validated, &report)
	_ = validated.Body.Close()
	if len(report.Findings) != 0 || report.Revision != 2 {
		t.Fatalf("validation = %#v", report)
	}

	publishBody, _ := json.Marshal(workflowDraftPublishRequest{ID: created.ID, ExpectedRevision: 2, Version: "1.1.0"})
	publishedResponse := workflowRequest(t, endpoint, http.MethodPost, "/api/v1/workflows/drafts/publish", publishBody)
	if publishedResponse.StatusCode != http.StatusCreated {
		t.Fatalf("publish status = %d", publishedResponse.StatusCode)
	}
	var published workflow.DraftPublishResult
	decodeJSON(t, publishedResponse, &published)
	_ = publishedResponse.Body.Close()
	if published.Published.Version != "1.1.0" || published.DraftRevision != 2 {
		t.Fatalf("publish = %#v", published)
	}
	archiveBody, _ := json.Marshal(workflowArchiveRequest{Name: "api-workflow", Version: "1.1.0"})
	archived := workflowRequest(t, endpoint, http.MethodPost, "/api/v1/workflows/archive", archiveBody)
	if archived.StatusCode != http.StatusOK {
		t.Fatalf("archive status = %d", archived.StatusCode)
	}
	drainWorkflowResponse(t, archived)

	libraryResponse := workflowRequest(t, endpoint, http.MethodGet, "/api/v1/workflows/library", nil)
	var library workflow.Library
	decodeJSON(t, libraryResponse, &library)
	_ = libraryResponse.Body.Close()
	if len(library.Drafts) != 1 || len(library.Versions) != 1 || len(library.Archives) != 1 {
		t.Fatalf("library = %#v", library)
	}
	var auditCount int
	if err := database.SQL().QueryRowContext(ctx, `SELECT count(*) FROM workflow_authoring_events`).Scan(&auditCount); err != nil { t.Fatal(err) }
	if auditCount < 4 { t.Fatalf("workflow authoring audit event count = %d", auditCount) }
}

func drainWorkflowResponse(t *testing.T, response *http.Response) {
	t.Helper()
	if _, err := io.Copy(io.Discard, response.Body); err != nil {
		t.Fatal(err)
	}
	if err := response.Body.Close(); err != nil {
		t.Fatal(err)
	}
}

func workflowRequest(t *testing.T, endpoint Endpoint, method, resource string, body []byte) *http.Response {
	t.Helper()
	request, err := http.NewRequest(method, endpoint.BaseURL()+resource, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", endpoint.AuthorizationHeader())
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	return response
}

func workflowRequestWithKey(t *testing.T, endpoint Endpoint, resource string, body []byte, key string) *http.Response {
	t.Helper()
	request, err := http.NewRequest(http.MethodPost, endpoint.BaseURL()+resource, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", endpoint.AuthorizationHeader())
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Idempotency-Key", key)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	return response
}

type emptyWorkflowSource struct{}

func (emptyWorkflowSource) Load(context.Context) ([]workflowstore.Candidate, error) { return nil, nil }

func apiWorkflowDocument() string {
	return `{"apiVersion":"darkstar.local/v1alpha1","kind":"Workflow","metadata":{"name":"api-workflow","version":"1.0.0"},"spec":{"routeDefaults":{"entry":"finish","terminals":["finish"]},"nodes":{"finish":{"type":"reasoning","entry":true,"terminal":true,"inputs":{},"outputs":{},"reasoning":{"agent":"fake"},"checkpoint":{"mode":"none"},"transitions":[]}}}}`
}
