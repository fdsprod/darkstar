// Package runexecution coordinates the minimal persisted provider-run lifecycle.
package runexecution

import (
	"context"
	"crypto/sha256"
	"darkstar/src/ports/valueschema"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"darkstar/src/core/nodes"
	"darkstar/src/core/preparation"
	"darkstar/src/core/workflow"
	"darkstar/src/ports"
	"darkstar/src/ports/extension"
	"darkstar/src/ports/provider"
	"darkstar/src/ports/routeadvisor"
	"darkstar/src/ports/statestore"
)

const (
	ScenarioSuccess              = "fake-success"
	ScenarioRestart              = "fake-restart"
	ScenarioWorkflow             = "workflow"
	ProviderFake                 = "fake"
	ProviderCodex                = "codex"
	commandScope                 = "runs.start"
	createScope                  = "runs.create"
	prepareScope                 = "runs.prepare"
	DefaultWorkflowID            = "darkstar/story-execution"
	DefaultWorkflowVersion       = "2.0.3"
	compatibilityWorkflowID      = "darkstar/mvp-walking-skeleton"
	compatibilityWorkflowVersion = "1.0.0"
	nodeID                       = "technical_design"
)

var (
	ErrInvalidScenario     = errors.New("unsupported fake-provider scenario")
	ErrCommandInProgress   = errors.New("run start command is still being recovered")
	ErrInvalidRequest      = errors.New("invalid run request")
	ErrWorkflowUnavailable = errors.New("workflow planning is not configured")
	ErrSchedulingBlocked   = errors.New("run scheduling is blocked until startup reconciliation is resolved")
	ErrPageCursor          = errors.New("run page cursor was not found")
)

// StartRequest is the closed public input for the M1 scenario-backed run.
type StartRequest struct {
	Scenario string `json:"scenario"`
}

// CreateRequest starts one work-backed run from an exact installed workflow.
type CreateRequest struct {
	WorkItemID      string            `json:"workItemId"`
	WorkflowID      string            `json:"workflowId"`
	WorkflowVersion string            `json:"workflowVersion"`
	Profile         string            `json:"profile,omitempty"`
	Preparation     *PreparationInput `json:"preparation,omitempty"`
}

type PreparationInput struct {
	Answers       map[string]string                       `json:"answers,omitempty"`
	RunInputs     map[workflow.Identifier]json.RawMessage `json:"runInputs,omitempty"`
	Evidence      []string                                `json:"evidence,omitempty"`
	RouteOverride *workflow.RouteRequest                  `json:"routeOverride,omitempty"`
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

// WorkflowProfilePlanner is an optional capability for planners that can
// resolve an authored route profile. Keeping it separate preserves the base
// planning contract for integrations that only support explicit routes.
type WorkflowProfilePlanner interface {
	PreviewProfile(context.Context, string, string, workflow.Identifier, workflow.RouteContext) (workflow.RoutePreview, workflow.ValidationErrors, error)
}

// WorkflowDefinitionReader rehydrates the exact typed workflow used to build
// an attempt request. Installed definitions are immutable, and callers reject
// any digest mismatch with the frozen run before provider dispatch.
type WorkflowDefinitionReader interface {
	Definition(context.Context, string, string) (workflow.Definition, error)
}

// View combines the persisted run projection with its node-visit and attempt
// projections. Keeping visits in the query response lets every client render
// the durable execution timeline without reconstructing state from events.
type View struct {
	Assessment       *preparation.Assessment        `json:"assessment,omitempty"`
	SchemaVersion    int                            `json:"schemaVersion"`
	Run              statestore.RunProjection       `json:"run"`
	Nodes            []statestore.NodeProjection    `json:"nodes"`
	Attempts         []statestore.AttemptProjection `json:"attempts"`
	Timeline         []TimelineEntry                `json:"timeline"`
	TimelinePageInfo TimelinePageInfo               `json:"timelinePageInfo"`
	Commands         []CommandSummary               `json:"commands"`
	CommandsPageInfo CommandPageInfo                `json:"commandsPageInfo"`
	Issue            *RunIssueSummary               `json:"issue,omitempty"`
}

const (
	viewTimelineLimit = 200
	viewCommandLimit  = 100
)

// TimelinePageInfo makes the bounded nature of the embedded audit window
// explicit. Full evidence remains available through the run export boundary.
type TimelinePageInfo struct {
	HasEarlier    bool    `json:"hasEarlier"`
	FirstPosition *uint64 `json:"firstPosition,omitempty"`
	LastPosition  *uint64 `json:"lastPosition,omitempty"`
}

// CommandPageInfo identifies whether older command summaries were omitted.
type CommandPageInfo struct {
	HasEarlier bool `json:"hasEarlier"`
}

type RunIssueKind string

const (
	RunIssueInputRequired     RunIssueKind = "input_required"
	RunIssueFailure           RunIssueKind = "failure"
	RunIssueReconcileRequired RunIssueKind = "reconcile_required"
)

// RunIssueSummary exposes only the daemon-authored actionable classification
// from the latest input wait, failure, or reconciliation stop. Provider
// payloads remain behind the evidence export boundary.
type RunIssueSummary struct {
	Kind    RunIssueKind `json:"kind"`
	Code    string       `json:"code"`
	Message string       `json:"message"`
}

// TimelineEntry is the deliberately metadata-only event shape exposed to
// dashboard clients. Event data and metadata stay behind the runtime boundary.
type TimelineEntry struct {
	ID                string                   `json:"id"`
	GlobalPosition    uint64                   `json:"globalPosition"`
	AggregateType     statestore.AggregateType `json:"aggregateType"`
	AggregateID       string                   `json:"aggregateId"`
	AggregateRevision uint64                   `json:"aggregateRevision"`
	Kind              string                   `json:"kind"`
	OccurredAt        time.Time                `json:"occurredAt"`
	RecordedAt        time.Time                `json:"recordedAt"`
	ActorType         statestore.ActorType     `json:"actorType"`
}

// CommandSummary omits replay credentials, request digests, and response
// bodies while retaining enough evidence to audit command completion.
type CommandSummary struct {
	Scope              string     `json:"scope"`
	Status             string     `json:"status"`
	ResponseStatus     *int       `json:"responseStatus,omitempty"`
	FirstEventPosition *uint64    `json:"firstEventPosition,omitempty"`
	LastEventPosition  *uint64    `json:"lastEventPosition,omitempty"`
	CreatedAt          time.Time  `json:"createdAt"`
	CompletedAt        *time.Time `json:"completedAt,omitempty"`
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

// ProviderRequest is the closed provider-selection input for a workflow-backed
// attempt. Provider and Scenario are durable projection values rather than
// configuration inferred again after a daemon restart.
type ProviderRequest struct {
	Ref       *extension.Ref
	Provider  string
	Scenario  string
	AttemptID string
	Resume    bool
}

// WorkflowProviderFactory resolves the configured adapter for a durable
// workflow-backed attempt.
type WorkflowProviderFactory interface {
	Provider(context.Context, ProviderRequest) (provider.Provider, error)
}

type WorkflowProviderFactoryFunc func(context.Context, ProviderRequest) (provider.Provider, error)

func (f WorkflowProviderFactoryFunc) Provider(ctx context.Context, request ProviderRequest) (provider.Provider, error) {
	return f(ctx, request)
}

// AttemptRequestContext is the immutable, fully rehydrated input available to
// application composition when it constructs a provider request.
type AttemptRequestContext struct {
	Attempt          statestore.AttemptProjection
	Run              statestore.RunProjection
	WorkItem         statestore.WorkItemProjection
	Project          statestore.ProjectProjection
	Workflow         workflow.Definition
	Node             workflow.Node
	FrozenRoute      workflow.Route
	RunInputs        map[workflow.Identifier]json.RawMessage
	AcceptedOutputs  map[workflow.Identifier]map[workflow.Identifier]json.RawMessage
	NodeInputs       map[workflow.Identifier]json.RawMessage
	ExecutionContext statestore.RunExecutionContext
	FrameSnapshot    workflow.FrameSnapshot
}

// AttemptRequestBuilder owns provider-specific prompt, workspace, permission,
// and context construction. Core verifies the returned execution identities.
type AttemptRequestBuilder interface {
	BuildAttemptRequest(context.Context, AttemptRequestContext) (provider.AttemptRequest, error)
}

type workflowAdmissionError struct {
	code    string
	message string
}

func (e *workflowAdmissionError) Error() string {
	return e.message
}

func workflowFailureCode(err error, fallback string) string {
	var admission *workflowAdmissionError
	if errors.As(err, &admission) && admission.code != "" {
		return admission.code
	}
	return fallback
}

type AttemptRequestBuilderFunc func(context.Context, AttemptRequestContext) (provider.AttemptRequest, error)

func (f AttemptRequestBuilderFunc) BuildAttemptRequest(ctx context.Context, request AttemptRequestContext) (provider.AttemptRequest, error) {
	return f(ctx, request)
}

// LogSink records human-readable evidence under an opaque reference.
type LogSink interface {
	AppendLog(context.Context, string, []byte) error
}

// Service owns provider workers for one daemon lifetime.
type Service struct {
	artifactReviews *artifactReviewBridge

	valueSchemas              valueschema.Validator
	advisor                   routeadvisor.Advisor
	evidenceResolver          routeadvisor.EvidenceResolver
	preparationPolicyResolver func(context.Context, statestore.ProjectProjection) (preparation.Policy, error)
	store                     statestore.Store
	factory                   ProviderFactory
	logs                      LogSink
	planner                   WorkflowPlanner
	workflowFactory           WorkflowProviderFactory
	requestBuilder            AttemptRequestBuilder
	workspace                 string
	schedulingAllowed         bool
	now                       func() time.Time
	ctx                       context.Context
	cancel                    context.CancelFunc
	mu                        sync.Mutex
	workers                   map[string]*worker
	wait                      sync.WaitGroup
	queueEnabled              bool
	queueLimit                func() (int, error)
	queueMu                   sync.Mutex
	checkpointMu              sync.Mutex
	runEventMu                sync.Mutex
}

// worker is the complete process-local ownership record for one provider
// attempt. Durable attempt/run projections remain authoritative; this record
// exists only so a control command can quiesce or terminate live work before
// committing its state transition.
type worker struct {
	attempt statestore.AttemptProjection
	cancel  context.CancelFunc
	done    chan struct{}
	adapter provider.Provider
	handle  provider.AttemptHandle
}

// New creates a run service whose workers are cancelled together on Close.
func New(parent context.Context, store statestore.Store, factory ProviderFactory, logs LogSink) (*Service, error) {
	if store == nil || factory == nil || logs == nil {
		return nil, errors.New("run execution requires state, provider factory, and log sink")
	}
	ctx, cancel := context.WithCancel(parent)
	return &Service{store: store, factory: factory, logs: logs, now: time.Now, ctx: ctx, cancel: cancel, workers: map[string]*worker{}, schedulingAllowed: true}, nil
}

// SetSchedulingAllowed applies the startup recovery admission decision to
// both resumed and newly submitted work.
func (s *Service) SetSchedulingAllowed(allowed bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.workers) != 0 {
		return errors.New("scheduling admission cannot change while runs are active")
	}
	s.schedulingAllowed = allowed
	return nil
}

func (s *Service) schedulingAdmitted() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.schedulingAllowed
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

// SetWorkflowDispatch installs the provider resolver and immutable request
// builder used only by work-backed workflow attempts.
func (s *Service) SetWorkflowDispatch(factory WorkflowProviderFactory, builder AttemptRequestBuilder) error {
	if factory == nil || builder == nil {
		return errors.New("workflow dispatch requires a provider factory and attempt request builder")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.workers) != 0 {
		return errors.New("workflow dispatch cannot change while runs are active")
	}
	s.workflowFactory, s.requestBuilder = factory, builder
	return nil
}

// SetAgentWorkspace records the compatibility runner's explicit workspace
// before any provider work starts. Workflow-backed attempts use their frozen
// context manifest instead.
func (s *Service) SetAgentWorkspace(workspace string) error {
	workspace = strings.TrimSpace(workspace)
	if workspace == "" {
		return errors.New("agent workspace is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.workers) != 0 {
		return errors.New("agent workspace cannot change while runs are active")
	}
	s.workspace = workspace
	return nil
}

// Create preserves the original create-and-start command for API and CLI
// compatibility. New interactive clients should Prepare and then Launch so
// the Ready state is durable and observable.
func (s *Service) Create(ctx context.Context, request CreateRequest, idempotencyKey string) (statestore.RunProjection, error) {
	if s.queueEnabled {
		run, err := s.Prepare(ctx, request, idempotencyKey)
		if err != nil || run.Status != statestore.RunReady {
			return run, err
		}
		var route workflow.Route
		if err := json.Unmarshal([]byte(run.RouteSnapshot), &route); err != nil {
			return run, err
		}
		assessment, err := readPreparation(route)
		if err != nil || (assessment != nil && assessment.Readiness() != "ready") {
			return run, err
		}
		return s.Launch(ctx, ControlRequest{RunID: run.RunID, ExpectedResourceVersion: run.ResourceVersion, IdempotencyKey: "queue-create:" + run.RunID, Actor: statestore.Actor{Type: statestore.ActorUser, ID: "local-user"}})
	}
	return s.createWorkflowRun(ctx, request, idempotencyKey, true)
}

// Prepare validates the exact workflow selection and freezes a ready run
// without starting work or allocating an attempt.
func (s *Service) Prepare(ctx context.Context, request CreateRequest, idempotencyKey string) (statestore.RunProjection, error) {
	return s.createWorkflowRun(ctx, request, idempotencyKey, false)
}

func (s *Service) createWorkflowRun(ctx context.Context, request CreateRequest, idempotencyKey string, start bool) (result statestore.RunProjection, failure error) {
	if !s.schedulingAdmitted() {
		return statestore.RunProjection{}, ErrSchedulingBlocked
	}
	request.WorkItemID = strings.TrimSpace(request.WorkItemID)
	request.WorkflowID = strings.TrimSpace(request.WorkflowID)
	request.WorkflowVersion = strings.TrimSpace(request.WorkflowVersion)
	request.Profile = strings.TrimSpace(request.Profile)
	if request.WorkItemID == "" {
		return statestore.RunProjection{}, fmt.Errorf("%w: workItemId is required", ErrInvalidRequest)
	}
	if strings.TrimSpace(idempotencyKey) != idempotencyKey || len(idempotencyKey) < 8 || len(idempotencyKey) > 128 {
		return statestore.RunProjection{}, fmt.Errorf("%w: idempotency key must be between 8 and 128 bytes without surrounding whitespace", ErrInvalidRequest)
	}
	// Claim the original request before reading mutable work or invoking advice.
	// Replays return the already frozen decision, even if the environment changed.
	scope := createScope
	if !start {
		scope = prepareScope
	}
	requestDigest := preparation.Digest(request)
	now := s.now().UTC().Round(0)
	command, reused, err := s.store.BeginCommand(ctx, statestore.BeginCommandRequest{Scope: scope, IdempotencyKey: idempotencyKey, RequestDigest: requestDigest, CreatedAt: now})
	if err != nil {
		return statestore.RunProjection{}, err
	}
	if reused {
		if command.Status == "completed" {
			var rejected struct {
				PreparationError string `json:"preparationError"`
			}
			_ = json.Unmarshal(command.Response, &rejected)
			if rejected.PreparationError != "" {
				return statestore.RunProjection{}, fmt.Errorf("%w: %s (retry preparation with a new idempotency key)", ErrInvalidRequest, rejected.PreparationError)
			}
			var value statestore.RunProjection
			if err := json.Unmarshal(command.Response, &value); err != nil {
				return value, err
			}
			return value, nil
		}
		value, readErr := s.store.Run(ctx, stableID("run_", scope+"\x00"+idempotencyKey))
		if readErr != nil {
			return statestore.RunProjection{}, ErrCommandInProgress
		}
		return value, s.completeCreateCommand(ctx, scope, idempotencyKey, value, nil)
	}
	defer func() {
		if failure != nil {
			body, _ := json.Marshal(map[string]string{"preparationError": failure.Error()})
			_, completeErr := s.store.CompleteCommand(context.WithoutCancel(ctx), statestore.CompleteCommandRequest{Scope: scope, IdempotencyKey: idempotencyKey, ResponseStatus: 422, Response: body, CompletedAt: s.now().UTC().Round(0)})
			if completeErr != nil {
				failure = errors.Join(failure, completeErr)
			}
		}
	}()
	work, err := s.store.WorkItem(ctx, request.WorkItemID)
	if err != nil {
		return statestore.RunProjection{}, err
	}
	if work.Status.Terminal() || work.Deletion != statestore.WorkRetained {
		return statestore.RunProjection{}, fmt.Errorf("%w: work item %s is %s", ErrInvalidRequest, work.WorkItemID, work.Status)
	}
	previousRuns, err := s.store.RunsForWorkItem(ctx, work.WorkItemID)
	if err != nil {
		return statestore.RunProjection{}, err
	}
	preparationRuns := []statestore.RunProjection{}
	for _, previous := range previousRuns {
		if previous.Status.Terminal() || previous.Status == statestore.RunFailed {
			continue
		}
		var previousRoute workflow.Route
		if json.Unmarshal([]byte(previous.RouteSnapshot), &previousRoute) != nil {
			return statestore.RunProjection{}, fmt.Errorf("%w: existing run %s must be resolved first", ErrInvalidRequest, previous.RunID)
		}
		prior, priorErr := readPreparation(previousRoute)
		attempts, attemptErr := s.store.AttemptsForRun(ctx, previous.RunID)
		if attemptErr != nil {
			return statestore.RunProjection{}, attemptErr
		}
		if priorErr != nil || prior == nil || len(attempts) > 0 || (previous.Status != statestore.RunReady && (previous.Status != statestore.RunWaiting || prior.Readiness() != "input_required")) {
			return statestore.RunProjection{}, fmt.Errorf("%w: existing run %s must be resolved before preparing another route", ErrInvalidRequest, previous.RunID)
		}
		preparationRuns = append(preparationRuns, previous)
	}
	if work.RoutingIntent.Mode == "" {
		work.RoutingIntent.Mode = statestore.WorkRoutingAutomatic
	}
	if request.WorkflowID == "" {
		if work.RoutingIntent.Mode == statestore.WorkRoutingOverride {
			request.WorkflowID, request.WorkflowVersion = work.RoutingIntent.WorkflowID, work.RoutingIntent.WorkflowVersion
		} else {
			request.WorkflowID, request.WorkflowVersion = DefaultWorkflowID, ""
		}
	}
	if request.WorkflowID == "" {
		return statestore.RunProjection{}, fmt.Errorf("%w: routing did not resolve a workflowId", ErrInvalidRequest)
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
	if request.WorkflowVersion == "" {
		if reader, ok := planner.(WorkflowDefinitionReader); ok {
			definition, readErr := reader.Definition(ctx, request.WorkflowID, "")
			if readErr != nil {
				return statestore.RunProjection{}, readErr
			}
			request.WorkflowVersion = definition.Version.Version
		}
	}
	routeContext, err := derivedRouteContext(ctx, planner, request, work, project)
	if err != nil {
		return statestore.RunProjection{}, err
	}
	if resolver, ok := s.requestBuilder.(interface {
		ResolveWorkflowConfig(context.Context, string, string, string) (map[workflow.Identifier]json.RawMessage, error)
	}); ok {
		values, resolveErr := resolver.ResolveWorkflowConfig(ctx, request.WorkflowID, request.WorkflowVersion, project.ProjectID)
		if resolveErr != nil {
			return statestore.RunProjection{}, resolveErr
		}
		for id, value := range values {
			routeContext.RunInputs[id] = value
		}
	}
	var preview workflow.RoutePreview
	var issues workflow.ValidationErrors
	routeRequest, err := workRouteRequest(work, request)
	if err != nil {
		return statestore.RunProjection{}, err
	}
	if request.Profile == "" {
		preview, issues, err = planner.Preview(ctx, request.WorkflowID, request.WorkflowVersion, routeRequest, routeContext)
	} else {
		if work.RoutingIntent.Mode == statestore.WorkRoutingOverride {
			return statestore.RunProjection{}, fmt.Errorf("%w: a route profile cannot replace the work item's routing override", ErrInvalidRequest)
		}
		profilePlanner, supported := planner.(WorkflowProfilePlanner)
		if !supported {
			return statestore.RunProjection{}, fmt.Errorf("%w: workflow planner does not support route profiles", ErrWorkflowUnavailable)
		}
		preview, issues, err = profilePlanner.PreviewProfile(ctx, request.WorkflowID, request.WorkflowVersion, workflow.Identifier(request.Profile), routeContext)
	}
	if err != nil {
		return statestore.RunProjection{}, err
	}
	if len(issues) != 0 {
		return statestore.RunProjection{}, issues
	}
	preview, err = s.assessPreparation(ctx, request, work, project, routeContext, preview)
	if err != nil {
		return statestore.RunProjection{}, err
	}
	assessment, err := readPreparation(preview.Route)
	if err != nil {
		return statestore.RunProjection{}, err
	}
	requiresInputs := len(preview.Route.InputRequirements) != 0
	if assessment != nil {
		requiresInputs = assessment.Readiness() == "input_required"
		if assessment.Readiness() == "confirmation_required" {
			start = false
		}
	}
	entryID := string(preview.Route.Entry)
	if entryID == "" || !routeContainsNode(preview.Route, preview.Route.Entry) {
		return statestore.RunProjection{}, fmt.Errorf("%w: frozen workflow route has no executable entry", ErrInvalidRequest)
	}
	if _, _, err := exactWorkflowNode(ctx, planner, preview.Workflow.Name, preview.Workflow.Version, preview.Workflow.Digest, preview.Route.Entry); err != nil {
		return statestore.RunProjection{}, err
	}
	request.WorkflowID, request.WorkflowVersion = preview.Workflow.Name, preview.Workflow.Version
	runID := stableID("run_", scope+"\x00"+idempotencyKey)

	routeJSON, err := json.Marshal(preview.Route)
	if err != nil {
		return statestore.RunProjection{}, fmt.Errorf("encode frozen route: %w", err)
	}
	routeDigest := fmt.Sprintf("%x", sha256.Sum256(routeJSON))
	visitID := stableID("visit_", runID+"\x00"+entryID)
	attemptID := stableID("attempt_", runID+"\x00"+entryID)
	logReference := strings.TrimPrefix(attemptID, "attempt_") + ".log"
	events := make([]statestore.PendingEvent, 0, 8)
	for _, previous := range preparationRuns {
		events = append(events, pendingEvent("run.cancelled", statestore.AggregateRun, previous.RunID, previous.ResourceVersion, runID, "preparation-superseded:"+runID+":"+previous.RunID, statestore.ActorUser, "local-user", now, map[string]any{"reason": "superseded_preparation", "replacementRunId": runID}))
	}
	if start && work.Status == statestore.WorkItemOpen {
		events = append(events, pendingEvent("work.started", statestore.AggregateWork, work.WorkItemID, work.ResourceVersion, runID, "work-start:"+runID, statestore.ActorUser, "local-user", now, map[string]any{}))
	}
	events = append(events,
		pendingEvent("run.created", statestore.AggregateRun, runID, 0, runID, idempotencyKey, statestore.ActorUser, "local-user", now, map[string]any{
			"workItemId": work.WorkItemID, "workflowId": preview.Workflow.Name, "workflowVersion": preview.Workflow.Version, "priority": work.Priority,
		}),
		pendingEvent("run.route_frozen", statestore.AggregateRun, runID, 1, runID, "route-frozen:"+runID, statestore.ActorSystem, "daemon", now, map[string]any{
			"workflowDigest": preview.Workflow.Digest, "routeDigest": routeDigest, "routeSnapshot": json.RawMessage(routeJSON),
		}),
	)
	if !start && !requiresInputs {
		committed, appendErr := s.store.Append(ctx, events...)
		if appendErr != nil {
			return statestore.RunProjection{}, appendErr
		}
		value, readErr := s.store.Run(ctx, runID)
		if readErr != nil {
			return statestore.RunProjection{}, readErr
		}
		if _, saveErr := s.saveInitialExecutionContext(ctx, value, preview.Route, routeContext.RunInputs); saveErr != nil {
			return statestore.RunProjection{}, saveErr
		}
		return value, s.completeCreateCommand(ctx, scope, idempotencyKey, value, committed)
	}
	events = append(events, pendingEvent("run.started", statestore.AggregateRun, runID, 2, runID, "run-start:"+runID, statestore.ActorUser, "local-user", now, map[string]any{}))
	if requiresInputs {
		message := inputRequirementValidationErrors(preview.Route.InputRequirements).Error()
		if assessment != nil {
			message = preparationQuestions(assessment)
		}
		events = append(events, pendingEvent("run.input_required", statestore.AggregateRun, runID, 3, runID, "run-input-required:"+runID, statestore.ActorSystem, "daemon", now, map[string]any{
			"code": "RUN_INPUT_REQUIRED", "message": message, "requirements": preview.Route.InputRequirements,
		}))
	} else {
		events = append(events,
			pendingEvent("visit.created", statestore.AggregateVisit, visitID, 0, runID, "visit-create:"+visitID, statestore.ActorSystem, "daemon", now, map[string]any{
				"runId": runID, "nodeId": entryID,
			}),
			pendingEvent("attempt.created", statestore.AggregateAttempt, attemptID, 0, runID, idempotencyKey, statestore.ActorSystem, "daemon", now, map[string]any{
				"runId": runID, "visitId": visitID, "nodeId": entryID, "scenario": ScenarioWorkflow, "provider": s.workflowProviderForRun(ctx, runID),
				"logReference": logReference, "priority": work.Priority,
			}),
		)
	}
	committed, err := s.store.Append(ctx, events...)
	if err != nil {
		return statestore.RunProjection{}, err
	}
	value, err := s.store.Run(ctx, runID)
	if err != nil {
		return statestore.RunProjection{}, err
	}
	if requiresInputs {
		if err := s.completeCreateCommand(ctx, scope, idempotencyKey, value, committed); err != nil {
			return statestore.RunProjection{}, err
		}
		return value, nil
	}
	if _, err := s.saveInitialExecutionContext(ctx, value, preview.Route, routeContext.RunInputs); err != nil {
		if failureErr := s.failQueuedRun(ctx, value, "RUN_CONTEXT_PERSIST_FAILED", err); failureErr != nil {
			return statestore.RunProjection{}, errors.Join(err, failureErr)
		}
		return s.store.Run(ctx, runID)
	}
	if err := s.completeCreateCommand(ctx, scope, idempotencyKey, value, committed); err != nil {
		return statestore.RunProjection{}, err
	}
	attempt, err := s.store.Attempt(ctx, attemptID)
	if err != nil {
		if failureErr := s.failQueuedRun(ctx, value, "WORKFLOW_DISPATCH_FAILED", fmt.Errorf("read created entry attempt: %w", err)); failureErr != nil {
			return statestore.RunProjection{}, errors.Join(err, failureErr)
		}
		return s.store.Run(ctx, runID)
	}
	if err := s.launch(attempt); err != nil {
		s.failAttemptWithCode(attempt.AttemptID, attempt.RunID, "WORKFLOW_DISPATCH_FAILED", err)
	}
	return value, nil
}

func workRouteRequest(work statestore.WorkItemProjection, request CreateRequest) (workflow.RouteRequest, error) {
	intent := work.RoutingIntent
	if intent.Mode == "" {
		intent.Mode = statestore.WorkRoutingAutomatic
	}
	switch intent.Mode {
	case statestore.WorkRoutingAutomatic:
		if request.Preparation != nil && request.Preparation.RouteOverride != nil {
			if request.Profile != "" {
				return workflow.RouteRequest{}, fmt.Errorf("%w: routeOverride cannot be combined with a profile", ErrInvalidRequest)
			}
			return *request.Preparation.RouteOverride, nil
		}
		return workflow.RouteRequest{}, nil
	case statestore.WorkRoutingOverride:
		if request.Preparation != nil && request.Preparation.RouteOverride != nil {
			return workflow.RouteRequest{}, fmt.Errorf("%w: preparation cannot replace the work item's routing override", ErrInvalidRequest)
		}
		if request.WorkflowID != intent.WorkflowID {
			return workflow.RouteRequest{}, fmt.Errorf("%w: workflowId conflicts with the work item's routing override", ErrInvalidRequest)
		}
		if intent.WorkflowVersion != "" && request.WorkflowVersion != intent.WorkflowVersion {
			return workflow.RouteRequest{}, fmt.Errorf("%w: workflowVersion conflicts with the work item's routing override", ErrInvalidRequest)
		}
		terminals := make([]workflow.Identifier, len(intent.TerminalNodeIDs))
		for index, nodeID := range intent.TerminalNodeIDs {
			terminals[index] = workflow.Identifier(nodeID)
		}
		return workflow.RouteRequest{From: workflow.Identifier(intent.EntryNodeID), Until: terminals}, nil
	default:
		return workflow.RouteRequest{}, fmt.Errorf("%w: work item has an unsupported routing intent", ErrInvalidRequest)
	}
}

func (s *Service) saveInitialExecutionContext(ctx context.Context, run statestore.RunProjection, route workflow.Route, inputs map[workflow.Identifier]json.RawMessage) (statestore.RunExecutionContext, error) {
	reader, ok := s.planner.(WorkflowDefinitionReader)
	if !ok {
		return statestore.RunExecutionContext{}, errors.New("workflow planner cannot persist execution context without an installed definition")
	}
	definition, err := reader.Definition(ctx, run.WorkflowID, run.WorkflowVersion)
	if err != nil {
		return statestore.RunExecutionContext{}, fmt.Errorf("read installed workflow for execution context: %w", err)
	}
	loaded := workflow.LoadedDefinition{Document: definition.Document, Digest: definition.Version.Digest}
	frame, err := workflow.NewRootFrame(stableID("frame_", run.RunID), run.RunID, loaded, route, inputs)
	if err != nil {
		return statestore.RunExecutionContext{}, fmt.Errorf("create root workflow frame: %w", err)
	}
	frameJSON, err := json.Marshal(frame.Snapshot())
	if err != nil {
		return statestore.RunExecutionContext{}, fmt.Errorf("encode root workflow frame: %w", err)
	}
	storedInputs := make(map[string]json.RawMessage, len(inputs))
	for id, value := range inputs {
		storedInputs[string(id)] = append(json.RawMessage(nil), value...)
	}
	var extensionPins map[string]extension.Ref
	if source, ok := s.workflowFactory.(ExtensionPinProvider); ok {
		extensionPins = source.ExtensionPins()
	}
	value, err := s.store.SaveRunExecutionContext(ctx, statestore.RunExecutionContext{
		Provider: s.workflowProviderName(), ExtensionPins: extensionPins,
		SchemaVersion: statestore.RunExecutionContextSchemaVersion,
		RunID:         run.RunID, RunInputs: storedInputs,
		AcceptedOutputs: map[string]map[string]json.RawMessage{}, FrameSnapshot: frameJSON,
	}, 0)
	if err != nil {
		return statestore.RunExecutionContext{}, fmt.Errorf("persist initial run execution context: %w", err)
	}
	return value, nil
}

func derivedRouteContext(ctx context.Context, planner WorkflowPlanner, request CreateRequest, work statestore.WorkItemProjection, project statestore.ProjectProjection) (workflow.RouteContext, error) {
	reader, ok := planner.(WorkflowDefinitionReader)
	if !ok {
		return workflow.RouteContext{}, errors.New("workflow planner cannot derive run inputs without an installed definition")
	}
	definition, err := reader.Definition(ctx, request.WorkflowID, request.WorkflowVersion)
	if err != nil {
		return workflow.RouteContext{}, fmt.Errorf("read installed workflow for run inputs: %w", err)
	}
	inputs := make(map[workflow.Identifier]json.RawMessage)
	if declaration, exists := definition.Document.Spec.Inputs["repository"]; exists && declaration.Type == workflow.ValueString {
		encoded, _ := json.Marshal(project.ProjectID)
		inputs["repository"] = encoded
	}
	if declaration, exists := definition.Document.Spec.Inputs["story"]; exists && declaration.Type == workflow.ValueObject {
		encoded, err := json.Marshal(map[string]any{
			"id": work.WorkItemID, "projectId": work.ProjectID, "title": work.Title,
			"priority": work.Priority, "sourceHash": work.SourceHash,
			"details": work.Details, "evidence": work.Evidence,
		})
		if err != nil {
			return workflow.RouteContext{}, fmt.Errorf("encode derived story input: %w", err)
		}
		inputs["story"] = encoded
	}
	for id, declaration := range definition.Document.Spec.Inputs {
		if declaration.Resource == nil {
			continue
		}
		var value any
		switch source := declaration.Resource.Source.(type) {
		case workflow.TaskResource:
			value = map[string]any{"id": work.WorkItemID, "projectId": work.ProjectID, "title": work.Title, "details": work.Details, "evidence": work.Evidence}
		case workflow.RepositoryResource:
			value = map[string]any{"projectId": project.ProjectID, "name": project.Name, "sourceHash": project.SourceHash}
		case workflow.ArtifactResource:
			value = source.Content
		case workflow.TemplateResource:
			value = source
		case workflow.ConstantResource:
			value = source.Value
		case workflow.OpenItemsResource:
			value = map[string]any{"kind": "open_items", "resourceId": id}
		case workflow.DecisionLogResource:
			value = map[string]any{"kind": "decision_log", "resourceId": id}
		case workflow.ConfigResource:
			// Config resources are supplied by the resolved run configuration boundary.
			continue
		default:
			return workflow.RouteContext{}, fmt.Errorf("unsupported resource %s", id)
		}
		encoded, encodeErr := json.Marshal(value)
		if encodeErr != nil {
			return workflow.RouteContext{}, encodeErr
		}
		inputs[id] = encoded
	}
	if request.Preparation != nil {
		for id, value := range request.Preparation.RunInputs {
			if _, derived := inputs[id]; derived {
				return workflow.RouteContext{}, fmt.Errorf("%w: run input %s is derived from work and cannot be replaced", ErrInvalidRequest, id)
			}
			declaration, exists := definition.Document.Spec.Inputs[id]
			if !exists {
				return workflow.RouteContext{}, fmt.Errorf("%w: unknown run input %s", ErrInvalidRequest, id)
			}
			if !preparationInputType(value, declaration.Type) {
				return workflow.RouteContext{}, fmt.Errorf("%w: run input %s must be %s", ErrInvalidRequest, id, declaration.Type)
			}
			inputs[id] = append(json.RawMessage(nil), value...)
		}
	}
	if request.Profile != "" {
		profile, exists := definition.Document.Spec.Profiles[workflow.Identifier(request.Profile)]
		if !exists {
			return workflow.RouteContext{RunInputs: inputs}, nil
		}
		for id, value := range profile.InputDefaults {
			if _, derived := inputs[id]; !derived {
				inputs[id] = append(json.RawMessage(nil), value...)
			}
		}
	}
	return workflow.RouteContext{RunInputs: inputs}, nil
}

func inputRequirementValidationErrors(requirements []workflow.InputRequirement) workflow.ValidationErrors {
	issues := make(workflow.ValidationErrors, 0, len(requirements))
	for _, requirement := range requirements {
		issues = append(issues, workflow.ValidationError{
			Code:     requirement.Code,
			Message:  fmt.Sprintf("run input %q required by node %q is not supplied by the run-start API", requirement.Source, requirement.Node),
			Location: fmt.Sprintf("/nodes/%s/inputs/%s", requirement.Node, requirement.Input),
			Details:  map[string][]string{"sources": {requirement.Source}},
		})
	}
	return issues
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

func (s *Service) completeCreateCommand(ctx context.Context, scope, key string, value statestore.RunProjection, events []statestore.Event) error {
	encoded, err := json.Marshal(value)
	if err != nil {
		return err
	}
	request := statestore.CompleteCommandRequest{Scope: scope, IdempotencyKey: key, ResponseStatus: 201, Response: encoded, CompletedAt: s.now().UTC().Round(0)}
	if len(events) != 0 {
		first, last := events[0].GlobalPosition, events[len(events)-1].GlobalPosition
		request.FirstEventPosition, request.LastEventPosition = &first, &last
	}
	_, err = s.store.CompleteCommand(ctx, request)
	return err
}

func routeContainsNode(route workflow.Route, nodeID workflow.Identifier) bool {
	for _, node := range route.Nodes {
		if node.ID == nodeID {
			return true
		}
	}
	return false
}

func exactWorkflowNode(ctx context.Context, planner WorkflowPlanner, name, version, digest string, nodeID workflow.Identifier) (workflow.Definition, workflow.Node, error) {
	reader, ok := planner.(WorkflowDefinitionReader)
	if !ok {
		return workflow.Definition{}, nil, errors.New("workflow planner cannot rehydrate installed definitions for dispatch")
	}
	definition, err := reader.Definition(ctx, name, version)
	if err != nil {
		return workflow.Definition{}, nil, fmt.Errorf("read installed workflow for dispatch: %w", err)
	}
	if definition.Version.Name != name || definition.Version.Version != version || definition.Version.Digest != digest {
		return workflow.Definition{}, nil, fmt.Errorf("installed workflow identity changed before dispatch: want %s %s %s", name, version, digest)
	}
	node, ok := definition.Document.Spec.Nodes[nodeID]
	if !ok {
		return workflow.Definition{}, nil, fmt.Errorf("frozen entry node %s is absent from installed workflow", nodeID)
	}
	return definition, node, nil
}

func (s *Service) ensureWorkflowEntryAttempt(ctx context.Context, run statestore.RunProjection) (statestore.AttemptProjection, error) {
	attempts, err := s.store.AttemptsForRun(ctx, run.RunID)
	if err != nil {
		return statestore.AttemptProjection{}, err
	}
	if len(attempts) != 0 {
		return attempts[0], nil
	}
	var route workflow.Route
	if run.RouteSnapshot == "" || json.Unmarshal([]byte(run.RouteSnapshot), &route) != nil || route.Entry == "" || !routeContainsNode(route, route.Entry) {
		return statestore.AttemptProjection{}, errors.New("frozen route has no valid executable entry")
	}
	if len(route.InputRequirements) != 0 {
		return statestore.AttemptProjection{}, &workflowAdmissionError{
			code:    "RUN_INPUT_REQUIRED",
			message: inputRequirementValidationErrors(route.InputRequirements).Error(),
		}
	}
	s.mu.Lock()
	planner := s.planner
	s.mu.Unlock()
	if planner == nil {
		return statestore.AttemptProjection{}, ErrWorkflowUnavailable
	}
	if _, _, err := exactWorkflowNode(ctx, planner, run.WorkflowID, run.WorkflowVersion, run.WorkflowDigest, route.Entry); err != nil {
		return statestore.AttemptProjection{}, err
	}
	if _, err := s.store.RunExecutionContext(ctx, run.RunID); errors.Is(err, statestore.ErrNotFound) {
		if _, err = s.saveInitialExecutionContext(ctx, run, route, map[workflow.Identifier]json.RawMessage{}); err != nil {
			return statestore.AttemptProjection{}, fmt.Errorf("repair missing workflow execution context: %w", err)
		}
	} else if err != nil {
		return statestore.AttemptProjection{}, fmt.Errorf("read workflow execution context during repair: %w", err)
	}
	entryID := string(route.Entry)
	visitID := stableID("visit_", run.RunID+"\x00"+entryID)
	attemptID := stableID("attempt_", run.RunID+"\x00"+entryID)
	now := s.now().UTC().Round(0)
	_, err = s.store.Append(ctx,
		pendingEvent("visit.created", statestore.AggregateVisit, visitID, 0, run.RunID, "visit-create:"+visitID, statestore.ActorSystem, "daemon", now, map[string]any{"runId": run.RunID, "nodeId": entryID}),
		pendingEvent("attempt.created", statestore.AggregateAttempt, attemptID, 0, run.RunID, "repair-entry:"+run.RunID, statestore.ActorSystem, "daemon", now, map[string]any{
			"runId": run.RunID, "visitId": visitID, "nodeId": entryID, "scenario": ScenarioWorkflow, "provider": s.workflowProviderForRun(ctx, run.RunID),
			"logReference": strings.TrimPrefix(attemptID, "attempt_") + ".log", "priority": run.Priority,
		}),
	)
	if err != nil {
		// A concurrent repair may have won the deterministic identities.
		attempts, readErr := s.store.AttemptsForRun(ctx, run.RunID)
		if readErr == nil && len(attempts) != 0 {
			return attempts[0], nil
		}
		return statestore.AttemptProjection{}, err
	}
	return s.store.Attempt(ctx, attemptID)
}

func (s *Service) failQueuedRun(ctx context.Context, run statestore.RunProjection, code string, cause error) error {
	if run.Status != statestore.RunQueued {
		return fmt.Errorf("cannot record queued dispatch failure for run %s in %s", run.RunID, run.Status)
	}
	now := s.now().UTC().Round(0)
	_, err := s.store.Append(ctx, pendingEvent("run.admission_failed", statestore.AggregateRun, run.RunID, run.ResourceVersion, run.RunID, "dispatch-failure:"+run.RunID, statestore.ActorSystem, "daemon", now, map[string]any{"code": code, "message": cause.Error()}))
	return err
}

// Start durably creates one run and attempt, closes command evidence, then
// schedules provider work. Repeating the idempotency key returns the same run.
func (s *Service) Start(ctx context.Context, request StartRequest, idempotencyKey string) (View, error) {
	if !s.schedulingAdmitted() {
		return View{}, ErrSchedulingBlocked
	}
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
		if err := s.launch(view.Attempts[0]); err != nil {
			s.failAttemptWithCode(view.Attempts[0].AttemptID, view.Run.RunID, "PROVIDER_FAILED", err)
		}
		return view, nil
	}

	logReference := strings.TrimPrefix(attemptID, "attempt_") + ".log"
	events, err := s.store.Append(ctx,
		pendingEvent("run.created", statestore.AggregateRun, runID, 0, runID, idempotencyKey, statestore.ActorUser, "local-user", now, map[string]any{
			"workItemId": stableID("work_", runID), "workflowId": compatibilityWorkflowID, "workflowVersion": compatibilityWorkflowVersion,
		}),
		pendingEvent("run.route_frozen", statestore.AggregateRun, runID, 1, runID, "route-frozen:"+runID, statestore.ActorSystem, "daemon", now, map[string]any{}),
		pendingEvent("run.started", statestore.AggregateRun, runID, 2, runID, "run-start:"+runID, statestore.ActorUser, "local-user", now, map[string]any{}),
		pendingEvent("visit.created", statestore.AggregateVisit, visitID, 0, runID, "visit-create:"+visitID, statestore.ActorSystem, "daemon", now, map[string]any{
			"runId": runID, "nodeId": nodeID,
		}),
		pendingEvent("visit.ready", statestore.AggregateVisit, visitID, 1, runID, "visit-ready:"+visitID, statestore.ActorSystem, "daemon", now, map[string]any{}),
		pendingEvent("visit.started", statestore.AggregateVisit, visitID, 2, runID, "visit-start:"+visitID, statestore.ActorSystem, "daemon", now, map[string]any{}),
		pendingEvent("attempt.created", statestore.AggregateAttempt, attemptID, 0, runID, idempotencyKey, statestore.ActorSystem, "daemon", now, map[string]any{
			"runId": runID, "visitId": visitID, "nodeId": nodeID, "scenario": request.Scenario, "provider": ProviderFake, "logReference": logReference,
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
	if err := s.launch(view.Attempts[0]); err != nil {
		s.failAttemptWithCode(view.Attempts[0].AttemptID, view.Run.RunID, "PROVIDER_FAILED", err)
	}
	return view, nil
}

// Get reads only persisted projections.
func (s *Service) Get(ctx context.Context, runID string) (View, error) {
	var evidence statestore.RunEvidence
	var err error
	if reader, ok := s.store.(interface {
		RunSummaryEvidence(context.Context, string) (statestore.RunEvidence, error)
	}); ok {
		evidence, err = reader.RunSummaryEvidence(ctx, runID)
	} else {
		evidence, err = s.store.RunEvidence(ctx, runID)
	}
	if err != nil {
		return View{}, err
	}
	nodes, err := s.store.NodesForRun(ctx, runID)
	if err != nil {
		return View{}, err
	}
	attempts, err := s.store.AttemptsForRun(ctx, runID)
	if err != nil {
		return View{}, err
	}
	if nodes == nil {
		nodes = []statestore.NodeProjection{}
	}
	if attempts == nil {
		attempts = []statestore.AttemptProjection{}
	}
	timeline, timelinePageInfo := summarizeTimeline(evidence.Events)
	commands, commandsPageInfo := summarizeCommands(evidence.Commands)
	var frozen workflow.Route
	var assessment *preparation.Assessment
	if evidence.Run.RouteSnapshot != "" {
		if err := json.Unmarshal([]byte(evidence.Run.RouteSnapshot), &frozen); err != nil {
			return View{}, err
		}
		var err error
		assessment, err = readPreparation(frozen)
		if err != nil {
			return View{}, err
		}
	}
	return View{
		Assessment:       assessment,
		SchemaVersion:    1,
		Run:              evidence.Run,
		Nodes:            nodes,
		Attempts:         attempts,
		Timeline:         timeline,
		TimelinePageInfo: timelinePageInfo,
		Commands:         commands,
		CommandsPageInfo: commandsPageInfo,
		Issue:            summarizeRunIssue(evidence.Events),
	}, nil
}

func summarizeRunIssue(events []statestore.Event) *RunIssueSummary {
	for index := len(events) - 1; index >= 0; index-- {
		event := events[index]
		if event.AggregateType != statestore.AggregateRun {
			continue
		}
		switch event.Kind {
		case "run.resumed", "run.retried", "run.completed", "run.cancelled", "run.continued":
			return nil
		}
		if event.Kind != "run.failed" && event.Kind != "run.admission_failed" && event.Kind != "run.reconcile_required" && event.Kind != "run.input_required" {
			continue
		}
		var value RunIssueSummary
		if json.Unmarshal(event.Data, &value) == nil && value.Code != "" && value.Message != "" {
			switch event.Kind {
			case "run.input_required":
				value.Kind = RunIssueInputRequired
			case "run.reconcile_required":
				value.Kind = RunIssueReconcileRequired
			default:
				value.Kind = RunIssueFailure
			}
			return &value
		}
		return nil
	}
	return nil
}

func summarizeTimeline(events []statestore.Event) ([]TimelineEntry, TimelinePageInfo) {
	start := 0
	if len(events) > viewTimelineLimit {
		start = len(events) - viewTimelineLimit
	}
	window := events[start:]
	values := make([]TimelineEntry, 0, len(window))
	for _, event := range window {
		values = append(values, TimelineEntry{
			ID: event.ID, GlobalPosition: event.GlobalPosition,
			AggregateType: event.AggregateType, AggregateID: event.AggregateID,
			AggregateRevision: event.AggregateRevision, Kind: event.Kind,
			OccurredAt: event.OccurredAt, RecordedAt: event.RecordedAt,
			ActorType: event.Actor.Type,
		})
	}
	page := TimelinePageInfo{HasEarlier: start > 0}
	if len(values) != 0 {
		first, last := values[0].GlobalPosition, values[len(values)-1].GlobalPosition
		page.FirstPosition, page.LastPosition = &first, &last
	}
	return values, page
}

func summarizeCommands(commands []statestore.CommandEvidence) ([]CommandSummary, CommandPageInfo) {
	start := 0
	if len(commands) > viewCommandLimit {
		start = len(commands) - viewCommandLimit
	}
	window := commands[start:]
	values := make([]CommandSummary, 0, len(window))
	for _, command := range window {
		values = append(values, CommandSummary{
			Scope: command.Scope, Status: command.Status,
			ResponseStatus:     command.ResponseStatus,
			FirstEventPosition: command.FirstEventPosition,
			LastEventPosition:  command.LastEventPosition,
			CreatedAt:          command.CreatedAt, CompletedAt: command.CompletedAt,
		})
	}
	return values, CommandPageInfo{HasEarlier: start > 0}
}

// ResumeActive schedules every non-terminal attempt after startup projection rebuild.
func (s *Service) ResumeActive(ctx context.Context) error {
	repaired := make(map[string]bool)
	runs, err := s.store.Runs(ctx)
	if err != nil {
		return err
	}
	for _, run := range runs {
		if run.Status == statestore.RunWaiting && run.WorkflowDigest != "" {
			if err := s.repairWorkflowCheckpoints(ctx, run); err != nil {
				return err
			}
		}
		if run.Status != statestore.RunQueued || run.WorkflowDigest == "" {
			continue
		}
		if s.queueEnabled {
			continue // Unclaimed queue entries are admitted by the queue loop.
		}
		attempts, readErr := s.store.AttemptsForRun(ctx, run.RunID)
		if readErr != nil {
			return readErr
		}
		if len(attempts) != 0 {
			continue
		}
		attempt, repairErr := s.ensureWorkflowEntryAttempt(ctx, run)
		if repairErr != nil {
			if workflowFailureCode(repairErr, "") == "RUN_INPUT_REQUIRED" {
				if waitErr := s.waitQueuedRunForInputs(ctx, run, repairErr); waitErr != nil {
					return errors.Join(repairErr, waitErr)
				}
				continue
			}
			if failErr := s.failQueuedRun(ctx, run, workflowFailureCode(repairErr, "WORKFLOW_DISPATCH_REPAIR_FAILED"), repairErr); failErr != nil {
				return errors.Join(repairErr, failErr)
			}
			continue
		}
		repaired[attempt.AttemptID] = true
	}
	attempts, err := s.store.ActiveAttempts(ctx)
	if err != nil {
		return err
	}
	for _, attempt := range attempts {
		run, runErr := s.store.Run(ctx, attempt.RunID)
		if runErr != nil {
			return runErr
		}
		if run.Status == statestore.RunQueued || run.Status == statestore.RunRunning {
			if attempt.Scenario == ScenarioWorkflow && attempt.Status == statestore.AttemptCreated && !repaired[attempt.AttemptID] {
				if reconcileErr := s.reconcileUncertainStart(ctx, attempt, run); reconcileErr != nil {
					return reconcileErr
				}
				continue
			}
			if launchErr := s.launch(attempt); launchErr != nil {
				s.failAttemptWithCode(attempt.AttemptID, attempt.RunID, "WORKFLOW_DISPATCH_FAILED", launchErr)
			}
		}
	}
	return nil
}

func (s *Service) waitQueuedRunForInputs(ctx context.Context, run statestore.RunProjection, cause error) error {
	if run.Status != statestore.RunQueued {
		return fmt.Errorf("cannot record input wait for run %s in %s", run.RunID, run.Status)
	}
	_, err := s.store.Append(ctx, pendingEvent("run.input_required", statestore.AggregateRun, run.RunID, run.ResourceVersion, run.RunID, "run-input-required:"+run.RunID, statestore.ActorSystem, "daemon", s.now(), map[string]any{"code": "RUN_INPUT_REQUIRED", "message": cause.Error()}))
	return err
}

func (s *Service) reconcileUncertainStart(ctx context.Context, attempt statestore.AttemptProjection, run statestore.RunProjection) error {
	message := "daemon restarted before the initial Codex provider identity was durably recorded; provider ownership must be reconciled before retry"
	_, err := s.store.Append(ctx,
		pendingEvent("attempt.reconcile_required", statestore.AggregateAttempt, attempt.AttemptID, attempt.ResourceVersion, run.RunID, "reconcile-start:"+attempt.AttemptID, statestore.ActorSystem, "daemon", s.now(), map[string]any{"code": "WORKFLOW_START_UNCERTAIN", "message": message}),
		pendingEvent("run.reconcile_required", statestore.AggregateRun, run.RunID, run.ResourceVersion, run.RunID, "reconcile-start:"+run.RunID, statestore.ActorSystem, "daemon", s.now(), map[string]any{"attemptId": attempt.AttemptID, "code": "WORKFLOW_START_UNCERTAIN", "message": message}),
	)
	return err
}

// Close cancels and joins provider workers.
func (s *Service) Close() error {
	s.cancel()
	s.wait.Wait()
	return nil
}

func (s *Service) workflowAttemptContext(ctx context.Context, attempt statestore.AttemptProjection, run statestore.RunProjection) (AttemptRequestContext, error) {
	s.mu.Lock()
	planner := s.planner
	s.mu.Unlock()
	if planner == nil {
		return AttemptRequestContext{}, ErrWorkflowUnavailable
	}
	var route workflow.Route
	if run.RouteSnapshot == "" || json.Unmarshal([]byte(run.RouteSnapshot), &route) != nil || !routeContainsNode(route, workflow.Identifier(attempt.NodeID)) {
		return AttemptRequestContext{}, errors.New("frozen route does not contain the attempted node")
	}
	definition, node, err := exactWorkflowNode(ctx, planner, run.WorkflowID, run.WorkflowVersion, run.WorkflowDigest, workflow.Identifier(attempt.NodeID))
	if err != nil {
		return AttemptRequestContext{}, err
	}
	work, err := s.store.WorkItem(ctx, run.WorkItemID)
	if err != nil {
		return AttemptRequestContext{}, fmt.Errorf("read workflow attempt work item: %w", err)
	}
	project, err := s.store.Project(ctx, work.ProjectID)
	if err != nil {
		return AttemptRequestContext{}, fmt.Errorf("read workflow attempt project: %w", err)
	}
	executionContext, err := s.store.RunExecutionContext(ctx, run.RunID)
	if err != nil {
		return AttemptRequestContext{}, fmt.Errorf("read durable workflow execution context: %w", err)
	}
	if len(executionContext.ExtensionPins) > 0 {
		resolver, ok := s.workflowFactory.(ExtensionPinProvider)
		if !ok {
			return AttemptRequestContext{}, errors.New("EXTENSION_UNAVAILABLE: pinned provider resolver is unavailable")
		}
		if err := resolver.ValidateExtensionPins(executionContext.ExtensionPins); err != nil {
			return AttemptRequestContext{}, err
		}
	}

	var frameSnapshot workflow.FrameSnapshot
	if err := json.Unmarshal(executionContext.FrameSnapshot, &frameSnapshot); err != nil {
		return AttemptRequestContext{}, fmt.Errorf("decode durable workflow frame: %w", err)
	}
	if _, err := workflow.RestoreFrame(frameSnapshot); err != nil {
		return AttemptRequestContext{}, fmt.Errorf("validate durable workflow frame: %w", err)
	}
	runInputs := make(map[workflow.Identifier]json.RawMessage, len(executionContext.RunInputs))
	for id, value := range executionContext.RunInputs {
		runInputs[workflow.Identifier(id)] = append(json.RawMessage(nil), value...)
	}
	acceptedOutputs := make(map[workflow.Identifier]map[workflow.Identifier]json.RawMessage, len(executionContext.AcceptedOutputs))
	for nodeID, outputs := range executionContext.AcceptedOutputs {
		acceptedOutputs[workflow.Identifier(nodeID)] = make(map[workflow.Identifier]json.RawMessage, len(outputs))
		for outputID, value := range outputs {
			acceptedOutputs[workflow.Identifier(nodeID)][workflow.Identifier(outputID)] = append(json.RawMessage(nil), value...)
		}
	}
	nodeInputs, err := resolveNodeInputs(node, runInputs, acceptedOutputs)
	if err != nil {
		return AttemptRequestContext{}, fmt.Errorf("resolve durable workflow node inputs: %w", err)
	}
	return s.reviewAttemptContext(ctx, AttemptRequestContext{Attempt: attempt, Run: run, WorkItem: work, Project: project, Workflow: definition, Node: node, FrozenRoute: route, RunInputs: runInputs, AcceptedOutputs: acceptedOutputs, NodeInputs: nodeInputs, ExecutionContext: executionContext, FrameSnapshot: frameSnapshot})
}

func validateBuiltAttemptRequest(request provider.AttemptRequest, attempt statestore.AttemptProjection) error {
	if request.AttemptID != attempt.AttemptID || request.RunID != attempt.RunID || request.NodeID != attempt.NodeID {
		return errors.New("attempt request identities do not match the durable attempt")
	}
	if request.IdempotencyKey != "start:"+attempt.AttemptID {
		return errors.New("attempt request must use the durable start idempotency key")
	}
	if strings.TrimSpace(request.Workspace) == "" || strings.TrimSpace(request.Prompt) == "" {
		return errors.New("attempt request requires workspace and prompt")
	}
	return nil
}

func (s *Service) launch(attempt statestore.AttemptProjection) error {
	runForDeletion, readErr := s.store.Run(s.ctx, attempt.RunID)
	if readErr != nil {
		return readErr
	}
	workForDeletion, readErr := s.store.WorkItem(s.ctx, runForDeletion.WorkItemID)
	if readErr == nil && workForDeletion.Deletion != statestore.WorkRetained {
		return nil
	}
	if readErr != nil && !errors.Is(readErr, statestore.ErrNotFound) {
		return readErr
	}
	s.mu.Lock()
	if _, exists := s.workers[attempt.AttemptID]; exists {
		s.mu.Unlock()
		return nil
	}
	if s.queueEnabled {
		current, err := s.store.Attempt(s.ctx, attempt.AttemptID)
		if err != nil {
			s.mu.Unlock()
			return err
		}
		run, err := s.store.Run(s.ctx, attempt.RunID)
		if err != nil {
			s.mu.Unlock()
			return err
		}
		if current.Status.Terminal() || (run.Status != statestore.RunQueued && run.Status != statestore.RunRunning) {
			s.mu.Unlock()
			return nil
		}
		attempt = current
		capacity, err := s.hasRunCapacityLocked(s.ctx, attempt.RunID)
		if err != nil || !capacity {
			s.mu.Unlock()
			return err // Remains durably queued; the loop retries when a slot opens.
		}
	}
	if err := s.ctx.Err(); err != nil {
		s.mu.Unlock()
		return fmt.Errorf("run service cannot launch attempt: %w", err)
	}
	ctx, cancel := context.WithCancel(s.ctx)
	active := &worker{attempt: attempt, cancel: cancel, done: make(chan struct{})}
	s.workers[attempt.AttemptID] = active
	s.wait.Add(1)
	s.mu.Unlock()
	go func() {
		defer s.wait.Done()
		defer func() {
			s.mu.Lock()
			delete(s.workers, attempt.AttemptID)
			close(active.done)
			s.mu.Unlock()
		}()
		s.execute(ctx, active, attempt)
	}()
	return nil
}

func (s *Service) execute(ctx context.Context, active *worker, attempt statestore.AttemptProjection) {
	resume := attempt.Status == statestore.AttemptRunning
	run, err := s.store.Run(ctx, attempt.RunID)
	if err != nil {
		return
	}
	var startRequest provider.AttemptRequest
	var adapter provider.Provider
	var dispatchContext AttemptRequestContext
	if attempt.Scenario == ScenarioWorkflow {
		var contextErr error
		dispatchContext, contextErr = s.workflowAttemptContext(ctx, attempt, run)
		if contextErr != nil {
			if ctx.Err() == nil {
				s.failAttemptWithCode(attempt.AttemptID, attempt.RunID, "WORKFLOW_DEFINITION_MISMATCH", contextErr)
			}
			return
		}

		handler, lookupErr := nodes.Lookup(dispatchContext.Node)
		if lookupErr != nil {
			s.failAttemptWithCode(attempt.AttemptID, attempt.RunID, "WORKFLOW_DISPATCH_UNAVAILABLE", lookupErr)
			return
		}
		if _, builtin := handler.(nodes.DeterministicHandler); builtin {
			s.executeBuiltinWorkflowAttempt(ctx, attempt, dispatchContext)
			return
		}
		s.mu.Lock()
		workflowFactory, requestBuilder := s.workflowFactory, s.requestBuilder
		s.mu.Unlock()
		if workflowFactory == nil || requestBuilder == nil {
			if ctx.Err() == nil {
				s.failAttemptWithCode(attempt.AttemptID, attempt.RunID, "WORKFLOW_DISPATCH_UNAVAILABLE", errors.New("codex workflow dispatch is not configured"))
			}
			return
		}
		{
			startRequest, err = requestBuilder.BuildAttemptRequest(ctx, dispatchContext)
			if err != nil {
				if ctx.Err() == nil {
					s.failAttemptWithCode(attempt.AttemptID, attempt.RunID, "ATTEMPT_REQUEST_BUILD_FAILED", err)
				}
				return
			}
			if err = validateBuiltAttemptRequest(startRequest, attempt); err != nil {
				if ctx.Err() == nil {
					s.failAttemptWithCode(attempt.AttemptID, attempt.RunID, "ATTEMPT_REQUEST_INVALID", err)
				}
				return
			}
		}
		var providerRef *extension.Ref
		if ref, ok := dispatchContext.ExecutionContext.ExtensionPins["provider:"+attempt.Provider]; ok {
			providerRef = &ref
		}
		adapter, err = workflowFactory.Provider(ctx, ProviderRequest{Ref: providerRef, Provider: attempt.Provider, Scenario: attempt.Scenario, AttemptID: attempt.AttemptID, Resume: resume})
	} else {
		adapter, err = s.factory.Provider(attempt.Scenario, attempt.AttemptID, resume)
	}
	if err != nil {
		if ctx.Err() == nil {
			code := "PROVIDER_FAILED"
			if attempt.Scenario == ScenarioWorkflow {
				code = "WORKFLOW_DISPATCH_FAILED"
			}
			s.failAttemptWithCode(attempt.AttemptID, attempt.RunID, code, err)
		}
		return
	}
	if attempt.Scenario == ScenarioWorkflow {
		health, healthErr := adapter.ProbeHealth(ctx)
		if healthErr != nil || (health.State != provider.HealthAvailable && health.State != provider.HealthDegraded) {
			if healthErr == nil {
				healthErr = fmt.Errorf("codex provider is not ready: %s", health.State)
			}
			if ctx.Err() == nil {
				s.failAttemptWithCode(attempt.AttemptID, attempt.RunID, "PROVIDER_NOT_READY", healthErr)
			}
			return
		}
	}
	if attempt.Scenario == ScenarioWorkflow {
		if err = s.startWorkflowVisit(ctx, attempt); err != nil {
			return
		}
	} else if run.Status == statestore.RunQueued {
		if _, err = s.store.Append(ctx, pendingEvent("run.visit_ready", statestore.AggregateRun, run.RunID, run.ResourceVersion,
			run.RunID, fmt.Sprintf("visit-ready:%s:%d", attempt.AttemptID, run.ResourceVersion), statestore.ActorSystem, "daemon", s.now(), map[string]any{"visitId": attempt.VisitID})); err != nil {
			return
		}
	}
	s.mu.Lock()
	active.adapter = adapter
	s.mu.Unlock()
	var handle provider.AttemptHandle
	if resume {
		handle, err = adapter.ResumeAttempt(ctx, provider.ResumeRequest{
			DynamicTools: startRequest.DynamicTools, ToolHandler: startRequest.ToolHandler,
			AttemptID: attempt.AttemptID, IdempotencyKey: "resume:" + attempt.AttemptID,
			ProviderThreadID: attempt.ProviderThreadID, ProviderTurnID: attempt.ProviderTurnID, LastSequence: attempt.LastSequence,
		})
	} else {
		if attempt.Scenario != ScenarioWorkflow {
			s.mu.Lock()
			workspace := s.workspace
			s.mu.Unlock()
			startRequest = provider.AttemptRequest{
				AttemptID: attempt.AttemptID, RunID: attempt.RunID, NodeID: attempt.NodeID,
				IdempotencyKey: "start:" + attempt.AttemptID, Access: provider.AccessReadOnly,
				Network: provider.NetworkDenied, CommandPolicy: provider.InteractionDeny,
				FilePolicy: provider.InteractionDeny, ToolPolicy: provider.InteractionDeny,
				Workspace: workspace, Prompt: "Execute the deterministic M1 fake-provider scenario.",
			}
		}
		handle, err = adapter.StartAttempt(ctx, startRequest)
	}
	if err != nil {
		if ctx.Err() == nil {
			if resume {
				s.pauseUnsafeResume(ctx, attempt, err)
			} else {
				s.failAttempt(attempt.AttemptID, attempt.RunID, err)
			}
		}
		return
	}
	s.mu.Lock()
	active.handle = handle
	s.mu.Unlock()
	identityChanged := resume && (handle.ProviderThreadID != attempt.ProviderThreadID || handle.ProviderTurnID != attempt.ProviderTurnID)
	if handle.AttemptID != attempt.AttemptID || handle.Provider != attempt.Provider || handle.ProviderThreadID == "" ||
		handle.ProviderTurnID == "" || handle.ProcessOwnerID == "" || identityChanged {
		inconsistent := errors.New("provider returned an inconsistent recovery handle")
		if resume {
			s.pauseUnsafeResume(ctx, attempt, inconsistent)
		} else {
			s.failAttempt(attempt.AttemptID, attempt.RunID, inconsistent)
		}
		return
	}
	current, err := s.store.Attempt(ctx, attempt.AttemptID)
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
		current.RunID, kind+":"+current.AttemptID, statestore.ActorProvider, current.Provider, s.now(), identity))
	started, err := s.store.Append(ctx, startEvents...)
	if err != nil || len(started) == 0 {
		return
	}

	stream, err := adapter.StreamEvents(ctx, provider.EventRequest{Handle: handle, AfterSequence: current.LastSequence})
	if err != nil {
		if ctx.Err() == nil {
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
			if ctx.Err() == nil {
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
		current, err = s.store.Attempt(ctx, current.AttemptID)
		if err != nil {
			return
		}
		payloadDigest := fmt.Sprintf("%x", sha256.Sum256(event.Payload))
		data := map[string]any{
			"sequence": event.Sequence, "kind": event.Kind, "provider": event.Provider,
			"providerVersion": event.ProviderVersion, "payloadDigest": payloadDigest,
			"payload": json.RawMessage(event.Payload), "providerThreadId": event.ProviderThreadID,
			"providerTurnId": event.ProviderTurnID, "providerItemId": event.ProviderItemID,
			"logReference": current.LogReference,
		}
		events := []statestore.PendingEvent{pendingEvent("attempt.provider_event", statestore.AggregateAttempt, current.AttemptID,
			current.ResourceVersion, current.RunID, fmt.Sprintf("provider:%s:%d", current.AttemptID, event.Sequence),
			statestore.ActorProvider, current.Provider, event.OccurredAt, data)}
		if event.Kind == provider.EventUserInputRequested || event.Kind == provider.EventPermissionRequested {
			checkpoint, present, checkpointErr := provider.InteractionCheckpointFromEvent(event)
			if checkpointErr != nil || !present {
				s.failAttempt(current.AttemptID, current.RunID, errors.New("provider interaction event has no valid checkpoint"))
				return
			}
			if event.Kind == provider.EventUserInputRequested && checkpoint.Kind == provider.InteractionUser {
				events = append(events, inputRequestedEvent(current, handle, event, checkpoint))
			} else if event.Kind == provider.EventPermissionRequested && checkpoint.Kind != provider.InteractionUser {
				events = append(events, permissionRequestedEvent(current, handle, event, checkpoint))
			} else {
				s.failAttempt(current.AttemptID, current.RunID, errors.New("provider interaction event checkpoint kind does not match"))
				return
			}
		}
		if _, err = s.store.Append(ctx, events...); err != nil {
			return
		}
		line, _ := json.Marshal(map[string]any{"sequence": event.Sequence, "kind": event.Kind, "payloadDigest": payloadDigest, "redacted": true})
		if err = s.logs.AppendLog(ctx, current.LogReference, append(line, '\n')); err != nil {
			s.failAttempt(current.AttemptID, current.RunID, errors.New("redacted provider log persistence failed"))
			return
		}
	}
	result, err := adapter.GetResult(ctx, provider.ResultRequest{Handle: handle})
	if err != nil {
		if ctx.Err() == nil {
			s.failAttempt(current.AttemptID, current.RunID, err)
		}
		return
	}
	if ctx.Err() == nil {
		s.completeAttempt(ctx, current.AttemptID, current.RunID, result)
	}
	// Release a completed transport only after its events and result have been
	// consumed. Uncertain results retain the existing reconciliation lifecycle.
	switch result.(type) {
	case provider.SucceededResult, provider.FailedResult, provider.CancelledResult:
		if owner, ok := s.workflowFactory.(interface{ ReleaseProvider(string) }); ok {
			owner.ReleaseProvider(current.AttemptID)
		}
	}
}

func (s *Service) pauseUnsafeResume(ctx context.Context, attempt statestore.AttemptProjection, cause error) {
	failure := ports.Failure{
		Code: ports.FailureUncertain, Message: "provider resume could not be proven safe", Retryable: false,
	}
	var classified *ports.Failure
	if errors.As(cause, &classified) {
		failure = *classified
		failure.Details = cloneStringMap(classified.Details)
	}
	s.completeAttempt(ctx, attempt.AttemptID, attempt.RunID, provider.UnknownResult{
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

func (s *Service) completeAttempt(ctx context.Context, attemptID, runID string, result provider.AttemptResult) {
	attempt, err := s.store.Attempt(ctx, attemptID)
	if err != nil || attempt.Status.Terminal() {
		return
	}
	run, err := s.store.Run(ctx, runID)
	if err != nil {
		return
	}
	node, err := s.store.Node(ctx, attempt.VisitID)
	if err != nil {
		return
	}
	attemptKind, nodeKind, runKind := "attempt.succeeded", "visit.succeeded", "run.completed"
	data := map[string]any{"lastSequence": attempt.LastSequence, "logReference": attempt.LogReference}
	runData := map[string]any{"attemptId": attemptID}
	switch value := result.(type) {
	case provider.SucceededResult:
		if attempt.Scenario == ScenarioWorkflow {
			if err := s.completeWorkflowSucceeded(ctx, attempt, run, node, value); err != nil {
				s.failAttemptWithCode(attemptID, runID, workflowFailureCode(err, "WORKFLOW_ADVANCE_FAILED"), err)
			}
			return
		}
		data["output"] = json.RawMessage(value.StructuredOutput)
	case provider.FailedResult:
		if handled, _ := s.failReviewAttempt(ctx, attempt, run, node, value.Failure.Message); handled {
			return
		}
		attemptKind, nodeKind, runKind, data["failure"] = "attempt.failed", "visit.failed", "run.failed", value.Failure
		runData["code"], runData["message"] = value.Failure.Code, value.Failure.Message
	case provider.CancelledResult:
		attemptKind, nodeKind, runKind = "attempt.cancelled", "visit.cancelled", "run.cancelled"
	case provider.InterruptedResult:
		attemptKind, nodeKind, runKind, data["failure"] = "attempt.interrupted", "visit.failed", "run.failed", value.Failure
		runData["code"], runData["message"] = value.Failure.Code, value.Failure.Message
	case provider.UnknownResult:
		attemptKind, nodeKind, runKind, data["failure"] = "attempt.reconcile_required", "visit.failed", "run.reconcile_required", value.Failure
		runData["code"], runData["message"] = value.Failure.Code, value.Failure.Message
	default:
		s.failAttempt(attemptID, runID, fmt.Errorf("unsupported provider result %T", result))
		return
	}
	events := make([]statestore.PendingEvent, 0, 5)
	if _, ok := result.(provider.SucceededResult); ok {
		events = append(events,
			pendingEvent("attempt.result_received", statestore.AggregateAttempt, attemptID, attempt.ResourceVersion, runID, "result:"+attemptID, statestore.ActorProvider, attempt.Provider, s.now(), data),
			pendingEvent(attemptKind, statestore.AggregateAttempt, attemptID, attempt.ResourceVersion+1, runID, "terminal:"+attemptID, statestore.ActorSystem, "daemon", s.now(), data),
			pendingEvent("visit.result_received", statestore.AggregateVisit, node.VisitID, node.ResourceVersion, runID, "result:"+node.VisitID+":"+attemptID, statestore.ActorProvider, attempt.Provider, s.now(), data),
			pendingEvent(nodeKind, statestore.AggregateVisit, node.VisitID, node.ResourceVersion+1, runID, "terminal:"+node.VisitID+":"+attemptID, statestore.ActorSystem, "daemon", s.now(), data),
		)
	} else {
		events = append(events,
			pendingEvent(attemptKind, statestore.AggregateAttempt, attemptID, attempt.ResourceVersion, runID, "terminal:"+attemptID, statestore.ActorProvider, attempt.Provider, s.now(), data),
			pendingEvent(nodeKind, statestore.AggregateVisit, node.VisitID, node.ResourceVersion, runID, "terminal:"+node.VisitID+":"+attemptID, statestore.ActorSystem, "daemon", s.now(), data),
		)
	}
	events = append(events, pendingEvent(runKind, statestore.AggregateRun, runID, run.ResourceVersion, runID, "terminal:"+runID+":"+attemptID, statestore.ActorSystem, "daemon", s.now(), runData))
	_, _ = s.store.Append(ctx, events...)
}

func identifierIn(values []workflow.Identifier, expected workflow.Identifier) bool {
	for _, value := range values {
		if value == expected {
			return true
		}
	}
	return false
}

func (s *Service) failAttempt(attemptID, runID string, cause error) {
	s.failAttemptWithCode(attemptID, runID, "PROVIDER_FAILED", cause)
}

func (s *Service) failAttemptWithCode(attemptID, runID, code string, cause error) {
	attempt, attemptErr := s.store.Attempt(context.Background(), attemptID)
	run, runErr := s.store.Run(context.Background(), runID)
	if attemptErr != nil || runErr != nil || attempt.Status.Terminal() {
		return
	}
	node, nodeErr := s.store.Node(context.Background(), attempt.VisitID)
	if nodeErr != nil {
		return
	}
	if handled, _ := s.failReviewAttempt(context.Background(), attempt, run, node, cause.Error()); handled {
		return
	}
	events := make([]statestore.PendingEvent, 0, 3)
	runKind := "run.failed"
	if run.Status == statestore.RunQueued {
		runKind = "run.admission_failed"
	} else if run.Status != statestore.RunRunning {
		return
	}
	nodeKind := "visit.failed"
	if node.Status == statestore.NodePending || node.Status == statestore.NodeReady {
		nodeKind = "visit.admission_failed"
	}
	events = append(events,
		pendingEvent("attempt.failed", statestore.AggregateAttempt, attemptID, attempt.ResourceVersion, runID, "failure:"+attemptID, statestore.ActorSystem, "daemon", s.now(), map[string]any{"code": code, "message": cause.Error(), "logReference": attempt.LogReference}),
		pendingEvent(nodeKind, statestore.AggregateVisit, node.VisitID, node.ResourceVersion, runID, "failure:"+node.VisitID+":"+attemptID, statestore.ActorSystem, "daemon", s.now(), map[string]any{"code": code, "message": cause.Error()}),
		pendingEvent(runKind, statestore.AggregateRun, runID, run.ResourceVersion, runID, "failure:"+runID+":"+attemptID, statestore.ActorSystem, "daemon", s.now(), map[string]any{"attemptId": attemptID, "code": code, "message": cause.Error()}),
	)
	_, _ = s.store.Append(context.Background(), events...)
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

func (service *Service) SetValueSchemaValidator(validator valueschema.Validator) {
	service.valueSchemas = validator
}
