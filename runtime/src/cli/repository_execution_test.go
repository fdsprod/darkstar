package cli

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	gitadapter "darkstar/src/adapters/repository/git"
	"darkstar/src/core/config"
	"darkstar/src/core/nodes"
	"darkstar/src/core/runexecution"
	"darkstar/src/core/workflow"
	"darkstar/src/ports/repository"
	"darkstar/src/ports/statestore"
)

type executionRepositoryStore struct {
	repositories map[string]statestore.RepositoryRecord
	memberships  map[string]statestore.RepositoryMembership
	removed      bool
}

func (s *executionRepositoryStore) Repository(_ context.Context, id string) (statestore.RepositoryRecord, error) {
	record, ok := s.repositories[id]
	if !ok {
		return record, errors.New("missing repository")
	}
	return record, nil
}

func (s *executionRepositoryStore) MembershipRevision(_ context.Context, project, repository string, revision uint64) (statestore.RepositoryMembership, error) {
	membership, ok := s.memberships[repository]
	if !ok || membership.ProjectID != project || membership.Revision != revision {
		return membership, errors.New("missing historical membership")
	}
	return membership, nil
}

func (s *executionRepositoryStore) ProjectRepositories(_ context.Context, project string) ([]statestore.ProjectRepository, error) {
	members := []statestore.ProjectRepository{}
	for _, member := range s.memberships {
		if member.ProjectID == project {
			members = append(members, statestore.ProjectRepository{Repository: s.repositories[member.RepositoryID], Membership: member})
		}
	}
	return members, nil
}

func (s *executionRepositoryStore) ProjectRepositoryConfiguration(context.Context, string) (statestore.ProjectRepositoryConfiguration, error) {
	return statestore.ProjectRepositoryConfiguration{}, nil
}

func (s *executionRepositoryStore) ResolveRepository(_ context.Context, project, id string, writer bool) (statestore.ProjectRepository, error) {
	selected := []statestore.ProjectRepository{}
	for _, member := range s.memberships {
		if !s.removed && member.ProjectID == project && member.Status == statestore.MembershipActive && (id == "" || member.RepositoryID == id) {
			selected = append(selected, statestore.ProjectRepository{Repository: s.repositories[member.RepositoryID], Membership: member})
		}
	}
	if len(selected) != 1 || (writer && selected[0].Membership.Role != statestore.RepositoryImplementation) {
		return statestore.ProjectRepository{}, errors.New("select exactly one active repository")
	}
	return selected[0], nil
}

func executionRepository(t *testing.T, id string) statestore.RepositoryRecord {
	t.Helper()
	root := t.TempDir()
	for _, args := range [][]string{{"init"}, {"config", "user.email", "test@example.invalid"}, {"config", "user.name", "Test"}, {"commit", "--allow-empty", "-m", "initial"}} {
		output, err := exec.Command("git", append([]string{"-C", root}, args...)...).CombinedOutput()
		if err != nil {
			t.Fatalf("git: %v: %s", err, output)
		}
	}
	manager, err := gitadapter.New("")
	if err != nil {
		t.Fatal(err)
	}
	observation, err := manager.Inspect(context.Background(), repository.InspectRequest{Path: root})
	if err != nil {
		t.Fatal(err)
	}
	return statestore.RepositoryRecord{RepositoryID: id, Root: observation.Repository.Root, CommonGitDir: observation.Repository.CommonGitDir, IdentityKey: observation.Repository.CommonGitDir}
}

func repositoryAttempt(t *testing.T, record statestore.RepositoryRecord) runexecution.AttemptRequestContext {
	t.Helper()
	resolved, err := config.ResolveRepositorySettings(config.RepositorySettingsLayer{Scope: config.ScopeMembership, Reference: "membership@1", Settings: statestore.RepositorySettings{ConfigurationRoot: record.Root, BaseRef: "HEAD", WorktreeBase: "custom-trees"}})
	if err != nil {
		t.Fatal(err)
	}
	resource, err := json.Marshal(repositoryResourceValue{ProjectID: "project", SourceHash: "project-identity", Binding: &nodes.RepositoryBinding{Repository: record, MembershipRevision: 1, Configuration: resolved}})
	if err != nil {
		t.Fatal(err)
	}
	return runexecution.AttemptRequestContext{
		Project: statestore.ProjectProjection{ProjectID: "project", Status: statestore.ProjectActive, SourceHash: "project-identity"},
		Run:     statestore.RunProjection{RunID: "run_one"}, WorkItem: statestore.WorkItemProjection{WorkItemID: "work_one"}, Attempt: statestore.AttemptProjection{NodeID: "prepare"},
		Node:             workflow.WorkspacePrepareNode{Executor: workflow.WorkspacePrepareExecutor{RepositoryInput: "repository", Checkout: workflow.NewWorktree{BaseRef: "project_default", Branch: "darkstar/{runId}"}}},
		NodeInputs:       map[workflow.Identifier]json.RawMessage{"repository": resource},
		ExecutionContext: statestore.RunExecutionContext{RunInputs: map[string]json.RawMessage{"repository": resource}},
	}
}

func TestMembershipWorkspaceFreezesRepositoryAndRejectsRetargeting(t *testing.T) {
	ctx := context.Background()
	record := executionRepository(t, "repo_one")
	other := executionRepository(t, "repo_two")
	store := &executionRepositoryStore{repositories: map[string]statestore.RepositoryRecord{record.RepositoryID: record, other.RepositoryID: other}, memberships: map[string]statestore.RepositoryMembership{}}
	for _, repository := range []statestore.RepositoryRecord{record, other} {
		store.memberships[repository.RepositoryID] = statestore.RepositoryMembership{ProjectID: "project", RepositoryID: repository.RepositoryID, Revision: 1, Status: statestore.MembershipActive, Role: statestore.RepositoryImplementation}
	}
	wiring := &daemonProviderWiring{projectRoot: t.TempDir(), toolDatabase: filepath.Join(t.TempDir(), "tools.db"), repositories: store, repositoryStore: store}
	request := repositoryAttempt(t, record)
	result, err := wiring.ExecuteWorkspaceNode(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	var outputs map[string]json.RawMessage
	if err := json.Unmarshal(result, &outputs); err != nil {
		t.Fatal(err)
	}
	prepared, err := wiring.resolvePreparedWorkspace(ctx, request, outputs["workspace"])
	if err != nil {
		t.Fatal(err)
	}
	if !sameRepositoryPath(prepared.Repository, record.Root) || filepath.Dir(prepared.Path) != filepath.Join(record.Root, "custom-trees") || prepared.Binding.MembershipRevision != 1 {
		t.Fatalf("wrong frozen workspace: %#v", prepared)
	}
	// Current membership resolution deliberately fails; exact historical access
	// must still resume the same workspace after removal or configuration edits.
	store.removed = true
	again, err := wiring.ExecuteWorkspaceNode(ctx, request)
	if err != nil || string(again) != string(result) {
		t.Fatalf("frozen resume: %s, %v", again, err)
	}
	retarget := repositoryAttempt(t, other)
	retarget.Run.RunID = "run_two"
	if _, err := wiring.ExecuteWorkspaceNode(ctx, retarget); err == nil || !strings.Contains(err.Error(), "already bound") {
		t.Fatalf("work-item retarget error: %v", err)
	}
	forged := request
	forged.ExecutionContext.RunInputs = nil
	if _, err := wiring.ExecuteWorkspaceNode(ctx, forged); err == nil || !strings.Contains(err.Error(), "frozen") {
		t.Fatalf("unadmitted resource error: %v", err)
	}
	wrongRun := request
	wrongRun.Run.RunID = "unrelated"
	if _, err := wiring.resolvePreparedWorkspace(ctx, wrongRun, outputs["workspace"]); err == nil {
		t.Fatal("accepted another run's prepared workspace")
	}
	member := store.memberships[record.RepositoryID]
	member.Role = statestore.RepositoryReadOnly
	store.memberships[record.RepositoryID] = member
	if _, err := wiring.ExecuteWorkspaceNode(ctx, request); err == nil || !strings.Contains(err.Error(), "implementation") {
		t.Fatalf("read-only writer error: %v", err)
	}
}

func TestRepositoryAdmissionSelectsExplicitMemberAndFreezesSettings(t *testing.T) {
	record := statestore.RepositoryRecord{RepositoryID: "repo_one", Root: t.TempDir()}
	other := statestore.RepositoryRecord{RepositoryID: "repo_two", Root: t.TempDir()}
	store := &executionRepositoryStore{repositories: map[string]statestore.RepositoryRecord{record.RepositoryID: record, other.RepositoryID: other}, memberships: map[string]statestore.RepositoryMembership{}}
	wiring := &daemonProviderWiring{repositories: store, repositoryStore: store}
	document := workflow.Document{Spec: workflow.Spec{Inputs: map[workflow.Identifier]workflow.ValueDeclaration{"repository": {Type: workflow.ValueRepository, Resource: &workflow.Resource{Source: workflow.RepositoryResource{}}}}}}
	project := statestore.ProjectProjection{ProjectID: "project"}
	if _, err := wiring.resolveRepositoryResources(t.Context(), document, project, nil); err == nil {
		t.Fatal("repositoryless project silently selected a repository")
	}
	store.memberships[record.RepositoryID] = statestore.RepositoryMembership{ProjectID: project.ProjectID, RepositoryID: record.RepositoryID, Revision: 1, Status: statestore.MembershipActive, Settings: statestore.RepositorySettings{BaseRef: "develop"}}
	values, err := wiring.resolveRepositoryResources(t.Context(), document, project, nil)
	if err != nil {
		t.Fatal(err)
	}
	var selected repositoryResourceValue
	if err := json.Unmarshal(values["repository"], &selected); err != nil {
		t.Fatal(err)
	}
	if selected.Binding.Repository.RepositoryID != record.RepositoryID || selected.Binding.Configuration.Settings.BaseRef != "develop" || selected.Binding.Configuration.Sources["/baseRef"].Scope != "membership" {
		t.Fatalf("sole membership resolution: %#v", selected)
	}
	store.memberships[other.RepositoryID] = statestore.RepositoryMembership{ProjectID: project.ProjectID, RepositoryID: other.RepositoryID, Revision: 1, Status: statestore.MembershipActive}
	if _, err := wiring.resolveRepositoryResources(t.Context(), document, project, nil); err == nil {
		t.Fatal("multi-repository project silently selected a repository")
	}
	values, err = wiring.resolveRepositoryResources(t.Context(), document, project, map[workflow.Identifier]string{"repository": other.RepositoryID})
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(values["repository"], &selected); err != nil {
		t.Fatal(err)
	}
	if selected.Binding.Repository.RepositoryID != other.RepositoryID {
		t.Fatal("explicit membership selection ignored")
	}
	for _, selections := range []map[workflow.Identifier]string{{"unknown": record.RepositoryID}, {"repository": " "}, {"repository": "unknown_repository"}} {
		if _, err := wiring.resolveRepositoryResources(t.Context(), document, project, selections); err == nil {
			t.Fatalf("invalid repository selection accepted: %#v", selections)
		}
	}
	for _, declaration := range []workflow.ValueDeclaration{
		{Type: workflow.ValueRepository},
		{Type: workflow.ValueRepository, Resource: &workflow.Resource{Source: workflow.ConstantResource{Value: json.RawMessage(`{"projectId":"project","sourceHash":"legacy"}`)}}},
	} {
		document.Spec.Inputs["repository"] = declaration
		if _, err := wiring.resolveRepositoryResources(t.Context(), document, project, nil); err == nil {
			t.Fatal("caller-authored repository authority accepted")
		}
	}
}

func TestLegacySourceHashCannotGrantNewRepositoryAccess(t *testing.T) {
	root := t.TempDir()
	boundAt := time.Now().UTC()
	store := &executionRepositoryStore{
		repositories: map[string]statestore.RepositoryRecord{"repo": {RepositoryID: "repo", Root: root}},
		memberships:  map[string]statestore.RepositoryMembership{"repo": {ProjectID: "project", RepositoryID: "repo", Revision: 1, UpdatedAt: boundAt, Status: statestore.MembershipActive, Role: statestore.RepositoryReadOnly}},
	}
	wiring := &daemonProviderWiring{projectRoot: root, repositories: store, repositoryStore: store}
	request := runexecution.AttemptRequestContext{Project: statestore.ProjectProjection{ProjectID: "project", Status: statestore.ProjectActive, SourceHash: fmt.Sprintf("%x", sha256.Sum256([]byte(root)))}}
	resource, err := json.Marshal(repositoryResourceValue{ProjectID: request.Project.ProjectID, SourceHash: request.Project.SourceHash})
	if err != nil {
		t.Fatal(err)
	}
	request.Run.CreatedAt = boundAt.Add(time.Second)
	if _, err := wiring.resourceRepository(t.Context(), request, resource, true); err == nil {
		t.Fatal("new run bypassed read-only membership with a legacy source hash")
	}
	request.Run.CreatedAt = boundAt.Add(-time.Second)
	if binding, err := wiring.resourceRepository(t.Context(), request, resource, true); err != nil || binding != nil {
		t.Fatalf("historical legacy run lost its original repository: %#v, %v", binding, err)
	}
}

func TestRepositorylessReasoningUsesIsolatedRunWorkspace(t *testing.T) {
	wiring := &daemonProviderWiring{projectRoot: t.TempDir(), repositories: &executionRepositoryStore{}}
	request := runexecution.AttemptRequestContext{Run: statestore.RunProjection{RunID: "run_repositoryless_test"}, Project: statestore.ProjectProjection{ProjectID: "project", Status: statestore.ProjectActive}, Node: workflow.ReasoningNode{}}
	root, err := wiring.attemptRepositoryRoot(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if root == wiring.projectRoot || !filepath.IsAbs(root) {
		t.Fatalf("repository leaked through repositoryless workspace: %s", root)
	}
	if _, err := os.Stat(root); err != nil {
		t.Fatal(err)
	}
	request.Run.RunID = "../escape"
	if _, err := wiring.attemptRepositoryRoot(context.Background(), request); err == nil {
		t.Fatal("unsafe run ID accepted")
	}
}
