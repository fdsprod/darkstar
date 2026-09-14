package api

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	repositorygit "darkstar/src/adapters/repository/git"
	snapshotgit "darkstar/src/adapters/repositorysnapshot/git"
	"darkstar/src/adapters/statestore/sqlite"
	"darkstar/src/core/repositoryscope"
	"darkstar/src/core/workmanagement"
	"darkstar/src/ports/statestore"
)

func TestInvestigationScopeAPIPinsSelectionAndRetainsReplay(t *testing.T) {
	ctx := context.Background()
	database, err := sqlite.Open(ctx, filepath.Join(t.TempDir(), "scopes.db"), sqlite.Options{})
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
	manager, err := repositorygit.New("")
	if err != nil {
		t.Fatal(err)
	}
	work.ConfigureRepositoryManager(manager)
	project, err := work.CreateProjectV2(ctx, workmanagement.CreateProjectRequest{Name: "Investigation product"}, "scope-api-project")
	if err != nil {
		t.Fatal(err)
	}
	exporter, err := snapshotgit.New(filepath.Join(t.TempDir(), "snapshots"), snapshotgit.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	scopes, err := repositoryscope.New(database, exporter)
	if err != nil {
		t.Fatal(err)
	}
	server, err := NewServer(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := server.SetRepositoryScopes(scopes); err != nil {
		t.Fatal(err)
	}
	if err := server.Start(ctx, 1234, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	defer closeTestServer(t, server)
	endpoint, _ := server.Endpoint()
	prepare := func(body, key string) repositoryscope.View {
		t.Helper()
		response := workRequest(t, endpoint, http.MethodPost, "/api/v1/investigation-scopes", body, key)
		defer func() {
			_ = response.Body.Close()
		}()
		var view repositoryscope.View
		if response.StatusCode != http.StatusCreated {
			var failure any
			decodeJSON(t, response, &failure)
			t.Fatalf("prepare: %d %#v", response.StatusCode, failure)
		}
		decodeJSON(t, response, &view)
		return view
	}
	encode := func(selections []repositoryscope.RepositorySelection) string {
		data, err := json.Marshal(repositoryscope.PrepareRequest{ProjectID: project.Project.ProjectID, Repositories: selections})
		if err != nil {
			t.Fatal(err)
		}
		return string(data)
	}
	empty := prepare(encode([]repositoryscope.RepositorySelection{}), "scope-api-none")
	if empty.Scope.Mode != statestore.InvestigationScopeNone || empty.Preparation.Status != statestore.RepositoryScopeReady {
		t.Fatalf("explicit zero-repository scope = %#v", empty)
	}
	invalid := workRequest(t, endpoint, http.MethodPost, "/api/v1/investigation-scopes", encode(nil), "scope-api-implicit")
	assertAPIError(t, invalid, http.StatusBadRequest, "VALIDATION_FAILED")
	_ = invalid.Body.Close()
	root := t.TempDir()
	git := func(args ...string) {
		t.Helper()
		command := exec.Command("git", append([]string{"-C", root}, args...)...)
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("git fixture: %v %s", err, output)
		}
	}
	git("init", "-b", "main")
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("original source\n"), 0600); err != nil {
		t.Fatal(err)
	}
	git("add", "README.md")
	git("-c", "user.name=Scope test", "-c", "user.email=scope@example.test", "commit", "-m", "initial")
	project, err = work.SetMembership(ctx, workmanagement.MembershipRequest{ProjectID: project.Project.ProjectID, RepositoryPath: root, Label: "Code", Role: statestore.RepositoryReadOnly, ExpectedVersion: project.Project.ResourceVersion}, "scope-api-attach")
	if err != nil {
		t.Fatal(err)
	}
	repositoryID := project.Repositories[0].Repository.RepositoryID
	request := encode([]repositoryscope.RepositorySelection{{RepositoryID: repositoryID, Ref: "refs/heads/main"}})
	first := prepare(request, "scope-api-pin")
	if first.Scope.Mode != statestore.InvestigationScopeReadOnly || first.Preparation.Status != statestore.RepositoryScopeReady || len(first.Evidence) != 1 {
		t.Fatalf("prepared scope = %#v", first)
	}
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("later source\n"), 0600); err != nil {
		t.Fatal(err)
	}
	git("add", "README.md")
	git("-c", "user.name=Scope test", "-c", "user.email=scope@example.test", "commit", "-m", "later")
	replayed := prepare(request, "scope-api-pin")
	if replayed.Scope.Digest != first.Scope.Digest || replayed.Scope.Repositories[0].Revision != first.Scope.Repositories[0].Revision || replayed.Evidence[0].Evidence.ManifestDigest != first.Evidence[0].Evidence.ManifestDigest {
		t.Fatal("idempotent scope preparation followed a moved branch")
	}
	read := workRequest(t, endpoint, http.MethodGet, "/api/v1/investigation-scopes/"+first.Scope.ScopeID, "", "")
	if read.StatusCode != http.StatusOK {
		t.Fatalf("scope read status=%d", read.StatusCode)
	}
	_ = read.Body.Close()
	denied := workRequest(t, endpoint, http.MethodPost, "/api/v1/investigation-scopes/"+first.Scope.ScopeID+"/attempts", `{}`, "caller-attempt")
	assertAPIError(t, denied, http.StatusNotFound, "NOT_FOUND")
	_ = denied.Body.Close()
}
