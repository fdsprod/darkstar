package cli

import (
	"encoding/json"
	"time"

	"github.com/fdsprod/darkstar/runtime/src/adapters/provider/fake"
	"github.com/fdsprod/darkstar/runtime/src/core/runexecution"
	"github.com/fdsprod/darkstar/runtime/src/ports/provider"
)

func newFakeRunProvider(scenarioName, attemptID string, resume bool) (provider.Provider, error) {
	steps := []fake.Step{
		fake.Emit(provider.Event{Sequence: 1, Kind: "turn.started", Payload: json.RawMessage(`{"phase":"started"}`)}),
		fake.Emit(provider.Event{Sequence: 2, Kind: "agent.message", Payload: json.RawMessage(`{"text":"deterministic fake-provider evidence"}`)}),
		fake.Emit(provider.Event{Sequence: 3, Kind: "turn.completed", Payload: json.RawMessage(`{"phase":"completed"}`)}),
	}
	options := []fake.Option{}
	if scenarioName == runexecution.ScenarioRestart {
		steps = []fake.Step{
			fake.Emit(provider.Event{Sequence: 1, Kind: "turn.started", Payload: json.RawMessage(`{"phase":"before-restart"}`)}),
			fake.Pause(24 * time.Hour),
			fake.Emit(provider.Event{Sequence: 2, Kind: "agent.message", Payload: json.RawMessage(`{"text":"resumed after daemon restart"}`)}),
			fake.Emit(provider.Event{Sequence: 3, Kind: "turn.completed", Payload: json.RawMessage(`{"phase":"completed"}`)}),
		}
		if !resume {
			options = append(options, fake.WithClock(fake.NewManualClock(time.Unix(0, 0).UTC())))
		}
	} else if scenarioName != runexecution.ScenarioSuccess {
		return nil, runexecution.ErrInvalidScenario
	}
	return fake.New(fake.Scenario{Attempts: []fake.AttemptScenario{{
		AttemptID: attemptID,
		Steps:     steps,
		Result:    provider.SucceededResult{StructuredOutput: json.RawMessage(`{"artifactId":"artifact:technical-design:1","status":"candidate"}`)},
	}}}, options...)
}
