// Package workflowstore defines workflow discovery and immutable persistence boundaries.
package workflowstore

import (
	"context"
	"encoding/json"
	"errors"
	"time"
)

var (
	// ErrNotFound classifies a missing installed workflow or run snapshot.
	ErrNotFound = errors.New("workflow state not found")
	// ErrVersionConflict means a name/version tuple already owns different bytes.
	ErrVersionConflict = errors.New("workflow version conflict")
	// ErrRunSnapshotConflict means a run already owns a different frozen selection.
	ErrRunSnapshotConflict = errors.New("run workflow snapshot conflict")
	// ErrDraftConflict means an editor supplied a stale expected draft revision.
	ErrDraftConflict = errors.New("workflow draft revision conflict")
	// ErrBuiltInImmutable means a built-in definition was targeted for mutation.
	ErrBuiltInImmutable = errors.New("built-in workflow is immutable")
)

// Scope identifies the configured source that supplied an authored workflow.
type Scope string

const (
	ScopeDefault Scope = "default"
	ScopeUser    Scope = "user"
	ScopeProject Scope = "project"
)

// Candidate is one authored document discovered in a configured scope. Content
// is normalized JSON, but is not trusted or canonical until core validation.
type Candidate struct {
	Scope     Scope
	Reference string
	Content   json.RawMessage
}

// Source discovers authored workflow candidates without installing them.
type Source interface {
	Load(context.Context) ([]Candidate, error)
}

// InstallRequest contains the canonical identity and bytes of one workflow version.
type InstallRequest struct {
	Name        string
	Version     string
	Digest      string
	Document    json.RawMessage
	SourceScope Scope
	SourceRef   string
	InstalledAt time.Time
}

// InstalledVersion is one immutable, content-addressed installed definition.
type InstalledVersion struct {
	Name        string          `json:"name"`
	Version     string          `json:"version"`
	Digest      string          `json:"digest"`
	Document    json.RawMessage `json:"document"`
	SourceScope Scope           `json:"sourceScope"`
	SourceRef   string          `json:"sourceReference"`
	InstalledAt time.Time       `json:"installedAt"`
}

// DraftScope is a closed, editable location. Built-in/default workflows are
// deliberately absent: they may be duplicated, never edited in place.
type DraftScope string

const (
	DraftScopeUser    DraftScope = "user"
	DraftScopeProject DraftScope = "project"
)

// Draft is the one mutable workflow-authoring aggregate. Document and Layout
// have separate revisions so presentation-only changes cannot affect execution
// digests. Revision is the compare-and-swap token for the aggregate as a whole.
type Draft struct {
	ID             string          `json:"id"`
	Name           string          `json:"name"`
	Scope          DraftScope      `json:"scope"`
	ScopeReference string          `json:"scopeReference"`
	BaseVersion    string          `json:"baseVersion,omitempty"`
	Revision       uint64          `json:"revision"`
	Document       json.RawMessage `json:"document"`
	Layout         json.RawMessage `json:"layout"`
	DocumentDigest string          `json:"documentDigest"`
	UpdatedAt      time.Time       `json:"updatedAt"`
}

type CreateDraftRequest struct {
	ID, Name, ScopeReference, BaseVersion, IdempotencyKey string
	Scope                                                 DraftScope
	Document, Layout                                      json.RawMessage
	CreatedAt                                             time.Time
}

type UpdateDraftRequest struct {
	ID               string
	ExpectedRevision uint64
	Name             string
	Document, Layout json.RawMessage
	UpdatedAt        time.Time
}

type Archive struct {
	Name       string    `json:"name"`
	Version    string    `json:"version"`
	ArchivedAt time.Time `json:"archivedAt"`
}

// RunSnapshotRequest freezes a selected installed definition and effective
// configuration for an already-created run.
type RunSnapshotRequest struct {
	RunID            string
	WorkflowName     string
	WorkflowVersion  string
	WorkflowDigest   string
	WorkflowDocument json.RawMessage
	ConfigDigest     string
	ConfigSnapshot   json.RawMessage
	CreatedAt        time.Time
}

// RunSnapshot is the complete immutable workflow/config selection for one run.
type RunSnapshot struct {
	RunID            string          `json:"runId"`
	WorkflowName     string          `json:"workflowName"`
	WorkflowVersion  string          `json:"workflowVersion"`
	WorkflowDigest   string          `json:"workflowDigest"`
	WorkflowDocument json.RawMessage `json:"workflowDocument"`
	ConfigDigest     string          `json:"configDigest"`
	ConfigSnapshot   json.RawMessage `json:"configSnapshot"`
	CreatedAt        time.Time       `json:"createdAt"`
}

// Store persists immutable installed versions and per-run snapshots.
type Store interface {
	Install(context.Context, InstallRequest) (InstalledVersion, bool, error)
	InstalledVersion(context.Context, string, string) (InstalledVersion, error)
	InstalledVersions(context.Context, string) ([]InstalledVersion, error)
	CreateDraft(context.Context, CreateDraftRequest) (Draft, bool, error)
	Draft(context.Context, string) (Draft, error)
	Drafts(context.Context) ([]Draft, error)
	UpdateDraft(context.Context, UpdateDraftRequest) (Draft, error)
	DiscardDraft(context.Context, string, uint64) error
	ArchiveVersion(context.Context, string, string, time.Time) (Archive, bool, error)
	Archives(context.Context) ([]Archive, error)
	CreateRunSnapshot(context.Context, RunSnapshotRequest) (RunSnapshot, bool, error)
	RunSnapshot(context.Context, string) (RunSnapshot, error)
}
