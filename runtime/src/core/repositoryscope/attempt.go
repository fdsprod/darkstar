package repositoryscope

import (
	"context"
	"errors"
	"reflect"
	"sort"
	"strings"

	"darkstar/src/ports/statestore"
)

// BindAttempt is daemon-only. It narrows an immutable scope and verifies the
// retained evidence; provider capability validation still precedes dispatch.
func (s *Service) BindAttempt(ctx context.Context, scopeID, attemptID string, repositoryIDs []string) (AttemptView, error) {
	if !strings.HasPrefix(attemptID, "attempt_") || len(attemptID) != 34 || repositoryIDs == nil {
		return AttemptView{}, ErrInvalidRequest
	}
	ids := append([]string{}, repositoryIDs...)
	sort.Strings(ids)
	for i, id := range ids {
		if id == "" || (i > 0 && ids[i-1] == id) {
			return AttemptView{}, ErrInvalidRequest
		}
	}
	view, err := s.Get(ctx, scopeID)
	if err != nil {
		return AttemptView{}, err
	}
	if view.Preparation.Status != statestore.RepositoryScopeReady {
		return AttemptView{}, ErrScopeUnavailable
	}
	result := AttemptView{Repositories: make([]statestore.FrozenRepositoryScopeEntry, 0, len(ids)), Evidence: make([]statestore.RepositoryScopeEvidence, 0, len(ids))}
	for _, id := range ids {
		found := false
		for _, entry := range view.Scope.Repositories {
			if entry.Repository.RepositoryID == id {
				result.Repositories = append(result.Repositories, entry)
				found = true
				break
			}
		}
		if !found {
			return AttemptView{}, ErrInvalidRequest
		}
		found = false
		for _, evidence := range view.Evidence {
			if evidence.RepositoryID == id {
				if err = s.exporter.Verify(ctx, evidence.Evidence); err != nil {
					return AttemptView{}, errors.Join(ErrScopeUnavailable, err)
				}
				result.Evidence = append(result.Evidence, evidence)
				found = true
				break
			}
		}
		if !found {
			return AttemptView{}, ErrScopeUnavailable
		}
	}
	binding, err := s.scopes.RepositoryScopeAttempt(ctx, attemptID)
	if err == nil {
		if binding.ScopeID != scopeID || !reflect.DeepEqual(binding.RepositoryIDs, ids) {
			return AttemptView{}, ErrScopeConflict
		}
		result.Binding = binding
		return result, nil
	}
	if !errors.Is(err, statestore.ErrNotFound) {
		return AttemptView{}, err
	}
	binding = statestore.RepositoryScopeAttemptBinding{AttemptID: attemptID, ScopeID: scopeID, RepositoryIDs: ids, ScopeDigest: view.Scope.Digest, EvidenceDigest: statestore.RepositoryScopeContentDigest(result.Evidence), CreatedAt: s.now().UTC()}
	binding.Digest = statestore.RepositoryScopeBindingDigest(binding)
	result.Binding, err = s.scopes.BindRepositoryScopeAttempt(ctx, binding)
	return result, err
}

func (s *Service) Attempt(ctx context.Context, attemptID string) (AttemptView, error) {
	binding, err := s.scopes.RepositoryScopeAttempt(ctx, attemptID)
	if err != nil {
		return AttemptView{}, err
	}
	return s.BindAttempt(ctx, binding.ScopeID, attemptID, binding.RepositoryIDs)
}
