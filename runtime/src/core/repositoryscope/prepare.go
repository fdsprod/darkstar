package repositoryscope

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"darkstar/src/core/config"
	"darkstar/src/core/identity"
	"darkstar/src/ports/repositorysnapshot"
	"darkstar/src/ports/statestore"
)

func (s *Service) Prepare(ctx context.Context, request PrepareRequest, key string) (View, error) {
	if strings.TrimSpace(request.ProjectID) == "" || request.Repositories == nil || len(request.Repositories) > 32 || strings.TrimSpace(key) == "" || len(key) > 128 {
		return View{}, ErrInvalidRequest
	}
	request.Repositories = append([]RepositorySelection{}, request.Repositories...)
	sort.Slice(request.Repositories, func(i, j int) bool {
		return request.Repositories[i].RepositoryID < request.Repositories[j].RepositoryID
	})
	for i, selection := range request.Repositories {
		if selection.RepositoryID == "" || strings.TrimSpace(selection.Ref) == "" || selection.Ref != strings.TrimSpace(selection.Ref) || (i > 0 && request.Repositories[i-1].RepositoryID == selection.RepositoryID) {
			return View{}, ErrInvalidRequest
		}
	}
	id := identity.Deterministic("scope_", "repository-scope.prepare\x00"+key)
	digest := statestore.RepositoryScopeContentDigest(request)
	frozen, err := s.scopes.RepositoryScope(ctx, id)
	if errors.Is(err, statestore.ErrNotFound) {
		frozen, err = s.freeze(ctx, id, digest, request)
	}
	if err != nil {
		return View{}, err
	}
	if frozen.RequestDigest != digest {
		return View{}, ErrScopeConflict
	}
	if err = s.materialize(ctx, frozen); err != nil {
		if updateErr := s.preparation(ctx, id, statestore.RepositoryScopeBlocked, err.Error()); updateErr != nil {
			return View{}, errors.Join(err, updateErr)
		}
		return s.Get(ctx, id)
	}
	if err = s.preparation(ctx, id, statestore.RepositoryScopeReady, ""); err != nil {
		return View{}, err
	}
	return s.Get(ctx, id)
}

func (s *Service) freeze(ctx context.Context, id, digest string, request PrepareRequest) (statestore.RepositoryScope, error) {
	project, err := s.memberships.ProjectRepositories(ctx, request.ProjectID)
	if err != nil {
		return statestore.RepositoryScope{}, err
	}
	if project.Project.Status != statestore.ProjectActive || project.Migration.State != "ready" {
		return statestore.RepositoryScope{}, fmt.Errorf("%w: project must have resolved active membership state", ErrInvalidRequest)
	}
	scope := statestore.RepositoryScope{SchemaVersion: 1, ScopeID: id, ProjectID: request.ProjectID, ProjectRevision: project.Project.ResourceVersion, Mode: statestore.InvestigationScopeNone, Repositories: make([]statestore.FrozenRepositoryScopeEntry, 0, len(request.Repositories)), ContentPolicy: "committed_only", RequestDigest: digest, CreatedAt: s.now().UTC()}
	for _, selection := range request.Repositories {
		member, resolveErr := s.memberships.ResolveRepository(ctx, request.ProjectID, selection.RepositoryID, false)
		if resolveErr != nil {
			return statestore.RepositoryScope{}, resolveErr
		}
		var resolved config.ResolvedRepositorySettings
		if s.configuration != nil {
			resolved, err = s.configuration(ctx, project.Project, member)
		} else {
			resolved, err = config.ResolveRepositorySettings(config.RepositorySettingsLayer{Scope: config.ScopeProject, Reference: fmt.Sprintf("%s@%d", project.Project.ProjectID, project.Project.ResourceVersion), Settings: project.Defaults}, config.RepositorySettingsLayer{Scope: config.ScopeMembership, Reference: fmt.Sprintf("%s/%s@%d", request.ProjectID, selection.RepositoryID, member.Membership.Revision), Settings: member.Membership.Settings})
		}
		if err != nil {
			return statestore.RepositoryScope{}, err
		}
		revision, resolveErr := s.exporter.Resolve(ctx, repositorysnapshot.ResolveRequest{RepositoryID: selection.RepositoryID, Root: member.Repository.Root, CommonGitDir: member.Repository.CommonGitDir, Ref: selection.Ref})
		if resolveErr != nil {
			return statestore.RepositoryScope{}, resolveErr
		}
		encoded, marshalErr := json.Marshal(resolved)
		if marshalErr != nil {
			return statestore.RepositoryScope{}, marshalErr
		}
		scope.Repositories = append(scope.Repositories, statestore.FrozenRepositoryScopeEntry{Repository: member.Repository, Membership: member.Membership, Ref: selection.Ref, Revision: revision, Configuration: statestore.JSONSnapshot(encoded), ConfigurationDigest: resolved.Digest})
	}
	if len(scope.Repositories) > 0 {
		scope.Mode = statestore.InvestigationScopeReadOnly
	}
	scope.Digest = statestore.RepositoryScopeDigest(scope)
	return s.scopes.FreezeRepositoryScope(ctx, scope)
}

func (s *Service) materialize(ctx context.Context, scope statestore.RepositoryScope) error {
	retained, err := s.scopes.RepositoryScopeEvidence(ctx, scope.ScopeID)
	if err != nil {
		return err
	}
	byID := make(map[string]repositorysnapshot.Evidence, len(retained))
	for _, item := range retained {
		byID[item.RepositoryID] = item.Evidence
	}
	for _, entry := range scope.Repositories {
		if evidence, exists := byID[entry.Repository.RepositoryID]; exists {
			if err = s.exporter.Verify(ctx, evidence); err != nil {
				return err
			}
			continue
		}
		var configuration config.ResolvedRepositorySettings
		if err = json.Unmarshal([]byte(entry.Configuration), &configuration); err != nil {
			return err
		}
		evidence, exportErr := s.exporter.Materialize(ctx, repositorysnapshot.ExportRequest{ScopeID: scope.ScopeID, RepositoryID: entry.Repository.RepositoryID, Root: entry.Repository.Root, CommonGitDir: entry.Repository.CommonGitDir, CommitSHA: entry.Revision.CommitSHA, PathScope: configuration.Settings.PathScope})
		if exportErr != nil {
			return exportErr
		}
		if err = s.exporter.Verify(ctx, evidence); err != nil {
			return err
		}
		if err = s.scopes.SaveRepositoryScopeEvidence(ctx, statestore.RepositoryScopeEvidence{ScopeID: scope.ScopeID, RepositoryID: entry.Repository.RepositoryID, Evidence: evidence}); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) preparation(ctx context.Context, id string, status statestore.RepositoryScopePreparationStatus, reason string) error {
	for i := 0; i < 3; i++ {
		previous, err := s.scopes.RepositoryScopePreparation(ctx, id)
		if err != nil {
			return err
		}
		if previous.Status == status && previous.Reason == reason {
			return nil
		}
		if _, err = s.scopes.SetRepositoryScopePreparation(ctx, id, previous.Revision, status, reason); err == nil {
			return nil
		} else if !errors.Is(err, statestore.ErrRepositoryScopeConflict) {
			return err
		}
	}
	return statestore.ErrRepositoryScopeConflict
}

func (s *Service) Get(ctx context.Context, id string) (View, error) {
	scope, err := s.scopes.RepositoryScope(ctx, id)
	if err != nil {
		return View{}, err
	}
	preparation, err := s.scopes.RepositoryScopePreparation(ctx, id)
	if err != nil {
		return View{}, err
	}
	evidence, err := s.scopes.RepositoryScopeEvidence(ctx, id)
	return View{SchemaVersion: 1, Scope: scope, Preparation: preparation, Evidence: evidence}, err
}
