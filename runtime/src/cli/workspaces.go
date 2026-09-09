package cli

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	gitadapter "darkstar/src/adapters/repository/git"
	"darkstar/src/core/runexecution"
	"darkstar/src/core/workflow"
	"darkstar/src/ports/repository"
	"darkstar/src/ports/statestore"
	_ "modernc.org/sqlite"
)

type preparedWorkspace struct {
	ID         string `json:"id"`
	RunID      string `json:"runId"`
	ProjectID  string `json:"projectId"`
	Repository string `json:"repository"`
	Path       string `json:"path"`
	Branch     string `json:"branch"`
	BaseSHA    string `json:"baseSha"`
	BaseRef    string `json:"baseRef"`
	Mode       string `json:"mode"`
}

func (w *daemonProviderWiring) workspaceDB(ctx context.Context) (*sql.DB, error) {
	db, err := sql.Open("sqlite", w.toolDatabase)
	if err != nil {
		return nil, err
	}
	_, err = db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS workflow_workspaces(id TEXT PRIMARY KEY,run_id TEXT NOT NULL,record TEXT NOT NULL)`)
	if err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}
func (w *daemonProviderWiring) authorizeWorkspaceProject(r runexecution.AttemptRequestContext) error {
	root := filepath.Clean(w.projectRoot)
	if !filepath.IsAbs(root) || r.Project.Status != statestore.ProjectActive || r.Project.SourceHash != fmt.Sprintf("%x", sha256.Sum256([]byte(root))) {
		return errors.New("repository is not the authorized daemon project")
	}
	return nil
}
func (w *daemonProviderWiring) resolvePreparedWorkspace(ctx context.Context, r runexecution.AttemptRequestContext, raw json.RawMessage) (preparedWorkspace, error) {
	var ref struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(raw, &ref); err != nil {
		return preparedWorkspace{}, err
	}
	db, err := w.workspaceDB(ctx)
	if err != nil {
		return preparedWorkspace{}, err
	}
	defer db.Close()
	var encoded string
	if err = db.QueryRowContext(ctx, `SELECT record FROM workflow_workspaces WHERE id=? AND run_id=?`, ref.ID, r.Run.RunID).Scan(&encoded); err != nil {
		return preparedWorkspace{}, errors.New("connect a workspace prepared by this run")
	}
	var p preparedWorkspace
	if err = json.Unmarshal([]byte(encoded), &p); err != nil {
		return p, err
	}
	if err = w.authorizeWorkspaceProject(r); err != nil {
		return p, err
	}
	if p.ProjectID != r.Project.ProjectID || filepath.Clean(p.Repository) != filepath.Clean(w.projectRoot) {
		return p, errors.New("prepared workspace belongs to another repository")
	}
	manager, err := gitadapter.New("")
	if err != nil {
		return p, err
	}
	observed, err := manager.Inspect(ctx, repository.InspectRequest{Path: p.Repository})
	if err != nil {
		return p, err
	}
	for _, tree := range observed.Worktrees {
		if filepath.Clean(tree.Path) == filepath.Clean(p.Path) {
			branch := ""
			if b, ok := tree.Checkout.(repository.BranchCheckout); ok {
				branch = b.Name
			}
			if tree.HeadSHA != p.BaseSHA || branch != p.Branch {
				return p, errors.New("workspace branch or HEAD changed since preparation; start a new run or restore the expected checkout")
			}
			return p, nil
		}
	}
	return p, errors.New("prepared worktree no longer exists")
}

// ExecuteWorkspaceNode runs deterministic workspace operations under daemon
// ownership. No model is asked to create worktrees or claim checks succeeded.
func (w *daemonProviderWiring) ExecuteWorkspaceNode(ctx context.Context, r runexecution.AttemptRequestContext) (json.RawMessage, error) {
	if err := w.authorizeWorkspaceProject(r); err != nil {
		return nil, err
	}
	switch n := r.Node.(type) {
	case workflow.WorkspacePrepareNode:
		var source struct {
			ProjectID  string `json:"projectId"`
			SourceHash string `json:"sourceHash"`
		}
		if err := json.Unmarshal(r.NodeInputs[n.Executor.RepositoryInput], &source); err != nil {
			return nil, err
		}
		if source.ProjectID != r.Project.ProjectID || source.SourceHash != r.Project.SourceHash {
			return nil, errors.New("connect the run's repository resource to Prepare workspace")
		}
		id := fmt.Sprintf("workspace_%x", sha256.Sum256([]byte(r.Run.RunID+"\x00"+r.Attempt.NodeID)))
		db, err := w.workspaceDB(ctx)
		if err != nil {
			return nil, err
		}
		defer db.Close()
		manager, err := gitadapter.New("")
		if err != nil {
			return nil, err
		}
		var encoded string
		err = db.QueryRowContext(ctx, `SELECT record FROM workflow_workspaces WHERE id=?`, id).Scan(&encoded)
		var p preparedWorkspace
		if errors.Is(err, sql.ErrNoRows) {
			p = preparedWorkspace{ID: id, RunID: r.Run.RunID, ProjectID: r.Project.ProjectID, Repository: filepath.Clean(w.projectRoot), Path: filepath.Clean(w.projectRoot)}
			switch plan := n.Executor.Checkout.(type) {
			case workflow.CurrentCheckout:
				p.Mode = "current_checkout"
				p.BaseRef = "HEAD"
			case workflow.NewWorktree:
				p.Mode = "new_worktree"
				p.BaseRef = plan.BaseRef
				p.Branch = strings.ReplaceAll(plan.Branch, "{runId}", r.Run.RunID)
				p.Path = filepath.Join(filepath.Dir(w.toolDatabase), "worktrees", id)
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
			raw, _ := json.Marshal(p)
			if _, err = db.ExecContext(ctx, `INSERT OR IGNORE INTO workflow_workspaces(id,run_id,record) VALUES(?,?,?)`, id, r.Run.RunID, string(raw)); err != nil {
				return nil, err
			}
			if err = db.QueryRowContext(ctx, `SELECT record FROM workflow_workspaces WHERE id=?`, id).Scan(&encoded); err != nil {
				return nil, err
			}
		} else if err != nil {
			return nil, err
		}
		if err = json.Unmarshal([]byte(encoded), &p); err != nil {
			return nil, err
		}
		existingRef, _ := json.Marshal(p)
		if existing, resolveErr := w.resolvePreparedWorkspace(ctx, r, existingRef); resolveErr == nil {
			return json.Marshal(map[string]any{"workspace": existing})
		}
		if p.Mode == "new_worktree" {
			base, resolveErr := manager.ResolveBase(ctx, repository.ResolveBaseRequest{RepositoryPath: p.Repository, BaseRef: p.BaseSHA})
			if resolveErr != nil {
				return nil, resolveErr
			}
			_, err = manager.Attach(ctx, repository.AttachRequest{RepositoryPath: p.Repository, WorktreePath: p.Path, OperationID: id, Owner: repository.Ownership{DeliveryLineID: r.Run.RunID, WorkItemID: r.WorkItem.WorkItemID}, Branch: repository.CreateBranch{Name: p.Branch, Base: base}})
			if err != nil {
				return nil, err
			}
		}
		raw, _ := json.Marshal(p)
		verified, err := w.resolvePreparedWorkspace(ctx, r, raw)
		if err != nil {
			return nil, err
		}
		return json.Marshal(map[string]any{"workspace": verified})
	case workflow.WorkspaceValidateNode:
		p, err := w.resolvePreparedWorkspace(ctx, r, r.NodeInputs[n.Executor.WorkspaceInput])
		if err != nil {
			return nil, err
		}
		if len(n.Executor.Checks) == 0 {
			return nil, errors.New("at least one required check must be configured")
		}
		results := []map[string]any{}
		for _, argv := range n.Executor.Checks {
			if len(argv) == 0 || strings.TrimSpace(argv[0]) == "" {
				return nil, errors.New("validation check requires an executable")
			}
			checkCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
			command := exec.CommandContext(checkCtx, argv[0], argv[1:]...)
			command.Dir = p.Path
			output := &boundedCheckOutput{}
			command.Stdout = output
			command.Stderr = output
			err = command.Run()
			cancel()
			if err != nil {
				return nil, fmt.Errorf("required check %q failed: %w\n%s", argv, err, output.text)
			}
			results = append(results, map[string]any{"argv": argv, "exitCode": 0, "output": output.text})
		}
		return json.Marshal(map[string]any{"validation": map[string]any{"workspaceId": p.ID, "passed": true, "checks": results}})
	default:
		return nil, errors.New("unsupported workspace operation")
	}
}

type boundedCheckOutput struct{ text string }

func (b *boundedCheckOutput) Write(p []byte) (int, error) {
	n := len(p)
	if remaining := 65536 - len(b.text); remaining > 0 {
		if len(p) > remaining {
			p = p[:remaining]
		}
		b.text += string(p)
	}
	return n, nil
}
