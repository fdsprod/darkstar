// Package fake provides a deterministic, network-free provider adapter for
// runtime tests.
package fake

import (
	"fmt"
	"time"

	"github.com/fdsprod/darkstar/runtime/src/ports"
	"github.com/fdsprod/darkstar/runtime/src/ports/provider"
)

// StepKind identifies one operation in an attempt's replayable event script.
type StepKind string

const (
	StepEvent         StepKind = "event"
	StepDelay         StepKind = "delay"
	StepAwaitResponse StepKind = "await_response"
	StepFailure       StepKind = "failure"
)

// Step is one deterministic action in an attempt scenario. Use the constructor
// functions below instead of populating it directly.
type Step struct {
	Kind      StepKind
	Event     provider.Event
	Delay     time.Duration
	RequestID string
	Failure   *ports.Failure
}

// Emit appends a normalized provider event to a stream. Invalid JSON payloads
// are deliberately accepted so callers can test protocol drift.
func Emit(event provider.Event) Step {
	return Step{Kind: StepEvent, Event: event}
}

// Pause advances the configured logical clock or waits for a manually
// controlled clock.
func Pause(delay time.Duration) Step {
	return Step{Kind: StepDelay, Delay: delay}
}

// AwaitResponse blocks the stream until Respond records the provider request.
// It models tool calls, permission prompts, approvals, and user-input requests.
func AwaitResponse(providerRequestID string) Step {
	return Step{Kind: StepAwaitResponse, RequestID: providerRequestID}
}

// Fail terminates a stream at the scripted point with a stable port failure.
func Fail(failure *ports.Failure) Step {
	return Step{Kind: StepFailure, Failure: failure}
}

// AttemptScenario scripts one attempt selected by AttemptRequest.AttemptID.
type AttemptScenario struct {
	AttemptID     string
	Handle        provider.AttemptHandle
	Steps         []Step
	Result        provider.AttemptResult
	StartFailure  *ports.Failure
	ResumeFailure *ports.Failure
	CancelResult  provider.CancelResult
}

// Scenario contains the complete observable behavior of a Fake provider.
type Scenario struct {
	Health       provider.Health
	Capabilities provider.CapabilityManifest
	Attempts     []AttemptScenario
}

func normalizeScenario(scenario Scenario) (Scenario, error) {
	if scenario.Health.Provider == "" {
		scenario.Health = provider.Health{
			State:           provider.HealthAvailable,
			Provider:        "fake",
			ProviderVersion: "scenario-v1",
			Authenticated:   true,
		}
	}
	if scenario.Capabilities.Provider == "" {
		scenario.Capabilities = provider.CapabilityManifest{
			Provider:    scenario.Health.Provider,
			Fingerprint: "0000000000000000000000000000000000000000000000000000000000000000",
			Features:    map[string]provider.Capability{},
		}
	}

	seen := make(map[string]struct{}, len(scenario.Attempts))
	for index := range scenario.Attempts {
		attempt := &scenario.Attempts[index]
		if attempt.AttemptID == "" {
			return Scenario{}, fmt.Errorf("attempt scenario %d has an empty attempt ID", index)
		}
		if _, exists := seen[attempt.AttemptID]; exists {
			return Scenario{}, fmt.Errorf("attempt scenario %q is duplicated", attempt.AttemptID)
		}
		seen[attempt.AttemptID] = struct{}{}

		normalizeAttempt(attempt, scenario.Health)
		if err := validateSteps(*attempt); err != nil {
			return Scenario{}, err
		}
	}
	return scenario, nil
}

func normalizeAttempt(attempt *AttemptScenario, health provider.Health) {
	if attempt.Handle.AttemptID == "" {
		attempt.Handle.AttemptID = attempt.AttemptID
	}
	if attempt.Handle.Provider == "" {
		attempt.Handle.Provider = health.Provider
	}
	if attempt.Handle.ProviderThreadID == "" {
		attempt.Handle.ProviderThreadID = "fake-thread-" + attempt.AttemptID
	}
	if attempt.Handle.ProviderTurnID == "" {
		attempt.Handle.ProviderTurnID = "fake-turn-" + attempt.AttemptID
	}
	if attempt.Handle.ProcessOwnerID == "" {
		attempt.Handle.ProcessOwnerID = "fake-owner-" + attempt.AttemptID
	}
	if attempt.Result.Status == "" {
		attempt.Result.Status = provider.AttemptSucceeded
	}
	if attempt.Result.Recovery.ProviderThreadID == "" {
		attempt.Result.Recovery.ProviderThreadID = attempt.Handle.ProviderThreadID
	}
	if attempt.Result.Recovery.ProviderTurnID == "" {
		attempt.Result.Recovery.ProviderTurnID = attempt.Handle.ProviderTurnID
	}
	if attempt.Result.Recovery.ProcessOwnerID == "" {
		attempt.Result.Recovery.ProcessOwnerID = attempt.Handle.ProcessOwnerID
	}
	if attempt.CancelResult.Disposition == "" {
		attempt.CancelResult.Disposition = provider.CancelGraceful
	}

	for index := range attempt.Steps {
		step := &attempt.Steps[index]
		if step.Kind != StepEvent {
			continue
		}
		if step.Event.SchemaVersion == 0 {
			step.Event.SchemaVersion = 1
		}
		if step.Event.AttemptID == "" {
			step.Event.AttemptID = attempt.AttemptID
		}
		if step.Event.Provider == "" {
			step.Event.Provider = health.Provider
		}
		if step.Event.ProviderVersion == "" {
			step.Event.ProviderVersion = health.ProviderVersion
		}
		if step.Event.ProviderThreadID == "" {
			step.Event.ProviderThreadID = attempt.Handle.ProviderThreadID
		}
		if step.Event.ProviderTurnID == "" {
			step.Event.ProviderTurnID = attempt.Handle.ProviderTurnID
		}
	}
}

func validateSteps(attempt AttemptScenario) error {
	var sequence uint64
	requestIDs := make(map[string]struct{})
	for index, step := range attempt.Steps {
		switch step.Kind {
		case StepEvent:
			if step.Event.AttemptID != attempt.AttemptID {
				return fmt.Errorf("attempt %q step %d event belongs to %q", attempt.AttemptID, index, step.Event.AttemptID)
			}
			if step.Event.Sequence <= sequence {
				return fmt.Errorf("attempt %q step %d sequence %d is not strictly increasing", attempt.AttemptID, index, step.Event.Sequence)
			}
			sequence = step.Event.Sequence
		case StepDelay:
			if step.Delay < 0 {
				return fmt.Errorf("attempt %q step %d has a negative delay", attempt.AttemptID, index)
			}
		case StepAwaitResponse:
			if step.RequestID == "" {
				return fmt.Errorf("attempt %q step %d has an empty response request ID", attempt.AttemptID, index)
			}
			if _, exists := requestIDs[step.RequestID]; exists {
				return fmt.Errorf("attempt %q waits for response %q more than once", attempt.AttemptID, step.RequestID)
			}
			requestIDs[step.RequestID] = struct{}{}
		case StepFailure:
			if step.Failure == nil || step.Failure.Code == "" {
				return fmt.Errorf("attempt %q step %d has no classified failure", attempt.AttemptID, index)
			}
		default:
			return fmt.Errorf("attempt %q step %d has unknown kind %q", attempt.AttemptID, index, step.Kind)
		}
	}
	return nil
}
