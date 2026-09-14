package configmutation

import (
	"context"
	"errors"

	"darkstar/src/core/config"
	"darkstar/src/ports/configurationstore"
	"darkstar/src/ports/statestore"
)

// NewWithProjectStores retains the legacy daemon-project file while routing
// independent product identities to their own configuration and recovery files.
func NewWithProjectStores(files configurationstore.Store, audit AuditStore, projectRoot string, projects configurationstore.ProjectStores) (*Service, error) {
	if projects == nil {
		return nil, errors.New("project configuration stores are required")
	}
	service, err := New(files, audit, projectRoot)
	if err != nil {
		return nil, err
	}
	service.projectStores = projects
	return service, nil
}

func (s *Service) forScope(ctx context.Context, scope config.MutationScope) (*Service, error) {
	if scope.Kind() != config.MutationScopeProject || s.boundProjectID == scope.ProjectID() {
		return s, nil
	}
	project, err := s.audit.Project(ctx, scope.ProjectID())
	if errors.Is(err, statestore.ErrNotFound) {
		return nil, ErrProjectNotFound
	}
	if err != nil {
		return nil, err
	}
	if project.Status != statestore.ProjectActive {
		return nil, ErrProjectMismatch
	}
	if project.SourceHash == digest(s.projectRoot) {
		return s, nil
	}
	registry, ok := s.audit.(interface {
		ProjectRepositoryConfiguration(context.Context, string) (statestore.ProjectRepositoryConfiguration, error)
	})
	if !ok || s.projectStores == nil {
		return nil, ErrProjectMismatch
	}
	configuration, err := registry.ProjectRepositoryConfiguration(ctx, project.ProjectID)
	if err != nil {
		return nil, err
	}
	if configuration.Migration.State != "ready" {
		return nil, ErrProjectMismatch
	}
	files, err := s.projectStores.ForProject(project.ProjectID)
	if err != nil {
		return nil, err
	}
	selected := *s
	selected.files = files
	selected.boundProjectID = project.ProjectID
	return &selected, nil
}
