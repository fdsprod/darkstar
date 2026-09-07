// Package worklifecycle owns the server-authoritative work board transition contract.
package worklifecycle

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"darkstar/src/core/identity"
	"darkstar/src/core/runexecution"
	"darkstar/src/ports/statestore"
)

const commandScope = "work.transition"

var (
	ErrInvalidRequest  = errors.New("invalid work transition request")
	ErrRejected        = errors.New("work transition is not currently allowed")
	ErrVersionConflict = errors.New("work transition resource version conflict")
)

// State is the closed board lifecycle derived from durable work and run truth.
type State string

const (
	StateBacklog State = "backlog"
	StateReady   State = "ready"
	StateRunning State = "running"
	StateWaiting State = "waiting"
	StateBlocked State = "blocked"
	StateReview  State = "review"
	StateFailed  State = "failed"
	StateDone    State = "done"
)

var allStates = [...]State{StateBacklog, StateReady, StateRunning, StateWaiting, StateBlocked, StateReview, StateFailed, StateDone}

type Availability string

const (
	AvailabilityEnabled  Availability = "enabled"
	AvailabilityDisabled Availability = "disabled"
)

type DisabledReason string

const (
	ReasonCurrentState         DisabledReason = "current_state"
	ReasonUnsupportedTarget    DisabledReason = "unsupported_target"
	ReasonPreparationRequired  DisabledReason = "preparation_required"
	ReasonProjectArchived      DisabledReason = "project_archived"
	ReasonTerminalWork         DisabledReason = "terminal_work"
	ReasonActiveRun            DisabledReason = "active_run"
	ReasonUnresolvedCheckpoint DisabledReason = "unresolved_checkpoint"
	ReasonReadinessRequired    DisabledReason = "readiness_required"
	ReasonPolicyBlocked        DisabledReason = "policy_blocked"
	ReasonConcurrencyConflict  DisabledReason = "concurrency_conflict"
	ReasonRunNotReady          DisabledReason = "run_not_ready"
)

type Confirmation string

const (
	ConfirmationNone     Confirmation = "none"
	ConfirmationRequired Confirmation = "required"
)

// Preparation is present only when requesting Backlog -> Ready.
type Preparation struct {
	WorkflowID      string `json:"workflowId"`
	WorkflowVersion string `json:"workflowVersion"`
	Profile         string `json:"profile,omitempty"`
}

// PlanRequest is target-specific input. Preparation is rejected for non-Ready targets.
type PlanRequest struct {
	Target      State        `json:"target"`
	Preparation *Preparation `json:"preparation,omitempty"`
}

// TargetDecision is one row in the exhaustive server decision table.
type TargetDecision struct {
	Target          State            `json:"target"`
	Availability    Availability     `json:"availability"`
	DisabledReasons []DisabledReason `json:"disabledReasons"`
	Confirmation    Confirmation     `json:"confirmation"`
}

// Plan is a rebuildable projection. ResourceVersion is the newest durable
// event position among every projection that can affect lifecycle authority.
// Apply uses that composite watermark for optimistic concurrency.
type Plan struct {
	SchemaVersion   int              `json:"schemaVersion"`
	WorkItemID      string           `json:"workItemId"`
	State           State            `json:"state"`
	RunID           string           `json:"runId,omitempty"`
	ResourceVersion uint64           `json:"resourceVersion"`
	Targets         []TargetDecision `json:"targets"`
}

// ApplyRequest adds concurrency and confirmation to the requested transition.
type ApplyRequest struct {
	PlanRequest
	ExpectedResourceVersion uint64 `json:"expectedResourceVersion"`
	Confirmation            string `json:"confirmation,omitempty"`
	IdempotencyKey          string `json:"-"`
}

type Effect string

const (
	EffectPrepared  Effect = "run_prepared"
	EffectStarted   Effect = "run_started"
	EffectPaused    Effect = "run_paused"
	EffectResumed   Effect = "run_resumed"
	EffectRetried   Effect = "run_retried"
	EffectCancelled Effect = "cancelled"
)

type Result struct {
	SchemaVersion int                       `json:"schemaVersion"`
	Target        State                     `json:"target"`
	Effect        Effect                    `json:"effect"`
	Before        Plan                      `json:"before"`
	After         Plan                      `json:"after"`
	Run           *statestore.RunProjection `json:"run,omitempty"`
}

// VersionConflictError carries the refreshed authoritative plan needed for recovery.
type VersionConflictError struct {
	Expected uint64
	Current  Plan
}

func (e *VersionConflictError) Error() string {
	return fmt.Sprintf("%v: expected %d, current %d", ErrVersionConflict, e.Expected, e.Current.ResourceVersion)
}
func (e *VersionConflictError) Unwrap() error { return ErrVersionConflict }

type Runtime interface {
	Prepare(context.Context, runexecution.CreateRequest, string) (statestore.RunProjection, error)
	Launch(context.Context, runexecution.ControlRequest) (statestore.RunProjection, error)
	Pause(context.Context, runexecution.ControlRequest) (statestore.RunProjection, error)
	Resume(context.Context, runexecution.ControlRequest) (statestore.RunProjection, error)
	Retry(context.Context, runexecution.RetryRequest) (statestore.RunProjection, error)
	Cancel(context.Context, runexecution.ControlRequest) (statestore.RunProjection, error)
}

type Service struct {
	store   statestore.Store
	runtime Runtime
	now     func() time.Time
}

func New(store statestore.Store, runtime Runtime) (*Service, error) {
	if store == nil || runtime == nil {
		return nil, errors.New("work lifecycle requires state and runtime control")
	}
	return &Service{store: store, runtime: runtime, now: time.Now}, nil
}

type facts struct {
	work                                                        statestore.WorkItemProjection
	run                                                         *statestore.RunProjection
	state                                                       State
	version                                                     uint64
	projectArchived, checkpoint, readiness, policy, concurrency bool
}

// Plan derives every target from the same durable runtime snapshot shape.
func (s *Service) Plan(ctx context.Context, workItemID string, request PlanRequest) (Plan, error) {
	request, err := normalize(request)
	if err != nil {
		return Plan{}, err
	}
	value, err := s.facts(ctx, strings.TrimSpace(workItemID))
	if err != nil {
		return Plan{}, err
	}
	plan := Plan{SchemaVersion: 1, WorkItemID: value.work.WorkItemID, State: value.state, ResourceVersion: value.version, Targets: make([]TargetDecision, 0, len(allStates))}
	if value.run != nil {
		plan.RunID = value.run.RunID
	}
	for _, target := range allStates {
		decision := TargetDecision{Target: target, Availability: AvailabilityDisabled, DisabledReasons: []DisabledReason{}, Confirmation: ConfirmationNone}
		if target == value.state {
			decision.DisabledReasons = append(decision.DisabledReasons, ReasonCurrentState)
		} else {
			decision.DisabledReasons = append(decision.DisabledReasons, s.reasons(value, target, request)...)
		}
		if len(decision.DisabledReasons) == 0 {
			decision.Availability = AvailabilityEnabled
		}
		if target == StateDone && decision.Availability == AvailabilityEnabled {
			decision.Confirmation = ConfirmationRequired
		}
		plan.Targets = append(plan.Targets, decision)
	}
	return plan, nil
}

func normalize(request PlanRequest) (PlanRequest, error) {
	valid := false
	for _, state := range allStates {
		valid = valid || request.Target == state
	}
	if !valid {
		return PlanRequest{}, fmt.Errorf("%w: target must be a supported lifecycle state", ErrInvalidRequest)
	}
	if request.Target != StateReady && request.Preparation != nil {
		return PlanRequest{}, fmt.Errorf("%w: preparation is allowed only for target ready", ErrInvalidRequest)
	}
	if request.Preparation != nil {
		request.Preparation.WorkflowID = strings.TrimSpace(request.Preparation.WorkflowID)
		request.Preparation.WorkflowVersion = strings.TrimSpace(request.Preparation.WorkflowVersion)
		request.Preparation.Profile = strings.TrimSpace(request.Preparation.Profile)
		if request.Preparation.WorkflowID == "" || request.Preparation.WorkflowVersion == "" {
			return PlanRequest{}, fmt.Errorf("%w: ready preparation requires workflowId and workflowVersion", ErrInvalidRequest)
		}
	}
	return request, nil
}

func (s *Service) reasons(value facts, target State, request PlanRequest) []DisabledReason {
	reasons := []DisabledReason{}
	if value.work.Status.Terminal() {
		return []DisabledReason{ReasonTerminalWork}
	}
	if value.projectArchived {
		reasons = append(reasons, ReasonProjectArchived)
	}
	if value.concurrency {
		reasons = append(reasons, ReasonConcurrencyConflict)
	}
	if value.checkpoint {
		reasons = append(reasons, ReasonUnresolvedCheckpoint)
	}
	if value.policy {
		reasons = append(reasons, ReasonPolicyBlocked)
	} else if value.readiness {
		reasons = append(reasons, ReasonReadinessRequired)
	}
	switch target {
	case StateReady:
		if value.state != StateBacklog {
			reasons = append(reasons, ReasonActiveRun)
		}
	case StateRunning:
		if value.run == nil || (value.run.Status != statestore.RunReady && value.run.Status != statestore.RunWaiting && value.run.Status != statestore.RunBlocked && value.run.Status != statestore.RunFailed) {
			reasons = append(reasons, ReasonRunNotReady)
		}
	case StateWaiting:
		if value.run == nil || (value.run.Status != statestore.RunQueued && value.run.Status != statestore.RunRunning) {
			reasons = append(reasons, ReasonUnsupportedTarget)
		}
	case StateDone:
		// Cancellation is supported from every non-terminal state.
	default:
		reasons = append(reasons, ReasonUnsupportedTarget)
	}
	return distinct(reasons)
}

func distinct(values []DisabledReason) []DisabledReason {
	seen := map[DisabledReason]bool{}
	result := make([]DisabledReason, 0, len(values))
	for _, value := range values {
		if !seen[value] {
			seen[value] = true
			result = append(result, value)
		}
	}
	return result
}

func (s *Service) facts(ctx context.Context, id string) (facts, error) {
	if id == "" {
		return facts{}, fmt.Errorf("%w: work item ID is required", ErrInvalidRequest)
	}
	work, err := s.store.WorkItem(ctx, id)
	if err != nil {
		return facts{}, err
	}
	project, err := s.store.Project(ctx, work.ProjectID)
	if err != nil {
		return facts{}, err
	}
	runs, err := s.store.RunsForWorkItem(ctx, id)
	if err != nil {
		return facts{}, err
	}
	sort.Slice(runs, func(i, j int) bool { return runs[i].LastGlobalPosition > runs[j].LastGlobalPosition })
	value := facts{work: work, state: StateBacklog, projectArchived: project.Status != statestore.ProjectActive}
	advanceVersion(&value.version, work.LastGlobalPosition, project.LastGlobalPosition)
	active := 0
	for i := range runs {
		advanceVersion(&value.version, runs[i].LastGlobalPosition)
		if !runs[i].Status.Terminal() {
			active++
			if value.run == nil {
				value.run = &runs[i]
			}
		}
	}
	// Terminal history is authoritative only when no live run exists. This
	// prevents a newer cancelled/completed record from hiding older resumable
	// work during recovery.
	if value.run == nil && len(runs) != 0 {
		value.run = &runs[0]
	}
	value.concurrency = active > 1
	if value.run == nil {
		if work.Status.Terminal() {
			value.state = StateDone
		}
		return value, nil
	}
	run := value.run
	nodes, err := s.store.NodesForRun(ctx, run.RunID)
	if err != nil {
		return facts{}, err
	}
	for _, node := range nodes {
		advanceVersion(&value.version, node.LastGlobalPosition)
		value.checkpoint = value.checkpoint || node.Status == statestore.NodeWaitingCheckpoint
	}
	attempts, err := s.store.AttemptsForRun(ctx, run.RunID)
	if err != nil {
		return facts{}, err
	}
	inputs, err := s.store.InputRequestsForRun(ctx, run.RunID)
	if err != nil {
		return facts{}, err
	}
	for _, input := range inputs {
		advanceVersion(&value.version, input.LastGlobalPosition)
		value.readiness = value.readiness || input.Status != statestore.InputRequestAnswered
	}
	for _, attempt := range attempts {
		advanceVersion(&value.version, attempt.LastGlobalPosition)
		permissions, permissionErr := s.store.ProviderPermissionsForAttempt(ctx, attempt.AttemptID)
		if permissionErr != nil {
			return facts{}, permissionErr
		}
		for _, permission := range permissions {
			advanceVersion(&value.version, permission.LastGlobalPosition)
			value.policy = value.policy || permission.Status != statestore.ProviderPermissionResponded
		}
	}
	assessment, assessErr := s.store.LatestReadinessAssessmentForRun(ctx, run.RunID)
	if assessErr == nil {
		advanceVersion(&value.version, assessment.LastGlobalPosition)
		approved := assessment.Status == statestore.ReadinessAssessmentDecided && assessment.Decision != nil && (assessment.Decision.Choice == "continue" || assessment.Decision.Choice == "accept_route_change")
		if !approved && assessment.Disposition != "ready" {
			value.policy = assessment.Disposition == "policy_blocked" || assessment.Disposition == "invariant_blocked"
			value.readiness = !value.policy
		}
	} else if !errors.Is(assessErr, statestore.ErrNotFound) {
		return facts{}, assessErr
	}
	value.state = deriveState(work, *run, value.checkpoint)
	return value, nil
}

func advanceVersion(current *uint64, positions ...uint64) {
	for _, position := range positions {
		if position > *current {
			*current = position
		}
	}
}

func deriveState(work statestore.WorkItemProjection, run statestore.RunProjection, checkpoint bool) State {
	if work.Status.Terminal() {
		return StateDone
	}
	if checkpoint {
		return StateReview
	}
	switch run.Status {
	case statestore.RunDraft:
		return StateBacklog
	case statestore.RunReady:
		return StateReady
	case statestore.RunQueued, statestore.RunRunning:
		return StateRunning
	case statestore.RunWaiting:
		return StateWaiting
	case statestore.RunBlocked, statestore.RunReconcileRequired:
		return StateBlocked
	case statestore.RunFailed:
		return StateFailed
	case statestore.RunCompleted, statestore.RunCancelled:
		return StateDone
	default:
		return StateBacklog
	}
}

type commandResponse struct {
	Result  *Result         `json:"result,omitempty"`
	Failure *commandFailure `json:"failure,omitempty"`
}
type commandFailure struct {
	Kind    string `json:"kind"`
	Message string `json:"message"`
	Current *Plan  `json:"current,omitempty"`
}

// Apply verifies the exact plan version, then delegates one replay-safe runtime effect.
func (s *Service) Apply(ctx context.Context, workItemID string, request ApplyRequest) (Result, error) {
	normalized, err := normalize(request.PlanRequest)
	if err != nil {
		return Result{}, err
	}
	request.PlanRequest = normalized
	if request.ExpectedResourceVersion == 0 || strings.TrimSpace(request.IdempotencyKey) != request.IdempotencyKey || len(request.IdempotencyKey) < 8 || len(request.IdempotencyKey) > 128 {
		return Result{}, fmt.Errorf("%w: positive expectedResourceVersion and an 8-128 byte idempotency key are required", ErrInvalidRequest)
	}
	if request.Target != StateDone && request.Confirmation != "" {
		return Result{}, fmt.Errorf("%w: confirmation is accepted only when required", ErrInvalidRequest)
	}
	encodedRequest, _ := json.Marshal(struct {
		WorkItemID string `json:"workItemId"`
		PlanRequest
		Expected     uint64 `json:"expectedResourceVersion"`
		Confirmation string `json:"confirmation,omitempty"`
	}{workItemID, request.PlanRequest, request.ExpectedResourceVersion, request.Confirmation})
	command, reused, err := s.store.BeginCommand(ctx, statestore.BeginCommandRequest{Scope: commandScope, IdempotencyKey: request.IdempotencyKey, RequestDigest: fmt.Sprintf("%x", sha256.Sum256(encodedRequest)), CreatedAt: s.now().UTC().Round(0)})
	if err != nil {
		return Result{}, err
	}
	if reused && command.Status == "completed" {
		return decodeResponse(command.Response)
	}
	before, err := s.Plan(ctx, workItemID, request.PlanRequest)
	if err != nil {
		return Result{}, err
	}
	if before.ResourceVersion != request.ExpectedResourceVersion {
		conflict := &VersionConflictError{Expected: request.ExpectedResourceVersion, Current: before}
		return Result{}, s.finishFailure(ctx, request.IdempotencyKey, "version", conflict.Error(), &before, 412, conflict)
	}
	decision := decisionFor(before, request.Target)
	if decision.Availability != AvailabilityEnabled {
		return Result{}, s.finishFailure(ctx, request.IdempotencyKey, "rejected", ErrRejected.Error(), &before, 409, ErrRejected)
	}
	if decision.Confirmation == ConfirmationRequired && request.Confirmation != "confirmed" {
		return Result{}, s.finishFailure(ctx, request.IdempotencyKey, "confirmation", "confirmation is required", &before, 409, ErrRejected)
	}
	var run *statestore.RunProjection
	var effect Effect
	control := runexecution.ControlRequest{IdempotencyKey: request.IdempotencyKey, Actor: statestore.Actor{Type: statestore.ActorUser, ID: "local-user"}}
	switch request.Target {
	case StateReady:
		p := request.Preparation
		create := runexecution.CreateRequest{WorkItemID: workItemID}
		if p != nil {
			create.WorkflowID, create.WorkflowVersion, create.Profile = p.WorkflowID, p.WorkflowVersion, p.Profile
		}
		value, callErr := s.runtime.Prepare(ctx, create, request.IdempotencyKey)
		err = callErr
		run = &value
		effect = EffectPrepared
	case StateRunning:
		control.RunID, control.ExpectedResourceVersion = before.RunID, currentRunVersion(ctx, s.store, before.RunID)
		var value statestore.RunProjection
		switch before.State {
		case StateFailed:
			value, err = s.runtime.Retry(ctx, runexecution.RetryRequest{ControlRequest: control})
			effect = EffectRetried
		case StateWaiting, StateBlocked:
			value, err = s.runtime.Resume(ctx, control)
			effect = EffectResumed
		default:
			value, err = s.runtime.Launch(ctx, control)
			effect = EffectStarted
		}
		run = &value
	case StateWaiting:
		control.RunID, control.ExpectedResourceVersion = before.RunID, currentRunVersion(ctx, s.store, before.RunID)
		value, callErr := s.runtime.Pause(ctx, control)
		err = callErr
		run = &value
		effect = EffectPaused
	case StateDone:
		if before.RunID == "" {
			work, readErr := s.store.WorkItem(ctx, workItemID)
			if readErr != nil {
				err = readErr
			} else {
				_, err = s.store.Append(ctx, workEvent(work, request.IdempotencyKey, s.now()))
				effect = EffectCancelled
			}
		} else {
			control.RunID, control.ExpectedResourceVersion = before.RunID, currentRunVersion(ctx, s.store, before.RunID)
			value, callErr := s.runtime.Cancel(ctx, control)
			err = callErr
			run = &value
			effect = EffectCancelled
		}
	}
	if err != nil {
		current, planErr := s.Plan(ctx, workItemID, request.PlanRequest)
		if planErr == nil && current.ResourceVersion != before.ResourceVersion {
			conflict := &VersionConflictError{Expected: request.ExpectedResourceVersion, Current: current}
			return Result{}, s.finishFailure(ctx, request.IdempotencyKey, "version", conflict.Error(), &current, 412, conflict)
		}
		return Result{}, s.finishFailure(ctx, request.IdempotencyKey, "runtime", err.Error(), nil, 409, ErrRejected)
	}
	after, err := s.Plan(ctx, workItemID, request.PlanRequest)
	if err != nil {
		return Result{}, err
	}
	result := Result{SchemaVersion: 1, Target: request.Target, Effect: effect, Before: before, After: after, Run: run}
	response, _ := json.Marshal(commandResponse{Result: &result})
	_, err = s.store.CompleteCommand(ctx, statestore.CompleteCommandRequest{Scope: commandScope, IdempotencyKey: request.IdempotencyKey, ResponseStatus: 200, Response: response, CompletedAt: s.now().UTC().Round(0)})
	return result, err
}

func currentRunVersion(ctx context.Context, store statestore.Store, id string) uint64 {
	value, err := store.Run(ctx, id)
	if err != nil {
		return 0
	}
	return value.ResourceVersion
}
func decisionFor(plan Plan, target State) TargetDecision {
	for _, value := range plan.Targets {
		if value.Target == target {
			return value
		}
	}
	return TargetDecision{Target: target, Availability: AvailabilityDisabled, DisabledReasons: []DisabledReason{ReasonUnsupportedTarget}, Confirmation: ConfirmationNone}
}
func workEvent(work statestore.WorkItemProjection, key string, now time.Time) statestore.PendingEvent {
	data := json.RawMessage(`{}`)
	return statestore.PendingEvent{SchemaVersion: 1, ID: identity.Random("event_"), AggregateType: statestore.AggregateWork, AggregateID: work.WorkItemID, ExpectedRevision: work.ResourceVersion, Kind: "work.cancelled", OccurredAt: now.UTC().Round(0), CorrelationID: work.WorkItemID, CommandID: key, Actor: statestore.Actor{Type: statestore.ActorUser, ID: "local-user"}, Data: data, Metadata: data}
}

func (s *Service) finishFailure(ctx context.Context, key, kind, message string, current *Plan, status int, returned error) error {
	encoded, _ := json.Marshal(commandResponse{Failure: &commandFailure{Kind: kind, Message: message, Current: current}})
	_, err := s.store.CompleteCommand(ctx, statestore.CompleteCommandRequest{Scope: commandScope, IdempotencyKey: key, ResponseStatus: status, Response: encoded, CompletedAt: s.now().UTC().Round(0)})
	if err != nil {
		return err
	}
	return returned
}
func decodeResponse(encoded json.RawMessage) (Result, error) {
	var response commandResponse
	if err := json.Unmarshal(encoded, &response); err != nil {
		return Result{}, err
	}
	if response.Result != nil {
		return *response.Result, nil
	}
	if response.Failure == nil {
		return Result{}, errors.New("empty replayed transition response")
	}
	if response.Failure.Kind == "version" && response.Failure.Current != nil {
		return Result{}, &VersionConflictError{Current: *response.Failure.Current}
	}
	return Result{}, ErrRejected
}
