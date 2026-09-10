package nodes

import (
	"context"
	"crypto/sha256"
	"darkstar/src/core/workflow"
	"darkstar/src/ports/repository"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
)

var ErrWorkspaceNotFound = errors.New("workspace record not found")

type WorkspacePrepare struct{ Node workflow.WorkspacePrepareNode }

func (WorkspacePrepare) isHandler() {}

func (n WorkspacePrepare) Execute(ctx context.Context, inputs Inputs, services BuiltinServices) (json.RawMessage, error) {
	identity := services.Workspaces.Identity
	var source struct {
		ProjectID  string `json:"projectId"`
		SourceHash string `json:"sourceHash"`
	}
	if err := json.Unmarshal(inputs[n.Node.Executor.RepositoryInput], &source); err != nil {
		return nil, err
	}
	if source.ProjectID != identity.ProjectID || source.SourceHash != identity.SourceHash {
		return nil, errors.New("connect the run's repository resource to Prepare workspace")
	}
	id := fmt.Sprintf("workspace_%x", sha256.Sum256([]byte(identity.RunID+"\x00"+identity.NodeID)))
	p, err := services.Workspaces.Store.Load(ctx, id)
	manager := services.Workspaces.Repository
	if errors.Is(err, ErrWorkspaceNotFound) {
		p = Workspace{ID: id, RunID: identity.RunID, ProjectID: identity.ProjectID, Repository: filepath.Clean(identity.Root), Path: filepath.Clean(identity.Root)}
		switch plan := n.Node.Executor.Checkout.(type) {
		case workflow.CurrentCheckout:
			p.Mode = "current_checkout"
			p.BaseRef = "HEAD"
		case workflow.NewWorktree:
			p.Mode = "new_worktree"
			p.BaseRef = plan.BaseRef
			p.Branch = strings.ReplaceAll(plan.Branch, "{runId}", identity.RunID)
			// Agent tools must not traverse the daemon's private data directory.
			// Keep editable checkouts in project-local state; the authoritative
			// ownership record remains in the daemon database. Existing records
			// retain their original path across retries.
			p.Path = filepath.Join(identity.Root, ".darkstar", "worktrees", id)
		default:
			return nil, errors.New("choose a supported checkout mode")
		}
		base, resolveErr := manager.ResolveBase(ctx, repository.ResolveBaseRequest{RepositoryPath: p.Repository, BaseRef: p.BaseRef})
		if resolveErr != nil {
			return nil, fmt.Errorf("resolve base %q: %w", p.BaseRef, resolveErr)
		}
		p.BaseSHA = base.CommitSHA
		if p.Mode == "current_checkout" {
			observation, e := manager.Inspect(ctx, repository.InspectRequest{Path: p.Repository})
			if e != nil {
				return nil, e
			}
			for _, tree := range observation.Worktrees {
				if filepath.Clean(tree.Path) == filepath.Clean(base.Repository.Root) {
					p.Path = tree.Path
					if b, ok := tree.Checkout.(repository.BranchCheckout); ok {
						p.Branch = b.Name
					}
				}
			}
		}
		p, err = services.Workspaces.Store.Create(ctx, p)
		if err != nil {
			return nil, err
		}
	} else if err != nil {
		return nil, err
	}
	existingRef, _ := json.Marshal(p)
	if existing, resolveErr := services.Workspaces.Store.Resolve(ctx, existingRef); resolveErr == nil {
		return json.Marshal(map[string]any{"workspace": existing})
	}
	if p.Mode == "new_worktree" {
		base, resolveErr := manager.ResolveBase(ctx, repository.ResolveBaseRequest{RepositoryPath: p.Repository, BaseRef: p.BaseSHA})
		if resolveErr != nil {
			return nil, resolveErr
		}
		_, err = manager.Attach(ctx, repository.AttachRequest{RepositoryPath: p.Repository, WorktreePath: p.Path, OperationID: id, Owner: repository.Ownership{DeliveryLineID: identity.RunID, WorkItemID: identity.WorkItemID}, Branch: repository.CreateBranch{Name: p.Branch, Base: base}})
		if err != nil {
			return nil, err
		}
	}
	raw, _ := json.Marshal(p)
	verified, err := services.Workspaces.Store.Resolve(ctx, raw)
	if err != nil {
		return nil, err
	}
	return json.Marshal(map[string]any{"workspace": verified})

}
