package api

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/url"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	repositorygit "darkstar/src/adapters/repository/git"
	"darkstar/src/adapters/statestore/sqlite"
	"darkstar/src/core/workmanagement"
	"darkstar/src/ports/statestore"
)

func TestRepositoryMembershipAPICommandsAndCompatibility(t *testing.T) {
	ctx := context.Background()
	database, err := sqlite.Open(ctx, filepath.Join(t.TempDir(), "memberships.db"), sqlite.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = database.Close()
	})
	service, err := workmanagement.New(database)
	if err != nil {
		t.Fatal(err)
	}
	manager, err := repositorygit.New("")
	if err != nil {
		t.Fatal(err)
	}
	service.ConfigureRepositoryManager(manager)
	server, err := NewServer(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := server.SetWork(service); err != nil {
		t.Fatal(err)
	}
	if err := server.Start(ctx, 1234, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	defer closeTestServer(t, server)
	endpoint, _ := server.Endpoint()
	response := workRequest(t, endpoint, http.MethodPost, "/api/v1/projects-v2", `{"name":"Product","defaults":{"baseRef":"main"}}`, "product-create")
	if response.StatusCode != http.StatusCreated {
		t.Fatalf("create status = %d", response.StatusCode)
	}
	var view workmanagement.ProjectRepositoriesView
	decodeJSON(t, response, &view)
	_ = response.Body.Close()
	if view.SchemaVersion != 2 || len(view.Repositories) != 0 || view.Defaults.BaseRef != "main" {
		t.Fatalf("empty project = %#v", view)
	}
	projectPath := "/api/v1/projects-v2/" + view.Project.ProjectID
	legacy := workRequest(t, endpoint, http.MethodGet, "/api/v1/projects/"+view.Project.ProjectID, "", "")
	assertAPIError(t, legacy, http.StatusConflict, "PROJECT_CARDINALITY_UNSUPPORTED")
	_ = legacy.Body.Close()
	root := t.TempDir()
	if output, err := exec.Command("git", "init", root).CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, output)
	}
	body := fmt.Sprintf(`{"repositoryPath":%q,"label":"Code","role":"implementation","settings":{"pathScope":[]},"expectedMembershipRevision":0}`, root)
	firstVersion := view.Project.ResourceVersion
	attached := repositoryMutation(t, endpoint, http.MethodPost, projectPath+"/repositories", body, "attach-code", firstVersion)
	if attached.StatusCode != http.StatusOK {
		var problem any
		decodeJSON(t, attached, &problem)
		t.Fatalf("attach status = %d: %#v", attached.StatusCode, problem)
	}
	decodeJSON(t, attached, &view)
	_ = attached.Body.Close()
	if len(view.Repositories) != 1 || view.Repositories[0].Membership.Settings.PathScope == nil {
		t.Fatalf("attached membership lost empty scope: %#v", view)
	}
	member := view.Repositories[0]
	repositoryPath := projectPath + "/repositories/" + member.Repository.RepositoryID
	replayed := repositoryMutation(t, endpoint, http.MethodPost, projectPath+"/repositories", body, "attach-code", firstVersion)
	var replayView workmanagement.ProjectRepositoriesView
	decodeJSON(t, replayed, &replayView)
	_ = replayed.Body.Close()
	if replayView.Project.ResourceVersion != view.Project.ResourceVersion || len(replayView.Repositories) != 1 {
		t.Fatal("attachment replay changed membership state")
	}
	legacy = workRequest(t, endpoint, http.MethodGet, "/api/v1/projects/"+view.Project.ProjectID, "", "")
	if legacy.StatusCode != http.StatusOK {
		t.Fatalf("single-repository compatibility status = %d", legacy.StatusCode)
	}
	_ = legacy.Body.Close()
	stale := repositoryMutation(t, endpoint, http.MethodPut, repositoryPath, `{"label":"Other","role":"read_only","settings":{},"expectedMembershipRevision":1}`, "stale-membership", firstVersion)
	assertAPIError(t, stale, http.StatusConflict, "REPOSITORY_REVISION_CONFLICT")
	_ = stale.Body.Close()
	contradictory := repositoryMutation(t, endpoint, http.MethodPut, repositoryPath, body, "identity-change", view.Project.ResourceVersion)
	assertAPIError(t, contradictory, http.StatusBadRequest, "VALIDATION_FAILED")
	_ = contradictory.Body.Close()
	updated := repositoryMutation(t, endpoint, http.MethodPut, repositoryPath, `{"label":"Research code","role":"read_only","settings":{},"expectedMembershipRevision":1}`, "update-code", view.Project.ResourceVersion)
	if updated.StatusCode != http.StatusOK {
		t.Fatalf("update status = %d", updated.StatusCode)
	}
	decodeJSON(t, updated, &view)
	_ = updated.Body.Close()
	member = view.Repositories[0]
	if member.Membership.Revision != 2 || member.Membership.Role != statestore.RepositoryReadOnly {
		t.Fatalf("updated membership = %#v", member.Membership)
	}
	discovery := workRequest(t, endpoint, http.MethodGet, "/api/v1/projects-v2/discover?path="+url.QueryEscape(root), "", "")
	var matches []workmanagement.ProjectRepositoriesView
	decodeJSON(t, discovery, &matches)
	_ = discovery.Body.Close()
	if len(matches) != 1 || matches[0].Project.ProjectID != view.Project.ProjectID {
		t.Fatalf("discovery = %#v", matches)
	}
	defaults := repositoryMutation(t, endpoint, http.MethodPut, projectPath+"/defaults", `{"defaults":{"baseRef":"next"}}`, "update-defaults", view.Project.ResourceVersion)
	if defaults.StatusCode != http.StatusOK {
		t.Fatalf("defaults status = %d", defaults.StatusCode)
	}
	decodeJSON(t, defaults, &view)
	_ = defaults.Body.Close()
	if view.Defaults.BaseRef != "next" {
		t.Fatalf("defaults = %#v", view.Defaults)
	}
	removed := repositoryMutation(t, endpoint, http.MethodDelete, repositoryPath, fmt.Sprintf(`{"expectedMembershipRevision":%d}`, member.Membership.Revision), "remove-code", view.Project.ResourceVersion)
	if removed.StatusCode != http.StatusOK {
		t.Fatalf("remove status = %d", removed.StatusCode)
	}
	decodeJSON(t, removed, &view)
	_ = removed.Body.Close()
	if view.Repositories[0].Membership.Status != statestore.MembershipRemoved || view.Repositories[0].Membership.Removal == nil {
		t.Fatalf("removal history = %#v", view)
	}
	response = workRequest(t, endpoint, http.MethodGet, projectPath+"/repositories", "", "")
	decodeJSON(t, response, &view)
	_ = response.Body.Close()
	if len(view.Repositories) != 1 || view.Repositories[0].Repository.RepositoryID != member.Repository.RepositoryID {
		t.Fatal("removed membership no longer addressable")
	}
}

func repositoryMutation(t *testing.T, endpoint Endpoint, method, resource, body, key string, version uint64) *http.Response {
	t.Helper()
	request, err := http.NewRequest(method, endpoint.BaseURL()+resource, bytes.NewBufferString(body))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", endpoint.AuthorizationHeader())
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Idempotency-Key", key)
	request.Header.Set("If-Match", fmt.Sprintf("\"%d\"", version))
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	return response
}
