package provider

import (
	"encoding/hex"
	"encoding/json"
	"strings"

	"github.com/fdsprod/darkstar/runtime/src/ports"
)

// EventKind is the closed DARKSTAR vocabulary emitted by provider adapters.
// Provider-native event names must be translated to one of these values. An
// unrecognized provider frame is preserved as EventUnknownProvider rather than
// being forwarded as an implementation-specific kind or silently discarded.
type EventKind string

const (
	EventAttemptStarted   EventKind = "attempt.started"
	EventAttemptWaiting   EventKind = "attempt.waiting"
	EventAttemptCompleted EventKind = "attempt.completed"
	EventAttemptFailed    EventKind = "attempt.failed"
	EventAttemptCancelled EventKind = "attempt.cancelled"

	EventTurnStarted     EventKind = "turn.started"
	EventTurnCompleted   EventKind = "turn.completed"
	EventTurnInterrupted EventKind = "turn.interrupted"

	EventMessageDelta              EventKind = "message.delta"
	EventMessageCompleted          EventKind = "message.completed"
	EventPlanUpdated               EventKind = "plan.updated"
	EventStructuredOutputCompleted EventKind = "structured_output.completed"

	EventCommandStarted      EventKind = "command.started"
	EventCommandOutput       EventKind = "command.output"
	EventCommandCompleted    EventKind = "command.completed"
	EventFileChangeStarted   EventKind = "file_change.started"
	EventFileChangeCompleted EventKind = "file_change.completed"
	EventToolStarted         EventKind = "tool.started"
	EventToolCompleted       EventKind = "tool.completed"

	EventPermissionRequested        EventKind = "permission.requested"
	EventPermissionResponseRecorded EventKind = "permission.response_recorded"
	EventUserInputRequested         EventKind = "user_input.requested"
	EventUserInputResponseRecorded  EventKind = "user_input.response_recorded"

	EventUsageUpdated    EventKind = "usage.updated"
	EventWarning         EventKind = "warning"
	EventError           EventKind = "error"
	EventUnknownProvider EventKind = "unknown.provider_event"
)

var canonicalEventKinds = [...]EventKind{
	EventAttemptStarted,
	EventAttemptWaiting,
	EventAttemptCompleted,
	EventAttemptFailed,
	EventAttemptCancelled,
	EventTurnStarted,
	EventTurnCompleted,
	EventTurnInterrupted,
	EventMessageDelta,
	EventMessageCompleted,
	EventPlanUpdated,
	EventStructuredOutputCompleted,
	EventCommandStarted,
	EventCommandOutput,
	EventCommandCompleted,
	EventFileChangeStarted,
	EventFileChangeCompleted,
	EventToolStarted,
	EventToolCompleted,
	EventPermissionRequested,
	EventPermissionResponseRecorded,
	EventUserInputRequested,
	EventUserInputResponseRecorded,
	EventUsageUpdated,
	EventWarning,
	EventError,
	EventUnknownProvider,
}

// CanonicalEventKinds returns a copy of the normalized event vocabulary.
func CanonicalEventKinds() []EventKind {
	return append([]EventKind(nil), canonicalEventKinds[:]...)
}

// IsCanonical reports whether the kind can cross the provider port. Adapters
// must use EventUnknownProvider for provider frames they do not recognize.
func (kind EventKind) IsCanonical() bool {
	for _, candidate := range canonicalEventKinds {
		if kind == candidate {
			return true
		}
	}
	return false
}

// Validate checks the normalized event envelope before core persists or acts on
// it. Validation failures are protocol drift: the adapter returned data outside
// the provider contract, not an invalid request supplied by core.
func (event Event) Validate() error {
	switch {
	case event.SchemaVersion != 1:
		return eventContractFailure("schemaVersion", "must be 1")
	case strings.TrimSpace(event.AttemptID) == "":
		return eventContractFailure("attemptId", "must not be empty")
	case event.Sequence == 0:
		return eventContractFailure("sequence", "must be greater than zero")
	case event.OccurredAt.IsZero():
		return eventContractFailure("occurredAt", "must not be zero")
	case !event.Kind.IsCanonical():
		return eventContractFailure("kind", "must be a canonical normalized event kind")
	case strings.TrimSpace(event.Provider) == "":
		return eventContractFailure("provider", "must not be empty")
	case len(event.Payload) == 0:
		return eventContractFailure("payload", "must be present")
	case !json.Valid(event.Payload):
		return eventContractFailure("payload", "must be valid JSON")
	default:
		return nil
	}
}

// InteractionCheckpointFromEvent returns the durable checkpoint carried by an
// interaction request or response event. Ordinary provider events return
// present=false; malformed checkpoints are protocol drift and must not be
// interpreted as an untyped provider payload by core.
func InteractionCheckpointFromEvent(event Event) (checkpoint InteractionCheckpoint, present bool, err error) {
	var envelope struct {
		Checkpoint *InteractionCheckpoint `json:"checkpoint"`
	}
	if unmarshalErr := json.Unmarshal(event.Payload, &envelope); unmarshalErr != nil {
		return InteractionCheckpoint{}, false, eventContractFailure("payload.checkpoint", "must be carried in a JSON object")
	}
	if envelope.Checkpoint == nil {
		return InteractionCheckpoint{}, false, nil
	}
	checkpoint = *envelope.Checkpoint
	switch checkpoint.Kind {
	case InteractionCommand, InteractionFile, InteractionNetwork, InteractionPermission, InteractionTool, InteractionUser:
	default:
		return InteractionCheckpoint{}, false, eventContractFailure("payload.checkpoint.kind", "must be a canonical interaction kind")
	}
	if strings.TrimSpace(checkpoint.ProviderRequestID) == "" || strings.TrimSpace(checkpoint.ProviderRequestID) != checkpoint.ProviderRequestID {
		return InteractionCheckpoint{}, false, eventContractFailure("payload.checkpoint.providerRequestId", "must be an opaque non-empty provider request identity")
	}
	digest, decodeErr := hex.DecodeString(checkpoint.ScopeDigest)
	if decodeErr != nil || len(digest) != 32 {
		return InteractionCheckpoint{}, false, eventContractFailure("payload.checkpoint.scopeDigest", "must be a SHA-256 digest")
	}
	return checkpoint, true, nil
}

func eventContractFailure(field, requirement string) *ports.Failure {
	return &ports.Failure{
		Code:      ports.FailureProtocolDrift,
		Message:   "provider event violates the normalized contract",
		Retryable: false,
		Details: map[string]string{
			"field":       field,
			"requirement": requirement,
		},
	}
}
