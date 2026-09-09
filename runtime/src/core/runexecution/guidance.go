package runexecution

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"darkstar/src/ports/provider"
	"darkstar/src/ports/statestore"
)

type GuidanceResult struct {
	ID      string `json:"id"`
	Status  string `json:"status"`
	Message string `json:"message,omitempty"`
}

func (s *Service) Guide(ctx context.Context, request ControlRequest, message string) (GuidanceResult, error) {
	s.runEventMu.Lock()
	locked := true
	defer func() {
		if locked {
			s.runEventMu.Unlock()
		}
	}()
	if request.Actor.Type != statestore.ActorUser || strings.TrimSpace(message) == "" || len(message) > 32000 {
		return GuidanceResult{}, errors.New("a human message of at most 32000 bytes is required")
	}
	run, err := s.store.Run(ctx, request.RunID)
	if err != nil {
		return GuidanceResult{}, err
	}
	source := s.store.(interface {
		EventByCommand(context.Context, string, string) (statestore.Event, error)
	})
	if recorded, readErr := source.EventByCommand(ctx, run.RunID, request.IdempotencyKey); readErr == nil {
		var old struct {
			Message string `json:"message"`
		}
		_ = json.Unmarshal(recorded.Data, &old)
		if recorded.Kind != "run.guidance_requested" || old.Message != message {
			return GuidanceResult{}, errors.New("message key was reused")
		}
		status := "unconfirmed"
		if outcome, e := source.EventByCommand(ctx, run.RunID, "delivery:"+request.IdempotencyKey); e == nil {
			var body GuidanceResult
			_ = json.Unmarshal(outcome.Data, &body)
			return body, nil
		}
		return GuidanceResult{ID: request.IdempotencyKey, Status: status, Message: "Message is saved; delivery has not been confirmed. It will not be resent automatically."}, nil
	}
	if run.Status != statestore.RunRunning || run.ResourceVersion != request.ExpectedResourceVersion {
		return GuidanceResult{}, errors.New("the active run changed; refresh before sending")
	}
	s.mu.Lock()
	var adapter interface {
		Steer(context.Context, provider.AttemptHandle, string, string) error
	}
	var handle provider.AttemptHandle
	for _, worker := range s.workers {
		if worker.attempt.RunID == run.RunID && worker.adapter != nil {
			adapter, _ = worker.adapter.(interface {
				Steer(context.Context, provider.AttemptHandle, string, string) error
			})
			handle = worker.handle
			break
		}
	}
	s.mu.Unlock()
	if adapter == nil {
		return GuidanceResult{}, errors.New("this run has no active agent that accepts messages")
	}
	if _, err = s.store.Append(ctx, pendingEvent("run.guidance_requested", statestore.AggregateRun, run.RunID, run.ResourceVersion, run.RunID, request.IdempotencyKey, statestore.ActorUser, request.Actor.ID, s.now(), map[string]any{"id": request.IdempotencyKey, "message": message, "attemptId": handle.AttemptID})); err != nil {
		return GuidanceResult{}, err
	}
	deliveryCtx, cancel := context.WithTimeout(s.ctx, 15*time.Second)
	s.runEventMu.Unlock()
	locked = false
	defer cancel()
	result := GuidanceResult{ID: request.IdempotencyKey, Status: "accepted"}
	if err = adapter.Steer(deliveryCtx, handle, request.IdempotencyKey, message); err != nil {
		result.Status = "unconfirmed"
		result.Message = err.Error()
	}
	s.runEventMu.Lock()
	locked = true
	for retry := 0; retry < 3; retry++ {
		current, readErr := s.store.Run(deliveryCtx, run.RunID)
		if readErr != nil {
			return result, readErr
		}
		_, err = s.store.Append(deliveryCtx, pendingEvent("run.guidance_delivery", statestore.AggregateRun, run.RunID, current.ResourceVersion, run.RunID, "delivery:"+request.IdempotencyKey, statestore.ActorSystem, "daemon", s.now(), result))
		if err == nil {
			return result, nil
		}
	}
	return result, err
}
