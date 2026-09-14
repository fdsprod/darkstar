package statestore

import (
	"context"
	"time"
)

// RepositorySettings contains repository-scoped values only. Nil maps and lists
// inherit; present empty maps/lists are intentional values. Paths are rooted at
// ConfigurationRoot rather than the daemon's current directory.
type RepositorySettings struct {
	ConfigurationRoot  string                     `json:"configurationRoot,omitempty"`
	BaseRef            string                     `json:"baseRef,omitempty"`
	WorktreeBase       string                     `json:"worktreeBase,omitempty"`
	ValidationProfiles map[string][]string        `json:"validationProfiles"`
	PathScope          []string                   `json:"pathScope"`
	Delivery           RepositoryDeliveryDefaults `json:"delivery,omitempty"`
}

type RepositoryDeliveryDefaults struct {
	Remote       string `json:"remote,omitempty"`
	TargetBranch string `json:"targetBranch,omitempty"`
}

// RepositoryRecord is shared by all memberships of one physical Git repository.
// Coordinates are immutable; a missing path cannot silently adopt a replacement.
type RepositoryRecord struct {
	RepositoryID string    `json:"id"`
	Root         string    `json:"root"`
	CommonGitDir string    `json:"commonGitDir"`
	IdentityKey  string    `json:"identityKey"`
	CreatedAt    time.Time `json:"createdAt"`
}

type RepositoryRole string

const (
	RepositoryReadOnly       RepositoryRole = "read_only"
	RepositoryImplementation RepositoryRole = "implementation"
)

type MembershipStatus string

const (
	MembershipActive  MembershipStatus = "active"
	MembershipRemoved MembershipStatus = "removed"
)

// MembershipRemoval is present exactly when Status is removed; storage enforces
// this closed lifecycle and retains the exact actor and time across rebuilds.
type MembershipRemoval struct {
	Actor     Actor     `json:"actor"`
	RemovedAt time.Time `json:"removedAt"`
}

type RepositoryMembership struct {
	ProjectID    string             `json:"projectId"`
	RepositoryID string             `json:"repositoryId"`
	Revision     uint64             `json:"revision"`
	Label        string             `json:"label"`
	Role         RepositoryRole     `json:"role"`
	Status       MembershipStatus   `json:"status"`
	Settings     RepositorySettings `json:"settings"`
	Removal      *MembershipRemoval `json:"removal,omitempty"`
	UpdatedAt    time.Time          `json:"updatedAt"`
}

type ProjectRepository struct {
	Repository RepositoryRecord     `json:"repository"`
	Membership RepositoryMembership `json:"membership"`
}

// RepositoryMigration distinguishes a new empty project from preserved legacy
// evidence for which a source hash alone cannot prove repository coordinates.
type RepositoryMigration struct {
	State       string `json:"state"`
	EvidenceRef string `json:"evidenceRef,omitempty"`
	Reason      string `json:"reason,omitempty"`
}

type ProjectRepositoryConfiguration struct {
	Defaults  RepositorySettings  `json:"defaults"`
	Migration RepositoryMigration `json:"migration"`
}

// RepositoryStore exposes rebuildable membership state independently of tracker
// selection and execution projections.
type RepositoryStore interface {
	ProjectRepositories(context.Context, string) ([]ProjectRepository, error)
	Repository(context.Context, string) (RepositoryRecord, error)
	MembershipRevision(context.Context, string, string, uint64) (RepositoryMembership, error)
	ProjectRepositoryConfiguration(context.Context, string) (ProjectRepositoryConfiguration, error)
}
