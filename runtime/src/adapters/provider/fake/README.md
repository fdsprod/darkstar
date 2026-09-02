# Deterministic fake provider

The `fake` adapter implements the complete `ports/provider.Provider` lifecycle
without an executable, network connection, random source, or wall-clock sleep.
Tests compose an in-memory `Scenario` from four replayable step types:

| Step | Behavior |
|---|---|
| `Emit` | Returns one ordered provider event. Payload bytes are preserved exactly, including malformed JSON. |
| `Pause` | Advances the default logical clock, or blocks on a caller-controlled `ManualClock`. |
| `AwaitResponse` | Blocks until `Respond` records the named tool, permission, approval, or user-input response. |
| `Fail` | Returns a cloned, classified `ports.Failure` at that point in the stream. |

Each attempt also scripts its start/resume failures, terminal result, and cancel
disposition. `ResumeAttempt` and `StreamEvents.AfterSequence` replay only the
undelivered suffix. `CancelAttempt` interrupts a blocked delay or response gate,
and `Calls` exposes an ordered snapshot for assertions about idempotency and
adapter usage.

The default `LogicalClock` starts at the Unix epoch. Event timestamps omitted by
the scenario use its current time, and delays advance it synchronously. Use
`ManualClock` when a test must prove that work remains blocked until time is
advanced or cancellation arrives.

```go
clock := fake.NewManualClock(time.Unix(0, 0).UTC())
adapter, err := fake.New(fake.Scenario{Attempts: []fake.AttemptScenario{{
    AttemptID: "attempt-1",
    Steps: []fake.Step{
        fake.Emit(provider.Event{Sequence: 1, Kind: provider.EventToolStarted, Payload: json.RawMessage(`{}`)}),
        fake.AwaitResponse("tool-request-1"),
        fake.Pause(time.Minute),
        fake.Emit(provider.Event{Sequence: 2, Kind: provider.EventTurnCompleted, Payload: json.RawMessage(`{}`)}),
    },
}}}, fake.WithClock(clock))
```

Scenario validation rejects duplicate attempts, unordered event sequences,
negative delays, duplicate response gates, and unclassified failure steps.
Provider event payloads and structured results are deliberately opaque so tests
can cover malformed and forward-compatible provider output.
