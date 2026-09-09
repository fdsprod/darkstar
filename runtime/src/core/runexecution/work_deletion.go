package runexecution

import (
	"context"
	"darkstar/src/ports/statestore"
	"errors"
	"fmt"
	"strings"
)

// DeleteWork records durable intent before stopping execution. Reconciliation
// retries from this intent after restart; retained events remain addressable.
func (s *Service) DeleteWork(ctx context.Context, id string, expected uint64, key string) (statestore.WorkItemProjection, error) {
	if expected == 0 || len(key) < 8 || len(key) > 128 || strings.TrimSpace(key) != key {
		return statestore.WorkItemProjection{}, ErrInvalidControl
	}
	work, err := s.store.WorkItem(ctx, id)
	if err != nil {
		return work, err
	}
	if work.Deletion == statestore.WorkRetained {
		if work.ResourceVersion != expected {
			return work, &ControlConflictError{RunID: id, Expected: expected, Current: work.ResourceVersion}
		}
		_, err = s.store.Append(ctx, pendingEvent("work.deletion_requested", statestore.AggregateWork, id, expected, id, "delete:"+key, statestore.ActorUser, "local-user", s.now(), map[string]any{}))
		if err != nil {
			return work, err
		}
	}
	if err := s.reconcileWorkDeletion(ctx, id); err != nil {
		return s.store.WorkItem(ctx, id)
	}
	return s.store.WorkItem(ctx, id)
}

func (s *Service) ReconcileWorkDeletions(ctx context.Context) error {
	items, err := s.store.WorkItems(ctx)
	if err != nil {
		return err
	}
	for _, work := range items {
		if work.Deletion == statestore.WorkDeleting {
			if err := s.reconcileWorkDeletion(ctx, work.WorkItemID); err != nil {
				continue
			}
		}
	}
	return nil
}

func (s *Service) reconcileWorkDeletion(ctx context.Context, id string) error {
	work, err := s.store.WorkItem(ctx, id)
	if err != nil || work.Deletion != statestore.WorkDeleting {
		return err
	}
	runs, err := s.store.RunsForWorkItem(ctx, id)
	if err != nil {
		return err
	}
	// Cancel pending human decisions without accepting their candidates.
	if reader, ok := s.store.(interface {
		Approvals(context.Context, statestore.ApprovalStatus) ([]statestore.ApprovalProjection, error)
	}); ok {
		approvals, err := reader.Approvals(ctx, statestore.ApprovalPending)
		if err != nil {
			return err
		}
		owned := map[string]bool{}
		for _, run := range runs {
			owned[run.RunID] = true
		}
		for _, approval := range approvals {
			if owned[approval.RunID] {
				_, err := s.store.Append(ctx, pendingEvent("approval.cancelled", statestore.AggregateApproval, approval.ApprovalID, approval.ResourceVersion, approval.RunID, "delete-approval:"+approval.ApprovalID, statestore.ActorSystem, "daemon", s.now(), map[string]any{"reason": "work_deleted"}))
				if err != nil {
					return err
				}
			}
		}
	}
	var failures []error
	for _, run := range runs {
		if run.Status == statestore.RunCompleted || run.Status == statestore.RunCancelled {
			continue
		}
		result, err := s.Cancel(ctx, ControlRequest{RunID: run.RunID, ExpectedResourceVersion: run.ResourceVersion, IdempotencyKey: fmt.Sprintf("delete:%s:%d", run.RunID, run.ResourceVersion), Actor: statestore.Actor{Type: statestore.ActorSystem, ID: "daemon"}})
		if err != nil {
			failures = append(failures, err)
			continue
		}
		if result.Status != statestore.RunCompleted && result.Status != statestore.RunCancelled {
			failures = append(failures, errors.New("provider cancellation requires reconciliation"))
		}
	}
	if len(failures) != 0 {
		return errors.Join(failures...)
	}
	work, err = s.store.WorkItem(ctx, id)
	if err != nil {
		return err
	}
	if work.Deletion == statestore.WorkDeleted {
		return nil
	}
	_, err = s.store.Append(ctx, pendingEvent("work.deleted", statestore.AggregateWork, id, work.ResourceVersion, id, "deleted:"+id, statestore.ActorSystem, "daemon", s.now(), map[string]any{}))
	return err
}
