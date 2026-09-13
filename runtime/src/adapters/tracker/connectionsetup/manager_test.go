package connectionsetup

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"darkstar/src/ports/tracker"
	"darkstar/src/ports/trackerconnection"
)

type setupTransport func(*http.Request) (*http.Response, error)

func (f setupTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

func TestBootstrapRetainsExactAccountsAndImmutableConnectionRevisions(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	account := "github-account-id"
	calls := 0
	const token = "secret-never-retained-in-configuration"
	client := &http.Client{Transport: setupTransport(func(request *http.Request) (*http.Response, error) {
		calls++
		content, _ := io.ReadAll(request.Body)
		var envelope struct {
			Query string `json:"query"`
		}
		if err := json.Unmarshal(content, &envelope); err != nil {
			t.Fatal(err)
		}
		data := `{"data":{"viewer":{"id":"` + account + `","login":"developer"}}}`
		if request.URL.Host == "api.linear.app" {
			if request.Header.Get("Authorization") != token {
				t.Fatal("Linear bootstrap did not use the protected API-key mode")
			}
			data = `{"data":{"viewer":{"id":"linear-account-id","name":"Developer"},"organization":{"id":"linear-workspace-id","name":"Workspace"}}}`
		} else {
			if request.Header.Get("Authorization") != "Bearer "+token {
				t.Fatal("GitHub bootstrap did not use the protected token")
			}
			if strings.Contains(envelope.Query, "repositories(") {
				data = `{"data":{"viewer":{"id":"` + account + `","repositories":{"nodes":[{"id":"repo-node","nameWithOwner":"owner/repository","url":"https://github.com/owner/repository","hasIssuesEnabled":true,"owner":{"id":"owner-node"}}],"pageInfo":{"hasNextPage":false,"endCursor":null}}}}}`
			}
		}
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(data))}, nil
	})}
	manager, err := New(root, Options{HTTPClient: client})
	if err != nil {
		t.Fatal(err)
	}
	for _, ref := range []string{"github-ref", "github-ref-new", "linear-ref"} {
		if err := manager.StoreCredential(ctx, ref, token); err != nil {
			t.Fatal(err)
		}
	}
	request := trackerconnection.GitHubTokenSetupRequest{SchemaVersion: 1, ConnectionID: "github-personal", Revision: "1", Host: "github.com", CredentialRef: "github-ref"}
	first, err := manager.CreateGitHubToken(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	config, ok := first.Configuration.(trackerconnection.GitHubTokenConfiguration)
	if !ok || config.AccountID != account || first.EvidenceRef == "" {
		t.Fatalf("bootstrap did not retain exact account: %#v", first)
	}
	before := calls
	repeated, err := manager.CreateGitHubToken(ctx, request)
	if err != nil || repeated.ObservedAt != first.ObservedAt || calls != before {
		t.Fatal("same connection revision was not replayed immutably")
	}
	request.CredentialRef = "github-ref-new"
	if _, err := manager.CreateGitHubToken(ctx, request); err == nil {
		t.Fatal("existing connection revision was overwritten")
	}
	request.Revision = "2"
	if _, err := manager.CreateGitHubToken(ctx, request); err != nil {
		t.Fatal(err)
	}
	linearRecord, err := manager.CreateLinear(ctx, trackerconnection.LinearSetupRequest{SchemaVersion: 1, ConnectionID: "linear-work", Revision: "1", CredentialRef: "linear-ref", Authentication: "personal_api_key"})
	if err != nil {
		t.Fatal(err)
	}
	linearConfig, ok := linearRecord.Configuration.(trackerconnection.LinearConfiguration)
	if !ok || linearConfig.AccountID != "linear-account-id" || linearConfig.WorkspaceID != "linear-workspace-id" {
		t.Fatal("Linear bootstrap did not retain account and workspace identities")
	}
	manager, err = New(root, Options{HTTPClient: client})
	if err != nil {
		t.Fatal(err)
	}
	list, err := manager.List(ctx)
	if err != nil || len(list) != 3 {
		t.Fatalf("connection revisions did not survive restart: %v", err)
	}
	destinations, err := manager.Destinations(ctx, "github-personal", "1", "", 50)
	if err != nil || len(destinations.Destinations) != 1 || destinations.Destinations[0].ScopeID != "repo-node" || destinations.NextCursor != "" {
		t.Fatalf("exact pinned destination discovery failed: %#v %v", destinations, err)
	}
	scope := tracker.Scope{Namespace: tracker.Namespace{Provider: "github_issues", Host: "github.com", TenantID: "owner-node", ScopeID: "repo-node"}, ContainerID: "repo-node"}
	before = calls
	source, err := manager.ResolveSource(ctx, "github-personal", "1", scope, "binding-7")
	if err != nil || source.Config.AccountID != account || source.Config.ConfigRevision != "1" || source.Config.BindingRevision != "binding-7" || calls != before {
		t.Fatalf("exact source factory changed selection or performed intake: %v", err)
	}
	scope.Namespace.Host = "other.example"
	if _, err := manager.ResolveSource(ctx, "github-personal", "1", scope, "binding-7"); err == nil {
		t.Fatal("source factory silently retargeted connection host")
	}
	account = "different-account"
	if _, err := manager.Health(ctx, "github-personal", "1"); err == nil {
		t.Fatal("credential authority change silently rebound saved connection")
	}
	if _, err := manager.Destinations(ctx, "github-personal", "1", "", 50); err == nil {
		t.Fatal("destination discovery silently accepted changed account")
	}
	if err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() || (runtime.GOOS != "windows" && filepath.Base(filepath.Dir(path)) == "credentials") {
			return nil
		}
		content, err := os.ReadFile(path)
		if err == nil && bytes.Contains(content, []byte(token)) {
			t.Fatal("secret persisted outside protected credential storage")
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
}
