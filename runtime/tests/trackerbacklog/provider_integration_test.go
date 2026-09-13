package trackerbacklog_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"darkstar/src/adapters/statestore/sqlite"
	"darkstar/src/adapters/tracker/githubissues"
	"darkstar/src/adapters/tracker/linear"
	"darkstar/src/adapters/tracker/localconnection"
	"darkstar/src/core/backlog"
	"darkstar/src/core/workmanagement"
	"darkstar/src/ports/statestore"
	"darkstar/src/ports/tracker"
)

const sourceText = "Read-only integration original: start no workflow from this provider text."
const sourceCredential = "fixture-only-secret-never-retain"

type credentialFixture struct{}

func (credentialFixture) Resolve(context.Context, string) (string, error) {
	return sourceCredential, nil
}

type resolverFunc func(context.Context, statestore.BacklogBinding) (backlog.ResolvedSource, error)

func (f resolverFunc) Resolve(ctx context.Context, binding statestore.BacklogBinding) (backlog.ResolvedSource, error) {
	return f(ctx, binding)
}

type fixtureTransport struct {
	target *url.URL
	base   http.RoundTripper
}

func (f fixtureTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	if request.URL.String() != "https://api.github.com/graphql" {
		return nil, errors.New("unexpected provider endpoint in integration fixture")
	}
	local := request.Clone(request.Context())
	location := *request.URL
	location.Scheme = f.target.Scheme
	location.Host = f.target.Host
	local.URL = &location
	return f.base.RoundTrip(local)
}

func TestRealTrackerAdaptersPersistBacklogWithoutExecution(t *testing.T) {
	for _, provider := range []string{"linear", "github_issues"} {
		t.Run(provider, func(t *testing.T) {
			ctx := context.Background()
			root := t.TempDir()
			databasePath := filepath.Join(root, "backlog.db")
			database, err := sqlite.Open(ctx, databasePath, sqlite.Options{})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				_ = database.Close()
			})
			work, err := workmanagement.New(database)
			if err != nil {
				t.Fatal(err)
			}
			project, err := work.RegisterProject(ctx, workmanagement.ProjectRegistration{Name: "Provider-backed backlog", Source: root}, "provider-project-key")
			if err != nil {
				t.Fatal(err)
			}
			evidencePath := filepath.Join(root, "source-evidence")
			evidence, err := localconnection.NewEvidenceStore(evidencePath)
			if err != nil {
				t.Fatal(err)
			}
			var requests atomic.Int64
			server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
				requests.Add(1)
				var body struct {
					Query string `json:"query"`
				}
				if request.Method != http.MethodPost || json.NewDecoder(request.Body).Decode(&body) != nil || !strings.HasPrefix(strings.TrimSpace(body.Query), "query") || strings.Contains(strings.ToLower(body.Query), "mutation") {
					t.Error("source adapter attempted a non-read GraphQL operation")
					response.WriteHeader(http.StatusBadRequest)
					return
				}
				expectedAuthorization := sourceCredential
				if provider == "github_issues" {
					expectedAuthorization = "Bearer " + sourceCredential
				}
				if request.Header.Get("Authorization") != expectedAuthorization {
					t.Error("source adapter did not resolve the fixture credential")
					response.WriteHeader(http.StatusUnauthorized)
					return
				}
				response.Header().Set("Content-Type", "application/json")
				var data map[string]any
				if provider == "linear" {
					data = linearResponse(body.Query)
				} else {
					data = githubResponse(body.Query)
				}
				if err := json.NewEncoder(response).Encode(map[string]any{"data": data}); err != nil {
					t.Error(err)
				}
			}))
			t.Cleanup(server.Close)
			githubURL, err := url.Parse(server.URL)
			if err != nil {
				t.Fatal(err)
			}
			githubClient := &http.Client{Transport: fixtureTransport{target: githubURL, base: server.Client().Transport}, Timeout: 5 * time.Second}
			scope := tracker.Scope{Namespace: tracker.Namespace{Provider: "linear", Host: "linear.app", TenantID: "workspace", ScopeID: "workspace"}, ContainerID: "team"}
			if provider == "github_issues" {
				scope = tracker.Scope{Namespace: tracker.Namespace{Provider: "github_issues", Host: "github.com", TenantID: "O_owner", ScopeID: "R_repository"}, ContainerID: "R_repository"}
			}
			online := true
			resolver := resolverFunc(func(ctx context.Context, binding statestore.BacklogBinding) (backlog.ResolvedSource, error) {
				if !online {
					return backlog.ResolvedSource{}, errors.New("cached view unexpectedly resolved a live provider")
				}
				bindingRevision := strconv.FormatUint(binding.Revision, 10)
				if provider == "linear" {
					adapter, err := linear.New(linear.Config{Endpoint: server.URL, InstallationID: "fixture-connection", AccountID: "account", WorkspaceID: "workspace", TeamID: "team", BindingRevision: bindingRevision, ConfigRevision: "1", CredentialRef: "fixture-reference", Authentication: linear.PersonalAPIKey}, server.Client(), credentialFixture{}, evidence)
					if err != nil {
						return backlog.ResolvedSource{}, err
					}
					return backlog.ResolvedSource{Source: adapter, Browser: adapter, Config: adapter.ConfigPin()}, nil
				}
				adapter, err := githubissues.New(githubissues.Config{Host: "github.com", InstallationID: "fixture-connection", AccountID: "U_account", TenantID: "O_owner", RepositoryID: "R_repository", BindingRevision: bindingRevision, ConfigRevision: "1", CredentialRef: "fixture-reference"}, githubissues.Options{HTTPClient: githubClient, Credentials: credentialFixture{}, Evidence: evidence})
				if err != nil {
					return backlog.ResolvedSource{}, err
				}
				return backlog.ResolvedSource{Source: adapter, Browser: adapter, Config: adapter.ConfigPin()}, nil
			})
			service, err := backlog.New(database, resolver, backlog.Options{MaxPages: 1, MaxTickets: 10, Timeout: 5 * time.Second})
			if err != nil {
				t.Fatal(err)
			}
			selected, err := service.SelectSource(ctx, project.ProjectID, 1, statestore.ExternalBacklogSource{ConnectionID: "fixture-connection", ConnectionRevision: "1", Scope: scope})
			if err != nil {
				t.Fatal(err)
			}
			query := tracker.Query{PageSize: 10}
			result, err := service.Refresh(ctx, project.ProjectID, selected.Revision, query)
			if err != nil || !result.Complete || result.Processed != 1 {
				t.Fatalf("real provider did not finish one durable page: %#v %v", result, err)
			}
			view, err := service.View(ctx, project.ProjectID, backlog.ViewRequest{})
			if err != nil || len(view.Tickets) != 1 || view.Tickets[0].Status != backlog.Fresh || view.Tickets[0].Ticket.Description != sourceText {
				t.Fatalf("normalized provider ticket did not reach cached view: %#v %v", view, err)
			}
			first := view.Tickets[0]
			retained, err := database.BacklogObservation(ctx, first.ObservationID)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := evidence.Read(ctx, retained.EvidenceRef); err != nil {
				t.Fatalf("ticket provenance is not durably retained: %v", err)
			}
			assertNoExecution(t, database)
			if err := database.Close(); err != nil {
				t.Fatal(err)
			}
			database, err = sqlite.Open(ctx, databasePath, sqlite.Options{})
			if err != nil {
				t.Fatal(err)
			}
			service, err = backlog.New(database, resolver, backlog.Options{MaxPages: 1, MaxTickets: 10, Timeout: 5 * time.Second})
			if err != nil {
				t.Fatal(err)
			}
			online = false
			before := requests.Load()
			view, err = service.View(ctx, project.ProjectID, backlog.ViewRequest{})
			if err != nil || len(view.Tickets) != 1 || view.Tickets[0].Key != first.Key || view.Tickets[0].ObservationID != first.ObservationID || requests.Load() != before {
				t.Fatalf("restart did not serve the same durable identity offline: %#v %v", view, err)
			}
			online = true
			if _, err := service.Refresh(ctx, project.ProjectID, selected.Revision, query); err != nil {
				t.Fatal(err)
			}
			view, err = service.View(ctx, project.ProjectID, backlog.ViewRequest{})
			if err != nil || len(view.Tickets) != 1 || view.Tickets[0].ObservationID != first.ObservationID || view.Tickets[0].Ticket.Ref != first.Ticket.Ref {
				t.Fatalf("same provider revision duplicated ticket identity: %#v %v", view, err)
			}
			again, err := database.BacklogObservation(ctx, first.ObservationID)
			if err != nil || !bytes.Equal(again.Ticket, retained.Ticket) || !again.ObservedAt.Equal(retained.ObservedAt) {
				t.Fatal("refresh rewrote an immutable original observation")
			}
			assertNoExecution(t, database)
			assertOriginalEvidenceWithoutCredential(t, evidencePath)
		})
	}
}

func assertNoExecution(t *testing.T, database *sqlite.Database) {
	t.Helper()
	work, err := database.WorkItems(context.Background())
	if err != nil || len(work) != 0 {
		t.Fatal("provider browsing created work")
	}
	runs, err := database.Runs(context.Background())
	if err != nil || len(runs) != 0 {
		t.Fatal("provider browsing created execution")
	}
}

func assertOriginalEvidenceWithoutCredential(t *testing.T, root string) {
	t.Helper()
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, entry := range entries {
		content, err := os.ReadFile(filepath.Join(root, entry.Name()))
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Contains(content, []byte(sourceCredential)) {
			t.Fatal("source evidence retained a request credential")
		}
		found = found || bytes.Contains(content, []byte(sourceText))
	}
	if !found {
		t.Fatal("raw provider content was not retained")
	}
}

func connection(nodes []any) map[string]any {
	return map[string]any{"nodes": nodes, "pageInfo": map[string]any{"hasNextPage": false, "endCursor": ""}}
}

func linearResponse(query string) map[string]any {
	data := map[string]any{"viewer": map[string]any{"id": "account", "name": "Person"}, "organization": map[string]any{"id": "workspace", "name": "Workspace"}}
	if strings.Contains(query, "DarkstarHealth") {
		data["team"] = map[string]any{"id": "team", "name": "Team"}
		return data
	}
	issue := map[string]any{"id": "11111111-1111-4111-8111-111111111111", "identifier": "TEAM-1", "url": "https://linear.app/workspace/issue/TEAM-1", "title": "Provider-backed ticket", "description": sourceText, "updatedAt": "2026-09-12T12:00:00Z", "archivedAt": nil, "priority": 2, "priorityLabel": "High", "team": map[string]any{"id": "team", "name": "Team"}, "project": nil, "state": map[string]any{"id": "open-state", "name": "Open"}, "assignee": nil, "cycle": nil, "parent": nil, "labels": connection([]any{}), "inverseRelations": connection([]any{}), "attachments": connection([]any{}), "comments": connection([]any{})}
	data["issues"] = connection([]any{issue})
	return data
}

func githubResponse(query string) map[string]any {
	data := map[string]any{"viewer": map[string]any{"id": "U_account", "login": "developer"}}
	if strings.Contains(query, "issues(first:$first") {
		issue := map[string]any{"__typename": "Issue", "id": "I_ticket", "number": 42, "url": "https://github.com/owner/repository/issues/42", "title": "Provider-backed ticket", "body": sourceText, "state": "OPEN", "stateReason": nil, "updatedAt": "2026-09-12T12:00:00Z", "repository": map[string]any{"id": "R_repository", "owner": map[string]any{"id": "O_owner"}}, "assignees": connection([]any{}), "labels": connection([]any{})}
		data["node"] = map[string]any{"id": "R_repository", "issues": connection([]any{issue})}
	} else {
		data["node"] = map[string]any{"__typename": "Repository", "id": "R_repository", "nameWithOwner": "owner/repository", "url": "https://github.com/owner/repository", "hasIssuesEnabled": true, "owner": map[string]any{"id": "O_owner"}, "issues": connection([]any{})}
	}
	return data
}
