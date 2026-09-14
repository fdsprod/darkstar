package workmanagement

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"runtime"
	"strings"

	"darkstar/src/core/identity"
	"darkstar/src/ports/repository"
	"darkstar/src/ports/statestore"
)

var (
	ErrRepositorySelection    = errors.New("repository selection requires an active explicit membership")
	ErrRepositoryRevision     = errors.New("repository membership revision conflict")
	ErrUnsupportedCardinality = errors.New("legacy project representation requires exactly one active repository; use projects-v2")
)

type CreateProjectRequest struct {
	Name     string                        `json:"name"`
	Defaults statestore.RepositorySettings `json:"defaults"`
}

type MembershipRequest struct {
	ProjectID                  string                        `json:"projectId"`
	RepositoryPath             string                        `json:"repositoryPath,omitempty"`
	RepositoryID               string                        `json:"repositoryId,omitempty"`
	Label                      string                        `json:"label"`
	Role                       statestore.RepositoryRole     `json:"role"`
	Settings                   statestore.RepositorySettings `json:"settings"`
	ExpectedVersion            uint64                        `json:"expectedVersion"`
	ExpectedMembershipRevision uint64                        `json:"expectedMembershipRevision"`
}

type RemoveMembershipRequest struct {
	ProjectID                  string `json:"projectId"`
	RepositoryID               string `json:"repositoryId"`
	ExpectedVersion            uint64 `json:"expectedVersion"`
	ExpectedMembershipRevision uint64 `json:"expectedMembershipRevision"`
}

type ProjectDefaultsRequest struct {
	ProjectID       string                        `json:"projectId"`
	Defaults        statestore.RepositorySettings `json:"defaults"`
	ExpectedVersion uint64                        `json:"expectedVersion"`
}

type ProjectRepositoriesView struct {
	SchemaVersion int                            `json:"schemaVersion"`
	Project       statestore.ProjectProjection   `json:"project"`
	Repositories  []statestore.ProjectRepository `json:"repositories"`
	Defaults      statestore.RepositorySettings  `json:"defaults"`
	Migration     statestore.RepositoryMigration `json:"migration"`
}

func (s *Service) ConfigureRepositoryManager(manager repository.Manager) {
	s.repositories = manager
}

func (s *Service) repositoryStore() (statestore.RepositoryStore, error) {
	store, ok := s.store.(statestore.RepositoryStore)
	if !ok {
		return nil, errors.New("repository registry is not configured")
	}
	return store, nil
}

func (s *Service) ProjectRepositories(ctx context.Context, projectID string) (ProjectRepositoriesView, error) {
	store, err := s.repositoryStore()
	if err != nil {
		return ProjectRepositoriesView{}, err
	}
	project, err := s.store.Project(ctx, projectID)
	if err != nil {
		return ProjectRepositoriesView{}, err
	}
	repositories, err := store.ProjectRepositories(ctx, projectID)
	if err != nil {
		return ProjectRepositoriesView{}, err
	}
	configuration, err := store.ProjectRepositoryConfiguration(ctx, projectID)
	if err != nil {
		return ProjectRepositoriesView{}, err
	}
	return ProjectRepositoriesView{SchemaVersion: 2, Project: project, Repositories: repositories, Defaults: configuration.Defaults, Migration: configuration.Migration}, nil
}

func (s *Service) ProjectsV2(ctx context.Context) ([]ProjectRepositoriesView, error) {
	projects, err := s.store.Projects(ctx)
	if err != nil {
		return nil, err
	}
	values := make([]ProjectRepositoriesView, 0, len(projects))
	for _, project := range projects {
		value, err := s.ProjectRepositories(ctx, project.ProjectID)
		if err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	return values, nil
}

func (s *Service) CreateProjectV2(ctx context.Context, request CreateProjectRequest, key string) (ProjectRepositoriesView, error) {
	request.Name = strings.TrimSpace(request.Name)
	if request.Name == "" {
		return ProjectRepositoriesView{}, fmt.Errorf("%w: project name is required", ErrInvalidRequest)
	}
	if err := validateRepositorySettings(request.Defaults); err != nil {
		return ProjectRepositoriesView{}, err
	}
	projectID := identity.Deterministic("project_", "projects.create.v2\x00"+key)
	return s.repositoryCommand(ctx, "projects.create.v2", projectID, key, request, func() ([]statestore.Event, error) {
		event := pendingEvent("project.created", statestore.AggregateProject, projectID, projectID, repositoryCommandID("projects.create.v2", key), s.now().UTC().Round(0), map[string]any{"name": request.Name, "sourceHash": digest(projectID), "repositoryModelVersion": 2, "defaults": request.Defaults})
		return s.store.Append(ctx, event)
	})
}

func (s *Service) SetMembership(ctx context.Context, request MembershipRequest, key string) (ProjectRepositoriesView, error) {
	if request.ProjectID == "" || strings.TrimSpace(request.Label) == "" || strings.TrimSpace(request.Label) != request.Label || (request.Role != statestore.RepositoryReadOnly && request.Role != statestore.RepositoryImplementation) {
		return ProjectRepositoriesView{}, fmt.Errorf("%w: projectId, label, and read_only or implementation role are required", ErrInvalidRequest)
	}
	if err := validateRepositorySettings(request.Settings); err != nil {
		return ProjectRepositoriesView{}, err
	}
	return s.repositoryCommand(ctx, "projects.repositories.set", request.ProjectID, key, request, func() ([]statestore.Event, error) {
		view, err := s.ProjectRepositories(ctx, request.ProjectID)
		if err != nil {
			return nil, err
		}
		if view.Project.ResourceVersion != request.ExpectedVersion {
			return nil, ErrRepositoryRevision
		}
		record, err := s.resolveRegistration(ctx, request.RepositoryID, request.RepositoryPath)
		if err != nil {
			return nil, err
		}
		var revision uint64
		for _, entry := range view.Repositories {
			if entry.Repository.RepositoryID == record.RepositoryID {
				revision = entry.Membership.Revision
			}
			if entry.Membership.Status == statestore.MembershipActive && entry.Membership.Label == request.Label && entry.Repository.RepositoryID != record.RepositoryID {
				return nil, fmt.Errorf("%w: active repository labels must be unique within the project", ErrInvalidRequest)
			}
		}
		if revision != request.ExpectedMembershipRevision {
			return nil, ErrRepositoryRevision
		}
		now := s.now().UTC().Round(0)
		membership := statestore.RepositoryMembership{ProjectID: request.ProjectID, RepositoryID: record.RepositoryID, Revision: revision + 1, Label: request.Label, Role: request.Role, Status: statestore.MembershipActive, Settings: request.Settings, UpdatedAt: now}
		data := map[string]any{"repository": record, "membership": membership}
		if view.Migration.State == "legacy_unresolved" {
			if digest(request.RepositoryPath) != view.Project.SourceHash && digest(record.Root) != view.Project.SourceHash {
				return nil, fmt.Errorf("%w: verified repository path does not match retained legacy source hash", ErrRepositorySelection)
			}
			data["legacyEvidence"] = "project:" + request.ProjectID + ":sourceHash:" + view.Project.SourceHash
		}
		event := pendingEvent("project.repository_set", statestore.AggregateProject, request.ProjectID, request.ProjectID, repositoryCommandID("projects.repositories.set", key), now, data)
		event.ExpectedRevision = request.ExpectedVersion
		return s.store.Append(ctx, event)
	})
}

func (s *Service) RemoveMembership(ctx context.Context, request RemoveMembershipRequest, key string) (ProjectRepositoriesView, error) {
	return s.repositoryCommand(ctx, "projects.repositories.remove", request.ProjectID, key, request, func() ([]statestore.Event, error) {
		view, err := s.ProjectRepositories(ctx, request.ProjectID)
		if err != nil {
			return nil, err
		}
		if view.Project.ResourceVersion != request.ExpectedVersion {
			return nil, ErrRepositoryRevision
		}
		for _, entry := range view.Repositories {
			if entry.Repository.RepositoryID != request.RepositoryID {
				continue
			}
			if entry.Membership.Revision != request.ExpectedMembershipRevision {
				return nil, ErrRepositoryRevision
			}
			if entry.Membership.Status != statestore.MembershipActive {
				return nil, ErrRepositorySelection
			}
			now := s.now().UTC().Round(0)
			entry.Membership.Revision++
			entry.Membership.Status = statestore.MembershipRemoved
			entry.Membership.UpdatedAt = now
			entry.Membership.Removal = &statestore.MembershipRemoval{Actor: statestore.Actor{Type: statestore.ActorUser, ID: "local-user"}, RemovedAt: now}
			event := pendingEvent("project.repository_removed", statestore.AggregateProject, request.ProjectID, request.ProjectID, repositoryCommandID("projects.repositories.remove", key), now, map[string]any{"repository": entry.Repository, "membership": entry.Membership})
			event.ExpectedRevision = request.ExpectedVersion
			return s.store.Append(ctx, event)
		}
		return nil, ErrRepositorySelection
	})
}

func (s *Service) UpdateProjectDefaults(ctx context.Context, request ProjectDefaultsRequest, key string) (ProjectRepositoriesView, error) {
	if err := validateRepositorySettings(request.Defaults); err != nil {
		return ProjectRepositoriesView{}, err
	}
	return s.repositoryCommand(ctx, "projects.repositories.defaults", request.ProjectID, key, request, func() ([]statestore.Event, error) {
		project, err := s.store.Project(ctx, request.ProjectID)
		if err != nil {
			return nil, err
		}
		if project.ResourceVersion != request.ExpectedVersion {
			return nil, ErrRepositoryRevision
		}
		event := pendingEvent("project.repository_defaults_updated", statestore.AggregateProject, request.ProjectID, request.ProjectID, repositoryCommandID("projects.repositories.defaults", key), s.now().UTC().Round(0), map[string]any{"defaults": request.Defaults})
		event.ExpectedRevision = request.ExpectedVersion
		return s.store.Append(ctx, event)
	})
}

func (s *Service) ResolveRepository(ctx context.Context, projectID, repositoryID string, writer bool) (statestore.ProjectRepository, error) {
	view, err := s.ProjectRepositories(ctx, projectID)
	if err != nil {
		return statestore.ProjectRepository{}, err
	}
	if view.Project.Status != statestore.ProjectActive {
		return statestore.ProjectRepository{}, fmt.Errorf("%w: project is archived", ErrRepositorySelection)
	}
	if view.Migration.State != "ready" {
		return statestore.ProjectRepository{}, fmt.Errorf("%w: %s", ErrRepositorySelection, view.Migration.Reason)
	}
	selected := make([]statestore.ProjectRepository, 0)
	for _, entry := range view.Repositories {
		if entry.Membership.Status == statestore.MembershipActive && (repositoryID == "" || entry.Repository.RepositoryID == repositoryID) {
			selected = append(selected, entry)
		}
	}
	if len(selected) != 1 || (writer && selected[0].Membership.Role != statestore.RepositoryImplementation) {
		return statestore.ProjectRepository{}, ErrRepositorySelection
	}
	return selected[0], nil
}

func (s *Service) DiscoverProjects(ctx context.Context, path string) ([]ProjectRepositoriesView, error) {
	record, err := s.resolveRegistration(ctx, "", path)
	if err != nil {
		return nil, err
	}
	projects, err := s.ProjectsV2(ctx)
	if err != nil {
		return nil, err
	}
	result := make([]ProjectRepositoriesView, 0)
	for _, project := range projects {
		for _, entry := range project.Repositories {
			if entry.Repository.RepositoryID == record.RepositoryID && entry.Membership.Status == statestore.MembershipActive {
				result = append(result, project)
			}
		}
	}
	return result, nil
}

func (s *Service) resolveRegistration(ctx context.Context, id, path string) (statestore.RepositoryRecord, error) {
	store, err := s.repositoryStore()
	if err != nil {
		return statestore.RepositoryRecord{}, err
	}
	if s.repositories == nil {
		return statestore.RepositoryRecord{}, errors.New("repository inspection is not configured")
	}
	var existing statestore.RepositoryRecord
	if id != "" {
		existing, err = store.Repository(ctx, id)
		if err != nil {
			return statestore.RepositoryRecord{}, err
		}
		if path == "" {
			path = existing.Root
		}
	}
	if path == "" || !filepath.IsAbs(path) {
		return statestore.RepositoryRecord{}, fmt.Errorf("%w: absolute repositoryPath or registered repositoryId is required", ErrInvalidRequest)
	}
	observation, err := s.repositories.Inspect(ctx, repository.InspectRequest{Path: path})
	if err != nil {
		return statestore.RepositoryRecord{}, fmt.Errorf("%w: repository is unavailable: %v", ErrRepositorySelection, err)
	}
	canonical := repositoryIdentityKey(observation.Repository.CommonGitDir)
	repositoryID := identity.Deterministic("repository_", canonical)
	if id != "" && id != repositoryID {
		return statestore.RepositoryRecord{}, fmt.Errorf("%w: registered repository identity changed; explicit relocation is required", ErrRepositorySelection)
	}
	registered, err := store.Repository(ctx, repositoryID)
	if err == nil {
		return registered, nil
	}
	if !errors.Is(err, statestore.ErrNotFound) {
		return statestore.RepositoryRecord{}, err
	}
	return statestore.RepositoryRecord{RepositoryID: repositoryID, Root: observation.Repository.Root, CommonGitDir: observation.Repository.CommonGitDir, IdentityKey: canonical, CreatedAt: s.now().UTC().Round(0)}, nil
}

func repositoryIdentityKey(path string) string {
	value := filepath.Clean(path)
	if runtime.GOOS == "windows" {
		value = strings.ToLower(value)
	}
	return value
}

func validateRepositorySettings(value statestore.RepositorySettings) error {
	if value.ConfigurationRoot != "" && !filepath.IsAbs(value.ConfigurationRoot) {
		return fmt.Errorf("%w: configurationRoot must be absolute", ErrInvalidRequest)
	}
	if value.WorktreeBase != "" && !filepath.IsAbs(value.WorktreeBase) && value.ConfigurationRoot == "" {
		return fmt.Errorf("%w: relative worktreeBase requires configurationRoot", ErrInvalidRequest)
	}
	for _, path := range value.PathScope {
		if strings.TrimSpace(path) != path || path == "" || filepath.IsAbs(path) || filepath.VolumeName(path) != "" || strings.Contains(path, "\\") || filepath.Clean(path) == ".." || strings.HasPrefix(filepath.Clean(path), ".."+string(filepath.Separator)) {
			return fmt.Errorf("%w: pathScope entries must be relative repository paths without traversal", ErrInvalidRequest)
		}
	}
	for name, commands := range value.ValidationProfiles {
		if strings.TrimSpace(name) == "" || commands == nil {
			return fmt.Errorf("%w: validation profiles require a name and command list", ErrInvalidRequest)
		}
		for _, command := range commands {
			if strings.TrimSpace(command) == "" {
				return fmt.Errorf("%w: validation commands must not be empty", ErrInvalidRequest)
			}
		}
	}
	return nil
}

func (s *Service) repositoryCommand(ctx context.Context, scope, projectID, key string, request any, apply func() ([]statestore.Event, error)) (ProjectRepositoriesView, error) {
	command, reused, err := s.begin(ctx, scope, key, request)
	if err != nil {
		return ProjectRepositoriesView{}, err
	}
	if reused && command.Status == "completed" {
		var value ProjectRepositoriesView
		err := json.Unmarshal(command.Response, &value)
		return value, err
	}
	if reused {
		// A crash after event commit but before response completion must not append
		// another membership revision. Events are the durable recovery evidence.
		var position uint64
		for {
			events, err := s.store.EventsAfter(ctx, position, 1000)
			if err != nil {
				return ProjectRepositoriesView{}, err
			}
			for _, event := range events {
				position = event.GlobalPosition
				if event.AggregateID == projectID && event.CommandID == repositoryCommandID(scope, key) && repositoryScopeEvent(scope) == event.Kind {
					value, err := s.ProjectRepositories(ctx, projectID)
					if err != nil {
						return ProjectRepositoriesView{}, err
					}
					return value, s.complete(ctx, scope, key, httpOK, value, []statestore.Event{event})
				}
			}
			if len(events) < 1000 {
				break
			}
		}
	}
	events, err := apply()
	if err != nil {
		return ProjectRepositoriesView{}, err
	}
	value, err := s.ProjectRepositories(ctx, projectID)
	if err != nil {
		return ProjectRepositoriesView{}, err
	}
	return value, s.complete(ctx, scope, key, httpOK, value, events)
}

func repositoryScopeEvent(scope string) string {
	switch scope {
	case "projects.create.v2":
		return "project.created"
	case "projects.repositories.set":
		return "project.repository_set"
	case "projects.repositories.remove":
		return "project.repository_removed"
	case "projects.repositories.defaults":
		return "project.repository_defaults_updated"
	default:
		return ""
	}
}

func repositoryCommandID(scope, key string) string {
	return identity.Deterministic("command_", scope+"\x00"+key)
}

func (s *Service) validateLegacyCardinality(ctx context.Context, projectID string) error {
	if _, ok := s.store.(statestore.RepositoryStore); !ok {
		return nil
	}
	view, err := s.ProjectRepositories(ctx, projectID)
	if err != nil {
		return err
	}
	if view.Migration.State == "legacy_unresolved" {
		return nil
	}
	count := 0
	for _, entry := range view.Repositories {
		if entry.Membership.Status == statestore.MembershipActive {
			count++
		}
	}
	if count != 1 {
		return ErrUnsupportedCardinality
	}
	return nil
}

// MigrateLegacyRepositories binds only verified coordinates whose retained
// source hash matches. Missing or ambiguous roots stay readable and unresolved.
// Deterministic commands make interruption and restart safe without changing
// historical events, work IDs, configuration, branches, or leases.
func (s *Service) MigrateLegacyRepositories(ctx context.Context, roots []string) error {
	views, err := s.ProjectsV2(ctx)
	if err != nil {
		return err
	}
	for _, view := range views {
		if view.Migration.State != "legacy_unresolved" || view.Project.Status != statestore.ProjectActive {
			continue
		}
		for _, root := range roots {
			if digest(root) != view.Project.SourceHash {
				continue
			}
			_, err := s.SetMembership(ctx, MembershipRequest{ProjectID: view.Project.ProjectID, RepositoryPath: root, Label: view.Project.Name, Role: statestore.RepositoryImplementation, ExpectedVersion: view.Project.ResourceVersion}, "membership-migration-"+view.Project.ProjectID)
			if err != nil && !errors.Is(err, ErrRepositorySelection) {
				return err
			}
			break
		}
	}
	return nil
}
