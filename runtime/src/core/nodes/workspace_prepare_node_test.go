package nodes

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"

	"darkstar/src/core/workflow"
	"darkstar/src/ports/repository"
)

type workspaceRepositoryFake struct {
	store    *workspaceStoreFake
	refs     []string
	attached []repository.AttachRequest
	err      error
}

func (r *workspaceRepositoryFake) Inspect(context.Context, repository.InspectRequest) (repository.Observation, error) {
	return repository.Observation{Worktrees: []repository.Worktree{{Path: r.store.record.Path, Checkout: repository.BranchCheckout{Name: "feature"}}}}, nil
}
func (r *workspaceRepositoryFake) ResolveBase(_ context.Context, q repository.ResolveBaseRequest) (repository.BaseRevision, error) {
	r.refs = append(r.refs, q.BaseRef)
	return repository.BaseRevision{CommitSHA: "frozen-sha", Repository: repository.Identity{Root: q.RepositoryPath}}, r.err
}
func (r *workspaceRepositoryFake) Attach(_ context.Context, q repository.AttachRequest) (repository.Worktree, error) {
	r.attached = append(r.attached, q)
	if r.err != nil {
		return repository.Worktree{}, r.err
	}
	r.store.resolveErr = nil
	return repository.Worktree{}, nil
}

func TestWorkspacePreparationFreezesBaseAndReusesDurableRecord(t *testing.T) {
	root := t.TempDir()
	store := &workspaceStoreFake{loadErr: ErrWorkspaceNotFound, resolveErr: errors.New("not attached")}
	repo := &workspaceRepositoryFake{store: store}
	services := BuiltinServices{Workspaces: WorkspaceServices{Identity: WorkspaceIdentity{Root: root, RunID: "run-1", NodeID: "prepare", ProjectID: "project", SourceHash: "hash", WorkItemID: "work"}, Store: store, Repository: repo}}
	n := WorkspacePrepare{Node: workflow.WorkspacePrepareNode{Executor: workflow.WorkspacePrepareExecutor{RepositoryInput: "repository", Checkout: workflow.NewWorktree{BaseRef: "release", Branch: "task/{runId}"}}}}
	inputs := Inputs{"repository": json.RawMessage(`{"projectId":"project","sourceHash":"hash"}`)}
	first, err := n.Execute(t.Context(), inputs, services)
	if err != nil {
		t.Fatal(err)
	}
	if store.created != 1 || len(repo.attached) != 1 || len(repo.refs) != 2 || repo.refs[0] != "release" || repo.refs[1] != "frozen-sha" {
		t.Fatal("base was not frozen before attach")
	}
	if store.record.Path != filepath.Join(root, ".darkstar", "worktrees", store.record.ID) || store.record.Branch != "task/run-1" {
		t.Fatalf("record = %#v", store.record)
	}
	attach := repo.attached[0]
	if attach.Owner.DeliveryLineID != "run-1" || attach.Owner.WorkItemID != "work" {
		t.Fatal("lost durable ownership")
	}
	// A new configuration on recovery must not replace the persisted checkout.
	n.Node.Executor.Checkout = workflow.NewWorktree{BaseRef: "new-head", Branch: "other"}
	second, err := n.Execute(t.Context(), inputs, services)
	if err != nil {
		t.Fatal(err)
	}
	if string(first) != string(second) || store.created != 1 || len(repo.attached) != 1 || len(repo.refs) != 2 {
		t.Fatal("retry recreated or changed the prepared worktree")
	}
}

func TestWorkspacePreparationRejectsForeignInputBeforeSideEffects(t *testing.T) {
	n := WorkspacePrepare{Node: workflow.WorkspacePrepareNode{Executor: workflow.WorkspacePrepareExecutor{RepositoryInput: "repository"}}}
	_, err := n.Execute(t.Context(), Inputs{"repository": json.RawMessage(`{"projectId":"foreign"}`)}, BuiltinServices{Workspaces: WorkspaceServices{Identity: WorkspaceIdentity{ProjectID: "ours"}}})
	if err == nil {
		t.Fatal("foreign repository accepted")
	}
}
