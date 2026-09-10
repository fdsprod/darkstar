package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"darkstar/src/adapters/statestore/sqlite"
	"darkstar/src/core/workflow"
	registryport "darkstar/src/ports/capabilityregistry"
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

func TestWorkflowSubworkflowResolutionPinsExactChildrenAndRejectsUnsafeMappings(t *testing.T) {
	ctx := context.Background()
	database, err := sqlite.Open(ctx, filepath.Join(t.TempDir(), "workflow-children.db"), sqlite.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	catalog, err := workflow.NewCatalog(emptyWorkflowSource{}, database)
	if err != nil {
		t.Fatal(err)
	}
	child, err := catalog.Install(ctx, workflowstore.Candidate{Scope: workflowstore.ScopeProject, Reference: "child.json", Content: json.RawMessage(childWorkflowDocument())})
	if err != nil {
		t.Fatal(err)
	}
	parent := parentWorkflowDocument(child.Version.Digest)
	installed, err := catalog.Install(ctx, workflowstore.Candidate{Scope: workflowstore.ScopeProject, Reference: "parent.json", Content: json.RawMessage(parent)})
	if err != nil {
		t.Fatalf("install resolved parent: %v", err)
	}
	definition, err := catalog.Definition(ctx, installed.Version.Name, installed.Version.Version)
	if err != nil {
		t.Fatal(err)
	}
	call := definition.Document.Spec.Nodes["call"].(workflow.SubworkflowNode)
	if call.Call.Workflow.Digest != child.Version.Digest {
		t.Fatalf("child digest was not pinned: %#v", call.Call.Workflow)
	}

	tests := []struct {
		name     string
		document string
		message  string
	}{
		{"digest mismatch", parentWorkflowDocument("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"), "does not match installed digest"},
		{"missing required child input", replaceParentCall(parent, `"inputs":{"request":"request"}`, `"inputs":{}`), "has no parent mapping"},
		{"absent parent input", replaceParentCall(parent, `"inputs":{"request":"request"}`, `"inputs":{"request":"missing"}`), "maps unknown parent input"},
		{"absent parent output", replaceParentCall(parent, `"outputs":{"result":{"type":"object"}},"call"`, `"outputs":{},"call"`), "maps unknown parent output"},
		{"invalid child entry", replaceParentCall(parent, `"entry":"finish"`, `"entry":"missing"`), "entry is absent or not entry-capable"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := catalog.Install(ctx, workflowstore.Candidate{Scope: workflowstore.ScopeProject, Reference: test.name + ".json", Content: json.RawMessage(test.document)})
			if err == nil || !containsWorkflowIssue(err, test.message) {
				t.Fatalf("install error = %v, want issue containing %q", err, test.message)
			}
		})
	}
	resolvedValidation := catalog.ValidateCandidate(ctx, workflowstore.Candidate{Scope: workflowstore.ScopeProject, Reference: "missing-child.json", Content: json.RawMessage(parentWorkflowDocumentFor("unresolved-parent", "missing-child", ""))})
	if len(resolvedValidation.Issues) == 0 || !strings.Contains(resolvedValidation.Issues[0].Message, "not installed") {
		t.Fatalf("candidate validation did not resolve child references: %#v", resolvedValidation)
	}

	installUncheckedWorkflow(t, ctx, database, cyclicWorkflowDocument("cycle-a", "cycle-b"))
	installUncheckedWorkflow(t, ctx, database, cyclicWorkflowDocument("cycle-b", "cycle-a"))
	cycleParent := parentWorkflowDocumentFor("cycle-parent", "cycle-a", "")
	if _, err := catalog.Install(ctx, workflowstore.Candidate{Scope: workflowstore.ScopeProject, Reference: "cycle-parent.json", Content: json.RawMessage(cycleParent)}); err == nil || !containsWorkflowIssue(err, "recursive sub-workflow call graph") {
		t.Fatalf("cycle install error = %v", err)
	}
	grandchildDocument := strings.Replace(childWorkflowDocument(), `"name":"child"`, `"name":"grandchild"`, 1)
	if _, err := catalog.Install(ctx, workflowstore.Candidate{Scope: workflowstore.ScopeProject, Reference: "grandchild.json", Content: json.RawMessage(grandchildDocument)}); err != nil {
		t.Fatal(err)
	}
	installUncheckedWorkflow(t, ctx, database, transitiveMissingMappingDocument())
	if _, err := catalog.Install(ctx, workflowstore.Candidate{Scope: workflowstore.ScopeProject, Reference: "transitive-parent.json", Content: json.RawMessage(cyclicWorkflowDocument("transitive-parent", "intermediate"))}); err == nil || !containsWorkflowIssue(err, "has no parent mapping") {
		t.Fatalf("transitive contract error = %v", err)
	}
}

func TestWorkflowValidationResolvesExactReusableDefinitionBeforeInstall(t *testing.T) {
	ctx := context.Background()
	database, err := sqlite.Open(ctx, filepath.Join(t.TempDir(), "node-definitions.db"), sqlite.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	builtin := workflow.NodeDefinition{Ref: workflow.ResolvedNodeDefinitionRef{Ref: workflow.BuiltInNodeDefinitionRef{Name: "route/assessment", Version: "1.0.0"}}, DisplayName: "Route assessment", Description: "Select route", Compatibility: workflow.APIVersionV1Alpha3, Inputs: map[workflow.Identifier]workflow.ValueDeclaration{}, Outputs: map[workflow.Identifier]workflow.OutputDeclaration{"selected_route": {Type: workflow.ValueString}, "rationale": {Type: workflow.ValueString}, "advice": {Type: workflow.ValueString}, "missing_information": {Type: workflow.ValueArray}, "assumptions": {Type: workflow.ValueArray}, "confirmation_required": {Type: workflow.ValueBoolean}}, ConfigurationSchema: json.RawMessage(`{"type":"object"}`), Implementation: workflow.NodeImplementationRouting, RequiredCapabilities: []workflow.CapabilityReference{}, Lifecycle: workflow.NodeDefinitionActive, CreatedAt: time.Now().UTC()}
	library, err := workflow.NewNodeDefinitionLibrary(builtin)
	if err != nil {
		t.Fatal(err)
	}
	exact := library.Search(workflow.NodeDefinitionFilter{})[0].Ref
	catalog, err := workflow.NewCatalog(emptyWorkflowSource{}, database)
	if err != nil {
		t.Fatal(err)
	}
	catalog.WithNodeDefinitionLibrary(library)
	document := strings.Replace(routingWorkflowDocument(), `"definition":null`, fmt.Sprintf(`"definition":{"scope":"built_in","name":"route/assessment","version":"1.0.0","digest":%q}`, exact.Digest), 1)
	if _, err := catalog.Install(ctx, workflowstore.Candidate{Scope: workflowstore.ScopeProject, Reference: "router.json", Content: json.RawMessage(document)}); err != nil {
		t.Fatalf("install exact reusable node: %v", err)
	}
	wrong := strings.Replace(document, exact.Digest, strings.Repeat("f", 64), 1)
	if _, err := catalog.Install(ctx, workflowstore.Candidate{Scope: workflowstore.ScopeProject, Reference: "wrong.json", Content: json.RawMessage(wrong)}); err == nil || !containsWorkflowIssue(err, "exact reusable node definition is unavailable") {
		t.Fatalf("wrong digest error = %v", err)
	}
}

func TestNodeDefinitionAPIMutationsPersistExactVersionsAndProtectBuiltIns(t *testing.T) {
	ctx := context.Background()
	database, err := sqlite.Open(ctx, filepath.Join(t.TempDir(), "node-definition-api.db"), sqlite.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	builtin := workflow.NodeDefinition{Ref: workflow.ResolvedNodeDefinitionRef{Ref: workflow.BuiltInNodeDefinitionRef{Name: "route/assessment", Version: "1.0.0"}}, DisplayName: "Route assessment", Compatibility: workflow.APIVersionV1Alpha3, Inputs: map[workflow.Identifier]workflow.ValueDeclaration{}, Outputs: map[workflow.Identifier]workflow.OutputDeclaration{}, ConfigurationSchema: json.RawMessage(`{"type":"object"}`), Implementation: workflow.NodeImplementationRouting, RequiredCapabilities: []workflow.CapabilityReference{}, Lifecycle: workflow.NodeDefinitionActive, CreatedAt: time.Now().UTC()}
	library, err := workflow.NewNodeDefinitionLibrary(builtin)
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := workflow.NewCatalog(emptyWorkflowSource{}, database)
	if err != nil {
		t.Fatal(err)
	}
	catalog.WithNodeDefinitionLibrary(library)
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

	create, _ := json.Marshal(workflow.NodeDefinitionCreateRequest{Scope: workflow.NodeDefinitionUser, Owner: "user-1", Name: "nodes/check", Version: "1.0.0", DisplayName: "Check", Description: "Reusable check", Inputs: map[workflow.Identifier]workflow.ValueDeclaration{}, Outputs: map[workflow.Identifier]workflow.OutputDeclaration{}, ConfigurationSchema: json.RawMessage(`{"type":"object"}`), Implementation: workflow.NodeImplementationExecutor, RequiredCapabilities: []workflow.CapabilityReference{}})
	createdResponse := workflowRequest(t, endpoint, http.MethodPost, "/api/v1/workflows/node-definitions/create", create)
	if createdResponse.StatusCode != http.StatusCreated {
		t.Fatalf("create status = %d", createdResponse.StatusCode)
	}
	var created workflow.NodeDefinition
	decodeJSON(t, createdResponse, &created)
	_ = createdResponse.Body.Close()
	if created.Ref.Digest == "" || created.CreatedAt.IsZero() {
		t.Fatalf("server did not seal definition: %#v", created)
	}

	versionBody, _ := json.Marshal(map[string]any{"source": created.Ref, "version": "1.1.0"})
	versionResponse := workflowRequest(t, endpoint, http.MethodPost, "/api/v1/workflows/node-definitions/version", versionBody)
	if versionResponse.StatusCode != http.StatusCreated {
		t.Fatalf("version status = %d", versionResponse.StatusCode)
	}
	var versioned workflow.NodeDefinition
	decodeJSON(t, versionResponse, &versioned)
	_ = versionResponse.Body.Close()
	if versioned.Ref.Ref.DefinitionVersion() != "1.1.0" || versioned.DerivedFrom == nil || versioned.DerivedFrom.Digest != created.Ref.Digest {
		t.Fatalf("version provenance = %#v", versioned)
	}

	archiveBody, _ := json.Marshal(map[string]any{"ref": created.Ref})
	archiveResponse := workflowRequest(t, endpoint, http.MethodPost, "/api/v1/workflows/node-definitions/archive", archiveBody)
	if archiveResponse.StatusCode != http.StatusOK {
		t.Fatalf("archive status = %d", archiveResponse.StatusCode)
	}
	drainWorkflowResponse(t, archiveResponse)
	listResponse := workflowRequest(t, endpoint, http.MethodGet, "/api/v1/workflows/node-definitions?scope=user&lifecycle=archived", nil)
	var archived []workflow.NodeDefinition
	decodeJSON(t, listResponse, &archived)
	_ = listResponse.Body.Close()
	if len(archived) != 1 || archived[0].Ref.Digest != created.Ref.Digest {
		t.Fatalf("archived exact version = %#v", archived)
	}

	builtinRef := library.Search(workflow.NodeDefinitionFilter{})[0].Ref
	builtinBody, _ := json.Marshal(map[string]any{"ref": builtinRef})
	builtinResponse := workflowRequest(t, endpoint, http.MethodPost, "/api/v1/workflows/node-definitions/archive", builtinBody)
	if builtinResponse.StatusCode != http.StatusConflict {
		t.Fatalf("built-in archive status = %d", builtinResponse.StatusCode)
	}
	drainWorkflowResponse(t, builtinResponse)
}

func routingWorkflowDocument() string {
	return `{"apiVersion":"darkstar.local/v1alpha3","kind":"Workflow","metadata":{"name":"routing-workflow","version":"1.0.0"},"spec":{"routeDefaults":{"entry":"route","terminals":["assessment"]},"nodes":{"route":{"displayName":"Route","definition":null,"type":"routing","entry":true,"terminal":false,"inputs":{},"outputs":{"selected_route":{"type":"string"},"rationale":{"type":"string"},"advice":{"type":"string"},"missing_information":{"type":"array"},"assumptions":{"type":"array"},"confirmation_required":{"type":"boolean"}},"routing":{"agent":"router","branches":[{"name":"assessment","transition":"to_assessment"}],"routeOutput":"selected_route","rationaleOutput":"rationale","adviceOutput":"advice","missingInformationOutput":"missing_information","assumptionsOutput":"assumptions","confirmationOutput":"confirmation_required"},"checkpoint":{"mode":"none"},"transitions":[{"id":"to_assessment","to":"assessment"}]},"assessment":{"type":"command","entry":false,"terminal":true,"inputs":{},"outputs":{},"command":{"argv":["assess"]},"checkpoint":{"mode":"none"},"transitions":[]}}}}`
}

func containsWorkflowIssue(err error, message string) bool {
	var issues workflow.ValidationErrors
	if !errors.As(err, &issues) {
		return false
	}
	for _, issue := range issues {
		if strings.Contains(issue.Message, message) {
			return true
		}
	}
	return false
}

func installUncheckedWorkflow(t *testing.T, ctx context.Context, database *sqlite.Database, document string) {
	t.Helper()
	parsed, canonical, digest, err := workflow.Canonicalize([]byte(document))
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = database.Install(ctx, workflowstore.InstallRequest{Name: parsed.Metadata.Name, Version: parsed.Metadata.Version, Digest: digest, Document: canonical, SourceScope: workflowstore.ScopeProject, SourceRef: parsed.Metadata.Name + ".json", InstalledAt: time.Now().UTC()})
	if err != nil {
		t.Fatal(err)
	}
}

func replaceParentCall(document, old, replacement string) string {
	return strings.Replace(document, old, replacement, 1)
}

func childWorkflowDocument() string {
	return `{"apiVersion":"darkstar.local/v1alpha1","kind":"Workflow","metadata":{"name":"child","version":"1.0.0"},"spec":{"inputs":{"request":{"type":"object"}},"routeDefaults":{"entry":"finish","terminals":["finish"]},"nodes":{"finish":{"type":"reasoning","entry":true,"terminal":true,"inputs":{"request":{"from":"run.input.request","type":"object","required":true}},"outputs":{"result":{"type":"object"}},"reasoning":{"agent":"fake"},"checkpoint":{"mode":"none"},"transitions":[]}}}}`
}

func parentWorkflowDocument(digest string) string {
	return parentWorkflowDocumentFor("parent", "child", digest)
}

func parentWorkflowDocumentFor(name, child, digest string) string {
	digestField := ""
	if digest != "" {
		digestField = fmt.Sprintf(`,"digest":%q`, digest)
	}
	return fmt.Sprintf(`{"apiVersion":"darkstar.local/v1alpha1","kind":"Workflow","metadata":{"name":%q,"version":"1.0.0"},"spec":{"inputs":{"request":{"type":"object"}},"routeDefaults":{"entry":"call","terminals":["call"]},"nodes":{"call":{"type":"subworkflow","entry":true,"terminal":true,"inputs":{"request":{"from":"run.input.request","type":"object","required":true}},"outputs":{"result":{"type":"object"}},"call":{"workflow":{"name":%q,"version":"1.0.0"%s},"entry":"finish","terminals":["finish"],"inputs":{"request":"request"},"outputs":{"result":"node.finish.output.result"}},"checkpoint":{"mode":"none"},"transitions":[]}}}}`, name, child, digestField)
}

func cyclicWorkflowDocument(name, child string) string {
	return fmt.Sprintf(`{"apiVersion":"darkstar.local/v1alpha1","kind":"Workflow","metadata":{"name":%q,"version":"1.0.0"},"spec":{"routeDefaults":{"entry":"call","terminals":["call"]},"nodes":{"call":{"type":"subworkflow","entry":true,"terminal":true,"inputs":{},"outputs":{},"call":{"workflow":{"name":%q,"version":"1.0.0"},"entry":"call","terminals":["call"],"inputs":{},"outputs":{}},"checkpoint":{"mode":"none"},"transitions":[]}}}}`, name, child)
}

func transitiveMissingMappingDocument() string {
	return `{"apiVersion":"darkstar.local/v1alpha1","kind":"Workflow","metadata":{"name":"intermediate","version":"1.0.0"},"spec":{"routeDefaults":{"entry":"call","terminals":["call"]},"nodes":{"call":{"type":"subworkflow","entry":true,"terminal":true,"inputs":{},"outputs":{},"call":{"workflow":{"name":"grandchild","version":"1.0.0"},"entry":"finish","terminals":["finish"],"inputs":{},"outputs":{}},"checkpoint":{"mode":"none"},"transitions":[]}}}}`
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
	if _, _, err := database.RegisterCapability(ctx, registryport.Record{SchemaVersion: 1, ID: "cap_review", Name: "project:review", Kind: registryport.KindSkill, Class: registryport.ClassRegistered, DeclaredVersion: "1.0.0", Fingerprint: strings.Repeat("a", 64), Source: registryport.Source{Type: "test", Locator: "must-not-leak"}, Availability: registryport.AvailabilityAvailable, ObservedAt: time.Now().UTC()}, "catalog-review"); err != nil {
		t.Fatal(err)
	}
	catalog.WithCapabilityRegistry(database)
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
	if report.DocumentDigest != updated.DocumentDigest || len(report.Digest) != 64 {
		t.Fatalf("validation evidence is not bound to the persisted document: %#v", report)
	}

	previewDraft := workflowRequest(t, endpoint, http.MethodPost, "/api/v1/workflows/drafts/preview", validateBody)
	var draftPreview workflow.DraftPreview
	decodeJSON(t, previewDraft, &draftPreview)
	_ = previewDraft.Body.Close()
	if draftPreview.DraftID != created.ID || draftPreview.Revision != 2 || draftPreview.DocumentDigest != updated.DocumentDigest || draftPreview.Route.Entry != "finish" {
		t.Fatalf("draft preview = %#v", draftPreview)
	}

	authoringCatalogResponse := workflowRequest(t, endpoint, http.MethodGet, "/api/v1/workflows/authoring-catalog", nil)
	var authoringCatalog workflow.AuthoringCatalog
	decodeJSON(t, authoringCatalogResponse, &authoringCatalog)
	_ = authoringCatalogResponse.Body.Close()
	if authoringCatalog.SchemaVersion != 1 || len(authoringCatalog.NodeTypes) != 10 || authoringCatalog.Workflows.Status != workflow.ReferenceKnown || len(authoringCatalog.Workflows.Items) != 0 || authoringCatalog.Agents.Status != workflow.ReferenceUnavailable || authoringCatalog.Skills.Status != workflow.ReferenceKnown || len(authoringCatalog.Skills.Items) != 1 || authoringCatalog.Skills.Items[0].Name != "project:review" {
		t.Fatalf("authoring catalog = %#v", authoringCatalog)
	}

	publishBody, _ := json.Marshal(workflowDraftPublishRequest{ID: created.ID, ExpectedRevision: 2, Version: "1.1.0"})
	publishedResponse := workflowRequest(t, endpoint, http.MethodPost, "/api/v1/workflows/drafts/publish", publishBody)
	if publishedResponse.StatusCode != http.StatusCreated {
		t.Fatalf("publish status = %d", publishedResponse.StatusCode)
	}
	var published workflow.DraftPublishResult
	decodeJSON(t, publishedResponse, &published)
	_ = publishedResponse.Body.Close()
	if published.Published.Version != "1.1.0" || published.DraftRevision != 2 || published.SourceValidationDigest != report.Digest {
		t.Fatalf("publish = %#v", published)
	}
	archiveBody, _ := json.Marshal(workflowArchiveRequest{Name: "api-workflow", Version: "1.1.0"})
	archived := workflowRequest(t, endpoint, http.MethodPost, "/api/v1/workflows/archive", archiveBody)
	if archived.StatusCode != http.StatusOK {
		t.Fatalf("archive status = %d", archived.StatusCode)
	}
	drainWorkflowResponse(t, archived)
	activeCatalogResponse := workflowRequest(t, endpoint, http.MethodGet, "/api/v1/workflows/authoring-catalog", nil)
	var activeCatalog workflow.AuthoringCatalog
	decodeJSON(t, activeCatalogResponse, &activeCatalog)
	_ = activeCatalogResponse.Body.Close()
	if activeCatalog.Workflows.Status != workflow.ReferenceKnown || len(activeCatalog.Workflows.Items) != 0 {
		t.Fatalf("archived workflow was advertised as authorable: %#v", activeCatalog.Workflows)
	}

	libraryResponse := workflowRequest(t, endpoint, http.MethodGet, "/api/v1/workflows/library", nil)
	var library workflow.Library
	decodeJSON(t, libraryResponse, &library)
	_ = libraryResponse.Body.Close()
	if len(library.Drafts) != 1 || len(library.Versions) != 1 || len(library.Archives) != 1 {
		t.Fatalf("library = %#v", library)
	}
	var auditCount int
	if err := database.SQL().QueryRowContext(ctx, `SELECT count(*) FROM workflow_authoring_events`).Scan(&auditCount); err != nil {
		t.Fatal(err)
	}
	if auditCount < 4 {
		t.Fatalf("workflow authoring audit event count = %d", auditCount)
	}
	invalidDocument := strings.Replace(apiWorkflowDocument(), `"transitions":[]`, `"transitions":[{"id":"go","to":"missing"}]`, 1)
	invalidDraft, err := catalog.CreateDraft(ctx, workflow.DraftCreateRequest{Name: "invalid-edge", Scope: workflowstore.DraftScopeProject, ScopeReference: "project-test", IdempotencyKey: "invalid-edge-draft", Document: json.RawMessage(invalidDocument), Layout: json.RawMessage(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	invalidReport, err := catalog.ValidateDraft(ctx, invalidDraft.ID, invalidDraft.Revision)
	if err != nil {
		t.Fatal(err)
	}
	foundEdge := false
	for _, finding := range invalidReport.Findings {
		if finding.EdgeID == "go" && strings.HasPrefix(finding.Field, "transitions.0") {
			foundEdge = true
		}
	}
	if !foundEdge {
		t.Fatalf("transition finding was not bound to the actual transition id: %#v", invalidReport.Findings)
	}
	malformedDocument := strings.Replace(invalidDocument, `{"id":"go","to":"missing"}`, `{"id":"go","to":"missing","kind":"bounded","maxTraversals":"bad"}`, 1)
	malformedDraft, err := catalog.CreateDraft(ctx, workflow.DraftCreateRequest{Name: "malformed-edge", Scope: workflowstore.DraftScopeProject, ScopeReference: "project-test", IdempotencyKey: "malformed-edge-draft", Document: json.RawMessage(malformedDocument), Layout: json.RawMessage(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	malformedReport, err := catalog.ValidateDraft(ctx, malformedDraft.ID, malformedDraft.Revision)
	if err != nil {
		t.Fatal(err)
	}
	if len(malformedReport.Findings) != 1 || malformedReport.Findings[0].NodeID != "finish" || malformedReport.Findings[0].Field != "transitions.0.maxTraversals" {
		t.Fatalf("typed decoder finding = %#v", malformedReport.Findings)
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
