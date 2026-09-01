// Package runexecution coordinates the minimal persisted provider-run lifecycle.
package runexecution

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/fdsprod/darkstar/runtime/src/ports/provider"
	"github.com/fdsprod/darkstar/runtime/src/ports/statestore"
)

const (
	ScenarioSuccess = "fake-success"
	ScenarioRestart = "fake-restart"
	commandScope    = "runs.start"
	workflowID      = "darkstar/mvp-walking-skeleton"
	workflowVersion = "1.0.0"
	nodeID          = "technical_design"
)

var (
	ErrInvalidScenario   = errors.New("unsupported fake-provider scenario")
	ErrCommandInProgress = errors.New("run start command is still being recovered")
)

// StartRequest is the closed public input for the M1 scenario-backed run.
type StartRequest struct {
	Scenario string `json:"scenario"`
}

// View combines the persisted run projection with its attempt projections.
type View struct {
	SchemaVersion int                            `json:"schemaVersion"`
	Run           statestore.RunProjection       `json:"run"`
	Attempts      []statestore.AttemptProjection `json:"attempts"`
}

// ProviderFactory constructs a deterministic provider for a new or resumed attempt.
type ProviderFactory interface {
	Provider(string, string, bool) (provider.Provider, error)
}

// ProviderFactoryFunc adapts a function into a factory.
type ProviderFactoryFunc func(string, string, bool) (provider.Provider, error)

func (f ProviderFactoryFunc) Provider(scenario, attemptID string, resume bool) (provider.Provider, error) {
	return f(scenario, attemptID, resume)
}

// LogSink records human-readable evidence under an opaque reference.
type LogSink interface {
	AppendLog(context.Context, string, []byte) error
}

// Service owns provider workers for one daemon lifetime.
type Service struct {
	store   statestore.Store
	factory ProviderFactory
	logs    LogSink
	now     func() time.Time
	ctx     context.Context
	cancel  context.CancelFunc
	mu      sync.Mutex
	workers map[string]struct{}
	wait    sync.WaitGroup
}

// New creates a run service whose workers are cancelled together on Close.
func New(parent context.Context, store statestore.Store, factory ProviderFactory, logs LogSink) (*Service, error) {
	if store == nil || factory == nil || logs == nil {
		return nil, errors.New("run execution requires state, provider factory, and log sink")
	}
	ctx, cancel := context.WithCancel(parent)
	return &Service{store: store, factory: factory, logs: logs, now: time.Now, ctx: ctx, cancel: cancel, workers: map[string]struct{}{}}, nil
}

// Start durably creates one run and attempt, closes command evidence, then
// schedules provider work. Repeating the idempotency key returns the same run.
func (s *Service) Start(ctx context.Context, request StartRequest, idempotencyKey string) (View, error) {
	if request.Scenario != ScenarioSuccess && request.Scenario != ScenarioRestart {
		return View{}, ErrInvalidScenario
	}
	if strings.TrimSpace(idempotencyKey) != idempotencyKey || len(idempotencyKey) < 8 || len(idempotencyKey) > 128 {
		return View{}, errors.New("idempotency key must be between 8 and 128 bytes without surrounding whitespace")
	}
	requestJSON, _ := json.Marshal(request)
	digest := fmt.Sprintf("%x", sha256.Sum256(requestJSON))
	now := s.now().UTC().Round(0)
	command, reused, err := s.store.BeginCommand(ctx, statestore.BeginCommandRequest{
		Scope: commandScope, IdempotencyKey: idempotencyKey, RequestDigest: digest, CreatedAt: now,
	})
	if err != nil {
		return View{}, err
	}
	if reused && command.Status == "completed" {
		var view View
		if err := json.Unmarshal(command.Response, &view); err != nil {
			return View{}, fmt.Errorf("decode replayed run response: %w", err)
		}
		return s.Get(ctx, view.Run.RunID)
	}

	runID := stableID("run_", commandScope+"\x00"+idempotencyKey)
	attemptID := stableID("attempt_", runID+"\x00"+nodeID)
	if reused {
		view, getErr := s.Get(ctx, runID)
		if getErr != nil {
			return View{}, ErrCommandInProgress
		}
		encoded, _ := json.Marshal(view)
		if _, err := s.store.CompleteCommand(ctx, statestore.CompleteCommandRequest{
			Scope: commandScope, IdempotencyKey: idempotencyKey, ResponseStatus: 202, Response: encoded, CompletedAt: now,
		}); err != nil {
			return View{}, err
		}
		s.launch(view.Attempts[0])
		return view, nil
	}

	logReference := strings.TrimPrefix(attemptID, "attempt_") + ".log"
	events, err := s.store.Append(ctx,
		pendingEvent("run.created", statestore.AggregateRun, runID, 0, runID, idempotencyKey, statestore.ActorUser, "cli", now, map[string]any{
			"workItemId": stableID("work_", runID), "workflowId": workflowID, "workflowVersion": workflowVersion,
		}),
		pendingEvent("attempt.created", statestore.AggregateAttempt, attemptID, 0, runID, idempotencyKey, statestore.ActorSystem, "daemon", now, map[string]any{
			"runId": runID, "nodeId": nodeID, "scenario": request.Scenario, "provider": "fake", "logReference": logReference,
		}),
	)
	if err != nil {
		return View{}, err
	}
	view, err := s.Get(ctx, runID)
	if err != nil {
		return View{}, err
	}
	encoded, _ := json.Marshal(view)
	first, last := events[0].GlobalPosition, events[len(events)-1].GlobalPosition
	if _, err := s.store.CompleteCommand(ctx, statestore.CompleteCommandRequest{
		Scope: commandScope, IdempotencyKey: idempotencyKey, ResponseStatus: 202, Response: encoded,
		FirstEventPosition: &first, LastEventPosition: &last, CompletedAt: s.now().UTC().Round(0),
	}); err != nil {
		return View{}, err
	}
	s.launch(view.Attempts[0])
	return view, nil
}

// Get reads only persisted projections.
func (s *Service) Get(ctx context.Context, runID string) (View, error) {
	run, err := s.store.Run(ctx, runID)
	if err != nil {
		return View{}, err
	}
	attempts, err := s.store.AttemptsForRun(ctx, runID)
	if err != nil {
		return View{}, err
	}
	return View{SchemaVersion: 1, Run: run, Attempts: attempts}, nil
}

// ResumeActive schedules every non-terminal attempt after startup projection rebuild.
func (s *Service) ResumeActive(ctx context.Context) error {
	attempts, err := s.store.ActiveAttempts(ctx)
	if err != nil {
		return err
	}
	for _, attempt := range attempts {
		s.launch(attempt)
	}
	return nil
}

// Close cancels and joins provider workers.
func (s *Service) Close() error {
	s.cancel()
	s.wait.Wait()
	return nil
}

func (s *Service) launch(attempt statestore.AttemptProjection) {
	s.mu.Lock()
	if _, exists := s.workers[attempt.AttemptID]; exists {
		s.mu.Unlock()
		return
	}
	s.workers[attempt.AttemptID] = struct{}{}
	s.wait.Add(1)
	s.mu.Unlock()
	go func() {
		defer s.wait.Done()
		defer func() { s.mu.Lock(); delete(s.workers, attempt.AttemptID); s.mu.Unlock() }()
		s.execute(attempt)
	}()
}

func (s *Service) execute(attempt statestore.AttemptProjection) {
	resume := attempt.Status == statestore.AttemptRunning
	adapter, err := s.factory.Provider(attempt.Scenario, attempt.AttemptID, resume)
	if err != nil {
		s.failAttempt(attempt.AttemptID, attempt.RunID, err)
		return
	}
	var handle provider.AttemptHandle
	if resume {
		handle, err = adapter.ResumeAttempt(s.ctx, provider.ResumeRequest{
			AttemptID: attempt.AttemptID, IdempotencyKey: "resume:" + attempt.AttemptID,
			ProviderThreadID: attempt.ProviderThreadID, ProviderTurnID: attempt.ProviderTurnID, LastSequence: attempt.LastSequence,
		})
	} else {
		handle, err = adapter.StartAttempt(s.ctx, provider.AttemptRequest{
			AttemptID: attempt.AttemptID, RunID: attempt.RunID, NodeID: attempt.NodeID,
			IdempotencyKey: "start:" + attempt.AttemptID, Access: provider.AccessReadOnly,
			Network: provider.NetworkDenied, CommandPolicy: provider.InteractionDeny,
			FilePolicy: provider.InteractionDeny, ToolPolicy: provider.InteractionDeny,
			Prompt: "Execute the deterministic M1 fake-provider scenario.",
		})
	}
	if err != nil {
		if s.ctx.Err() == nil {
			s.failAttempt(attempt.AttemptID, attempt.RunID, err)
		}
		return
	}
	if handle.AttemptID != attempt.AttemptID || handle.Provider != attempt.Provider || handle.ProviderThreadID == "" || handle.ProviderTurnID == "" || handle.ProcessOwnerID == "" {
		s.failAttempt(attempt.AttemptID, attempt.RunID, errors.New("provider returned an inconsistent recovery handle"))
		return
	}
	current, err := s.store.Attempt(s.ctx, attempt.AttemptID)
	if err != nil {
		return
	}
	kind := "attempt.started"
	if resume {
		kind = "attempt.resumed"
	}
	started, err := s.store.Append(s.ctx, pendingEvent(kind, statestore.AggregateAttempt, current.AttemptID, current.ResourceVersion,
		current.RunID, kind+":"+current.AttemptID, statestore.ActorProvider, "fake", s.now(), map[string]any{
			"providerThreadId": handle.ProviderThreadID, "providerTurnId": handle.ProviderTurnID, "processOwnerId": handle.ProcessOwnerID,
		}))
	if err != nil || len(started) == 0 {
		return
	}
	run, err := s.store.Run(s.ctx, current.RunID)
	if err != nil {
		return
	}
	if run.Status == statestore.RunPending {
		if _, err = s.store.Append(s.ctx, pendingEvent("run.started", statestore.AggregateRun, run.RunID, run.ResourceVersion,
			run.RunID, "run-start:"+run.RunID, statestore.ActorSystem, "daemon", s.now(), map[string]any{"attemptId": current.AttemptID})); err != nil {
			return
		}
	}

	stream, err := adapter.StreamEvents(s.ctx, provider.EventRequest{Handle: handle, AfterSequence: current.LastSequence})
	if err != nil {
		if s.ctx.Err() == nil {
			s.failAttempt(current.AttemptID, current.RunID, err)
		}
		return
	}
	defer func() {
		_ = stream.Close()
	}()
	for {
		event, receiveErr := stream.Receive()
		if errors.Is(receiveErr, io.EOF) {
			break
		}
		if receiveErr != nil {
			if s.ctx.Err() == nil {
				s.failAttempt(current.AttemptID, current.RunID, receiveErr)
			}
			return
		}
		if event.AttemptID != current.AttemptID || event.Provider != current.Provider || event.Sequence == 0 {
			s.failAttempt(current.AttemptID, current.RunID, errors.New("provider event identity or sequence is invalid"))
			return
		}
		current, err = s.store.Attempt(s.ctx, current.AttemptID)
		if err != nil {
			return
		}
		data := map[string]any{
			"sequence": event.Sequence, "kind": event.Kind, "provider": event.Provider,
			"providerVersion": event.ProviderVersion, "payload": json.RawMessage(event.Payload),
			"logReference": current.LogReference,
		}
		if _, err = s.store.Append(s.ctx, pendingEvent("attempt.provider_event", statestore.AggregateAttempt, current.AttemptID,
			current.ResourceVersion, current.RunID, fmt.Sprintf("provider:%s:%d", current.AttemptID, event.Sequence),
			statestore.ActorProvider, "fake", event.OccurredAt, data)); err != nil {
			return
		}
		line, _ := json.Marshal(map[string]any{"sequence": event.Sequence, "kind": event.Kind, "payload": json.RawMessage(event.Payload)})
		_ = s.logs.AppendLog(s.ctx, current.LogReference, append(line, '\n'))
	}
	result, err := adapter.GetResult(s.ctx, provider.ResultRequest{Handle: handle})
	if err != nil {
		if s.ctx.Err() == nil {
			s.failAttempt(current.AttemptID, current.RunID, err)
		}
		return
	}
	s.completeAttempt(current.AttemptID, current.RunID, result)
}

func (s *Service) completeAttempt(attemptID, runID string, result provider.AttemptResult) {
	attempt, err := s.store.Attempt(s.ctx, attemptID)
	if err != nil || attempt.Status.Terminal() {
		return
	}
	run, err := s.store.Run(s.ctx, runID)
	if err != nil {
		return
	}
	attemptKind, runKind := "attempt.completed", "run.completed"
	data := map[string]any{"lastSequence": attempt.LastSequence, "logReference": attempt.LogReference}
	switch value := result.(type) {
	case provider.SucceededResult:
		data["output"] = json.RawMessage(value.StructuredOutput)
	case provider.FailedResult:
		attemptKind, runKind, data["failure"] = "attempt.failed", "run.failed", value.Failure
	case provider.CancelledResult:
		attemptKind, runKind = "attempt.cancelled", "run.cancelled"
	case provider.InterruptedResult:
		attemptKind, runKind, data["failure"] = "attempt.interrupted", "run.failed", value.Failure
	case provider.UnknownResult:
		attemptKind, runKind, data["failure"] = "attempt.reconcile_required", "run.reconcile_required", value.Failure
	default:
		s.failAttempt(attemptID, runID, fmt.Errorf("unsupported provider result %T", result))
		return
	}
	_, _ = s.store.Append(s.ctx,
		pendingEvent(attemptKind, statestore.AggregateAttempt, attemptID, attempt.ResourceVersion, runID, "terminal:"+attemptID, statestore.ActorProvider, "fake", s.now(), data),
		pendingEvent(runKind, statestore.AggregateRun, runID, run.ResourceVersion, runID, "terminal:"+runID, statestore.ActorSystem, "daemon", s.now(), map[string]any{"attemptId": attemptID}),
	)
}

func (s *Service) failAttempt(attemptID, runID string, cause error) {
	attempt, attemptErr := s.store.Attempt(context.Background(), attemptID)
	run, runErr := s.store.Run(context.Background(), runID)
	if attemptErr != nil || runErr != nil || attempt.Status.Terminal() {
		return
	}
	_, _ = s.store.Append(context.Background(),
		pendingEvent("attempt.failed", statestore.AggregateAttempt, attemptID, attempt.ResourceVersion, runID, "failure:"+attemptID, statestore.ActorSystem, "daemon", s.now(), map[string]any{"code": "PROVIDER_FAILED", "message": cause.Error(), "logReference": attempt.LogReference}),
		pendingEvent("run.failed", statestore.AggregateRun, runID, run.ResourceVersion, runID, "failure:"+runID, statestore.ActorSystem, "daemon", s.now(), map[string]any{"attemptId": attemptID}),
	)
}

func pendingEvent(kind string, aggregateType statestore.AggregateType, aggregateID string, revision uint64, correlationID, commandID string,
	actorType statestore.ActorType, actorID string, occurredAt time.Time, data any) statestore.PendingEvent {
	encoded, _ := json.Marshal(data)
	return statestore.PendingEvent{
		SchemaVersion: 1, ID: randomID("event_"), AggregateType: aggregateType, AggregateID: aggregateID,
		ExpectedRevision: revision, Kind: kind, OccurredAt: occurredAt.UTC().Round(0), CorrelationID: correlationID,
		CommandID: commandID, Actor: statestore.Actor{Type: actorType, ID: actorID}, Data: encoded, Metadata: json.RawMessage(`{}`),
	}
}
