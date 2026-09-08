package runexecution

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"time"

	"darkstar/src/core/workflow"
	"darkstar/src/ports/statestore"
)

// EnableQueue is called before recovery. Durable run states own admission;
// the loop and worker map are disposable process-local coordination only.
func (s *Service) EnableQueue(limit func() (int, error)) error {
	if limit == nil {
		return errors.New("queue limit resolver is required")
	}
	if n, err := limit(); err != nil || n < 1 {
		return errors.New("queue limit must be positive")
	}
	s.queueEnabled, s.queueLimit = true, limit
	return nil
}

func (s *Service) StartQueue() {
	s.wait.Add(1)
	go func() {
		defer s.wait.Done()
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-s.ctx.Done():
				return
			case <-ticker.C:
				_ = s.DispatchQueue(s.ctx)
			}
		}
	}()
}

// hasRunCapacityLocked counts runs, not nodes. A retained run may traverse
// multiple nodes without consuming extra slots. Waiting runs release capacity
// only after their provider workers have actually quiesced.
func (s *Service) hasRunCapacityLocked(ctx context.Context, runID string) (bool, error) {
	limit, err := s.queueLimit()
	if err != nil {
		return false, err
	}
	active := map[string]bool{}
	for _, worker := range s.workers {
		active[worker.attempt.RunID] = true
	}
	runs, err := s.store.Runs(ctx)
	if err != nil {
		return false, err
	}
	for _, run := range runs {
		if run.Status == statestore.RunRunning {
			active[run.RunID] = true
		}
	}
	return active[runID] || len(active) < limit, nil
}

// DispatchQueue admits oldest work first within priority. The run version and
// deterministic entry attempt identity prevent duplicate claims on restart.
func (s *Service) DispatchQueue(ctx context.Context) error {
	s.queueMu.Lock()
	defer s.queueMu.Unlock()
	if !s.queueEnabled || !s.schedulingAdmitted() {
		return nil
	}
	runs, err := s.store.Runs(ctx)
	if err != nil {
		return err
	}
	sort.Slice(runs, func(i, j int) bool {
		if runs[i].Priority != runs[j].Priority {
			return runs[i].Priority > runs[j].Priority
		}
		if !runs[i].CreatedAt.Equal(runs[j].CreatedAt) {
			return runs[i].CreatedAt.Before(runs[j].CreatedAt)
		}
		return runs[i].RunID < runs[j].RunID
	})
	for _, run := range runs {
		if run.Status == statestore.RunReady {
			if _, err := s.store.RunExecutionContext(ctx, run.RunID); err != nil {
				continue
			}
			var route workflow.Route
			if json.Unmarshal([]byte(run.RouteSnapshot), &route) != nil {
				continue
			}
			assessment, err := readPreparation(route)
			if err != nil || (assessment != nil && assessment.Readiness() != "ready") {
				continue
			}
			run, err = s.Launch(ctx, ControlRequest{RunID: run.RunID, ExpectedResourceVersion: run.ResourceVersion, IdempotencyKey: "queue:" + run.RunID, Actor: statestore.Actor{Type: statestore.ActorSystem, ID: "daemon"}})
			if err != nil {
				continue
			}
		}
		if run.Status != statestore.RunQueued && run.Status != statestore.RunRunning {
			continue
		}
		s.mu.Lock()
		capacity, err := s.hasRunCapacityLocked(ctx, run.RunID)
		s.mu.Unlock()
		if err != nil {
			return err
		}
		if !capacity {
			continue
		}
		attempts, err := s.store.AttemptsForRun(ctx, run.RunID)
		if err != nil {
			return err
		}
		if len(attempts) == 0 && run.Status == statestore.RunQueued {
			attempt, err := s.ensureWorkflowEntryAttempt(ctx, run)
			if err != nil {
				if failureErr := s.failQueuedRun(ctx, run, "QUEUE_ADMISSION_FAILED", err); failureErr != nil {
					return failureErr
				}
				continue
			}
			if err := s.launch(attempt); err != nil {
				return err
			}
		}
	}
	// Resume/retry may already have an attempt. Only attempts without a live
	// worker are picked up; recovery resolves uncertain starts before this loop.
	attempts, err := s.store.ActiveAttempts(ctx)
	if err != nil {
		return err
	}
	for _, attempt := range attempts {
		run, err := s.store.Run(ctx, attempt.RunID)
		if err != nil {
			return err
		}
		if run.Status == statestore.RunQueued || run.Status == statestore.RunRunning {
			if err := s.launch(attempt); err != nil {
				return err
			}
		}
	}
	return nil
}
