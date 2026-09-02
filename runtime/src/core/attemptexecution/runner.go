// Package attemptexecution coordinates one durable node-attempt invocation.
package attemptexecution

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"darkstar/src/ports/executor"
)

var (
	ErrInvalidRequest   = errors.New("invalid attempt execution request")
	ErrInvalidReference = errors.New("executor returned an invalid reference")
	ErrInvalidEvent     = errors.New("executor returned an invalid event")
	ErrInvalidResult    = errors.New("executor returned an invalid result")
)

// ResourcePlan is a closed choice between an isolated read-only workspace and
// a fenced repository writer. A request cannot accidentally combine both.
type ResourcePlan interface {
	isResourcePlan()
}

// ReadOnlyResources requests an immutable or isolated workspace.
type ReadOnlyResources struct {
	WorkspaceID string
}

func (ReadOnlyResources) isResourcePlan() {}

// RepositoryWriteResources requests exclusive write ownership of one
// repository and its attached worktree.
type RepositoryWriteResources struct {
	RepositoryID string
	WorktreeID   string
}

func (RepositoryWriteResources) isResourcePlan() {}

// Invocation is a closed choice between a new execution and recovery of an
// already-started execution.
type Invocation interface {
	isInvocation()
}

// Start begins a new executor invocation.
type Start struct {
	Timeout time.Duration
}

func (Start) isInvocation() {}

// Resume recovers the exact executor invocation and event cursor recorded by a
// prior runner owner.
type Resume struct {
	RecoveryRef     string
	LastEventCursor string
}

func (Resume) isInvocation() {}

// Request identifies one attempt and its immutable execution choices.
type Request struct {
	AttemptID    string
	RunID        string
	VisitID      string
	NodeID       string
	ExecutorKind string
	Resources    ResourcePlan
	Invocation   Invocation
}

// Allocation is the acquired, fenced workspace capability used by one
// attempt. Evidence is committed with the accepted result.
type Allocation struct {
	ID              string
	Workspace       string
	WorkspaceDigest string
	Evidence        []executor.Evidence
}

// ReleaseOutcome states why acquired resources are being relinquished.
type ReleaseOutcome string

const (
	ReleaseCommitted   ReleaseOutcome = "committed"
	ReleaseFailed      ReleaseOutcome = "failed"
	ReleaseInterrupted ReleaseOutcome = "interrupted"
)

// ResourceManager acquires and releases all resources as one attempt-owned
// capability. Implementations fence every mutating operation with Allocation.
type ResourceManager interface {
	Acquire(context.Context, string, ResourcePlan) (Allocation, error)
	Release(context.Context, Allocation, ReleaseOutcome) error
}

// ContextSnapshot is the one frozen authority for executor input and policy.
type ContextSnapshot struct {
	Digest       string
	Inputs       json.RawMessage
	PolicyDigest string
}

// ContextBuilder selects and freezes the context for an acquired workspace.
type ContextBuilder interface {
	Build(context.Context, Request, Allocation) (ContextSnapshot, error)
}

// ExecutorResolver selects the configured executor without leaking adapter
// mechanics into the runner.
type ExecutorResolver interface {
	Resolve(context.Context, string) (executor.Executor, error)
}

// ValidationRequest contains the candidate and the exact context that produced
// it. Validators must not read floating run inputs.
type ValidationRequest struct {
	AttemptID string
	Context   ContextSnapshot
	Workspace string
	Result    executor.Result
}

// ValidationResult carries durable evidence produced by deterministic
// validators. Successful validation evidence is committed with the candidate.
type ValidationResult struct {
	Evidence []executor.Evidence
}

// OutputValidator accepts or rejects candidate output before any success state
// is committed.
type OutputValidator interface {
	Validate(context.Context, ValidationRequest) (ValidationResult, error)
}

// StartedRecord is durable recovery evidence for an active executor.
type StartedRecord struct {
	AttemptID string
	Executor  string
	Context   ContextSnapshot
	Resources Allocation
	Reference executor.Reference
}

// EventRecord is one normalized, ordered executor observation.
type EventRecord struct {
	AttemptID string
	Executor  string
	Event     executor.Event
}

// FailureStage precisely locates the boundary that rejected an attempt.
type FailureStage string

const (
	FailureResources  FailureStage = "resources"
	FailureContext    FailureStage = "context"
	FailureExecutor   FailureStage = "executor"
	FailureEvents     FailureStage = "events"
	FailureResult     FailureStage = "result"
	FailureValidation FailureStage = "validation"
	FailureCommit     FailureStage = "commit"
)

// FailureRecord is the durable terminal observation for a rejected attempt.
type FailureRecord struct {
	AttemptID string
	Stage     FailureStage
	Cause     error
}

// InterruptedRecord preserves the resumable distinction between lost runner
// ownership/cancellation and an executor or validation failure.
type InterruptedRecord struct {
	AttemptID string
	Stage     FailureStage
	Cause     error
}

// Commit records accepted output, artifacts, evidence, and terminal attempt
// state in one transaction. Implementations must be idempotent by AttemptID.
type Commit struct {
	AttemptID       string
	Executor        string
	Context         ContextSnapshot
	Resources       Allocation
	Reference       executor.Reference
	LastEventCursor string
	Result          executor.Result
}

// Journal is the durable state boundary. Commit is the sole success boundary:
// result data and successful state must be written atomically.
type Journal interface {
	ResourcesAcquired(context.Context, string, Allocation) error
	Started(context.Context, StartedRecord) error
	Event(context.Context, EventRecord) error
	Commit(context.Context, Commit) error
	Failed(context.Context, FailureRecord) error
	Interrupted(context.Context, InterruptedRecord) error
}

// Runner owns the ordered node-attempt lifecycle.
type Runner struct {
	resources ResourceManager
	contexts  ContextBuilder
	executors ExecutorResolver
	validator OutputValidator
	journal   Journal
}

// New constructs a runner with every required lifecycle boundary.
func New(resources ResourceManager, contexts ContextBuilder, executors ExecutorResolver, validator OutputValidator, journal Journal) (*Runner, error) {
	if resources == nil || contexts == nil || executors == nil || validator == nil || journal == nil {
		return nil, errors.New("attempt runner requires resources, context, executors, validation, and journal")
	}
	return &Runner{resources: resources, contexts: contexts, executors: executors, validator: validator, journal: journal}, nil
}

// Run acquires resources, freezes context, invokes the executor, streams durable
// events, validates the candidate, and atomically commits accepted state.
func (runner *Runner) Run(ctx context.Context, request Request) (commit Commit, err error) {
	if err := validateRequest(request); err != nil {
		return Commit{}, err
	}

	allocation, err := runner.resources.Acquire(ctx, request.AttemptID, request.Resources)
	if err != nil {
		return Commit{}, runner.recordFailure(ctx, request.AttemptID, FailureResources, err)
	}
	if err := validateAllocation(allocation); err != nil {
		releaseErr := runner.resources.Release(context.WithoutCancel(ctx), allocation, ReleaseFailed)
		return Commit{}, errors.Join(runner.recordFailure(ctx, request.AttemptID, FailureResources, err), releaseErr)
	}
	releaseOutcome := ReleaseFailed
	defer func() {
		if releaseOutcome != ReleaseCommitted && (ctx.Err() != nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)) {
			releaseOutcome = ReleaseInterrupted
		}
		releaseErr := runner.resources.Release(context.WithoutCancel(ctx), allocation, releaseOutcome)
		if releaseErr != nil {
			err = errors.Join(err, fmt.Errorf("release attempt resources: %w", releaseErr))
		}
	}()

	if err := runner.journal.ResourcesAcquired(ctx, request.AttemptID, cloneAllocation(allocation)); err != nil {
		return Commit{}, runner.recordFailure(ctx, request.AttemptID, FailureResources, err)
	}
	snapshot, err := runner.contexts.Build(ctx, request, allocation)
	if err != nil {
		return Commit{}, runner.recordFailure(ctx, request.AttemptID, FailureContext, err)
	}
	if err := validateContext(snapshot); err != nil {
		return Commit{}, runner.recordFailure(ctx, request.AttemptID, FailureContext, err)
	}

	resolved, err := runner.executors.Resolve(ctx, request.ExecutorKind)
	if err != nil {
		return Commit{}, runner.recordFailure(ctx, request.AttemptID, FailureExecutor, err)
	}
	if resolved == nil || resolved.Kind() != request.ExecutorKind {
		return Commit{}, runner.recordFailure(ctx, request.AttemptID, FailureExecutor, errors.New("executor kind does not match request"))
	}
	execution, err := invoke(ctx, resolved, request, allocation, snapshot)
	if err != nil {
		return Commit{}, runner.recordFailure(ctx, request.AttemptID, FailureExecutor, err)
	}
	if execution == nil {
		return Commit{}, runner.recordFailure(ctx, request.AttemptID, FailureExecutor, errors.New("executor returned no execution"))
	}
	reference := execution.Reference()
	if err := validateReference(request.AttemptID, reference); err != nil {
		return Commit{}, runner.recordFailure(ctx, request.AttemptID, FailureExecutor, err)
	}
	if err := runner.journal.Started(ctx, StartedRecord{
		AttemptID: request.AttemptID, Executor: request.ExecutorKind, Context: cloneContext(snapshot), Resources: cloneAllocation(allocation), Reference: reference,
	}); err != nil {
		return Commit{}, runner.recordFailure(ctx, request.AttemptID, FailureExecutor, err)
	}

	lastCursor, err := runner.stream(ctx, request, execution)
	if err != nil {
		return Commit{}, runner.recordFailure(ctx, request.AttemptID, FailureEvents, err)
	}
	result, err := execution.Wait(ctx)
	if err != nil {
		return Commit{}, runner.recordFailure(ctx, request.AttemptID, FailureResult, err)
	}
	if err := validateResult(result); err != nil {
		return Commit{}, runner.recordFailure(ctx, request.AttemptID, FailureResult, err)
	}
	validation, err := runner.validator.Validate(ctx, ValidationRequest{
		AttemptID: request.AttemptID, Context: snapshot, Workspace: allocation.Workspace, Result: result,
	})
	if err != nil {
		return Commit{}, runner.recordFailure(ctx, request.AttemptID, FailureValidation, err)
	}
	if err := validateEvidence(validation.Evidence); err != nil {
		return Commit{}, runner.recordFailure(ctx, request.AttemptID, FailureValidation, err)
	}
	result.Evidence = append(result.Evidence, validation.Evidence...)

	commit = Commit{
		AttemptID: request.AttemptID, Executor: request.ExecutorKind, Context: cloneContext(snapshot), Resources: cloneAllocation(allocation),
		Reference: reference, LastEventCursor: lastCursor, Result: cloneResult(result),
	}
	if err := runner.journal.Commit(ctx, commit); err != nil {
		return Commit{}, runner.recordFailure(ctx, request.AttemptID, FailureCommit, err)
	}
	releaseOutcome = ReleaseCommitted
	return commit, nil
}

func (runner *Runner) stream(ctx context.Context, request Request, execution executor.Execution) (string, error) {
	stream := execution.Events()
	if stream == nil {
		return "", fmt.Errorf("%w: executor returned no event stream", ErrInvalidEvent)
	}
	var last string
	seen := map[string]struct{}{}
	for {
		event, err := stream.Receive()
		if errors.Is(err, io.EOF) {
			if closeErr := stream.Close(); closeErr != nil {
				return last, fmt.Errorf("close executor events: %w", closeErr)
			}
			return last, nil
		}
		if err != nil {
			_ = stream.Close()
			return last, fmt.Errorf("receive executor event: %w", err)
		}
		if err := validateEvent(event); err != nil {
			_ = stream.Close()
			return last, err
		}
		if _, duplicate := seen[event.Cursor]; duplicate {
			_ = stream.Close()
			return last, fmt.Errorf("%w: duplicate cursor %q", ErrInvalidEvent, event.Cursor)
		}
		seen[event.Cursor] = struct{}{}
		if err := runner.journal.Event(ctx, EventRecord{AttemptID: request.AttemptID, Executor: request.ExecutorKind, Event: cloneEvent(event)}); err != nil {
			_ = stream.Close()
			return last, fmt.Errorf("record executor event: %w", err)
		}
		last = event.Cursor
	}
}

func (runner *Runner) recordFailure(ctx context.Context, attemptID string, stage FailureStage, cause error) error {
	journalCtx := context.WithoutCancel(ctx)
	if ctx.Err() != nil || errors.Is(cause, context.Canceled) || errors.Is(cause, context.DeadlineExceeded) {
		recordErr := runner.journal.Interrupted(journalCtx, InterruptedRecord{AttemptID: attemptID, Stage: stage, Cause: cause})
		if recordErr != nil {
			return errors.Join(cause, fmt.Errorf("record attempt interruption: %w", recordErr))
		}
		return cause
	}
	recordErr := runner.journal.Failed(journalCtx, FailureRecord{AttemptID: attemptID, Stage: stage, Cause: cause})
	if recordErr != nil {
		return errors.Join(cause, fmt.Errorf("record attempt failure: %w", recordErr))
	}
	return cause
}

func invoke(ctx context.Context, resolved executor.Executor, request Request, allocation Allocation, snapshot ContextSnapshot) (executor.Execution, error) {
	switch invocation := request.Invocation.(type) {
	case Start:
		return resolved.Start(ctx, executor.Request{
			AttemptID: request.AttemptID, RunID: request.RunID, VisitID: request.VisitID, NodeID: request.NodeID,
			IdempotencyKey: "attempt-start:" + request.AttemptID, InputDigest: snapshot.Digest,
			Inputs: cloneJSON(snapshot.Inputs), Workspace: allocation.Workspace, Timeout: invocation.Timeout, PolicyDigest: snapshot.PolicyDigest,
		})
	case Resume:
		return resolved.Resume(ctx, executor.ResumeRequest{
			AttemptID: request.AttemptID, InputDigest: snapshot.Digest, RecoveryRef: invocation.RecoveryRef, LastEventCursor: invocation.LastEventCursor,
		})
	default:
		return nil, fmt.Errorf("%w: invocation is unsupported", ErrInvalidRequest)
	}
}

func validateRequest(request Request) error {
	for _, field := range []struct{ name, value string }{
		{"attempt ID", request.AttemptID}, {"run ID", request.RunID}, {"visit ID", request.VisitID},
		{"node ID", request.NodeID}, {"executor kind", request.ExecutorKind},
	} {
		if strings.TrimSpace(field.value) == "" || field.value != strings.TrimSpace(field.value) {
			return fmt.Errorf("%w: %s is required and must be trimmed", ErrInvalidRequest, field.name)
		}
	}
	switch resources := request.Resources.(type) {
	case ReadOnlyResources:
		if strings.TrimSpace(resources.WorkspaceID) == "" {
			return fmt.Errorf("%w: read-only workspace ID is required", ErrInvalidRequest)
		}
	case RepositoryWriteResources:
		if strings.TrimSpace(resources.RepositoryID) == "" || strings.TrimSpace(resources.WorktreeID) == "" {
			return fmt.Errorf("%w: repository and worktree IDs are required", ErrInvalidRequest)
		}
	default:
		return fmt.Errorf("%w: resource plan is unsupported", ErrInvalidRequest)
	}
	switch invocation := request.Invocation.(type) {
	case Start:
		if invocation.Timeout <= 0 {
			return fmt.Errorf("%w: start timeout must be positive", ErrInvalidRequest)
		}
	case Resume:
		if strings.TrimSpace(invocation.RecoveryRef) == "" {
			return fmt.Errorf("%w: resume recovery reference is required", ErrInvalidRequest)
		}
	default:
		return fmt.Errorf("%w: invocation is unsupported", ErrInvalidRequest)
	}
	return nil
}

func validateAllocation(allocation Allocation) error {
	if strings.TrimSpace(allocation.ID) == "" || strings.TrimSpace(allocation.Workspace) == "" || strings.TrimSpace(allocation.WorkspaceDigest) == "" {
		return fmt.Errorf("%w: resource allocation identity, workspace, and digest are required", ErrInvalidRequest)
	}
	return nil
}

func validateContext(snapshot ContextSnapshot) error {
	if strings.TrimSpace(snapshot.Digest) == "" || strings.TrimSpace(snapshot.PolicyDigest) == "" || !json.Valid(snapshot.Inputs) {
		return fmt.Errorf("%w: context digest, policy digest, and valid JSON inputs are required", ErrInvalidRequest)
	}
	return nil
}

func validateReference(attemptID string, reference executor.Reference) error {
	if reference.AttemptID != attemptID || strings.TrimSpace(reference.ExternalID) == "" || strings.TrimSpace(reference.RecoveryRef) == "" {
		return fmt.Errorf("%w for attempt %s", ErrInvalidReference, attemptID)
	}
	return nil
}

func validateEvent(event executor.Event) error {
	if strings.TrimSpace(event.Cursor) == "" || strings.TrimSpace(event.Kind) == "" || event.OccurredAt.IsZero() || !json.Valid(event.Data) {
		return fmt.Errorf("%w at cursor %q", ErrInvalidEvent, event.Cursor)
	}
	return nil
}

func validateResult(result executor.Result) error {
	if len(result.CandidateOutput) == 0 || !json.Valid(result.CandidateOutput) || strings.TrimSpace(result.RecoveryRef) == "" {
		return fmt.Errorf("%w: candidate output and recovery reference are required", ErrInvalidResult)
	}
	return nil
}

func validateEvidence(values []executor.Evidence) error {
	for index, value := range values {
		if strings.TrimSpace(value.Kind) == "" || strings.TrimSpace(value.Ref) == "" || strings.TrimSpace(value.Digest) == "" {
			return fmt.Errorf("%w: validation evidence %d requires kind, ref, and digest", ErrInvalidResult, index+1)
		}
	}
	return nil
}

func cloneEvent(event executor.Event) executor.Event {
	event.Data = cloneJSON(event.Data)
	return event
}

func cloneResult(result executor.Result) executor.Result {
	result.CandidateOutput = cloneJSON(result.CandidateOutput)
	result.Artifacts = append([]executor.Artifact(nil), result.Artifacts...)
	result.Evidence = append([]executor.Evidence(nil), result.Evidence...)
	return result
}

func cloneContext(snapshot ContextSnapshot) ContextSnapshot {
	snapshot.Inputs = cloneJSON(snapshot.Inputs)
	return snapshot
}

func cloneAllocation(allocation Allocation) Allocation {
	allocation.Evidence = append([]executor.Evidence(nil), allocation.Evidence...)
	return allocation
}

func cloneJSON(value json.RawMessage) json.RawMessage {
	return append(json.RawMessage(nil), value...)
}
