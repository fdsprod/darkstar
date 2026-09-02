// Package contextmanifest defines immutable attempt input snapshots.
package contextmanifest

import (
	"context"
	"errors"
	"time"

	"darkstar/src/ports/capabilityregistry"
)

var (
	ErrNotFound = errors.New("context manifest not found")
	ErrFrozen   = errors.New("attempt context is already frozen with different inputs")
)

type DigestRef struct {
	ID     string `json:"id"`
	Digest string `json:"digest"`
}

type WorkspaceAccess string

const (
	WorkspaceReadOnly WorkspaceAccess = "read_only"
	WorkspaceWrite    WorkspaceAccess = "workspace_write"
)

type Workspace struct {
	ID     string          `json:"id"`
	Digest string          `json:"digest"`
	Access WorkspaceAccess `json:"access"`
}

type Entry struct {
	ArtifactID       string `json:"artifactId"`
	ArtifactVersion  uint64 `json:"artifactVersion"`
	RepresentationID string `json:"representationId"`
	Digest           string `json:"digest"`
	Required         bool   `json:"required"`
	TokenEstimate    int64  `json:"tokenEstimate"`
}

type OmissionReason string

const (
	OmissionBudget      OmissionReason = "budget"
	OmissionUnsupported OmissionReason = "unsupported"
	OmissionSensitivity OmissionReason = "sensitivity"
	OmissionCapability  OmissionReason = "capability"
	OmissionStale       OmissionReason = "stale"
)

type Omission struct {
	RepresentationID string         `json:"representationId"`
	Reason           OmissionReason `json:"reason"`
}

type Manifest struct {
	ManifestID    string                         `json:"manifestId"`
	RunID         string                         `json:"runId"`
	NodeID        string                         `json:"nodeId"`
	AttemptID     string                         `json:"attemptId"`
	PolicyVersion string                         `json:"policyVersion"`
	Budget        int64                          `json:"budget"`
	Reserved      int64                          `json:"reservedTokens"`
	Entries       []Entry                        `json:"entries"`
	Omissions     []Omission                     `json:"omissions"`
	Instructions  []DigestRef                    `json:"instructions"`
	Schemas       []DigestRef                    `json:"schemas"`
	Permissions   []string                       `json:"permissions"`
	Workspace     Workspace                      `json:"workspace"`
	Capabilities  []capabilityregistry.Selection `json:"capabilities"`
	Digest        string                         `json:"digest"`
	FrozenAt      time.Time                      `json:"frozenAt"`
}

func (manifest Manifest) UsedTokens() int64 {
	var total int64
	for _, entry := range manifest.Entries {
		total += entry.TokenEstimate
	}
	return total
}

type Store interface {
	StoreManifest(context.Context, Manifest, string) (Manifest, bool, error)
	Manifest(context.Context, string) (Manifest, error)
	ManifestForAttempt(context.Context, string) (Manifest, error)
}
