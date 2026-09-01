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
	CreateRunSnapshot(context.Context, RunSnapshotRequest) (RunSnapshot, bool, error)
	RunSnapshot(context.Context, string) (RunSnapshot, error)
}
