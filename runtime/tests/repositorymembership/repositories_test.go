package repositorymembership_test

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sync"
	"testing"

	gitadapter "darkstar/src/adapters/repository/git"
	"darkstar/src/adapters/statestore/sqlite"
	"darkstar/src/core/workmanagement"
	"darkstar/src/ports/statestore"
)

func repositoryFixture(t *testing.T) (*workmanagement.Service, *sqlite.Database, string) {
	t.Helper()
	db, err := sqlite.Open(context.Background(), filepath.Join(t.TempDir(), "state.db"), sqlite.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = db.Close()
	})
	service, err := workmanagement.New(db)
	if err != nil {
		t.Fatal(err)
	}
	manager, err := gitadapter.New("")
	if err != nil {
		t.Fatal(err)
	}
	service.ConfigureRepositoryManager(manager)
	root := t.TempDir()
	repositoryGit(t, root, "init", "--initial-branch=main")
	repositoryGit(t, root, "-c", "user.name=Membership Test", "-c", "user.email=membership@example.invalid", "commit", "--allow-empty", "-m", "Initial")
	return service, db, root
}

func repositoryGit(t *testing.T, root string, args ...string) {
	t.Helper()
	command := exec.Command("git", append([]string{"-C", root}, args...)...)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v: %s", args, err, output)
	}
}

func createRepositoryProject(t *testing.T, service *workmanagement.Service, name, key string) workmanagement.ProjectRepositoriesView {
	t.Helper()
	value, err := service.CreateProjectV2(context.Background(), workmanagement.CreateProjectRequest{Name: name}, key)
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func addRepository(t *testing.T, service *workmanagement.Service, view workmanagement.ProjectRepositoriesView, root, label, key string) workmanagement.ProjectRepositoriesView {
	t.Helper()
	value, err := service.SetMembership(context.Background(), workmanagement.MembershipRequest{ProjectID: view.Project.ProjectID, RepositoryPath: root, Label: label, Role: statestore.RepositoryImplementation, ExpectedVersion: view.Project.ResourceVersion}, key)
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func TestRepositoryMembershipCardinalityHistoryReplayAndSourceIndependence(t *testing.T) {
	ctx := context.Background()
	service, db, root := repositoryFixture(t)
	view := createRepositoryProject(t, service, "Product", "create-product")
	if len(view.Repositories) != 0 || view.Migration.State != "ready" {
		t.Fatalf("new project = %#v", view)
	}
	if _, err := service.Project(ctx, view.Project.ProjectID); !errors.Is(err, workmanagement.ErrUnsupportedCardinality) {
		t.Fatalf("legacy empty representation = %v", err)
	}
	work, err := service.CreateWork(ctx, workmanagement.CreateWorkRequest{ProjectID: view.Project.ProjectID, Title: "Plan without a repository"}, "create-plan-work")
	if err != nil {
		t.Fatal(err)
	}
	view = addRepository(t, service, view, root, "api", "add-repository")
	first := view.Repositories[0]
	if _, err := service.ResolveRepository(ctx, view.Project.ProjectID, "", true); err != nil {
		t.Fatal(err)
	}
	clone := filepath.Join(t.TempDir(), "clone")
	repositoryGit(t, root, "clone", root, clone)
	view = addRepository(t, service, view, clone, "web", "add-other-repository")
	if len(view.Repositories) != 2 || view.Repositories[0].Repository.RepositoryID == view.Repositories[1].Repository.RepositoryID {
		t.Fatal("separate clone identities collapsed")
	}
	if _, err := service.ResolveRepository(ctx, view.Project.ProjectID, "", false); !errors.Is(err, workmanagement.ErrRepositorySelection) {
		t.Fatalf("ambiguous default selection = %v", err)
	}
	if _, err := service.Project(ctx, view.Project.ProjectID); !errors.Is(err, workmanagement.ErrUnsupportedCardinality) {
		t.Fatalf("legacy multiple representation = %v", err)
	}
	beforeSource, err := db.BacklogBinding(ctx, view.Project.ProjectID)
	if err != nil {
		t.Fatal(err)
	}
	removed, err := service.RemoveMembership(ctx, workmanagement.RemoveMembershipRequest{ProjectID: view.Project.ProjectID, RepositoryID: first.Repository.RepositoryID, ExpectedVersion: view.Project.ResourceVersion, ExpectedMembershipRevision: 1}, "remove-api-repository")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.ResolveRepository(ctx, view.Project.ProjectID, first.Repository.RepositoryID, false); !errors.Is(err, workmanagement.ErrRepositorySelection) {
		t.Fatalf("removed selection = %v", err)
	}
	history, err := db.MembershipRevision(ctx, view.Project.ProjectID, first.Repository.RepositoryID, 1)
	if err != nil || history.Status != statestore.MembershipActive {
		t.Fatalf("retained membership = %#v, %v", history, err)
	}
	view, err = service.SetMembership(ctx, workmanagement.MembershipRequest{ProjectID: view.Project.ProjectID, RepositoryID: first.Repository.RepositoryID, Label: "api", Role: statestore.RepositoryReadOnly, ExpectedVersion: removed.Project.ResourceVersion, ExpectedMembershipRevision: 2, Settings: statestore.RepositorySettings{PathScope: []string{}, ValidationProfiles: map[string][]string{}}}, "restore-api-repository")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.ResolveRepository(ctx, view.Project.ProjectID, first.Repository.RepositoryID, true); !errors.Is(err, workmanagement.ErrRepositorySelection) {
		t.Fatalf("read-only member writer = %v", err)
	}
	restored, err := db.MembershipRevision(ctx, view.Project.ProjectID, first.Repository.RepositoryID, 3)
	if err != nil || restored.Settings.PathScope == nil || restored.Settings.ValidationProfiles == nil {
		t.Fatalf("empty override lost: %#v %v", restored.Settings, err)
	}
	afterSource, err := db.BacklogBinding(ctx, view.Project.ProjectID)
	if err != nil || !reflect.DeepEqual(beforeSource, afterSource) {
		t.Fatalf("membership modified tracker source: %#v %#v %v", beforeSource, afterSource, err)
	}
	eventsBefore, err := db.EventsAfter(ctx, 0, 1000)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.RebuildProjections(ctx); err != nil {
		t.Fatal(err)
	}
	rebuilt, err := service.ProjectRepositories(ctx, view.Project.ProjectID)
	if err != nil || !reflect.DeepEqual(view, rebuilt) {
		t.Fatalf("rebuild changed project: %#v %#v %v", view, rebuilt, err)
	}
	eventsAfter, err := db.EventsAfter(ctx, 0, 1000)
	if err != nil || !reflect.DeepEqual(eventsBefore, eventsAfter) {
		t.Fatal("rebuild rewrote historical events")
	}
	retainedWork, err := db.WorkItem(ctx, work.WorkItemID)
	if err != nil || !reflect.DeepEqual(work, retainedWork) {
		t.Fatal("membership changes modified historical work")
	}
	if _, err := os.Stat(root); err != nil {
		t.Fatal("removal changed repository files")
	}
}

func TestSharedRepositoryAliasesConcurrentMembershipsAndRevisionCheck(t *testing.T) {
	ctx := context.Background()
	service, db, root := repositoryFixture(t)
	first := createRepositoryProject(t, service, "First", "create-first")
	second := createRepositoryProject(t, service, "Second", "create-second")
	linked := filepath.Join(t.TempDir(), "linked")
	repositoryGit(t, root, "worktree", "add", "--detach", linked, "HEAD")
	views := []workmanagement.ProjectRepositoriesView{first, second}
	roots := []string{root, linked}
	results := make([]workmanagement.ProjectRepositoriesView, 2)
	errorsFound := make([]error, 2)
	var wait sync.WaitGroup
	for index := range views {
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			results[index], errorsFound[index] = service.SetMembership(ctx, workmanagement.MembershipRequest{ProjectID: views[index].Project.ProjectID, RepositoryPath: roots[index], Label: "code", Role: statestore.RepositoryImplementation, ExpectedVersion: 1}, "concurrent-"+views[index].Project.ProjectID)
		}(index)
	}
	wait.Wait()
	for _, err := range errorsFound {
		if err != nil {
			t.Fatal(err)
		}
	}
	if results[0].Repositories[0].Repository.RepositoryID != results[1].Repositories[0].Repository.RepositoryID {
		t.Fatal("linked checkout created independent registry identities")
	}
	var count int
	if err := db.SQL().QueryRow(`SELECT COUNT(*) FROM repository_registry`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("registry count = %d, %v", count, err)
	}
	candidates, err := service.DiscoverProjects(ctx, linked)
	if err != nil || len(candidates) != 2 {
		t.Fatalf("shared discovery = %#v %v", candidates, err)
	}
	_, err = service.RemoveMembership(ctx, workmanagement.RemoveMembershipRequest{ProjectID: first.Project.ProjectID, RepositoryID: results[0].Repositories[0].Repository.RepositoryID, ExpectedVersion: 1, ExpectedMembershipRevision: 1}, "stale-removal")
	if !errors.Is(err, workmanagement.ErrRepositoryRevision) {
		t.Fatalf("stale edit = %v", err)
	}
	_, err = service.RemoveMembership(ctx, workmanagement.RemoveMembershipRequest{ProjectID: first.Project.ProjectID, RepositoryID: results[0].Repositories[0].Repository.RepositoryID, ExpectedVersion: 2, ExpectedMembershipRevision: 1}, "valid-removal")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.ResolveRepository(ctx, second.Project.ProjectID, "", true); err != nil {
		t.Fatal("another project's membership was affected", err)
	}
}

func TestLegacyRegistrationMigrationIsVerifiedIdempotentAndAtomic(t *testing.T) {
	ctx := context.Background()
	service, db, root := repositoryFixture(t)
	legacyService, err := workmanagement.New(db)
	if err != nil {
		t.Fatal(err)
	}
	legacy, err := legacyService.RegisterProject(ctx, workmanagement.ProjectRegistration{Name: "Existing", Source: root}, "legacy-registration")
	if err != nil {
		t.Fatal(err)
	}
	before, err := db.EventsAfter(ctx, 0, 1000)
	if err != nil {
		t.Fatal(err)
	}
	if err := service.MigrateLegacyRepositories(ctx, []string{t.TempDir()}); err != nil {
		t.Fatal(err)
	}
	if _, err := service.ResolveRepository(ctx, legacy.ProjectID, "", true); !errors.Is(err, workmanagement.ErrRepositorySelection) {
		t.Fatalf("unresolved legacy selection = %v", err)
	}
	if err := service.MigrateLegacyRepositories(ctx, []string{root}); err != nil {
		t.Fatal(err)
	}
	if err := service.MigrateLegacyRepositories(ctx, []string{root}); err != nil {
		t.Fatal(err)
	}
	view, err := service.ProjectRepositories(ctx, legacy.ProjectID)
	if err != nil || len(view.Repositories) != 1 || view.Repositories[0].Membership.Revision != 1 || view.Project.SourceHash != legacy.SourceHash {
		t.Fatalf("migration changed identity or duplicated membership: %#v %v", view, err)
	}
	after, err := db.EventsAfter(ctx, 0, 1000)
	if err != nil || !reflect.DeepEqual(before, after[:len(before)]) {
		t.Fatal("migration rewrote legacy event bytes")
	}
	other := t.TempDir()
	repositoryGit(t, other, "init", "--initial-branch=main")
	created, err := service.RegisterProject(ctx, workmanagement.ProjectRegistration{Name: "Compatibility", Source: other}, "compatibility-registration")
	if err != nil {
		t.Fatal(err)
	}
	createdView, err := service.ProjectRepositories(ctx, created.ProjectID)
	if err != nil || len(createdView.Repositories) != 1 || created.ResourceVersion != 2 {
		t.Fatalf("legacy creation omitted atomic membership: %#v %v", createdView, err)
	}
}

type interruptedRepositoryStore struct {
	statestore.Store
	statestore.RepositoryStore
	failCompletion bool
}

func (store *interruptedRepositoryStore) CompleteCommand(ctx context.Context, request statestore.CompleteCommandRequest) (statestore.CommandEvidence, error) {
	if store.failCompletion {
		store.failCompletion = false
		return statestore.CommandEvidence{}, errors.New("simulated crash after append")
	}
	return store.Store.CompleteCommand(ctx, request)
}

func TestMembershipCommandRecoversCommittedEventWithoutDuplicatingRevision(t *testing.T) {
	ctx := context.Background()
	_, db, root := repositoryFixture(t)
	store := &interruptedRepositoryStore{Store: db, RepositoryStore: db}
	service, err := workmanagement.New(store)
	if err != nil {
		t.Fatal(err)
	}
	manager, err := gitadapter.New("")
	if err != nil {
		t.Fatal(err)
	}
	service.ConfigureRepositoryManager(manager)
	view := createRepositoryProject(t, service, "Recover", "recovery-project")
	request := workmanagement.MembershipRequest{ProjectID: view.Project.ProjectID, RepositoryPath: root, Label: "code", Role: statestore.RepositoryImplementation, ExpectedVersion: 1}
	store.failCompletion = true
	if _, err := service.SetMembership(ctx, request, "recovery-membership"); err == nil {
		t.Fatal("expected simulated crash")
	}
	recovered, err := service.SetMembership(ctx, request, "recovery-membership")
	if err != nil || recovered.Project.ResourceVersion != 2 || recovered.Repositories[0].Membership.Revision != 1 {
		t.Fatalf("recovery = %#v, %v", recovered, err)
	}
	// Idempotency keys belong to command scopes; another scope may use the same
	// key without adopting membership evidence or colliding in the event stream.
	defaults, err := service.UpdateProjectDefaults(ctx, workmanagement.ProjectDefaultsRequest{ProjectID: view.Project.ProjectID, ExpectedVersion: 2, Defaults: statestore.RepositorySettings{BaseRef: "main"}}, "recovery-membership")
	if err != nil || defaults.Project.ResourceVersion != 3 || defaults.Defaults.BaseRef != "main" {
		t.Fatalf("independent command scope = %#v, %v", defaults, err)
	}
	request.Label = "different request"
	if _, err := service.SetMembership(ctx, request, "recovery-membership"); err == nil {
		t.Fatal("changed idempotent request was accepted")
	}
}
