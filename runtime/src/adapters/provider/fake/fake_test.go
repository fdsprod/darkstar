package fake_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/fdsprod/darkstar/runtime/src/adapters/provider/fake"
	"github.com/fdsprod/darkstar/runtime/src/ports"
	"github.com/fdsprod/darkstar/runtime/src/ports/provider"
)

func TestScenarioStreamsInteractionsAndControlledDelays(t *testing.T) {
	t.Parallel()

	start := time.Date(2026, time.August, 31, 12, 0, 0, 0, time.UTC)
	clock := fake.NewManualClock(start)
	adapter := newProvider(t, fake.Scenario{Attempts: []fake.AttemptScenario{{
		AttemptID: "attempt-scripted",
		Steps: []fake.Step{
			fake.Emit(event(1, provider.EventMessageDelta, `{"text":"working"}`)),
			fake.Emit(event(2, provider.EventToolStarted, `{"requestId":"tool-1","name":"inspect"}`)),
			fake.AwaitResponse("tool-1"),
			fake.Pause(5 * time.Minute),
			fake.Emit(event(3, provider.EventPermissionRequested, `{"requestId":"approval-1"}`)),
			fake.AwaitResponse("approval-1"),
			fake.Emit(event(4, provider.EventTurnCompleted, `{}`)),
		},
		Result: provider.SucceededResult{
			StructuredOutput: json.RawMessage(`{"summary":"done"}`),
		},
	}}}, fake.WithClock(clock))

	handle, err := adapter.StartAttempt(context.Background(), provider.AttemptRequest{
		AttemptID:      "attempt-scripted",
		IdempotencyKey: "start-1",
	})
	if err != nil {
		t.Fatalf("StartAttempt() error = %v", err)
	}
	stream, err := adapter.StreamEvents(context.Background(), provider.EventRequest{Handle: handle})
	if err != nil {
		t.Fatalf("StreamEvents() error = %v", err)
	}
	t.Cleanup(func() { _ = stream.Close() })

	first := receive(t, stream)
	second := receive(t, stream)
	if first.Kind != provider.EventMessageDelta || second.Kind != provider.EventToolStarted {
		t.Fatalf("event kinds = %q, %q, want message.delta, tool.started", first.Kind, second.Kind)
	}
	if !first.OccurredAt.Equal(start) || !second.OccurredAt.Equal(start) {
		t.Fatalf("initial event timestamps = %v, %v, want %v", first.OccurredAt, second.OccurredAt, start)
	}

	approvalEvent := receiveAsync(stream)
	assertBlocked(t, approvalEvent)
	respondAnswer(t, adapter, handle, "tool-1", "tool-response-1", json.RawMessage(`{"value":"inspected"}`))
	waitFor(t, func() bool { return clock.Pending() == 1 }, "scripted delay to become pending")
	clock.Advance(5 * time.Minute)
	third := awaitEvent(t, approvalEvent)
	if third.Kind != provider.EventPermissionRequested {
		t.Fatalf("event kind = %q, want permission.requested", third.Kind)
	}
	if want := start.Add(5 * time.Minute); !third.OccurredAt.Equal(want) {
		t.Fatalf("approval timestamp = %v, want %v", third.OccurredAt, want)
	}

	completedEvent := receiveAsync(stream)
	assertBlocked(t, completedEvent)
	respondPermission(t, adapter, handle, "approval-1", "approval-response-1", provider.PermissionAllowOnce)
	if got := awaitEvent(t, completedEvent); got.Kind != provider.EventTurnCompleted {
		t.Fatalf("event kind = %q, want turn.completed", got.Kind)
	}
	if _, err := stream.Receive(); !errors.Is(err, io.EOF) {
		t.Fatalf("Receive() terminal error = %v, want EOF", err)
	}

	calls := adapter.Calls()
	wantKinds := []fake.CallKind{fake.CallStart, fake.CallStream, fake.CallRespond, fake.CallRespond}
	if len(calls) != len(wantKinds) {
		t.Fatalf("Calls() length = %d, want %d: %#v", len(calls), len(wantKinds), calls)
	}
	for index, want := range wantKinds {
		if calls[index].Kind != want {
			t.Errorf("Calls()[%d].Kind = %q, want %q", index, calls[index].Kind, want)
		}
	}
	if calls[2].Decision != "answer" || calls[3].Decision != string(provider.PermissionAllowOnce) {
		t.Fatalf("response decisions = %q, %q, want answer, allow_once", calls[2].Decision, calls[3].Decision)
	}
}

func TestScenarioPreservesMalformedEventAndInjectsFailure(t *testing.T) {
	t.Parallel()

	adapter := newProvider(t, fake.Scenario{Attempts: []fake.AttemptScenario{{
		AttemptID: "attempt-malformed",
		Steps: []fake.Step{
			fake.Emit(event(1, provider.EventUnknownProvider, `{`)),
			fake.Fail(&ports.Failure{Code: ports.FailureProtocolDrift, Message: "malformed provider frame"}),
		},
		Result: provider.FailedResult{
			Failure: ports.Failure{Code: ports.FailureProtocolDrift, Message: "malformed provider result"},
		},
	}}})

	handle := start(t, adapter, "attempt-malformed")
	stream, err := adapter.StreamEvents(context.Background(), provider.EventRequest{Handle: handle})
	if err != nil {
		t.Fatalf("StreamEvents() error = %v", err)
	}
	got := receive(t, stream)
	if json.Valid(got.Payload) {
		t.Fatalf("event payload = %q, want deliberately malformed JSON", got.Payload)
	}
	_, err = stream.Receive()
	assertFailureCode(t, err, ports.FailureProtocolDrift)

	result, err := adapter.GetResult(context.Background(), provider.ResultRequest{Handle: handle})
	if err != nil {
		t.Fatalf("GetResult() error = %v", err)
	}
	failed, ok := result.(provider.FailedResult)
	if !ok || failed.Failure.Code != ports.FailureProtocolDrift {
		t.Fatalf("result = %#v, want classified failed result", result)
	}
}

func TestResumeStartsAfterLastDurableSequence(t *testing.T) {
	t.Parallel()

	adapter := newProvider(t, fake.Scenario{Attempts: []fake.AttemptScenario{{
		AttemptID: "attempt-resume",
		Steps: []fake.Step{
			fake.Emit(event(1, provider.EventTurnStarted, `{}`)),
			fake.Emit(event(2, provider.EventMessageDelta, `{"text":"before restart"}`)),
			fake.Pause(time.Hour),
			fake.Emit(event(3, provider.EventTurnCompleted, `{}`)),
		},
	}}})

	handle, err := adapter.ResumeAttempt(context.Background(), provider.ResumeRequest{
		AttemptID:      "attempt-resume",
		IdempotencyKey: "resume-1",
		LastSequence:   2,
	})
	if err != nil {
		t.Fatalf("ResumeAttempt() error = %v", err)
	}
	stream, err := adapter.StreamEvents(context.Background(), provider.EventRequest{Handle: handle, AfterSequence: 2})
	if err != nil {
		t.Fatalf("StreamEvents() error = %v", err)
	}
	if got := receive(t, stream); got.Sequence != 3 {
		t.Fatalf("resumed sequence = %d, want 3", got.Sequence)
	}
	if _, err := stream.Receive(); !errors.Is(err, io.EOF) {
		t.Fatalf("Receive() terminal error = %v, want EOF", err)
	}

	calls := adapter.Calls()
	if calls[0].Kind != fake.CallResume || calls[0].AfterSequence != 2 {
		t.Fatalf("first call = %#v, want resume at sequence 2", calls[0])
	}
	if calls[1].Kind != fake.CallStream || calls[1].AfterSequence != 2 {
		t.Fatalf("second call = %#v, want stream after sequence 2", calls[1])
	}
}

func TestCancellationInterruptsAControlledDelay(t *testing.T) {
	t.Parallel()

	clock := fake.NewManualClock(time.Unix(0, 0).UTC())
	adapter := newProvider(t, fake.Scenario{Attempts: []fake.AttemptScenario{{
		AttemptID: "attempt-cancel",
		Steps: []fake.Step{
			fake.Pause(24 * time.Hour),
			fake.Emit(event(1, "turn.completed", `{}`)),
		},
		CancelResult: provider.CancelResult{
			Disposition: provider.CancelForced,
			EvidenceRef: "evidence://fake/cancelled",
		},
	}}}, fake.WithClock(clock))

	handle := start(t, adapter, "attempt-cancel")
	stream, err := adapter.StreamEvents(context.Background(), provider.EventRequest{Handle: handle})
	if err != nil {
		t.Fatalf("StreamEvents() error = %v", err)
	}
	received := receiveAsync(stream)
	waitFor(t, func() bool { return clock.Pending() == 1 }, "cancellable delay to become pending")

	cancel, err := adapter.CancelAttempt(context.Background(), provider.CancelRequest{
		Handle:         handle,
		IdempotencyKey: "cancel-1",
	})
	if err != nil {
		t.Fatalf("CancelAttempt() error = %v", err)
	}
	if cancel.Disposition != provider.CancelForced {
		t.Fatalf("cancel disposition = %q, want forced", cancel.Disposition)
	}
	result := awaitReceive(t, received)
	assertFailureCode(t, result.err, ports.FailureCancelled)

	repeated, err := adapter.CancelAttempt(context.Background(), provider.CancelRequest{
		Handle:         handle,
		IdempotencyKey: "cancel-1",
	})
	if err != nil || repeated != cancel {
		t.Fatalf("repeated CancelAttempt() = %#v, %v; want %#v, nil", repeated, err, cancel)
	}
	attemptResult, err := adapter.GetResult(context.Background(), provider.ResultRequest{Handle: handle})
	if err != nil {
		t.Fatalf("GetResult() error = %v", err)
	}
	if _, ok := attemptResult.(provider.CancelledResult); !ok {
		t.Fatalf("result = %T, want provider.CancelledResult", attemptResult)
	}
}

func TestStartIsIdempotentAndRejectsConflictingKeys(t *testing.T) {
	t.Parallel()

	adapter := newProvider(t, fake.Scenario{Attempts: []fake.AttemptScenario{{AttemptID: "attempt-idempotent"}}})
	request := provider.AttemptRequest{AttemptID: "attempt-idempotent", IdempotencyKey: "start-key"}
	first, err := adapter.StartAttempt(context.Background(), request)
	if err != nil {
		t.Fatalf("first StartAttempt() error = %v", err)
	}
	second, err := adapter.StartAttempt(context.Background(), request)
	if err != nil || second != first {
		t.Fatalf("repeated StartAttempt() = %#v, %v; want %#v, nil", second, err, first)
	}
	request.IdempotencyKey = "different-key"
	_, err = adapter.StartAttempt(context.Background(), request)
	assertFailureCode(t, err, ports.FailureConflict)
}

func TestNewRejectsAmbiguousScenarios(t *testing.T) {
	t.Parallel()

	_, err := fake.New(fake.Scenario{Attempts: []fake.AttemptScenario{
		{AttemptID: "duplicate"},
		{AttemptID: "duplicate"},
	}})
	if err == nil {
		t.Fatal("New() error = nil, want duplicate-attempt validation failure")
	}

	_, err = fake.New(fake.Scenario{Attempts: []fake.AttemptScenario{{
		AttemptID: "unordered",
		Steps: []fake.Step{
			fake.Emit(event(2, provider.EventMessageDelta, `{}`)),
			fake.Emit(event(1, provider.EventMessageDelta, `{}`)),
		},
	}}})
	if err == nil {
		t.Fatal("New() error = nil, want sequence validation failure")
	}

	_, err = fake.New(fake.Scenario{Attempts: []fake.AttemptScenario{{
		AttemptID: "unclassified-result",
		Result:    provider.FailedResult{},
	}}})
	if err == nil {
		t.Fatal("New() error = nil, want classified-result validation failure")
	}
}

func newProvider(t *testing.T, scenario fake.Scenario, options ...fake.Option) *fake.Fake {
	t.Helper()
	adapter, err := fake.New(scenario, options...)
	if err != nil {
		t.Fatalf("fake.New() error = %v", err)
	}
	return adapter
}

func start(t *testing.T, adapter *fake.Fake, attemptID string) provider.AttemptHandle {
	t.Helper()
	handle, err := adapter.StartAttempt(context.Background(), provider.AttemptRequest{
		AttemptID:      attemptID,
		IdempotencyKey: "start-" + attemptID,
	})
	if err != nil {
		t.Fatalf("StartAttempt() error = %v", err)
	}
	return handle
}

func event(sequence uint64, kind provider.EventKind, payload string) provider.Event {
	return provider.Event{Sequence: sequence, Kind: kind, Payload: json.RawMessage(payload)}
}

func receive(t *testing.T, stream provider.EventStream) provider.Event {
	t.Helper()
	event, err := stream.Receive()
	if err != nil {
		t.Fatalf("Receive() error = %v", err)
	}
	return event
}

type receiveResult struct {
	event provider.Event
	err   error
}

func receiveAsync(stream provider.EventStream) <-chan receiveResult {
	result := make(chan receiveResult, 1)
	go func() {
		event, err := stream.Receive()
		result <- receiveResult{event: event, err: err}
	}()
	return result
}

func awaitReceive(t *testing.T, result <-chan receiveResult) receiveResult {
	t.Helper()
	select {
	case got := <-result:
		return got
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for scripted receive")
		return receiveResult{}
	}
}

func awaitEvent(t *testing.T, result <-chan receiveResult) provider.Event {
	t.Helper()
	got := awaitReceive(t, result)
	if got.err != nil {
		t.Fatalf("Receive() error = %v", got.err)
	}
	return got.event
}

func assertBlocked(t *testing.T, result <-chan receiveResult) {
	t.Helper()
	select {
	case got := <-result:
		t.Fatalf("Receive() completed early with %#v", got)
	default:
	}
}

func waitFor(t *testing.T, condition func() bool, description string) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for !condition() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", description)
		}
		time.Sleep(time.Millisecond)
	}
}

func interactionContext(handle provider.AttemptHandle, requestID, key string) provider.InteractionContext {
	return provider.InteractionContext{
		AttemptID:         handle.AttemptID,
		ProviderThreadID:  handle.ProviderThreadID,
		ProviderRequestID: requestID,
		IdempotencyKey:    key,
	}
}

func respondAnswer(t *testing.T, adapter *fake.Fake, handle provider.AttemptHandle, requestID, key string, answer json.RawMessage) {
	t.Helper()
	_, err := adapter.Respond(context.Background(), provider.AnswerResponse{
		InteractionContext: interactionContext(handle, requestID, key),
		Answer:             answer,
	})
	if err != nil {
		t.Fatalf("Respond() error = %v", err)
	}
}

func respondPermission(t *testing.T, adapter *fake.Fake, handle provider.AttemptHandle, requestID, key string, decision provider.PermissionDecision) {
	t.Helper()
	_, err := adapter.Respond(context.Background(), provider.PermissionResponse{
		InteractionContext: interactionContext(handle, requestID, key),
		Decision:           decision,
	})
	if err != nil {
		t.Fatalf("Respond() error = %v", err)
	}
}

func assertFailureCode(t *testing.T, err error, want ports.FailureCode) {
	t.Helper()
	var failure *ports.Failure
	if !errors.As(err, &failure) {
		t.Fatalf("error = %T %v, want *ports.Failure", err, err)
	}
	if failure.Code != want {
		t.Fatalf("failure code = %q, want %q", failure.Code, want)
	}
}
