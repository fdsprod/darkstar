package runexecution

import "context"

func (s *Service) prepareRepositoryPause(ctx context.Context, runID, key string) error {
	s.mu.Lock()
	builder := s.requestBuilder
	s.mu.Unlock()
	if coordinator, ok := builder.(interface {
		PrepareWorkflowPause(context.Context, string, string) error
	}); ok {
		return coordinator.PrepareWorkflowPause(ctx, runID, key)
	}
	return nil
}

func (s *Service) reconcileRepositoryCancellation(ctx context.Context, runID string) error {
	s.mu.Lock()
	builder := s.requestBuilder
	s.mu.Unlock()
	if coordinator, ok := builder.(interface {
		ReconcileWorkflowCancellation(context.Context, string) error
	}); ok {
		return coordinator.ReconcileWorkflowCancellation(ctx, runID)
	}
	return nil
}
