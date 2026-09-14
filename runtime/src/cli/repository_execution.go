package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	gitadapter "darkstar/src/adapters/repository/git"
	"darkstar/src/core/config"
	"darkstar/src/core/nodes"
	"darkstar/src/core/runexecution"
	"darkstar/src/core/workflow"
	"darkstar/src/ports/repository"
	"darkstar/src/ports/statestore"
)

type repositoryResolver interface {
	ResolveRepository(context.Context, string, string, bool) (statestore.ProjectRepository, error)
}

type repositoryResourceValue struct {
	ProjectID  string                   `json:"projectId"`
	Name       string                   `json:"name"`
	SourceHash string                   `json:"sourceHash"`
	Binding    *nodes.RepositoryBinding `json:"repositoryBinding,omitempty"`
}

// ResolveWorkflowRepositories binds repository resources before run admission.
// Paths and membership revisions come exclusively from daemon-owned records.
func (w *daemonProviderWiring) ResolveWorkflowRepositories(ctx context.Context, workflowID, version string, project statestore.ProjectProjection, selections map[workflow.Identifier]string) (map[workflow.Identifier]json.RawMessage, error) {
	values := make(map[workflow.Identifier]json.RawMessage)
	if w.repositories == nil {
		return values, nil
	}
	definition, err := w.workflows.Definition(ctx, workflowID, version)
	if err != nil {
		return nil, err
	}
	return w.resolveRepositoryResources(ctx, definition.Document, project, selections)
}

func (w *daemonProviderWiring) resolveRepositoryResources(ctx context.Context, document workflow.Document, project statestore.ProjectProjection, selections map[workflow.Identifier]string) (map[workflow.Identifier]json.RawMessage, error) {
	values := make(map[workflow.Identifier]json.RawMessage)
	for id, repositoryID := range selections {
		declaration, exists := document.Spec.Inputs[id]
		if !exists || declaration.Resource == nil || strings.TrimSpace(repositoryID) == "" || strings.TrimSpace(repositoryID) != repositoryID {
			return nil, fmt.Errorf("invalid repository selection for input %s", id)
		}
		if _, ok := declaration.Resource.Source.(workflow.RepositoryResource); !ok {
			return nil, fmt.Errorf("input %s is not a repository resource", id)
		}
	}
	for id, declaration := range document.Spec.Inputs {
		if declaration.Resource == nil {
			if declaration.Type == workflow.ValueRepository {
				return nil, fmt.Errorf("repository input %s must select a daemon repository resource", id)
			}
			continue
		}
		_, ok := declaration.Resource.Source.(workflow.RepositoryResource)
		if !ok {
			if declaration.Type == workflow.ValueRepository {
				return nil, fmt.Errorf("repository input %s must select a daemon repository resource", id)
			}
			continue
		}
		member, err := w.repositories.ResolveRepository(ctx, project.ProjectID, selections[id], false)
		if err != nil {
			return nil, err
		}
		resolved, err := w.resolveRepositoryConfiguration(ctx, project, member)
		if err != nil {
			return nil, err
		}
		binding := &nodes.RepositoryBinding{Repository: member.Repository, MembershipRevision: member.Membership.Revision, Configuration: resolved}
		values[id], err = json.Marshal(repositoryResourceValue{ProjectID: project.ProjectID, Name: member.Membership.Label, SourceHash: project.SourceHash, Binding: binding})
		if err != nil {
			return nil, err
		}
	}
	return values, nil
}

func (w *daemonProviderWiring) resolveRepositoryConfiguration(ctx context.Context, project statestore.ProjectProjection, member statestore.ProjectRepository) (config.ResolvedRepositorySettings, error) {
	projectConfig, err := w.repositoryStore.ProjectRepositoryConfiguration(ctx, project.ProjectID)
	if err != nil {
		return config.ResolvedRepositorySettings{}, err
	}
	defaults := statestore.RepositorySettings{ConfigurationRoot: member.Repository.Root, WorktreeBase: ".darkstar/worktrees"}
	defaultReference := "repository-defaults"
	if w.configuration != nil {
		scope, err := config.ProjectMutationScope(project.ProjectID)
		if err != nil {
			return config.ResolvedRepositorySettings{}, err
		}
		state, err := w.configuration.State(ctx, scope)
		if err != nil {
			return config.ResolvedRepositorySettings{}, err
		}
		for _, setting := range state.Effective {
			if setting.Key == "workspace.baseRef" {
				defaults.BaseRef, _ = setting.Value.Value().(string)
				defaultReference += "; workspace.baseRef=" + setting.Source.Scope().String() + ":" + setting.Source.Reference()
			}
		}
	}
	return config.ResolveRepositorySettings(
		config.RepositorySettingsLayer{Scope: config.ScopeDefault, Reference: defaultReference, Settings: defaults},
		config.RepositorySettingsLayer{Scope: config.ScopeProject, Reference: fmt.Sprintf("%s@%d", project.ProjectID, project.ResourceVersion), Settings: projectConfig.Defaults},
		config.RepositorySettingsLayer{Scope: config.ScopeMembership, Reference: fmt.Sprintf("%s/%s@%d", project.ProjectID, member.Repository.RepositoryID, member.Membership.Revision), Settings: member.Membership.Settings},
	)
}

func (w *daemonProviderWiring) verifyRepositoryBinding(ctx context.Context, project statestore.ProjectProjection, binding *nodes.RepositoryBinding, writer bool) error {
	if project.Status != statestore.ProjectActive || binding == nil || w.repositoryStore == nil {
		return errors.New("repository membership authorization unavailable")
	}
	stored, err := w.repositoryStore.Repository(ctx, binding.Repository.RepositoryID)
	if err != nil {
		return err
	}
	if stored.RepositoryID != binding.Repository.RepositoryID || stored.IdentityKey != binding.Repository.IdentityKey || !sameRepositoryPath(stored.Root, binding.Repository.Root) || !sameRepositoryPath(stored.CommonGitDir, binding.Repository.CommonGitDir) || !filepath.IsAbs(stored.Root) {
		return errors.New("frozen repository coordinates differ from the registered repository")
	}
	membership, err := w.repositoryStore.MembershipRevision(ctx, project.ProjectID, stored.RepositoryID, binding.MembershipRevision)
	if err != nil {
		return err
	}
	if membership.Status != statestore.MembershipActive || (writer && membership.Role != statestore.RepositoryImplementation) {
		return errors.New("repository membership does not grant implementation access")
	}
	manager, err := gitadapter.New("")
	if err != nil {
		return err
	}
	observed, err := manager.Inspect(ctx, repository.InspectRequest{Path: stored.Root})
	if err != nil {
		return err
	}
	if !sameRepositoryPath(observed.Repository.CommonGitDir, stored.CommonGitDir) || !sameRepositoryPath(observed.Repository.Root, stored.Root) {
		return errors.New("registered repository identity changed")
	}
	return nil
}

func sameRepositoryPath(left, right string) bool {
	left, right = filepath.Clean(left), filepath.Clean(right)
	if runtime.GOOS == "windows" {
		return strings.EqualFold(left, right)
	}
	return left == right
}

func (w *daemonProviderWiring) resourceRepository(ctx context.Context, r runexecution.AttemptRequestContext, raw json.RawMessage, writer bool) (*nodes.RepositoryBinding, error) {
	var resource repositoryResourceValue
	if err := json.Unmarshal(raw, &resource); err != nil {
		return nil, err
	}
	if resource.ProjectID != r.Project.ProjectID || resource.SourceHash != r.Project.SourceHash {
		return nil, errors.New("repository resource belongs to another project")
	}
	if resource.Binding == nil {
		if err := w.authorizeWorkspaceProject(r); err != nil {
			return nil, err
		}
		if w.repositories != nil {
			if err := w.authorizeLegacyRepositoryResource(ctx, r); err != nil {
				return nil, err
			}
		}
		return nil, nil
	}
	// Only a resource saved in the run's admitted inputs may grant access.
	frozen := false
	for _, input := range r.ExecutionContext.RunInputs {
		var candidate repositoryResourceValue
		if json.Unmarshal(input, &candidate) == nil && candidate.Binding != nil {
			left, _ := json.Marshal(candidate)
			right, _ := json.Marshal(resource)
			if string(left) == string(right) {
				frozen = true
			}
		}
	}
	if !frozen {
		return nil, errors.New("repository resource was not frozen at run admission")
	}
	if err := w.verifyRepositoryBinding(ctx, r.Project, resource.Binding, writer); err != nil {
		return nil, err
	}
	return resource.Binding, nil
}

// A source hash is a compatibility claim, never a new membership grant. Only
// runs created before this project's first canonical membership may use it.
func (w *daemonProviderWiring) authorizeLegacyRepositoryResource(ctx context.Context, r runexecution.AttemptRequestContext) error {
	if r.Run.CreatedAt.IsZero() || w.repositoryStore == nil {
		return errors.New("new repository preparation requires an admitted membership binding")
	}
	members, err := w.repositoryStore.ProjectRepositories(ctx, r.Project.ProjectID)
	if err != nil {
		return err
	}
	for _, member := range members {
		if !sameRepositoryPath(member.Repository.Root, w.projectRoot) {
			continue
		}
		first, err := w.repositoryStore.MembershipRevision(ctx, r.Project.ProjectID, member.Repository.RepositoryID, 1)
		if err != nil {
			return err
		}
		if r.Run.CreatedAt.Before(first.UpdatedAt) {
			return nil
		}
	}
	return errors.New("new repository preparation requires an admitted membership binding")
}

func (w *daemonProviderWiring) attemptRepositoryRoot(ctx context.Context, r runexecution.AttemptRequestContext) (string, error) {
	if node, ok := r.Node.(workflow.ImplementationNode); ok && node.Executor.WorkspaceInput != "" {
		prepared, err := w.resolvePreparedWorkspace(ctx, r, r.NodeInputs[node.Executor.WorkspaceInput])
		return prepared.Repository, err
	}
	root := ""
	for id, declaration := range r.Node.Fields().Inputs {
		if declaration.ValueType() != workflow.ValueRepository {
			continue
		}
		binding, err := w.resourceRepository(ctx, r, r.NodeInputs[id], false)
		if err != nil {
			return "", err
		}
		selected := w.projectRoot
		if binding != nil {
			selected = binding.Repository.Root
		}
		if root != "" && root != selected {
			return "", errors.New("multiple repository agent access requires a read-only snapshot")
		}
		root = selected
	}
	if root != "" {
		return root, nil
	}
	if w.repositories != nil {
		if _, readOnly := r.Node.(workflow.ReasoningNode); readOnly && r.Project.Status == statestore.ProjectActive {
			if r.Run.RunID == "" || filepath.Base(r.Run.RunID) != r.Run.RunID || strings.ContainsAny(r.Run.RunID, `/\\:`) {
				return "", errors.New("a durable run identity is required for the agent workspace")
			}
			root := filepath.Join(os.TempDir(), "darkstar-agent-workspaces", r.Run.RunID)
			if err := os.MkdirAll(root, 0700); err != nil {
				return "", err
			}
			return root, nil
		}
	}
	if err := w.authorizeWorkspaceProject(r); err != nil {
		return "", err
	}
	return w.projectRoot, nil
}
