package cli

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"

	nodeprocess "darkstar/src/adapters/executor/nodeprocess"
	gitadapter "darkstar/src/adapters/repository/git"
	"darkstar/src/core/nodes"
	"darkstar/src/core/runexecution"
	"darkstar/src/ports/repository"
	"darkstar/src/ports/statestore"
	_ "modernc.org/sqlite"
)

type preparedWorkspace = nodes.Workspace

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
	if p.ID != ref.ID || filepath.Base(p.ID) != p.ID || p.RunID != r.Run.RunID || p.ProjectID != r.Project.ProjectID || filepath.Clean(p.Repository) != filepath.Clean(w.projectRoot) {
		return p, errors.New("prepared workspace belongs to another repository")
	}
	manager, err := gitadapter.New("")
	if err != nil {
		return p, err
	}
	legacyPath := filepath.Join(filepath.Dir(w.toolDatabase), "worktrees", p.ID)
	if p.Mode == "new_worktree" && filepath.Clean(p.Path) == legacyPath {
		destination := filepath.Join(w.projectRoot, ".darkstar", "worktrees", p.ID)
		// Both absolute locations are derived from the authorized repository and
		// this run's durable workspace record, never from agent output paths.
		if err = manager.RelocateWorktree(ctx, p.Repository, p.Path, destination, p.Branch, p.BaseSHA); err != nil {
			return p, fmt.Errorf("relocate legacy workspace outside private daemon storage: %w", err)
		}
		p.Path = destination
		updated, _ := json.Marshal(p)
		result, updateErr := db.ExecContext(ctx, `UPDATE workflow_workspaces SET record=? WHERE id=? AND run_id=? AND record=?`, string(updated), p.ID, r.Run.RunID, encoded)
		if updateErr != nil {
			return p, updateErr
		}
		if count, _ := result.RowsAffected(); count == 0 {
			var current string
			if err = db.QueryRowContext(ctx, `SELECT record FROM workflow_workspaces WHERE id=? AND run_id=?`, p.ID, r.Run.RunID).Scan(&current); err != nil || current != string(updated) {
				return p, errors.New("workspace record changed during relocation")
			}
		}
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

// workspaceNodeServices binds authorization and durable storage to this attempt.
// Node handlers receive no workflow graph, scheduler, or transition authority.
type workspaceNodeServices struct {
	wiring  *daemonProviderWiring
	request runexecution.AttemptRequestContext
}

func (s workspaceNodeServices) Resolve(ctx context.Context, raw []byte) (nodes.Workspace, error) {
	return s.wiring.resolvePreparedWorkspace(ctx, s.request, raw)
}
func (s workspaceNodeServices) Load(ctx context.Context, id string) (nodes.Workspace, error) {
	db, err := s.wiring.workspaceDB(ctx)
	if err != nil {
		return nodes.Workspace{}, err
	}
	defer func() { _ = db.Close() }()
	var raw string
	err = db.QueryRowContext(ctx, "SELECT record FROM workflow_workspaces WHERE id=?", id).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return nodes.Workspace{}, nodes.ErrWorkspaceNotFound
	}
	if err != nil {
		return nodes.Workspace{}, err
	}
	var p nodes.Workspace
	err = json.Unmarshal([]byte(raw), &p)
	return p, err
}
func (s workspaceNodeServices) Create(ctx context.Context, p nodes.Workspace) (nodes.Workspace, error) {
	db, err := s.wiring.workspaceDB(ctx)
	if err != nil {
		return p, err
	}
	defer func() { _ = db.Close() }()
	raw, err := json.Marshal(p)
	if err != nil {
		return p, err
	}
	if _, err = db.ExecContext(ctx, "INSERT OR IGNORE INTO workflow_workspaces(id,run_id,record) VALUES(?,?,?)", p.ID, p.RunID, string(raw)); err != nil {
		return p, err
	}
	return s.Load(ctx, p.ID)
}
func (w *daemonProviderWiring) ExecuteWorkspaceNode(ctx context.Context, r runexecution.AttemptRequestContext) (json.RawMessage, error) {
	if err := w.authorizeWorkspaceProject(r); err != nil {
		return nil, err
	}
	handler, err := nodes.Lookup(r.Node)
	if err != nil {
		return nil, err
	}
	switch handler.(type) {
	case nodes.WorkspacePrepare, nodes.WorkspaceValidate:
	default:
		return nil, errors.New("unsupported workspace operation")
	}
	manager, err := gitadapter.New("")
	if err != nil {
		return nil, err
	}
	return handler.(nodes.DeterministicHandler).Execute(ctx, r.NodeInputs, nodes.BuiltinServices{
		Workspaces: nodes.WorkspaceServices{Identity: nodes.WorkspaceIdentity{RunID: r.Run.RunID, NodeID: r.Attempt.NodeID, ProjectID: r.Project.ProjectID, SourceHash: r.Project.SourceHash, WorkItemID: r.WorkItem.WorkItemID, Root: w.projectRoot}, Store: workspaceNodeServices{w, r}, Repository: manager},
		Commands:   nodeprocess.Runner{Environment: w.environment, OutputLimit: 65536},
	})
}

func (w *daemonProviderWiring) NodeCommandRunner() nodes.CommandRunner { return nodeprocess.Runner{} }

func (w *daemonProviderWiring) ExecuteExtensionNode(ctx context.Context, request runexecution.AttemptRequestContext) (json.RawMessage, error) {
	if err := w.authorizeWorkspaceProject(request); err != nil {
		return nil, err
	}
	handler, err := nodes.Lookup(request.Node)
	if err != nil {
		return nil, err
	}
	deterministic, ok := handler.(nodes.DeterministicHandler)
	if !ok {
		return nil, errors.New("extension must be deterministic")
	}
	return deterministic.Execute(ctx, request.NodeInputs, nodes.BuiltinServices{Extensions: w.nodeExtensions})
}
