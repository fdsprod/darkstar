package provider_test

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/fdsprod/darkstar/runtime/src/ports"
	"github.com/fdsprod/darkstar/runtime/src/ports/provider"
)

func TestCanonicalEventKindsAreCompleteAndImmutable(t *testing.T) {
	t.Parallel()

	want := []provider.EventKind{
		provider.EventAttemptStarted,
		provider.EventAttemptWaiting,
		provider.EventAttemptCompleted,
		provider.EventAttemptFailed,
		provider.EventAttemptCancelled,
		provider.EventTurnStarted,
		provider.EventTurnCompleted,
		provider.EventTurnInterrupted,
		provider.EventMessageDelta,
		provider.EventMessageCompleted,
		provider.EventPlanUpdated,
		provider.EventStructuredOutputCompleted,
		provider.EventCommandStarted,
		provider.EventCommandOutput,
		provider.EventCommandCompleted,
		provider.EventFileChangeStarted,
		provider.EventFileChangeCompleted,
		provider.EventToolStarted,
		provider.EventToolCompleted,
		provider.EventPermissionRequested,
		provider.EventPermissionResponseRecorded,
		provider.EventUserInputRequested,
		provider.EventUserInputResponseRecorded,
		provider.EventUsageUpdated,
		provider.EventWarning,
		provider.EventError,
		provider.EventUnknownProvider,
	}

	got := provider.CanonicalEventKinds()
	if len(got) != len(want) {
		t.Fatalf("CanonicalEventKinds() length = %d, want %d", len(got), len(want))
	}
	for index := range want {
		if got[index] != want[index] || !got[index].IsCanonical() {
			t.Errorf("CanonicalEventKinds()[%d] = %q, want canonical %q", index, got[index], want[index])
		}
	}
	got[0] = "provider.native"
	if provider.CanonicalEventKinds()[0] != provider.EventAttemptStarted {
		t.Fatal("CanonicalEventKinds() exposed mutable package state")
	}
	if provider.EventKind("provider.native").IsCanonical() {
		t.Fatal("provider-native kind passed normalized event validation")
	}
}

func TestEventValidateAcceptsNormalizedAndUnknownProviderEvents(t *testing.T) {
	t.Parallel()

	base := provider.Event{
		SchemaVersion:   1,
		AttemptID:       "attempt-1",
		Sequence:        1,
		OccurredAt:      time.Date(2026, time.September, 1, 12, 0, 0, 0, time.UTC),
		Kind:            provider.EventMessageCompleted,
		Provider:        "fake",
		ProviderVersion: "scenario-v1",
		Payload:         json.RawMessage(`{"text":"done"}`),
	}
	if err := base.Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}

	base.Kind = provider.EventUnknownProvider
	base.RawEvidenceRef = "evidence/provider/frame-17.json"
	base.Payload = json.RawMessage(`{"providerEventKind":"future/item"}`)
	if err := base.Validate(); err != nil {
		t.Fatalf("unknown provider Validate() error = %v", err)
	}
}

func TestEventValidateClassifiesContractDrift(t *testing.T) {
	t.Parallel()

	valid := provider.Event{
		SchemaVersion: 1,
		AttemptID:     "attempt-1",
		Sequence:      1,
		OccurredAt:    time.Unix(1, 0).UTC(),
		Kind:          provider.EventTurnStarted,
		Provider:      "fake",
		Payload:       json.RawMessage(`{}`),
	}
	tests := []struct {
		name  string
		field string
		edit  func(*provider.Event)
	}{
		{"schema version", "schemaVersion", func(event *provider.Event) { event.SchemaVersion = 2 }},
		{"attempt identity", "attemptId", func(event *provider.Event) { event.AttemptID = " " }},
		{"sequence", "sequence", func(event *provider.Event) { event.Sequence = 0 }},
		{"timestamp", "occurredAt", func(event *provider.Event) { event.OccurredAt = time.Time{} }},
		{"provider-native kind", "kind", func(event *provider.Event) { event.Kind = "codex/item.completed" }},
		{"provider identity", "provider", func(event *provider.Event) { event.Provider = "" }},
		{"missing payload", "payload", func(event *provider.Event) { event.Payload = nil }},
		{"malformed payload", "payload", func(event *provider.Event) { event.Payload = json.RawMessage(`{`) }},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			event := valid
			test.edit(&event)
			err := event.Validate()
			var failure *ports.Failure
			if !errors.As(err, &failure) || failure.Code != ports.FailureProtocolDrift {
				t.Fatalf("Validate() error = %#v, want protocol_drift failure", err)
			}
			if failure.Retryable || failure.Details["field"] != test.field {
				t.Fatalf("failure = %#v, want non-retryable field %q", failure, test.field)
			}
		})
	}
}

func TestInteractionCheckpointFromEventReturnsTypedCheckpoint(t *testing.T) {
	t.Parallel()

	event := provider.Event{Payload: json.RawMessage(`{"checkpoint":{"kind":"network","providerRequestId":"7","scopeDigest":"0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"}}`)}
	checkpoint, present, err := provider.InteractionCheckpointFromEvent(event)
	if err != nil || !present {
		t.Fatalf("InteractionCheckpointFromEvent() present = %t, error = %v", present, err)
	}
	if checkpoint.Kind != provider.InteractionNetwork || checkpoint.ProviderRequestID != "7" || checkpoint.ScopeDigest == "" {
		t.Fatalf("InteractionCheckpointFromEvent() = %#v", checkpoint)
	}

	ordinary := provider.Event{Payload: json.RawMessage(`{"text":"done"}`)}
	if _, present, err = provider.InteractionCheckpointFromEvent(ordinary); err != nil || present {
		t.Fatalf("ordinary event checkpoint present = %t, error = %v", present, err)
	}
}

func TestInteractionCheckpointFromEventRejectsMalformedCheckpoint(t *testing.T) {
	t.Parallel()

	tests := []json.RawMessage{
		json.RawMessage(`{"checkpoint":{"kind":"approval","providerRequestId":"7","scopeDigest":"0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"}}`),
		json.RawMessage(`{"checkpoint":{"kind":"command","providerRequestId":"","scopeDigest":"0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"}}`),
		json.RawMessage(`{"checkpoint":{"kind":"command","providerRequestId":"7","scopeDigest":"not-a-digest"}}`),
	}
	for _, payload := range tests {
		_, present, err := provider.InteractionCheckpointFromEvent(provider.Event{Payload: payload})
		var failure *ports.Failure
		if present || !errors.As(err, &failure) || failure.Code != ports.FailureProtocolDrift {
			t.Fatalf("payload %s: present = %t, error = %#v", payload, present, err)
		}
	}
}
