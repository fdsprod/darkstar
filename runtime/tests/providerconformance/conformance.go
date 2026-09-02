// Package providerconformance contains the shared contract tests that every
// completed provider adapter must run.
package providerconformance

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"testing"
	"time"

	"darkstar/src/ports"
	"darkstar/src/ports/provider"
)

// Suite supplies one deterministic, successful read-only attempt. NewProvider
// must return a fresh adapter because each contract case owns its attempt ID.
type Suite struct {
	NewProvider       func(*testing.T) provider.Provider
	NewHealthProvider func(*testing.T) provider.Provider
	Request           func(*testing.T) provider.AttemptRequest
}

// Run verifies the provider-neutral contracts shared by Fake, App Server, and
// exec adapters. Adapter-specific behavior remains in each implementation's
// focused tests.
func Run(t *testing.T, suite Suite) {
	t.Helper()
	if suite.NewProvider == nil || suite.Request == nil {
		t.Fatal("provider conformance suite requires NewProvider and Request")
	}
	if suite.NewHealthProvider == nil {
		suite.NewHealthProvider = suite.NewProvider
	}

	t.Run("health", func(t *testing.T) {
		adapter := suite.NewHealthProvider(t)
		observation, err := adapter.ProbeHealth(context.Background())
		if err != nil {
			t.Fatalf("ProbeHealth() error = %v", err)
		}
		manifest, err := adapter.Capabilities(context.Background())
		if err != nil {
			t.Fatalf("Capabilities() error = %v", err)
		}
		if observation.Provider == "" || observation.Provider != manifest.Provider {
			t.Fatalf("health provider = %q, capability provider = %q", observation.Provider, manifest.Provider)
		}
		if observation.ProviderVersion == "" {
			t.Fatal("health omitted exact provider version")
		}
		switch observation.State {
		case provider.HealthAvailable, provider.HealthUnavailable, provider.HealthUnauthenticated, provider.HealthUsageExhausted, provider.HealthDegraded:
		default:
			t.Fatalf("health state = %q, want canonical state", observation.State)
		}
		switch observation.Authentication {
		case provider.AuthenticationAuthenticated, provider.AuthenticationUnauthenticated, provider.AuthenticationUnknown:
		default:
			t.Fatalf("authentication state = %q, want canonical state", observation.Authentication)
		}
		switch observation.Usage {
		case provider.UsageReady, provider.UsageExhausted, provider.UsageUnknown:
		default:
			t.Fatalf("usage readiness = %q, want canonical state", observation.Usage)
		}
		if observation.InstructionSources == nil || observation.Diagnostics == nil {
			t.Fatalf("health collections must be present: %#v", observation)
		}
	})

	t.Run("capability_manifest", func(t *testing.T) {
		adapter := suite.NewProvider(t)
		manifest, err := adapter.Capabilities(context.Background())
		if err != nil {
			t.Fatalf("Capabilities() error = %v", err)
		}
		if manifest.Provider == "" {
			t.Fatal("capability manifest omitted provider identity")
		}
		fingerprint, err := hex.DecodeString(manifest.Fingerprint)
		if err != nil || len(fingerprint) != 32 {
			t.Fatalf("capability fingerprint = %q, want SHA-256 hex", manifest.Fingerprint)
		}
		if manifest.ObservedAt.IsZero() {
			t.Fatal("capability manifest omitted observation time")
		}
		for _, name := range []string{"text_input", "structured_output"} {
			capability, ok := manifest.Features[name]
			if !ok {
				t.Fatalf("capability manifest omitted required %q declaration", name)
			}
			if _, available := capability.(provider.AvailableCapability); !available {
				t.Fatalf("required capability %q = %T, want available", name, capability)
			}
		}
	})

	t.Run("successful_attempt_lifecycle", func(t *testing.T) {
		adapter := suite.NewProvider(t)
		request := suite.Request(t)
		manifest, err := adapter.Capabilities(context.Background())
		if err != nil {
			t.Fatalf("Capabilities() error = %v", err)
		}
		request.CapabilityFingerprint = manifest.Fingerprint

		handle, err := adapter.StartAttempt(context.Background(), request)
		if err != nil {
			t.Fatalf("StartAttempt() error = %v", err)
		}
		assertHandle(t, handle, request.AttemptID, manifest.Provider)

		duplicate, err := adapter.StartAttempt(context.Background(), request)
		if err != nil {
			t.Fatalf("idempotent StartAttempt() error = %v", err)
		}
		if duplicate != handle {
			t.Fatalf("idempotent StartAttempt() handle = %#v, want %#v", duplicate, handle)
		}

		conflict := request
		conflict.IdempotencyKey += "-conflict"
		if _, err := adapter.StartAttempt(context.Background(), conflict); !failureCodeIs(err, ports.FailureConflict) {
			t.Fatalf("conflicting StartAttempt() error = %#v, want conflict failure", err)
		}

		events := collectEvents(t, adapter, handle)
		if len(events) == 0 {
			t.Fatal("provider emitted no normalized events")
		}
		var previous uint64
		for _, event := range events {
			if err := event.Validate(); err != nil {
				t.Fatalf("event %d violates normalized contract: %v", event.Sequence, err)
			}
			if event.Sequence <= previous {
				t.Fatalf("event sequence = %d after %d, want strictly increasing", event.Sequence, previous)
			}
			if event.AttemptID != handle.AttemptID || event.Provider != handle.Provider {
				t.Fatalf("event identity = (%q, %q), want (%q, %q)", event.AttemptID, event.Provider, handle.AttemptID, handle.Provider)
			}
			if event.ProviderThreadID != "" && event.ProviderThreadID != handle.ProviderThreadID {
				t.Fatalf("event thread = %q, want %q", event.ProviderThreadID, handle.ProviderThreadID)
			}
			if event.ProviderTurnID != "" && event.ProviderTurnID != handle.ProviderTurnID {
				t.Fatalf("event turn = %q, want %q", event.ProviderTurnID, handle.ProviderTurnID)
			}
			previous = event.Sequence
		}

		result, err := adapter.GetResult(context.Background(), provider.ResultRequest{Handle: handle})
		if err != nil {
			t.Fatalf("GetResult() error = %v", err)
		}
		succeeded, ok := result.(provider.SucceededResult)
		if !ok {
			t.Fatalf("GetResult() = %T, want provider.SucceededResult", result)
		}
		if len(succeeded.StructuredOutput) == 0 || !json.Valid(succeeded.StructuredOutput) {
			t.Fatalf("structured output = %q, want non-empty JSON", succeeded.StructuredOutput)
		}
		if succeeded.Recovery.ProviderThreadID != handle.ProviderThreadID || succeeded.Recovery.ProviderTurnID != handle.ProviderTurnID {
			t.Fatalf("recovery identity = (%q, %q), want (%q, %q)", succeeded.Recovery.ProviderThreadID, succeeded.Recovery.ProviderTurnID, handle.ProviderThreadID, handle.ProviderTurnID)
		}
		if succeeded.Recovery.ProcessOwnerID != handle.ProcessOwnerID {
			t.Fatalf("recovery process owner = %q, want %q", succeeded.Recovery.ProcessOwnerID, handle.ProcessOwnerID)
		}
		if succeeded.Recovery.LastSequence < previous {
			t.Fatalf("recovery sequence = %d, want at least %d", succeeded.Recovery.LastSequence, previous)
		}
	})
}

func assertHandle(t *testing.T, handle provider.AttemptHandle, attemptID, providerName string) {
	t.Helper()
	if handle.AttemptID != attemptID || handle.Provider != providerName {
		t.Fatalf("attempt handle identity = (%q, %q), want (%q, %q)", handle.AttemptID, handle.Provider, attemptID, providerName)
	}
	if handle.ProviderThreadID == "" || handle.ProviderTurnID == "" {
		t.Fatalf("attempt handle omitted recovery identity: %#v", handle)
	}
}

func collectEvents(t *testing.T, adapter provider.Provider, handle provider.AttemptHandle) []provider.Event {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	stream, err := adapter.StreamEvents(ctx, provider.EventRequest{Handle: handle})
	if err != nil {
		t.Fatalf("StreamEvents() error = %v", err)
	}
	var events []provider.Event
	for {
		event, err := stream.Receive()
		if errors.Is(err, io.EOF) {
			if closeErr := stream.Close(); closeErr != nil {
				t.Fatalf("EventStream.Close() error = %v", closeErr)
			}
			return events
		}
		if err != nil {
			_ = stream.Close()
			t.Fatalf("EventStream.Receive() error = %v", err)
		}
		events = append(events, event)
	}
}

func failureCodeIs(err error, code ports.FailureCode) bool {
	var failure *ports.Failure
	return errors.As(err, &failure) && failure.Code == code
}
