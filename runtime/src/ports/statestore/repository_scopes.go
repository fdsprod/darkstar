package statestore

import (
	"context"
	"errors"
	"time"

	"darkstar/src/ports/repositorysnapshot"
)

var ErrRepositoryScopeConflict = errors.New("repository scope record conflict")

// InvestigationScopeMode excludes writer authority. Delivery remains governed
// by its existing single-writer workspace and lease contracts.
type InvestigationScopeMode string

const (
	InvestigationScopeNone     InvestigationScopeMode = "none"
	InvestigationScopeReadOnly InvestigationScopeMode = "read_only"
)

type FrozenRepositoryScopeEntry struct {
	Repository          RepositoryRecord                    `json:"repository"`
	Membership          RepositoryMembership                `json:"membership"`
	Ref                 string                              `json:"ref"`
	Revision            repositorysnapshot.ResolvedRevision `json:"revision"`
	Configuration       JSONSnapshot                        `json:"configuration"`
	ConfigurationDigest string                              `json:"configurationDigest"`
}

// RepositoryScope is frozen before materialization begins. Evidence is appended
// separately; preparation failures never replace refs, members or configuration.
type RepositoryScope struct {
	SchemaVersion   int                          `json:"schemaVersion"`
	ScopeID         string                       `json:"id"`
	ProjectID       string                       `json:"projectId"`
	ProjectRevision uint64                       `json:"projectRevision"`
	Mode            InvestigationScopeMode       `json:"mode"`
	Repositories    []FrozenRepositoryScopeEntry `json:"repositories"`
	ContentPolicy   string                       `json:"contentPolicy"`
	RequestDigest   string                       `json:"requestDigest"`
	Digest          string                       `json:"digest"`
	CreatedAt       time.Time                    `json:"createdAt"`
}

type RepositoryScopePreparationStatus string

const (
	RepositoryScopePreparing RepositoryScopePreparationStatus = "preparing"
	RepositoryScopeReady     RepositoryScopePreparationStatus = "ready"
	RepositoryScopeBlocked   RepositoryScopePreparationStatus = "blocked"
)

type RepositoryScopePreparation struct {
	Status    RepositoryScopePreparationStatus `json:"status"`
	Reason    string                           `json:"reason,omitempty"`
	Revision  uint64                           `json:"revision"`
	UpdatedAt time.Time                        `json:"updatedAt"`
}

type RepositoryScopeEvidence struct {
	ScopeID      string                      `json:"scopeId"`
	RepositoryID string                      `json:"repositoryId"`
	Evidence     repositorysnapshot.Evidence `json:"evidence"`
}

type RepositoryScopeAttemptBinding struct {
	AttemptID      string    `json:"attemptId"`
	ScopeID        string    `json:"scopeId"`
	RepositoryIDs  []string  `json:"repositoryIds"`
	ScopeDigest    string    `json:"scopeDigest"`
	EvidenceDigest string    `json:"evidenceDigest"`
	Digest         string    `json:"digest"`
	CreatedAt      time.Time `json:"createdAt"`
}

type RepositoryScopeStore interface {
	RepositoryScope(context.Context, string) (RepositoryScope, error)
	FreezeRepositoryScope(context.Context, RepositoryScope) (RepositoryScope, error)
	RepositoryScopePreparation(context.Context, string) (RepositoryScopePreparation, error)
	SetRepositoryScopePreparation(context.Context, string, uint64, RepositoryScopePreparationStatus, string) (RepositoryScopePreparation, error)
	RepositoryScopeEvidence(context.Context, string) ([]RepositoryScopeEvidence, error)
	SaveRepositoryScopeEvidence(context.Context, RepositoryScopeEvidence) error
	RepositoryScopeAttempt(context.Context, string) (RepositoryScopeAttemptBinding, error)
	BindRepositoryScopeAttempt(context.Context, RepositoryScopeAttemptBinding) (RepositoryScopeAttemptBinding, error)
}
