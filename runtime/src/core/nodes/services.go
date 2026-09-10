package nodes

import (
	"context"
	"darkstar/src/ports/nodeextension"
	"encoding/json"
	"time"

	"darkstar/src/ports/repository"
)

// Workspace preserves the existing durable workspace record and wire format.
type Workspace struct {
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

// WorkspaceIdentity is daemon execution metadata, never agent task content.
type WorkspaceIdentity struct{ RunID, NodeID, ProjectID, SourceHash, WorkItemID, Root string }

type WorkspaceStore interface {
	Load(context.Context, string) (Workspace, error)
	Create(context.Context, Workspace) (Workspace, error)
	Resolve(context.Context, []byte) (Workspace, error)
}

// WorkspaceRepository deliberately excludes commit, push, and delivery methods.
type WorkspaceRepository interface {
	Inspect(context.Context, repository.InspectRequest) (repository.Observation, error)
	ResolveBase(context.Context, repository.ResolveBaseRequest) (repository.BaseRevision, error)
	Attach(context.Context, repository.AttachRequest) (repository.Worktree, error)
}

type WorkspaceServices struct {
	Identity   WorkspaceIdentity
	Store      WorkspaceStore
	Repository WorkspaceRepository
}

type CommandRunner interface {
	Run(context.Context, string, []string, time.Duration) (string, error)
}

// LegacyCommandScope is only for the existing restricted command executor.
// It is not passed to agents and is not a general workflow execution context.
type LegacyCommandScope struct{ WorkflowID, NodeID, Workspace string }

type BuiltinServices struct {
	Extensions    nodeextension.Resolver
	Workspaces    WorkspaceServices
	Commands      CommandRunner
	LegacyCommand LegacyCommandScope
	// Existing gates may explicitly reference run inputs.
	RunInputs map[string]json.RawMessage
}
