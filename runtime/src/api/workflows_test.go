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

type emptyWorkflowSource struct{}

func (emptyWorkflowSource) Load(context.Context) ([]workflowstore.Candidate, error) { return nil, nil }

func apiWorkflowDocument() string {
	return `{"apiVersion":"darkstar.local/v1alpha1","kind":"Workflow","metadata":{"name":"api-workflow","version":"1.0.0"},"spec":{"routeDefaults":{"entry":"finish","terminals":["finish"]},"nodes":{"finish":{"type":"reasoning","entry":true,"terminal":true,"inputs":{},"outputs":{},"reasoning":{"agent":"fake"},"checkpoint":{"mode":"none"},"transitions":[]}}}}`
}
