package fake

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/fdsprod/darkstar/runtime/src/ports"
	"github.com/fdsprod/darkstar/runtime/src/ports/provider"
)

// CallKind identifies an observed port operation.
type CallKind string

const (
	CallProbeHealth  CallKind = "probe_health"
	CallCapabilities CallKind = "capabilities"
	CallStart        CallKind = "start"
	CallResume       CallKind = "resume"
	CallStream       CallKind = "stream"
	CallRespond      CallKind = "respond"
	CallCancel       CallKind = "cancel"
	CallGetResult    CallKind = "get_result"
)

// Call is an immutable assertion-friendly record of a provider operation.
type Call struct {
	Kind              CallKind
	AttemptID         string
	IdempotencyKey    string
	AfterSequence     uint64
	ProviderRequestID string
	Decision          provider.InteractionDecision
}

// Option configures a Fake.
type Option func(*Fake)

// WithClock supplies the clock used for event timestamps and delay steps.
func WithClock(clock Clock) Option {
	return func(fake *Fake) {
		if clock != nil {
			fake.clock = clock
		}
	}
}

// Fake implements the provider port with a replayable in-memory Scenario.
type Fake struct {
	mu       sync.Mutex
	scenario Scenario
	clock    Clock
	attempts map[string]*attemptState
	calls    []Call
}

type attemptState struct {
	scenario        AttemptScenario
	started         bool
	startKey        string
	responses       map[string]responseRecord
	responseChanged chan struct{}
	cancelled       bool
	cancelKey       string
	cancelResult    provider.CancelResult
	cancelledCh     chan struct{}
	done            bool
	lastSequence    uint64
}

type responseRecord struct {
	key     string
	receipt provider.InteractionReceipt
}

var _ provider.Provider = (*Fake)(nil)

// New validates and constructs a fake provider. Its default clock starts at the
// Unix epoch and advances scripted delays without waiting in real time.
func New(scenario Scenario, options ...Option) (*Fake, error) {
	normalized, err := normalizeScenario(scenario)
	if err != nil {
		return nil, err
	}
	fake := &Fake{
		scenario: normalized,
		clock:    NewLogicalClock(time.Unix(0, 0).UTC()),
		attempts: make(map[string]*attemptState, len(normalized.Attempts)),
	}
	for _, option := range options {
		option(fake)
	}
	for _, attempt := range normalized.Attempts {
		fake.attempts[attempt.AttemptID] = &attemptState{
			scenario:        attempt,
			responses:       make(map[string]responseRecord),
			responseChanged: make(chan struct{}),
			cancelledCh:     make(chan struct{}),
		}
	}
	return fake, nil
}

func (fake *Fake) ProbeHealth(ctx context.Context) (provider.Health, error) {
	if err := contextFailure(ctx); err != nil {
		return provider.Health{}, err
	}
	fake.record(Call{Kind: CallProbeHealth})
	return fake.scenario.Health, nil
}

func (fake *Fake) Capabilities(ctx context.Context) (provider.CapabilityManifest, error) {
	if err := contextFailure(ctx); err != nil {
		return provider.CapabilityManifest{}, err
	}
	fake.record(Call{Kind: CallCapabilities})
	return fake.scenario.Capabilities, nil
}

func (fake *Fake) StartAttempt(ctx context.Context, request provider.AttemptRequest) (provider.AttemptHandle, error) {
	if err := contextFailure(ctx); err != nil {
		return provider.AttemptHandle{}, err
	}
	fake.mu.Lock()
	defer fake.mu.Unlock()
	fake.calls = append(fake.calls, Call{Kind: CallStart, AttemptID: request.AttemptID, IdempotencyKey: request.IdempotencyKey})
	state, ok := fake.attempts[request.AttemptID]
	if !ok {
		return provider.AttemptHandle{}, failure(ports.FailureNotFound, "attempt scenario not found", false)
	}
	if state.started {
		if request.IdempotencyKey == state.startKey {
			return state.scenario.Handle, nil
		}
		return provider.AttemptHandle{}, failure(ports.FailureConflict, "attempt already started with another idempotency key", false)
	}
	if state.scenario.StartFailure != nil {
		return provider.AttemptHandle{}, cloneFailure(state.scenario.StartFailure)
	}
	state.started = true
	state.startKey = request.IdempotencyKey
	return state.scenario.Handle, nil
}

func (fake *Fake) ResumeAttempt(ctx context.Context, request provider.ResumeRequest) (provider.AttemptHandle, error) {
	if err := contextFailure(ctx); err != nil {
		return provider.AttemptHandle{}, err
	}
	fake.mu.Lock()
	defer fake.mu.Unlock()
	fake.calls = append(fake.calls, Call{Kind: CallResume, AttemptID: request.AttemptID, IdempotencyKey: request.IdempotencyKey, AfterSequence: request.LastSequence})
	state, ok := fake.attempts[request.AttemptID]
	if !ok {
		return provider.AttemptHandle{}, failure(ports.FailureNotFound, "attempt scenario not found", false)
	}
	if state.scenario.ResumeFailure != nil {
		return provider.AttemptHandle{}, cloneFailure(state.scenario.ResumeFailure)
	}
	if request.LastSequence > maxSequence(state.scenario.Steps) {
		return provider.AttemptHandle{}, failure(ports.FailureInvalidRequest, "resume sequence exceeds scenario", false)
	}
	state.started = true
	return state.scenario.Handle, nil
}

func (fake *Fake) StreamEvents(ctx context.Context, request provider.EventRequest) (provider.EventStream, error) {
	if err := contextFailure(ctx); err != nil {
		return nil, err
	}
	fake.mu.Lock()
	defer fake.mu.Unlock()
	fake.calls = append(fake.calls, Call{Kind: CallStream, AttemptID: request.Handle.AttemptID, AfterSequence: request.AfterSequence})
	state, ok := fake.attempts[request.Handle.AttemptID]
	if !ok {
		return nil, failure(ports.FailureNotFound, "attempt scenario not found", false)
	}
	if request.Handle != state.scenario.Handle {
		return nil, failure(ports.FailureInvalidRequest, "attempt handle does not match scenario", false)
	}
	if request.AfterSequence > maxSequence(state.scenario.Steps) {
		return nil, failure(ports.FailureInvalidRequest, "stream sequence exceeds scenario", false)
	}
	return &eventStream{
		ctx:      ctx,
		fake:     fake,
		state:    state,
		next:     stepAfter(state.scenario.Steps, request.AfterSequence),
		closedCh: make(chan struct{}),
	}, nil
}

func (fake *Fake) Respond(ctx context.Context, response provider.InteractionResponse) (provider.InteractionReceipt, error) {
	if err := contextFailure(ctx); err != nil {
		return provider.InteractionReceipt{}, err
	}
	fake.mu.Lock()
	defer fake.mu.Unlock()
	fake.calls = append(fake.calls, Call{
		Kind:              CallRespond,
		AttemptID:         response.AttemptID,
		IdempotencyKey:    response.IdempotencyKey,
		ProviderRequestID: response.ProviderRequestID,
		Decision:          response.Decision,
	})
	state, ok := fake.attempts[response.AttemptID]
	if !ok {
		return provider.InteractionReceipt{}, failure(ports.FailureNotFound, "attempt scenario not found", false)
	}
	if !awaitsRequest(state.scenario.Steps, response.ProviderRequestID) {
		return provider.InteractionReceipt{}, failure(ports.FailureNotFound, "provider request is not scripted", false)
	}
	if existing, ok := state.responses[response.ProviderRequestID]; ok {
		if existing.key == response.IdempotencyKey {
			return existing.receipt, nil
		}
		return provider.InteractionReceipt{}, failure(ports.FailureConflict, "provider request already answered", false)
	}
	receipt := provider.InteractionReceipt{
		ProviderRequestID: response.ProviderRequestID,
		Recorded:          true,
		RecordedAt:        fake.clock.Now(),
	}
	state.responses[response.ProviderRequestID] = responseRecord{key: response.IdempotencyKey, receipt: receipt}
	close(state.responseChanged)
	state.responseChanged = make(chan struct{})
	return receipt, nil
}

func (fake *Fake) CancelAttempt(ctx context.Context, request provider.CancelRequest) (provider.CancelResult, error) {
	if err := contextFailure(ctx); err != nil {
		return provider.CancelResult{}, err
	}
	fake.mu.Lock()
	defer fake.mu.Unlock()
	fake.calls = append(fake.calls, Call{Kind: CallCancel, AttemptID: request.Handle.AttemptID, IdempotencyKey: request.IdempotencyKey})
	state, ok := fake.attempts[request.Handle.AttemptID]
	if !ok {
		return provider.CancelResult{}, failure(ports.FailureNotFound, "attempt scenario not found", false)
	}
	if state.cancelled {
		return state.cancelResult, nil
	}
	if state.done {
		return provider.CancelResult{Disposition: provider.CancelAlreadyDone, EvidenceRef: state.scenario.CancelResult.EvidenceRef}, nil
	}
	state.cancelled = true
	state.cancelKey = request.IdempotencyKey
	state.cancelResult = state.scenario.CancelResult
	close(state.cancelledCh)
	return state.cancelResult, nil
}

func (fake *Fake) GetResult(ctx context.Context, request provider.ResultRequest) (provider.AttemptResult, error) {
	if err := contextFailure(ctx); err != nil {
		return provider.AttemptResult{}, err
	}
	fake.mu.Lock()
	defer fake.mu.Unlock()
	fake.calls = append(fake.calls, Call{Kind: CallGetResult, AttemptID: request.Handle.AttemptID})
	state, ok := fake.attempts[request.Handle.AttemptID]
	if !ok {
		return provider.AttemptResult{}, failure(ports.FailureNotFound, "attempt scenario not found", false)
	}
	result := cloneResult(state.scenario.Result)
	if state.cancelled {
		result.Status = provider.AttemptCancelled
	}
	if state.lastSequence > result.Recovery.LastSequence {
		result.Recovery.LastSequence = state.lastSequence
	}
	return result, nil
}

// Calls returns a stable snapshot of provider operations in invocation order.
func (fake *Fake) Calls() []Call {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	return append([]Call(nil), fake.calls...)
}

func (fake *Fake) record(call Call) {
	fake.mu.Lock()
	fake.calls = append(fake.calls, call)
	fake.mu.Unlock()
}

type eventStream struct {
	ctx       context.Context
	fake      *Fake
	state     *attemptState
	next      int
	closeOnce sync.Once
	closedCh  chan struct{}
}

func (stream *eventStream) Receive() (provider.Event, error) {
	for {
		select {
		case <-stream.closedCh:
			return provider.Event{}, io.ErrClosedPipe
		case <-stream.state.cancelledCh:
			return provider.Event{}, failure(ports.FailureCancelled, "attempt cancelled", false)
		case <-stream.ctx.Done():
			return provider.Event{}, contextFailure(stream.ctx)
		default:
		}

		if stream.next >= len(stream.state.scenario.Steps) {
			stream.fake.mu.Lock()
			stream.state.done = true
			stream.fake.mu.Unlock()
			return provider.Event{}, io.EOF
		}

		step := stream.state.scenario.Steps[stream.next]
		stream.next++
		switch step.Kind {
		case StepEvent:
			event := cloneEvent(step.Event)
			if event.OccurredAt.IsZero() {
				event.OccurredAt = stream.fake.clock.Now()
			}
			stream.fake.mu.Lock()
			if event.Sequence > stream.state.lastSequence {
				stream.state.lastSequence = event.Sequence
			}
			stream.fake.mu.Unlock()
			return event, nil
		case StepDelay:
			select {
			case <-stream.fake.clock.After(step.Delay):
			case <-stream.closedCh:
				return provider.Event{}, io.ErrClosedPipe
			case <-stream.state.cancelledCh:
				return provider.Event{}, failure(ports.FailureCancelled, "attempt cancelled", false)
			case <-stream.ctx.Done():
				return provider.Event{}, contextFailure(stream.ctx)
			}
		case StepAwaitResponse:
			for {
				stream.fake.mu.Lock()
				_, answered := stream.state.responses[step.RequestID]
				changed := stream.state.responseChanged
				stream.fake.mu.Unlock()
				if answered {
					break
				}
				select {
				case <-changed:
				case <-stream.closedCh:
					return provider.Event{}, io.ErrClosedPipe
				case <-stream.state.cancelledCh:
					return provider.Event{}, failure(ports.FailureCancelled, "attempt cancelled", false)
				case <-stream.ctx.Done():
					return provider.Event{}, contextFailure(stream.ctx)
				}
			}
		case StepFailure:
			return provider.Event{}, cloneFailure(step.Failure)
		default:
			return provider.Event{}, failure(ports.FailureInternal, fmt.Sprintf("unsupported scenario step %q", step.Kind), false)
		}
	}
}

func (stream *eventStream) Close() error {
	stream.closeOnce.Do(func() { close(stream.closedCh) })
	return nil
}

func stepAfter(steps []Step, after uint64) int {
	next := 0
	for index, step := range steps {
		if step.Kind == StepEvent && step.Event.Sequence <= after {
			next = index + 1
		}
	}
	return next
}

func maxSequence(steps []Step) uint64 {
	var maximum uint64
	for _, step := range steps {
		if step.Kind == StepEvent && step.Event.Sequence > maximum {
			maximum = step.Event.Sequence
		}
	}
	return maximum
}

func awaitsRequest(steps []Step, requestID string) bool {
	for _, step := range steps {
		if step.Kind == StepAwaitResponse && step.RequestID == requestID {
			return true
		}
	}
	return false
}

func contextFailure(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		if err == context.DeadlineExceeded {
			return failure(ports.FailureTimeout, "provider operation deadline exceeded", true)
		}
		return failure(ports.FailureCancelled, "provider operation cancelled", false)
	}
	return nil
}

func failure(code ports.FailureCode, message string, retryable bool) *ports.Failure {
	return &ports.Failure{Code: code, Message: message, Retryable: retryable}
}

func cloneFailure(source *ports.Failure) *ports.Failure {
	if source == nil {
		return nil
	}
	clone := *source
	if source.Details != nil {
		clone.Details = make(map[string]string, len(source.Details))
		for key, value := range source.Details {
			clone.Details[key] = value
		}
	}
	return &clone
}

func cloneEvent(source provider.Event) provider.Event {
	clone := source
	clone.Payload = append(json.RawMessage(nil), source.Payload...)
	return clone
}

func cloneResult(source provider.AttemptResult) provider.AttemptResult {
	clone := source
	clone.StructuredOutput = append(json.RawMessage(nil), source.StructuredOutput...)
	clone.ValidationFailure = cloneFailure(source.ValidationFailure)
	clone.WorkspaceEvidence = append([]provider.Evidence(nil), source.WorkspaceEvidence...)
	return clone
}
