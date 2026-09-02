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

	"darkstar/src/core/workflow"
	"darkstar/src/ports"
	"darkstar/src/ports/provider"
	"darkstar/src/ports/statestore"
)

const (
	ScenarioSuccess        = "fake-success"
	ScenarioRestart        = "fake-restart"
	commandScope           = "runs.start"
	createScope            = "runs.create"
	DefaultWorkflowID      = "darkstar/mvp-walking-skeleton"
	DefaultWorkflowVersion = "1.0.0"
	nodeID                 = "technical_design"
)

var (
	ErrInvalidScenario     = errors.New("unsupported fake-provider scenario")
	ErrCommandInProgress   = errors.New("run start command is still being recovered")
	ErrInvalidRequest      = errors.New("invalid run request")
	ErrWorkflowUnavailable = errors.New("workflow planning is not configured")
	ErrPageCursor          = errors.New("run page cursor was not found")
)

// StartRequest is the closed public input for the M1 scenario-backed run.
type StartRequest struct {
	Scenario string `json:"scenario"`
}

// CreateRequest starts one work-backed run from an exact installed workflow.
type CreateRequest struct {
	WorkItemID      string `json:"workItemId"`
	WorkflowID      string `json:"workflowId"`
	WorkflowVersion string `json:"workflowVersion"`
}

// PageInfo describes the next stable run-list cursor.
type PageInfo struct {
	NextCursor *string `json:"nextCursor"`
}

// Page is one bounded run projection page.
type Page struct {
	Items    []statestore.RunProjection `json:"items"`
	PageInfo PageInfo                   `json:"pageInfo"`
}

// WorkflowPlanner resolves and derives the immutable route for a new run.
type WorkflowPlanner interface {
	Preview(context.Context, string, string, workflow.RouteRequest, workflow.RouteContext) (workflow.RoutePreview, workflow.ValidationErrors, error)
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
	planner WorkflowPlanner
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

// SetWorkflowPlanner installs work-backed route planning before requests are served.
func (s *Service) SetWorkflowPlanner(planner WorkflowPlanner) error {
	if planner == nil {
		return ErrWorkflowUnavailable
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.workers) != 0 {
		return errors.New("workflow planner cannot change while runs are active")
	}
	s.planner = planner
	return nil
}

// Create durably validates a work item, freezes an installed workflow route,
// and queues the resulting run. Provider execution is owned by the scheduler.
func (s *Service) Create(ctx context.Context, request CreateRequest, idempotencyKey string) (statestore.RunProjection, error) {
	request.WorkItemID = strings.TrimSpace(request.WorkItemID)
	request.WorkflowID = strings.TrimSpace(request.WorkflowID)
	request.WorkflowVersion = strings.TrimSpace(request.WorkflowVersion)
	if request.WorkItemID == "" || request.WorkflowID == "" || request.WorkflowVersion == "" {
		return statestore.RunProjection{}, fmt.Errorf("%w: workItemId, workflowId, and workflowVersion are required", ErrInvalidRequest)
	}
	if strings.TrimSpace(idempotencyKey) != idempotencyKey || len(idempotencyKey) < 8 || len(idempotencyKey) > 128 {
		return statestore.RunProjection{}, fmt.Errorf("%w: idempotency key must be between 8 and 128 bytes without surrounding whitespace", ErrInvalidRequest)
	}
	work, err := s.store.WorkItem(ctx, request.WorkItemID)
	if err != nil {
		return statestore.RunProjection{}, err
	}
	if work.Status.Terminal() {
		return statestore.RunProjection{}, fmt.Errorf("%w: work item %s is %s", ErrInvalidRequest, work.WorkItemID, work.Status)
	}
	project, err := s.store.Project(ctx, work.ProjectID)
	if err != nil {
		return statestore.RunProjection{}, err
	}
	if project.Status != statestore.ProjectActive {
		return statestore.RunProjection{}, fmt.Errorf("%w: project %s is archived", ErrInvalidRequest, project.ProjectID)
	}
	s.mu.Lock()
	planner := s.planner
	s.mu.Unlock()
	if planner == nil {
		return statestore.RunProjection{}, ErrWorkflowUnavailable
	}
	preview, issues, err := planner.Preview(ctx, request.WorkflowID, request.WorkflowVersion, workflow.RouteRequest{}, workflow.RouteContext{})
	if err != nil {
		return statestore.RunProjection{}, err
	}
	if len(issues) != 0 {
		return statestore.RunProjection{}, issues
	}
	request.WorkflowID, request.WorkflowVersion = preview.Workflow.Name, preview.Workflow.Version
	requestJSON, _ := json.Marshal(request)
	requestDigest := fmt.Sprintf("%x", sha256.Sum256(requestJSON))
	now := s.now().UTC().Round(0)
	command, reused, err := s.store.BeginCommand(ctx, statestore.BeginCommandRequest{
		Scope: createScope, IdempotencyKey: idempotencyKey, RequestDigest: requestDigest, CreatedAt: now,
	})
	if err != nil {
		return statestore.RunProjection{}, err
	}
	if reused && command.Status == "completed" {
		var value statestore.RunProjection
		if err := json.Unmarshal(command.Response, &value); err != nil {
			return statestore.RunProjection{}, fmt.Errorf("decode replayed run creation: %w", err)
		}
		return s.store.Run(ctx, value.RunID)
	}
	runID := stableID("run_", createScope+"\x00"+idempotencyKey)
	if reused {
		value, getErr := s.store.Run(ctx, runID)
		if getErr != nil {
			return statestore.RunProjection{}, ErrCommandInProgress
		}
		if err := s.completeCreateCommand(ctx, idempotencyKey, value, nil); err != nil {
			return statestore.RunProjection{}, err
		}
		return value, nil
	}

	routeJSON, err := json.Marshal(preview.Route)
	if err != nil {
		return statestore.RunProjection{}, fmt.Errorf("encode frozen route: %w", err)
	}
	routeDigest := fmt.Sprintf("%x", sha256.Sum256(routeJSON))
	events := make([]statestore.PendingEvent, 0, 4)
	if work.Status == statestore.WorkItemOpen {
		events = append(events, pendingEvent("work.started", statestore.AggregateWork, work.WorkItemID, work.ResourceVersion, runID, "work-start:"+runID, statestore.ActorUser, "cli", now, map[string]any{}))
	}
	events = append(events,
		pendingEvent("run.created", statestore.AggregateRun, runID, 0, runID, idempotencyKey, statestore.ActorUser, "cli", now, map[string]any{
			"workItemId": work.WorkItemID, "workflowId": preview.Workflow.Name, "workflowVersion": preview.Workflow.Version, "priority": work.Priority,
		}),
		pendingEvent("run.route_frozen", statestore.AggregateRun, runID, 1, runID, "route-frozen:"+runID, statestore.ActorSystem, "daemon", now, map[string]any{
			"workflowDigest": preview.Workflow.Digest, "routeDigest": routeDigest, "routeSnapshot": json.RawMessage(routeJSON),
		}),
		pendingEvent("run.started", statestore.AggregateRun, runID, 2, runID, "run-start:"+runID, statestore.ActorUser, "cli", now, map[string]any{}),
	)
	committed, err := s.store.Append(ctx, events...)
	if err != nil {
		return statestore.RunProjection{}, err
	}
	value, err := s.store.Run(ctx, runID)
	if err != nil {
		return statestore.RunProjection{}, err
	}
	if err := s.completeCreateCommand(ctx, idempotencyKey, value, committed); err != nil {
		return statestore.RunProjection{}, err
	}
	return value, nil
}

// List returns a deterministic bounded page ordered by store priority and creation order.
func (s *Service) List(ctx context.Context, limit int, after string) (Page, error) {
	if limit < 1 || limit > 200 {
		return Page{}, fmt.Errorf("%w: limit must be between 1 and 200", ErrInvalidRequest)
	}
	values, err := s.store.Runs(ctx)
	if err != nil {
		return Page{}, err
	}
	start := 0
	if after != "" {
		start = -1
		for index := range values {
			if values[index].RunID == after {
				start = index + 1
				break
			}
		}
		if start < 0 {
			return Page{}, ErrPageCursor
		}
	}
	end := start + limit
	if end > len(values) {
		end = len(values)
	}
	items := make([]statestore.RunProjection, end-start)
	copy(items, values[start:end])
	page := Page{Items: items, PageInfo: PageInfo{}}
	if end < len(values) && len(items) != 0 {
		cursor := items[len(items)-1].RunID
		page.PageInfo.NextCursor = &cursor
	}
	return page, nil
}

func (s *Service) completeCreateCommand(ctx context.Context, key string, value statestore.RunProjection, events []statestore.Event) error {
	encoded, err := json.Marshal(value)
	if err != nil {
		return err
	}
	request := statestore.CompleteCommandRequest{Scope: createScope, IdempotencyKey: key, ResponseStatus: 201, Response: encoded, CompletedAt: s.now().UTC().Round(0)}
	if len(events) != 0 {
		first, last := events[0].GlobalPosition, events[len(events)-1].GlobalPosition
		request.FirstEventPosition, request.LastEventPosition = &first, &last
	}
	_, err = s.store.CompleteCommand(ctx, request)
	return err
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
	visitID := stableID("visit_", runID+"\x00"+nodeID)
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
			"workItemId": stableID("work_", runID), "workflowId": DefaultWorkflowID, "workflowVersion": DefaultWorkflowVersion,
		}),
		pendingEvent("run.route_frozen", statestore.AggregateRun, runID, 1, runID, "route-frozen:"+runID, statestore.ActorSystem, "daemon", now, map[string]any{}),
		pendingEvent("run.started", statestore.AggregateRun, runID, 2, runID, "run-start:"+runID, statestore.ActorUser, "cli", now, map[string]any{}),
		pendingEvent("visit.created", statestore.AggregateVisit, visitID, 0, runID, "visit-create:"+visitID, statestore.ActorSystem, "daemon", now, map[string]any{
			"runId": runID, "nodeId": nodeID,
		}),
		pendingEvent("visit.ready", statestore.AggregateVisit, visitID, 1, runID, "visit-ready:"+visitID, statestore.ActorSystem, "daemon", now, map[string]any{}),
		pendingEvent("visit.started", statestore.AggregateVisit, visitID, 2, runID, "visit-start:"+visitID, statestore.ActorSystem, "daemon", now, map[string]any{}),
		pendingEvent("attempt.created", statestore.AggregateAttempt, attemptID, 0, runID, idempotencyKey, statestore.ActorSystem, "daemon", now, map[string]any{
			"runId": runID, "visitId": visitID, "nodeId": nodeID, "scenario": request.Scenario, "provider": "fake", "logReference": logReference,
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
	run, err := s.store.Run(s.ctx, attempt.RunID)
	if err != nil {
		return
	}
	if run.Status == statestore.RunQueued {
		if _, err = s.store.Append(s.ctx, pendingEvent("run.visit_ready", statestore.AggregateRun, run.RunID, run.ResourceVersion,
			run.RunID, "visit-ready:"+attempt.VisitID, statestore.ActorSystem, "daemon", s.now(), map[string]any{"visitId": attempt.VisitID})); err != nil {
			return
		}
	}
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
			if resume {
				s.pauseUnsafeResume(attempt, err)
			} else {
				s.failAttempt(attempt.AttemptID, attempt.RunID, err)
			}
		}
		return
	}
	identityChanged := resume && (handle.ProviderThreadID != attempt.ProviderThreadID || handle.ProviderTurnID != attempt.ProviderTurnID)
	if handle.AttemptID != attempt.AttemptID || handle.Provider != attempt.Provider || handle.ProviderThreadID == "" ||
		handle.ProviderTurnID == "" || handle.ProcessOwnerID == "" || identityChanged {
		inconsistent := errors.New("provider returned an inconsistent recovery handle")
		if resume {
			s.pauseUnsafeResume(attempt, inconsistent)
		} else {
			s.failAttempt(attempt.AttemptID, attempt.RunID, inconsistent)
		}
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
	identity := map[string]any{
		"providerThreadId": handle.ProviderThreadID, "providerTurnId": handle.ProviderTurnID, "processOwnerId": handle.ProcessOwnerID,
	}
	startEvents := make([]statestore.PendingEvent, 0, 2)
	if current.Status == statestore.AttemptCreated {
		startEvents = append(startEvents, pendingEvent("attempt.resources_acquired", statestore.AggregateAttempt, current.AttemptID, current.ResourceVersion,
			current.RunID, "resources:"+current.AttemptID, statestore.ActorSystem, "daemon", s.now(), map[string]any{}))
		current.ResourceVersion++
	}
	startEvents = append(startEvents, pendingEvent(kind, statestore.AggregateAttempt, current.AttemptID, current.ResourceVersion,
		current.RunID, kind+":"+current.AttemptID, statestore.ActorProvider, "fake", s.now(), identity))
	started, err := s.store.Append(s.ctx, startEvents...)
	if err != nil || len(started) == 0 {
		return
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
		if err = event.Validate(); err != nil {
			s.failAttempt(current.AttemptID, current.RunID, err)
			return
		}
		if event.AttemptID != current.AttemptID || event.Provider != current.Provider {
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

func (s *Service) pauseUnsafeResume(attempt statestore.AttemptProjection, cause error) {
	failure := ports.Failure{
		Code: ports.FailureUncertain, Message: "provider resume could not be proven safe", Retryable: false,
	}
	var classified *ports.Failure
	if errors.As(cause, &classified) {
		failure = *classified
		failure.Details = cloneStringMap(classified.Details)
	}
	s.completeAttempt(attempt.AttemptID, attempt.RunID, provider.UnknownResult{
		AttemptResultMetadata: provider.AttemptResultMetadata{Recovery: provider.RecoveryMetadata{
			ProviderThreadID: attempt.ProviderThreadID, ProviderTurnID: attempt.ProviderTurnID,
			LastSequence: attempt.LastSequence, ProcessOwnerID: attempt.ProcessOwnerID, Resumable: true,
		}},
		Failure: failure,
	})
}

func cloneStringMap(source map[string]string) map[string]string {
	if source == nil {
		return nil
	}
	cloned := make(map[string]string, len(source))
	for key, value := range source {
		cloned[key] = value
	}
	return cloned
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
	node, err := s.store.Node(s.ctx, attempt.VisitID)
	if err != nil {
		return
	}
	attemptKind, nodeKind, runKind := "attempt.succeeded", "visit.succeeded", "run.completed"
	data := map[string]any{"lastSequence": attempt.LastSequence, "logReference": attempt.LogReference}
	switch value := result.(type) {
	case provider.SucceededResult:
		data["output"] = json.RawMessage(value.StructuredOutput)
	case provider.FailedResult:
		attemptKind, nodeKind, runKind, data["failure"] = "attempt.failed", "visit.failed", "run.failed", value.Failure
	case provider.CancelledResult:
		attemptKind, nodeKind, runKind = "attempt.cancelled", "visit.cancelled", "run.cancelled"
	case provider.InterruptedResult:
		attemptKind, nodeKind, runKind, data["failure"] = "attempt.interrupted", "visit.failed", "run.failed", value.Failure
	case provider.UnknownResult:
		attemptKind, nodeKind, runKind, data["failure"] = "attempt.reconcile_required", "visit.failed", "run.reconcile_required", value.Failure
	default:
		s.failAttempt(attemptID, runID, fmt.Errorf("unsupported provider result %T", result))
		return
	}
	events := make([]statestore.PendingEvent, 0, 5)
	if _, ok := result.(provider.SucceededResult); ok {
		events = append(events,
			pendingEvent("attempt.result_received", statestore.AggregateAttempt, attemptID, attempt.ResourceVersion, runID, "result:"+attemptID, statestore.ActorProvider, "fake", s.now(), data),
			pendingEvent(attemptKind, statestore.AggregateAttempt, attemptID, attempt.ResourceVersion+1, runID, "terminal:"+attemptID, statestore.ActorSystem, "daemon", s.now(), data),
			pendingEvent("visit.result_received", statestore.AggregateVisit, node.VisitID, node.ResourceVersion, runID, "result:"+node.VisitID, statestore.ActorProvider, "fake", s.now(), data),
			pendingEvent(nodeKind, statestore.AggregateVisit, node.VisitID, node.ResourceVersion+1, runID, "terminal:"+node.VisitID, statestore.ActorSystem, "daemon", s.now(), data),
		)
	} else {
		events = append(events,
			pendingEvent(attemptKind, statestore.AggregateAttempt, attemptID, attempt.ResourceVersion, runID, "terminal:"+attemptID, statestore.ActorProvider, "fake", s.now(), data),
			pendingEvent(nodeKind, statestore.AggregateVisit, node.VisitID, node.ResourceVersion, runID, "terminal:"+node.VisitID, statestore.ActorSystem, "daemon", s.now(), data),
		)
	}
	events = append(events, pendingEvent(runKind, statestore.AggregateRun, runID, run.ResourceVersion, runID, "terminal:"+runID, statestore.ActorSystem, "daemon", s.now(), map[string]any{"attemptId": attemptID}))
	_, _ = s.store.Append(s.ctx, events...)
}

func (s *Service) failAttempt(attemptID, runID string, cause error) {
	attempt, attemptErr := s.store.Attempt(context.Background(), attemptID)
	run, runErr := s.store.Run(context.Background(), runID)
	if attemptErr != nil || runErr != nil || attempt.Status.Terminal() {
		return
	}
	node, nodeErr := s.store.Node(context.Background(), attempt.VisitID)
	if nodeErr != nil {
		return
	}
	_, _ = s.store.Append(context.Background(),
		pendingEvent("attempt.failed", statestore.AggregateAttempt, attemptID, attempt.ResourceVersion, runID, "failure:"+attemptID, statestore.ActorSystem, "daemon", s.now(), map[string]any{"code": "PROVIDER_FAILED", "message": cause.Error(), "logReference": attempt.LogReference}),
		pendingEvent("visit.failed", statestore.AggregateVisit, node.VisitID, node.ResourceVersion, runID, "failure:"+node.VisitID, statestore.ActorSystem, "daemon", s.now(), map[string]any{"code": "PROVIDER_FAILED", "message": cause.Error()}),
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
