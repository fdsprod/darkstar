package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	gitadapter "darkstar/src/adapters/repository/git"
	"darkstar/src/adapters/statestore/sqlite"
	"darkstar/src/core/identity"
	"darkstar/src/core/runexecution"
	"darkstar/src/core/workflow"
	"darkstar/src/ports/repository"
	"darkstar/src/ports/statestore"
)

// AcquireWorkflowWrite holds the existing repository lease for the complete
// attempt, including the provider's lifetime. An uncertain exit never frees it.
func (w *daemonProviderWiring) AcquireWorkflowWrite(ctx context.Context, request runexecution.AttemptRequestContext) (context.Context, func(bool) error, error) {
	root, writer, err := w.writerRepository(ctx, request)
	if err != nil || !writer {
		return ctx, nil, err
	}
	if w.leaseStore == nil || w.daemonInstanceID == "" {
		return ctx, nil, errors.New("repository writer coordination is unavailable")
	}
	manager, err := gitadapter.New("")
	if err != nil {
		return ctx, nil, err
	}
	observed, err := manager.Inspect(ctx, repository.InspectRequest{Path: root})
	if err != nil {
		return ctx, nil, err
	}
	scope := filepath.Clean(observed.Repository.CommonGitDir)
	if runtime.GOOS == "windows" {
		scope = strings.ToLower(scope)
	}
	return acquireRepositoryAttemptLease(ctx, w.leaseStore, scope, w.daemonInstanceID, request.Attempt)
}

func (w *daemonProviderWiring) writerRepository(ctx context.Context, r runexecution.AttemptRequestContext) (string, bool, error) {
	var input workflow.Identifier
	switch node := r.Node.(type) {
	case workflow.WorkspacePrepareNode:
		binding, err := w.resourceRepository(ctx, r, r.NodeInputs[node.Executor.RepositoryInput], true)
		if err != nil {
			return "", true, err
		}
		if binding != nil {
			return binding.Repository.Root, true, nil
		}
		return w.projectRoot, true, nil
	case workflow.ImplementationNode:
		input = node.Executor.WorkspaceInput
	case workflow.GitCommitNode:
		input = node.Executor.WorkspaceInput
	case workflow.GitPushNode:
		input = node.Executor.WorkspaceInput
	case workflow.CreatePRNode:
		input = node.Executor.WorkspaceInput
	case workflow.WorkspaceValidateNode:
		input = node.Executor.WorkspaceInput
	case workflow.CommandNode:
		root, err := w.attemptRepositoryRoot(ctx, r)
		if err != nil {
			return "", true, err
		}
		for id, declaration := range node.Fields().Inputs {
			if declaration.ValueType() == workflow.ValueRepository {
				if _, err := w.resourceRepository(ctx, r, r.NodeInputs[id], true); err != nil {
					return "", true, err
				}
				return root, true, nil
			}
		}
		// A modern no-code command operates in its isolated work directory and
		// has no repository lease to acquire. Legacy commands retain their lock.
		return root, w.authorizeWorkspaceProject(r) == nil, nil
	default:
		return "", false, nil
	}
	if input == "" {
		return "", true, errors.New("writer requires a prepared workspace input")
	}
	workspace, err := w.resolveDeliveryWorkspace(ctx, r, r.NodeInputs[input], true)
	return workspace.Repository, true, err
}

func acquireRepositoryAttemptLease(ctx context.Context, store statestore.Store, scope, daemonID string, attempt statestore.AttemptProjection) (context.Context, func(bool) error, error) {
	payload, err := json.Marshal(map[string]string{"attemptId": attempt.AttemptID, "runId": attempt.RunID})
	if err != nil {
		return ctx, nil, err
	}
	_, err = store.Enqueue(ctx, statestore.EnqueueRequest{Kind: statestore.QueueRepositoryWrite, ScopeID: scope, ItemID: attempt.AttemptID, AvailableAt: attempt.CreatedAt, Payload: payload})
	if err != nil {
		return ctx, nil, err
	}
	acquired := false
	defer func() {
		if !acquired {
			_ = store.RemoveQueueEntry(context.WithoutCancel(ctx), statestore.QueueRepositoryWrite, scope, attempt.AttemptID)
		}
	}()
	request := statestore.AcquireLeaseRequest{
		LeaseID: identity.Deterministic("lease_", scope+"\x00"+attempt.AttemptID), ScopeKind: statestore.LeaseScopeRepository,
		ScopeID: scope, HolderAttemptID: attempt.AttemptID, DaemonInstanceID: daemonID,
		HostBootID: "unavailable", ProcessIdentity: payload,
	}
	var lease statestore.Lease
	if control, ok := store.(repositoryLeaseControlStore); ok {
		existing, readErr := control.OwnedRepositoryLease(ctx, request.LeaseID, attempt.AttemptID, daemonID)
		if readErr == nil {
			request.ExpectedFencingToken = existing.FencingToken - 1
		} else if !errors.Is(readErr, statestore.ErrNotFound) {
			return ctx, nil, readErr
		}
	}
	for {
		lease, err = store.AcquireRepositoryLock(ctx, attempt.AttemptID, request)
		if err == nil {
			break
		}
		var fencing *sqlite.FencingConflictError
		if errors.As(err, &fencing) {
			request.ExpectedFencingToken = fencing.Actual
			continue
		}
		var held *sqlite.LeaseHeldError
		var queued *sqlite.QueueHeadError
		if errors.As(err, &held) && (held.State != statestore.LeaseHeld || held.ExpiresAt.Before(time.Now())) {
			return ctx, nil, fmt.Errorf("repository requires reconciliation before another writer: %w", err)
		}
		if !errors.As(err, &held) && !errors.As(err, &queued) {
			return ctx, nil, err
		}
		if queued != nil {
			head, readErr := store.Attempt(ctx, queued.Actual)
			if readErr != nil {
				return ctx, nil, fmt.Errorf("repository queue head %s requires reconciliation: %w", queued.Actual, readErr)
			}
			if head.Status.Terminal() {
				if err := store.RemoveQueueEntry(ctx, statestore.QueueRepositoryWrite, scope, queued.Actual); err != nil {
					return ctx, nil, err
				}
				continue
			}
		}
		select {
		case <-ctx.Done():
			return ctx, nil, ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
	guard := statestore.LeaseGuard{LeaseID: lease.LeaseID, HolderAttemptID: lease.HolderAttemptID, DaemonInstanceID: lease.DaemonInstanceID, FencingToken: lease.FencingToken}
	if repositoryPauseIntent(lease) {
		control, ok := store.(repositoryLeaseControlStore)
		if !ok {
			return ctx, nil, errors.New("repository pause reconciliation is unavailable")
		}
		lease, err = control.ResumeRepositoryLease(ctx, guard)
		if err != nil {
			return ctx, nil, err
		}
	}
	if _, err := store.ValidateLease(ctx, statestore.LeaseScopeRepository, scope, guard); err != nil {
		return ctx, nil, err
	}
	// Adoption of an existing lease does not dequeue in older stores. Explicit
	// idempotent cleanup prevents a resumed owner from leaving an abandoned head.
	if err := store.RemoveQueueEntry(ctx, statestore.QueueRepositoryWrite, scope, attempt.AttemptID); err != nil {
		return ctx, nil, err
	}
	acquired = true
	child, cancel := context.WithCancel(ctx)
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(statestore.MaximumHeartbeatInterval)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case <-child.Done():
				return
			case <-ticker.C:
				if _, err := store.HeartbeatLease(child, guard, statestore.DefaultLeaseDuration); err != nil {
					cancel()
					return
				}
			}
		}
	}()
	var once sync.Once
	var finishErr error
	finish := func(settled bool) error {
		once.Do(func() {
			close(stop)
			<-done
			cancel()
			releaseCtx, releaseCancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			defer releaseCancel()
			evidence, _ := json.Marshal(map[string]any{"attemptId": attempt.AttemptID, "settled": settled})
			if control, ok := store.(repositoryLeaseControlStore); ok {
				current, readErr := control.RepositoryLease(releaseCtx, guard)
				if readErr != nil {
					finishErr = readErr
					return
				}
				if repositoryPauseIntent(current) {
					if settled {
						_, finishErr = store.CompleteLeaseRelease(releaseCtx, guard, evidence)
					}
					return
				}
			}
			if !settled {
				_, finishErr = store.MarkLeaseReconcileRequired(releaseCtx, guard, evidence)
				return
			}
			if _, finishErr = store.BeginLeaseRelease(releaseCtx, guard); finishErr == nil {
				_, finishErr = store.CompleteLeaseRelease(releaseCtx, guard, evidence)
			}
		})
		return finishErr
	}
	return child, finish, nil
}
