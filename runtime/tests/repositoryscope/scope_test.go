package repositoryscope_test

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

	"darkstar/src/adapters/statestore/sqlite"
	"darkstar/src/core/config"
	"darkstar/src/core/identity"
	"darkstar/src/core/repositoryscope"
	"darkstar/src/core/workmanagement"
	"darkstar/src/ports/repository"
	"darkstar/src/ports/repositorysnapshot"
	"darkstar/src/ports/statestore"
)

type manager struct{ repository.Manager }

func (manager) Inspect(_ context.Context, r repository.InspectRequest) (repository.Observation, error) {
	return repository.Observation{Repository: repository.Identity{Root: r.Path, CommonGitDir: filepath.Join(r.Path, ".git")}}, nil
}

type exporter struct {
	mu            sync.Mutex
	sha           string
	resolves      int
	exports       []repositorysnapshot.ExportRequest
	fail          bool
	corrupt       bool
	beforeResolve func()
}

func (e *exporter) Resolve(_ context.Context, _ repositorysnapshot.ResolveRequest) (repositorysnapshot.ResolvedRevision, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.resolves++
	if e.beforeResolve != nil {
		e.beforeResolve()
	}
	return repositorysnapshot.ResolvedRevision{CommitSHA: e.sha, TreeSHA: strings.Repeat("b", 40)}, nil
}
func (e *exporter) Materialize(_ context.Context, r repositorysnapshot.ExportRequest) (repositorysnapshot.Evidence, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.exports = append(e.exports, r)
	if e.fail {
		return repositorysnapshot.Evidence{}, errors.New("snapshot disk unavailable")
	}
	return repositorysnapshot.Evidence{CacheKey: statestore.RepositoryScopeContentDigest(r), Root: filepath.Join(r.Root, "cache"), ManifestDigest: strings.Repeat("c", 64), Exclusions: []repositorysnapshot.Exclusion{}}, nil
}
func (e *exporter) Verify(context.Context, repositorysnapshot.Evidence) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.corrupt {
		return errors.New("retained snapshot was modified")
	}
	return nil
}

func fixture(t *testing.T, count int) (*sqlite.Database, *workmanagement.Service, *repositoryscope.Service, *exporter, workmanagement.ProjectRepositoriesView) {
	t.Helper()
	ctx := context.Background()
	db, err := sqlite.Open(ctx, filepath.Join(t.TempDir(), "state.db"), sqlite.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = db.Close()
	})
	members, err := workmanagement.New(db)
	if err != nil {
		t.Fatal(err)
	}
	members.ConfigureRepositoryManager(manager{})
	project, err := members.CreateProjectV2(ctx, workmanagement.CreateProjectRequest{Name: "investigation"}, "project-create")
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < count; i++ {
		project, err = members.SetMembership(ctx, workmanagement.MembershipRequest{ProjectID: project.Project.ProjectID, RepositoryPath: t.TempDir(), Label: identity.Random("label_"), Role: statestore.RepositoryReadOnly, ExpectedVersion: project.Project.ResourceVersion, Settings: statestore.RepositorySettings{PathScope: []string{"src"}}}, identity.Random("key_"))
		if err != nil {
			t.Fatal(err)
		}
	}
	exports := &exporter{sha: strings.Repeat("a", 40)}
	scopes, err := repositoryscope.New(db, exports)
	if err != nil {
		t.Fatal(err)
	}
	return db, members, scopes, exports, project
}

func request(project workmanagement.ProjectRepositoriesView) repositoryscope.PrepareRequest {
	result := repositoryscope.PrepareRequest{ProjectID: project.Project.ProjectID, Repositories: []repositoryscope.RepositorySelection{}}
	for _, item := range project.Repositories {
		result.Repositories = append(result.Repositories, repositoryscope.RepositorySelection{RepositoryID: item.Repository.RepositoryID, Ref: "main"})
	}
	return result
}

func TestScopeExplicitZeroAndValidation(t *testing.T) {
	db, _, service, _, project := fixture(t, 0)
	ctx := context.Background()
	if _, err := service.Prepare(ctx, repositoryscope.PrepareRequest{ProjectID: project.Project.ProjectID}, "implicit"); !errors.Is(err, repositoryscope.ErrInvalidRequest) {
		t.Fatalf("implicit empty scope: %v", err)
	}
	view, err := service.Prepare(ctx, request(project), "explicit-none")
	if err != nil || view.Scope.Mode != statestore.InvestigationScopeNone || view.Preparation.Status != statestore.RepositoryScopeReady || view.Scope.Repositories == nil {
		t.Fatalf("explicit empty = %#v %v", view, err)
	}
	bound, err := service.BindAttempt(ctx, view.Scope.ScopeID, identity.Random("attempt_"), []string{})
	if err != nil || bound.Binding.RepositoryIDs == nil || len(bound.Repositories) != 0 {
		t.Fatalf("empty attempt: %#v %v", bound, err)
	}
	if _, err = db.SQL().ExecContext(ctx, `UPDATE repository_scopes SET digest=digest WHERE scope_id=?`, view.Scope.ScopeID); err == nil {
		t.Fatal("immutable scope update allowed")
	}
	if err = db.RebuildProjections(ctx); err != nil {
		t.Fatal(err)
	}
	after, err := service.Get(ctx, view.Scope.ScopeID)
	if err != nil || !reflect.DeepEqual(view, after) {
		t.Fatalf("replay changed frozen resource: %#v %v", after, err)
	}
}

func TestScopeRetryPinsCommitConfigurationAndRemovedMembership(t *testing.T) {
	db, members, service, exports, project := fixture(t, 2)
	ctx := context.Background()
	exports.fail = true
	req := request(project)
	first, err := service.Prepare(ctx, req, "frozen-retry")
	if err != nil || first.Preparation.Status != statestore.RepositoryScopeBlocked || len(first.Scope.Repositories) != 2 {
		t.Fatalf("initial freeze: %#v %v", first, err)
	}
	exports.fail = false
	exports.sha = strings.Repeat("d", 40)
	member := project.Repositories[0]
	if _, err = members.RemoveMembership(ctx, workmanagement.RemoveMembershipRequest{ProjectID: project.Project.ProjectID, RepositoryID: member.Repository.RepositoryID, ExpectedVersion: project.Project.ResourceVersion, ExpectedMembershipRevision: member.Membership.Revision}, "remove-after-freeze"); err != nil {
		t.Fatal(err)
	}
	restarted, err := repositoryscope.New(db, exports)
	if err != nil {
		t.Fatal(err)
	}
	ready, err := restarted.Prepare(ctx, req, "frozen-retry")
	if err != nil || ready.Preparation.Status != statestore.RepositoryScopeReady || !reflect.DeepEqual(first.Scope, ready.Scope) || exports.resolves != 2 {
		t.Fatalf("retry retargeted scope: %#v %v resolves=%d", ready, err, exports.resolves)
	}
	for _, r := range exports.exports {
		if r.CommitSHA != strings.Repeat("a", 40) || !reflect.DeepEqual(r.PathScope, []string{"src"}) {
			t.Fatalf("export did not use frozen inputs: %#v", r)
		}
	}
	attempt := identity.Random("attempt_")
	bound, err := restarted.BindAttempt(ctx, ready.Scope.ScopeID, attempt, []string{member.Repository.RepositoryID})
	if err != nil || len(bound.Repositories) != 1 || bound.Repositories[0].Membership.Status != statestore.MembershipActive {
		t.Fatalf("historical subset unavailable: %#v %v", bound, err)
	}
	if _, err = restarted.BindAttempt(ctx, ready.Scope.ScopeID, attempt, []string{}); !errors.Is(err, repositoryscope.ErrScopeConflict) {
		t.Fatalf("attempt binding mutated: %v", err)
	}
	exports.corrupt = true
	if _, err = restarted.Attempt(ctx, attempt); !errors.Is(err, repositoryscope.ErrScopeUnavailable) {
		t.Fatalf("modified evidence reused: %v", err)
	}
	exports.corrupt = false
	req.Repositories[0].Ref = "different"
	if _, err = restarted.Prepare(ctx, req, "frozen-retry"); !errors.Is(err, repositoryscope.ErrScopeConflict) {
		t.Fatalf("key changed request: %v", err)
	}
}

func TestScopeConcurrentPreparationAndStaleMembership(t *testing.T) {
	_, members, service, exports, project := fixture(t, 1)
	ctx := context.Background()
	req := request(project)
	var wg sync.WaitGroup
	results := make(chan repositoryscope.View, 2)
	failures := make(chan error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			result, err := service.Prepare(ctx, req, "same-key")
			results <- result
			failures <- err
		}()
	}
	wg.Wait()
	close(results)
	close(failures)
	for err := range failures {
		if err != nil {
			t.Fatal(err)
		}
	}
	var previous string
	for value := range results {
		if value.Preparation.Status != statestore.RepositoryScopeReady || (previous != "" && value.Scope.Digest != previous) {
			t.Fatalf("concurrent preparation: %#v", value)
		}
		previous = value.Scope.Digest
	}
	exports.beforeResolve = func() {
		_, err := members.RemoveMembership(ctx, workmanagement.RemoveMembershipRequest{ProjectID: project.Project.ProjectID, RepositoryID: project.Repositories[0].Repository.RepositoryID, ExpectedVersion: project.Project.ResourceVersion, ExpectedMembershipRevision: 1}, "remove-before-freeze")
		if err != nil {
			t.Fatal(err)
		}
	}
	if _, err := service.Prepare(ctx, req, "racing-detach"); !errors.Is(err, statestore.ErrRepositoryScopeConflict) {
		t.Fatalf("mixed/stale membership admitted: %v", err)
	}
}

func TestScopeSettingsConflictAndStoreCAS(t *testing.T) {
	db, members, service, _, project := fixture(t, 1)
	ctx := context.Background()
	service.SetConfigurationResolver(func(ctx context.Context, _ statestore.ProjectProjection, member statestore.ProjectRepository) (config.ResolvedRepositorySettings, error) {
		_, err := members.UpdateProjectDefaults(ctx, workmanagement.ProjectDefaultsRequest{ProjectID: project.Project.ProjectID, ExpectedVersion: project.Project.ResourceVersion, Defaults: statestore.RepositorySettings{BaseRef: "changed"}}, "defaults-during-resolution")
		if err != nil {
			return config.ResolvedRepositorySettings{}, err
		}
		return config.ResolveRepositorySettings(config.RepositorySettingsLayer{Scope: config.ScopeMembership, Reference: "frozen", Settings: member.Membership.Settings})
	})
	if _, err := service.Prepare(ctx, request(project), "settings-race"); !errors.Is(err, statestore.ErrRepositoryScopeConflict) {
		t.Fatalf("mixed configuration admitted: %v", err)
	}
	service.SetConfigurationResolver(nil)
	view, err := service.Prepare(ctx, request(project), "valid-after-settings")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.SetRepositoryScopePreparation(ctx, view.Scope.ScopeID, 1, statestore.RepositoryScopeBlocked, "stale failure"); !errors.Is(err, statestore.ErrRepositoryScopeConflict) {
		t.Fatalf("stale preparation overwrote ready: %v", err)
	}
	if _, err = db.SetRepositoryScopePreparation(ctx, identity.Random("scope_"), 1, statestore.RepositoryScopeReady, ""); !errors.Is(err, statestore.ErrNotFound) {
		t.Fatalf("invented preparation: %v", err)
	}
	if _, err = db.SetRepositoryScopePreparation(ctx, view.Scope.ScopeID, view.Preparation.Revision, statestore.RepositoryScopeBlocked, ""); err == nil {
		t.Fatal("blocked status without reason accepted")
	}
	changed := view.Scope
	changed.ScopeID = identity.Random("scope_")
	changed.Repositories = append([]statestore.FrozenRepositoryScopeEntry{}, changed.Repositories...)
	changed.Repositories[0].ConfigurationDigest = strings.Repeat("f", 64)
	changed.Digest = statestore.RepositoryScopeDigest(changed)
	if _, err = db.FreezeRepositoryScope(ctx, changed); err == nil {
		t.Fatal("configuration digest mismatch admitted")
	}
	changedEvidence := view.Evidence[0]
	changedEvidence.Evidence.Root = filepath.Join(t.TempDir(), "different")
	if err = db.SaveRepositoryScopeEvidence(ctx, changedEvidence); !errors.Is(err, statestore.ErrRepositoryScopeConflict) {
		t.Fatalf("evidence descriptor overwritten: %v", err)
	}
	if _, err = db.SQL().ExecContext(ctx, `DELETE FROM repository_scope_evidence WHERE scope_id=?`, view.Scope.ScopeID); err == nil {
		t.Fatal("retained evidence deleted")
	}
}

func TestScopeRejectsDuplicateSelectionAndReopensDurableBinding(t *testing.T) {
	db, _, service, exports, project := fixture(t, 1)
	ctx := context.Background()
	req := request(project)
	req.Repositories = append(req.Repositories, req.Repositories[0])
	if _, err := service.Prepare(ctx, req, "duplicates"); !errors.Is(err, repositoryscope.ErrInvalidRequest) {
		t.Fatalf("duplicate selection: %v", err)
	}
	view, err := service.Prepare(ctx, request(project), "durable-binding")
	if err != nil {
		t.Fatal(err)
	}
	attempt := identity.Random("attempt_")
	bound, err := service.BindAttempt(ctx, view.Scope.ScopeID, attempt, []string{project.Repositories[0].Repository.RepositoryID})
	if err != nil {
		t.Fatal(err)
	}
	var sequence int
	var name, path string
	if err = db.SQL().QueryRowContext(ctx, `PRAGMA database_list`).Scan(&sequence, &name, &path); err != nil {
		t.Fatal(err)
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := sqlite.Open(ctx, path, sqlite.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = reopened.Close()
	})
	restarted, err := repositoryscope.New(reopened, exports)
	if err != nil {
		t.Fatal(err)
	}
	after, err := restarted.Attempt(ctx, attempt)
	if err != nil || !reflect.DeepEqual(bound, after) {
		t.Fatalf("restarted binding = %#v, %v", after, err)
	}
	var events int
	if err = reopened.SQL().QueryRowContext(ctx, `SELECT count(*) FROM events WHERE kind='repository_scope.attempt_bound'`).Scan(&events); err != nil || events != 1 {
		t.Fatalf("binding audit events = %d, %v", events, err)
	}
}
