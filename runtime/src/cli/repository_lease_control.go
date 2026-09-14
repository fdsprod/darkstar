package cli

import (
	"context"
	"encoding/json"
	"errors"

	"darkstar/src/ports/statestore"
)

type repositoryLeaseControlStore interface {
	PrepareRepositoryPause(context.Context, string, string, string) error
	RepositoryLease(context.Context, statestore.LeaseGuard) (statestore.Lease, error)
	OwnedRepositoryLease(context.Context, string, string, string) (statestore.Lease, error)
	ResumeRepositoryLease(context.Context, statestore.LeaseGuard) (statestore.Lease, error)
	ReleaseCancelledRepositoryLeases(context.Context, string, string) error
}

func (w *daemonProviderWiring) PrepareWorkflowPause(ctx context.Context, runID, key string) error {
	store, ok := w.leaseStore.(repositoryLeaseControlStore)
	if !ok {
		return errors.New("repository pause reconciliation is unavailable")
	}
	return store.PrepareRepositoryPause(ctx, runID, w.daemonInstanceID, key)
}

func (w *daemonProviderWiring) ReconcileWorkflowCancellation(ctx context.Context, runID string) error {
	store, ok := w.leaseStore.(repositoryLeaseControlStore)
	if !ok {
		return errors.New("repository cancellation reconciliation is unavailable")
	}
	return store.ReleaseCancelledRepositoryLeases(ctx, runID, w.daemonInstanceID)
}

func repositoryPauseIntent(lease statestore.Lease) bool {
	var evidence struct {
		Kind string `json:"kind"`
	}
	return lease.State == statestore.LeaseReconcileRequired && json.Unmarshal(lease.Evidence, &evidence) == nil && evidence.Kind == "repository_pause_intent"
}
