package runexecution

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"darkstar/src/core/workflow"
	"darkstar/src/ports/provider"
	"darkstar/src/ports/statestore"
)

var (
	// ErrInvalidControl classifies malformed control inputs.
	ErrInvalidControl = errors.New("invalid run control request")
	// ErrControlConflict classifies an optimistic-concurrency mismatch.
	ErrControlConflict = errors.New("run resource version conflict")
)

// ControlRequest is the complete input for pause, resume, and cancel. Those
// operations intentionally have no action-specific nullable fields.
type ControlRequest struct {
	RunID                   string
	ExpectedResourceVersion uint64
	IdempotencyKey          string
	Actor                   statestore.Actor
}

// RetryRequest adds only the optional node selector accepted by retry.
type RetryRequest struct {
	ControlRequest
	NodeID string
}

// ContinueRequest requires the new terminal boundary used to extend a route.
type ContinueRequest struct {
	ControlRequest
	UntilNodeID string
}

// InvalidTransitionError identifies a legal command used from an illegal run state.
type InvalidTransitionError struct {
	Action string
	RunID  string
	Status statestore.RunStatus
}

func (e *InvalidTransitionError) Error() string {
	return fmt.Sprintf("run %s cannot %s from state %s", e.RunID, e.Action, e.Status)
}

// ControlConflictError reports both sides of the If-Match comparison.
type ControlConflictError struct {
	RunID    string
	Expected uint64
	Current  uint64
}

func (e *ControlConflictError) Error() string {
	return fmt.Sprintf("%v for %s: expected %d, current %d", ErrControlConflict, e.RunID, e.Expected, e.Current)
}

func (e *ControlConflictError) Unwrap() error { return ErrControlConflict }

type controlCommandResponse struct {
	Result  *statestore.RunProjection `json:"result,omitempty"`
	Failure *controlFailure           `json:"failure,omitempty"`
}

type controlFailure struct {
	Kind     string               `json:"kind"`
	Message  string               `json:"message"`
	Status   statestore.RunStatus `json:"status,omitempty"`
	Expected uint64               `json:"expected,omitempty"`
	Current  uint64               `json:"current,omitempty"`
}

type stoppedWorker struct {
	adapter provider.Provider
	handle  provider.AttemptHandle
	done    <-chan struct{}
}

// Pause quiesces live provider work and moves queued/running work to waiting.
// The attempt remains non-terminal with its durable provider cursor intact.
func (s *Service) Pause(ctx context.Context, request ControlRequest) (statestore.RunProjection, error) {
	request = normalizeControlRequest(request)
	const action, eventKind = "pause", "run.paused"
	replayed, done, err := s.beginControl(ctx, action, eventKind, request, map[string]any{})
	if err != nil || done {
		return replayed, err
	}
	if _, err := s.controlRun(ctx, request, action, statestore.RunQueued, statestore.RunRunning); err != nil {
		return statestore.RunProjection{}, s.finishControlFailure(ctx, action, request.IdempotencyKey, err)
	}
	s.quiesceRun(ctx, request.RunID, false, request.IdempotencyKey)
	run, err := s.controlRun(ctx, request, action, statestore.RunQueued, statestore.RunRunning)
	if err != nil {
		return statestore.RunProjection{}, s.finishControlFailure(ctx, action, request.IdempotencyKey, err)
	}
	committed, err := s.store.Append(ctx, controlEvent(eventKind, run, request, map[string]any{"reason": "user"}, s.now()))
	if err != nil {
		return statestore.RunProjection{}, err
	}
	value, err := s.store.Run(ctx, request.RunID)
	if err != nil {
		return statestore.RunProjection{}, err
	}
	return value, s.finishControl(ctx, action, request.IdempotencyKey, value, committed)
}

// Resume moves a waiting or dependency-blocked run back to the durable queue.
func (s *Service) Resume(ctx context.Context, request ControlRequest) (statestore.RunProjection, error) {
	request = normalizeControlRequest(request)
	const action, eventKind = "resume", "run.resumed"
	replayed, done, err := s.beginControl(ctx, action, eventKind, request, map[string]any{})
	if err != nil || done {
		return replayed, err
	}
	run, err := s.controlRun(ctx, request, action, statestore.RunWaiting, statestore.RunBlocked)
	if err != nil {
		return statestore.RunProjection{}, s.finishControlFailure(ctx, action, request.IdempotencyKey, err)
	}
	committed, err := s.store.Append(ctx, controlEvent(eventKind, run, request, map[string]any{}, s.now()))
	if err != nil {
		return statestore.RunProjection{}, err
	}
	value, err := s.store.Run(ctx, request.RunID)
	if err != nil {
		return statestore.RunProjection{}, err
	}
	attempts, _ := s.store.AttemptsForRun(ctx, request.RunID)
	for index := len(attempts) - 1; index >= 0; index-- {
		if !attempts[index].Status.Terminal() {
			s.launch(attempts[index])
			break
		}
	}
	return value, s.finishControl(ctx, action, request.IdempotencyKey, value, committed)
}

// Retry creates a fresh attempt beneath the failed owner; prior attempt
// evidence is immutable and remains visible in the run view.
func (s *Service) Retry(ctx context.Context, request RetryRequest) (statestore.RunProjection, error) {
	const action, eventKind = "retry", "run.retried"
	request.ControlRequest = normalizeControlRequest(request.ControlRequest)
	request.NodeID = strings.TrimSpace(request.NodeID)
	replayed, done, err := s.beginControl(ctx, action, eventKind, request.ControlRequest, map[string]any{"nodeId": request.NodeID})
	if err != nil || done {
		return replayed, err
	}
	run, err := s.controlRun(ctx, request.ControlRequest, action, statestore.RunFailed)
	if err != nil {
		return statestore.RunProjection{}, s.finishControlFailure(ctx, action, request.IdempotencyKey, err)
	}
	attempts, err := s.store.AttemptsForRun(ctx, request.RunID)
	if err != nil {
		return statestore.RunProjection{}, err
	}
	var previous *statestore.AttemptProjection
	for index := len(attempts) - 1; index >= 0; index-- {
		candidate := &attempts[index]
		if request.NodeID != "" && candidate.NodeID != request.NodeID {
			continue
		}
		if candidate.Status == statestore.AttemptFailed || candidate.Status == statestore.AttemptInterrupted {
			previous = candidate
			break
		}
	}
	if previous == nil {
		err = fmt.Errorf("%w: run %s has no failed attempt for node %q", ErrInvalidControl, request.RunID, request.NodeID)
		return statestore.RunProjection{}, s.finishControlFailure(ctx, action, request.IdempotencyKey, err)
	}
	now := s.now().UTC().Round(0)
	attemptID := stableID("attempt_", action+"\x00"+request.RunID+"\x00"+request.IdempotencyKey)
	logReference := strings.TrimPrefix(attemptID, "attempt_") + ".log"
	events := []statestore.PendingEvent{
		controlEvent(eventKind, run, request.ControlRequest, map[string]any{"nodeId": previous.NodeID, "previousAttemptId": previous.AttemptID}, now),
	}
	if previous.VisitID != "" {
		node, nodeErr := s.store.Node(ctx, previous.VisitID)
		if nodeErr != nil {
			return statestore.RunProjection{}, nodeErr
		}
		events = append(events,
			pendingEvent("visit.retrying", statestore.AggregateVisit, node.VisitID, node.ResourceVersion, run.RunID, request.IdempotencyKey+":retrying", request.Actor.Type, request.Actor.ID, now, map[string]any{"previousAttemptId": previous.AttemptID}),
			pendingEvent("visit.started", statestore.AggregateVisit, node.VisitID, node.ResourceVersion+1, run.RunID, request.IdempotencyKey+":started", request.Actor.Type, request.Actor.ID, now, map[string]any{}),
		)
	}
	owner := map[string]any{
		"runId": run.RunID, "nodeId": previous.NodeID, "scenario": previous.Scenario,
		"provider": previous.Provider, "logReference": logReference, "priority": previous.Priority,
	}
	if previous.VisitID != "" {
		owner["visitId"] = previous.VisitID
	} else {
		owner["pointId"], owner["pointRevision"] = previous.PointID, previous.PointRevision
	}
	events = append(events, pendingEvent("attempt.created", statestore.AggregateAttempt, attemptID, 0, run.RunID, request.IdempotencyKey,
		statestore.ActorSystem, "daemon", now, owner))
	committed, err := s.store.Append(ctx, events...)
	if err != nil {
		return statestore.RunProjection{}, err
	}
	value, err := s.store.Run(ctx, request.RunID)
	if err != nil {
		return statestore.RunProjection{}, err
	}
	if previous.VisitID != "" {
		created, getErr := s.store.Attempt(ctx, attemptID)
		if getErr == nil {
			s.launch(created)
		}
	}
	return value, s.finishControl(ctx, action, request.IdempotencyKey, value, committed)
}

// Continue extends a completed frozen route to a new authored terminal and
// queues only the newly included work. Existing route history is never removed.
func (s *Service) Continue(ctx context.Context, request ContinueRequest) (statestore.RunProjection, error) {
	const action, eventKind = "continue", "run.continued"
	request.ControlRequest = normalizeControlRequest(request.ControlRequest)
	request.UntilNodeID = strings.TrimSpace(request.UntilNodeID)
	if request.UntilNodeID == "" {
		return statestore.RunProjection{}, fmt.Errorf("%w: until node is required", ErrInvalidControl)
	}
	replayed, done, err := s.beginControl(ctx, action, eventKind, request.ControlRequest, map[string]any{"until": request.UntilNodeID})
	if err != nil || done {
		return replayed, err
	}
	run, err := s.controlRun(ctx, request.ControlRequest, action, statestore.RunCompleted)
	if err != nil {
		return statestore.RunProjection{}, s.finishControlFailure(ctx, action, request.IdempotencyKey, err)
	}
	if run.RouteSnapshot == "" {
		err = fmt.Errorf("%w: run %s has no extensible frozen route", ErrInvalidControl, run.RunID)
		return statestore.RunProjection{}, s.finishControlFailure(ctx, action, request.IdempotencyKey, err)
	}
	var current workflow.Route
	if err := json.Unmarshal([]byte(run.RouteSnapshot), &current); err != nil {
		return statestore.RunProjection{}, fmt.Errorf("decode frozen route: %w", err)
	}
	s.mu.Lock()
	planner := s.planner
	s.mu.Unlock()
	if planner == nil {
		return statestore.RunProjection{}, ErrWorkflowUnavailable
	}
	preview, issues, err := planner.Preview(ctx, run.WorkflowID, run.WorkflowVersion,
		workflow.RouteRequest{From: current.Entry, Until: []workflow.Identifier{workflow.Identifier(request.UntilNodeID)}}, workflow.RouteContext{})
	if err != nil {
		return statestore.RunProjection{}, err
	}
	if len(issues) != 0 {
		return statestore.RunProjection{}, issues
	}
	if !strictRouteExtension(current, preview.Route, workflow.Identifier(request.UntilNodeID)) {
		err = fmt.Errorf("%w: terminal %s does not strictly extend the completed route", ErrInvalidControl, request.UntilNodeID)
		return statestore.RunProjection{}, s.finishControlFailure(ctx, action, request.IdempotencyKey, err)
	}
	routeJSON, err := json.Marshal(preview.Route)
	if err != nil {
		return statestore.RunProjection{}, err
	}
	routeDigest := fmt.Sprintf("%x", sha256.Sum256(routeJSON))
	data := map[string]any{
		"workflowDigest": preview.Workflow.Digest, "routeDigest": routeDigest,
		"routeSnapshot": json.RawMessage(routeJSON), "until": request.UntilNodeID,
	}
	committed, err := s.store.Append(ctx, controlEvent(eventKind, run, request.ControlRequest, data, s.now()))
	if err != nil {
		return statestore.RunProjection{}, err
	}
	value, err := s.store.Run(ctx, request.RunID)
	if err != nil {
		return statestore.RunProjection{}, err
	}
	return value, s.finishControl(ctx, action, request.IdempotencyKey, value, committed)
}

// Cancel quiesces active work, asks the provider to terminate when a live
// handle exists, and atomically closes every active child plus the run.
func (s *Service) Cancel(ctx context.Context, request ControlRequest) (statestore.RunProjection, error) {
	request = normalizeControlRequest(request)
	const action, eventKind = "cancel", "run.cancelled"
	replayed, done, err := s.beginControl(ctx, action, eventKind, request, map[string]any{})
	if err != nil || done {
		return replayed, err
	}
	allowed := []statestore.RunStatus{statestore.RunDraft, statestore.RunReady, statestore.RunQueued, statestore.RunRunning, statestore.RunWaiting, statestore.RunBlocked, statestore.RunFailed}
	if _, err := s.controlRun(ctx, request, action, allowed...); err != nil {
		return statestore.RunProjection{}, s.finishControlFailure(ctx, action, request.IdempotencyKey, err)
	}
	cancelEvidence := s.quiesceRun(ctx, request.RunID, true, request.IdempotencyKey)
	run, err := s.controlRun(ctx, request, action, allowed...)
	if err != nil {
		return statestore.RunProjection{}, s.finishControlFailure(ctx, action, request.IdempotencyKey, err)
	}
	attempts, err := s.store.AttemptsForRun(ctx, request.RunID)
	if err != nil {
		return statestore.RunProjection{}, err
	}
	now := s.now().UTC().Round(0)
	events := make([]statestore.PendingEvent, 0, len(attempts)*2+1)
	cancelledVisits := map[string]bool{}
	for _, attempt := range attempts {
		if attempt.Status.Terminal() {
			continue
		}
		data := map[string]any{"reason": "user", "logReference": attempt.LogReference}
		if evidence := cancelEvidence[attempt.AttemptID]; evidence != nil {
			data["providerCancellation"] = evidence
		}
		events = append(events, pendingEvent("attempt.cancelled", statestore.AggregateAttempt, attempt.AttemptID, attempt.ResourceVersion,
			run.RunID, request.IdempotencyKey, request.Actor.Type, request.Actor.ID, now, data))
		if attempt.VisitID != "" && !cancelledVisits[attempt.VisitID] {
			node, nodeErr := s.store.Node(ctx, attempt.VisitID)
			if nodeErr != nil {
				return statestore.RunProjection{}, nodeErr
			}
			if !node.Status.Terminal() {
				events = append(events, pendingEvent("visit.cancelled", statestore.AggregateVisit, node.VisitID, node.ResourceVersion,
					run.RunID, request.IdempotencyKey, request.Actor.Type, request.Actor.ID, now, map[string]any{"reason": "user"}))
				cancelledVisits[attempt.VisitID] = true
			}
		}
	}
	events = append(events, controlEvent(eventKind, run, request, map[string]any{"reason": "user"}, now))
	committed, err := s.store.Append(ctx, events...)
	if err != nil {
		return statestore.RunProjection{}, err
	}
	value, err := s.store.Run(ctx, request.RunID)
	if err != nil {
		return statestore.RunProjection{}, err
	}
	return value, s.finishControl(ctx, action, request.IdempotencyKey, value, committed)
}

func (s *Service) beginControl(ctx context.Context, action, eventKind string, request ControlRequest, extra map[string]any) (statestore.RunProjection, bool, error) {
	request.RunID = strings.TrimSpace(request.RunID)
	if request.RunID == "" || request.ExpectedResourceVersion == 0 || strings.TrimSpace(request.IdempotencyKey) != request.IdempotencyKey || len(request.IdempotencyKey) < 8 || len(request.IdempotencyKey) > 128 {
		return statestore.RunProjection{}, false, fmt.Errorf("%w: run ID, positive resource version, and an 8-128 byte idempotency key are required", ErrInvalidControl)
	}
	payload := map[string]any{"runId": request.RunID, "expectedResourceVersion": request.ExpectedResourceVersion}
	for key, value := range extra {
		payload[key] = value
	}
	encoded, _ := json.Marshal(payload)
	digest := fmt.Sprintf("%x", sha256.Sum256(encoded))
	command, reused, err := s.store.BeginCommand(ctx, statestore.BeginCommandRequest{
		Scope: "runs." + action, IdempotencyKey: request.IdempotencyKey, RequestDigest: digest, CreatedAt: s.now().UTC().Round(0),
	})
	if err != nil {
		return statestore.RunProjection{}, false, err
	}
	if reused && command.Status == "completed" {
		value, replayErr := decodeControlResponse(command.Response, action, request.RunID)
		return value, true, replayErr
	}
	if reused {
		evidence, evidenceErr := s.store.RunEvidence(ctx, request.RunID)
		if evidenceErr == nil {
			for _, event := range evidence.Events {
				if event.Kind == eventKind && event.CommandID == request.IdempotencyKey {
					value, getErr := s.store.Run(ctx, request.RunID)
					if getErr != nil {
						return statestore.RunProjection{}, false, getErr
					}
					return value, true, s.finishControl(ctx, action, request.IdempotencyKey, value, nil)
				}
			}
		}
	}
	return statestore.RunProjection{}, false, nil
}

func (s *Service) controlRun(ctx context.Context, request ControlRequest, action string, allowed ...statestore.RunStatus) (statestore.RunProjection, error) {
	run, err := s.store.Run(ctx, request.RunID)
	if err != nil {
		return statestore.RunProjection{}, err
	}
	if run.ResourceVersion != request.ExpectedResourceVersion {
		return statestore.RunProjection{}, &ControlConflictError{RunID: run.RunID, Expected: request.ExpectedResourceVersion, Current: run.ResourceVersion}
	}
	for _, state := range allowed {
		if run.Status == state {
			return run, nil
		}
	}
	return statestore.RunProjection{}, &InvalidTransitionError{Action: action, RunID: run.RunID, Status: run.Status}
}

func (s *Service) finishControl(ctx context.Context, action, key string, value statestore.RunProjection, events []statestore.Event) error {
	response := controlCommandResponse{Result: &value}
	encoded, err := json.Marshal(response)
	if err != nil {
		return err
	}
	request := statestore.CompleteCommandRequest{Scope: "runs." + action, IdempotencyKey: key, ResponseStatus: 200, Response: encoded, CompletedAt: s.now().UTC().Round(0)}
	if len(events) != 0 {
		first, last := events[0].GlobalPosition, events[len(events)-1].GlobalPosition
		request.FirstEventPosition, request.LastEventPosition = &first, &last
	}
	_, err = s.store.CompleteCommand(ctx, request)
	return err
}

func (s *Service) finishControlFailure(ctx context.Context, action, key string, cause error) error {
	failure := controlFailure{Kind: "invalid", Message: cause.Error()}
	status := 409
	var transition *InvalidTransitionError
	var conflict *ControlConflictError
	switch {
	case errors.Is(cause, statestore.ErrNotFound):
		failure.Kind, status = "not_found", 404
	case errors.As(cause, &transition):
		failure.Kind, failure.Status = "transition", transition.Status
	case errors.As(cause, &conflict):
		failure.Kind, failure.Expected, failure.Current, status = "conflict", conflict.Expected, conflict.Current, 412
	case errors.Is(cause, ErrInvalidControl):
		status = 400
	default:
		return cause
	}
	encoded, _ := json.Marshal(controlCommandResponse{Failure: &failure})
	_, completeErr := s.store.CompleteCommand(ctx, statestore.CompleteCommandRequest{
		Scope: "runs." + action, IdempotencyKey: key, ResponseStatus: status, Response: encoded, CompletedAt: s.now().UTC().Round(0),
	})
	if completeErr != nil {
		return completeErr
	}
	return cause
}

func decodeControlResponse(encoded json.RawMessage, action, runID string) (statestore.RunProjection, error) {
	var response controlCommandResponse
	if err := json.Unmarshal(encoded, &response); err != nil {
		return statestore.RunProjection{}, fmt.Errorf("decode replayed %s response: %w", action, err)
	}
	if response.Result != nil {
		return *response.Result, nil
	}
	if response.Failure == nil {
		return statestore.RunProjection{}, fmt.Errorf("decode replayed %s response: empty response", action)
	}
	switch response.Failure.Kind {
	case "not_found":
		return statestore.RunProjection{}, statestore.ErrNotFound
	case "transition":
		return statestore.RunProjection{}, &InvalidTransitionError{Action: action, RunID: runID, Status: response.Failure.Status}
	case "conflict":
		return statestore.RunProjection{}, &ControlConflictError{RunID: runID, Expected: response.Failure.Expected, Current: response.Failure.Current}
	default:
		return statestore.RunProjection{}, fmt.Errorf("%w: %s", ErrInvalidControl, response.Failure.Message)
	}
}

func (s *Service) quiesceRun(ctx context.Context, runID string, terminate bool, key string) map[string]any {
	s.mu.Lock()
	stopped := make(map[string]stoppedWorker)
	for attemptID, active := range s.workers {
		if active.attempt.RunID != runID {
			continue
		}
		stopped[attemptID] = stoppedWorker{adapter: active.adapter, handle: active.handle, done: active.done}
		active.cancel()
	}
	s.mu.Unlock()
	evidence := make(map[string]any, len(stopped))
	if terminate {
		attempts, _ := s.store.AttemptsForRun(ctx, runID)
		for _, attempt := range attempts {
			if attempt.Status != statestore.AttemptRunning {
				continue
			}
			if _, live := stopped[attempt.AttemptID]; live {
				continue
			}
			adapter, err := s.factory.Provider(attempt.Scenario, attempt.AttemptID, true)
			if err != nil {
				evidence[attempt.AttemptID] = map[string]any{"error": err.Error()}
				continue
			}
			stopped[attempt.AttemptID] = stoppedWorker{adapter: adapter, handle: provider.AttemptHandle{
				AttemptID: attempt.AttemptID, Provider: attempt.Provider, ProviderThreadID: attempt.ProviderThreadID,
				ProviderTurnID: attempt.ProviderTurnID, ProcessOwnerID: attempt.ProcessOwnerID,
			}}
		}
		for attemptID, active := range stopped {
			if active.adapter == nil || active.handle.AttemptID == "" {
				continue
			}
			result, err := active.adapter.CancelAttempt(ctx, provider.CancelRequest{Handle: active.handle, IdempotencyKey: "cancel:" + key + ":" + attemptID})
			if err != nil {
				evidence[attemptID] = map[string]any{"error": err.Error()}
			} else {
				evidence[attemptID] = result
			}
		}
	}
	for _, active := range stopped {
		if active.done == nil {
			continue
		}
		select {
		case <-active.done:
		case <-ctx.Done():
			return evidence
		}
	}
	return evidence
}

func controlEvent(kind string, run statestore.RunProjection, request ControlRequest, data any, occurredAt time.Time) statestore.PendingEvent {
	return pendingEvent(kind, statestore.AggregateRun, run.RunID, run.ResourceVersion, run.RunID, request.IdempotencyKey,
		request.Actor.Type, request.Actor.ID, occurredAt, data)
}

func normalizeControlRequest(request ControlRequest) ControlRequest {
	request.RunID = strings.TrimSpace(request.RunID)
	if request.Actor.Type == "" || request.Actor.ID == "" {
		request.Actor = statestore.Actor{Type: statestore.ActorUser, ID: "local-user"}
	}
	return request
}

func strictRouteExtension(current, candidate workflow.Route, terminal workflow.Identifier) bool {
	currentNodes := make(map[workflow.Identifier]bool, len(current.Nodes))
	candidateNodes := make(map[workflow.Identifier]bool, len(candidate.Nodes))
	for _, node := range current.Nodes {
		currentNodes[node.ID] = true
	}
	for _, node := range candidate.Nodes {
		candidateNodes[node.ID] = true
	}
	if !candidateNodes[terminal] || len(candidateNodes) <= len(currentNodes) {
		return false
	}
	for node := range currentNodes {
		if !candidateNodes[node] {
			return false
		}
	}
	for _, oldTerminal := range current.Terminals {
		if oldTerminal == terminal {
			return false
		}
	}
	return true
}
