// Package repositoryscope freezes bounded read-only repository investigation
// inputs independently of delivery/workflow execution authority.
package repositoryscope

import (
	"context"
	"errors"
	"time"

	"darkstar/src/core/config"
	"darkstar/src/core/workmanagement"
	"darkstar/src/ports/repositorysnapshot"
	"darkstar/src/ports/statestore"
)

var (
	ErrInvalidRequest   = errors.New("invalid repository scope request")
	ErrScopeConflict    = errors.New("repository scope identity conflict")
	ErrScopeUnavailable = errors.New("repository scope evidence is unavailable")
)

type RepositorySelection struct {
	RepositoryID string `json:"repositoryId"`
	Ref          string `json:"ref"`
}

type PrepareRequest struct {
	ProjectID    string                `json:"projectId"`
	Repositories []RepositorySelection `json:"repositories"`
}

type View struct {
	SchemaVersion int                                   `json:"schemaVersion"`
	Scope         statestore.RepositoryScope            `json:"scope"`
	Preparation   statestore.RepositoryScopePreparation `json:"preparation"`
	Evidence      []statestore.RepositoryScopeEvidence  `json:"evidence"`
}

type AttemptView struct {
	Binding      statestore.RepositoryScopeAttemptBinding `json:"binding"`
	Repositories []statestore.FrozenRepositoryScopeEntry  `json:"repositories"`
	Evidence     []statestore.RepositoryScopeEvidence     `json:"evidence"`
}

type ConfigurationResolver func(context.Context, statestore.ProjectProjection, statestore.ProjectRepository) (config.ResolvedRepositorySettings, error)

type Service struct {
	scopes        statestore.RepositoryScopeStore
	memberships   *workmanagement.Service
	exporter      repositorysnapshot.Exporter
	configuration ConfigurationResolver
	now           func() time.Time
}

func New(store statestore.Store, exporter repositorysnapshot.Exporter) (*Service, error) {
	scopes, ok := store.(statestore.RepositoryScopeStore)
	if !ok || exporter == nil {
		return nil, errors.New("repository scopes require durable state and an immutable exporter")
	}
	memberships, err := workmanagement.New(store)
	if err != nil {
		return nil, err
	}
	return &Service{scopes: scopes, memberships: memberships, exporter: exporter, now: time.Now}, nil
}

func (s *Service) SetConfigurationResolver(resolver ConfigurationResolver) {
	s.configuration = resolver
}
