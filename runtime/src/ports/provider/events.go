package provider

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"io"
	"regexp"
	"strings"
	"unicode"

	"darkstar/src/ports"
)

var (
	safeInteractionSubjectPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`)
	safeCommandPattern            = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9 ./_@:+,=\-]{0,255}$`)
	safeRelativePathPattern       = regexp.MustCompile(`^[A-Za-z0-9._-][A-Za-z0-9 ./_-]{0,255}$`)
	windowsAbsolutePathPattern    = regexp.MustCompile(`[A-Za-z]:[\\/]`)
	safeCapabilityListPattern     = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9]*(,[A-Za-z][A-Za-z0-9]*)*$`)
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
	if strings.TrimSpace(checkpoint.ProviderTurnID) == "" || checkpoint.ProviderTurnID != strings.TrimSpace(checkpoint.ProviderTurnID) || len(checkpoint.ProviderTurnID) > 128 {
		return InteractionCheckpoint{}, false, eventContractFailure("payload.checkpoint.providerTurnId", "must be a safe non-empty provider turn identity")
	}
	if !ValidInteractionScope(checkpoint.Kind, checkpoint.Scope) {
		return InteractionCheckpoint{}, false, eventContractFailure("payload.checkpoint.scope", "must be a closed normalized interaction scope")
	}
	if strings.TrimSpace(checkpoint.ProviderRequestID) == "" || strings.TrimSpace(checkpoint.ProviderRequestID) != checkpoint.ProviderRequestID {
		return InteractionCheckpoint{}, false, eventContractFailure("payload.checkpoint.providerRequestId", "must be an opaque non-empty provider request identity")
	}
	digest, decodeErr := hex.DecodeString(checkpoint.ScopeDigest)
	if decodeErr != nil || len(digest) != 32 {
		return InteractionCheckpoint{}, false, eventContractFailure("payload.checkpoint.scopeDigest", "must be a SHA-256 digest")
	}
	policyDigest, decodeErr := hex.DecodeString(checkpoint.PolicyDigest)
	if decodeErr != nil || len(policyDigest) != 32 {
		return InteractionCheckpoint{}, false, eventContractFailure("payload.checkpoint.policyDigest", "must be a SHA-256 digest")
	}
	if checkpoint.Kind == InteractionUser {
		if checkpoint.Input == nil || !ValidUserInputRequest(*checkpoint.Input) {
			return InteractionCheckpoint{}, false, eventContractFailure("payload.checkpoint.input", "must contain safe normalized questions")
		}
	} else if checkpoint.Input != nil {
		return InteractionCheckpoint{}, false, eventContractFailure("payload.checkpoint.input", "is legal only for user input")
	}
	return checkpoint, true, nil
}

func ValidInteractionScope(kind InteractionKind, scope InteractionScope) bool {
	pair := string(kind) + ":" + scope.Target + ":" + scope.Operation
	switch pair {
	case "command:command:execute":
		return safeCommandPattern.MatchString(scope.Subject) && safeDisplayTarget(scope.Subject)
	case "file:file:modify":
		return safeRelativePathPattern.MatchString(scope.Subject) && safeDisplayTarget(scope.Subject) && !strings.Contains(scope.Subject, "..") && !strings.HasPrefix(scope.Subject, "/")
	case "network:network_host:connect":
		return safeInteractionSubjectPattern.MatchString(scope.Subject) && !strings.Contains(scope.Subject, "://")
	case "permission:provider_capabilities:elevate":
		return safeCapabilityListPattern.MatchString(scope.Subject)
	case "tool:provider_tool:invoke", "user:user_input:answer":
		return safeInteractionSubjectPattern.MatchString(scope.Subject)
	default:
		return false
	}
}

func safeDisplayTarget(value string) bool {
	lower := strings.ToLower(value)
	return !strings.Contains(lower, "://") && !containsSensitiveDisplayText(lower) &&
		!windowsAbsolutePathPattern.MatchString(value) && !strings.HasPrefix(value, `\\`)
}

func ValidUserInputRequest(input UserInputRequest) bool {
	if len(input.Questions) == 0 || len(input.Questions) > 16 {
		return false
	}
	seen := map[string]bool{}
	for _, question := range input.Questions {
		if !safeInteractionSubjectPattern.MatchString(question.ID) || seen[question.ID] || !safeDisplayText(question.Prompt, 512) || len(question.Options) > 32 || question.Schema.Type != "string" {
			return false
		}
		seen[question.ID] = true
		allowed := map[string]bool{}
		for _, option := range question.Options {
			if !safeDisplayText(option, 128) || allowed[option] {
				return false
			}
			allowed[option] = true
		}
		if len(question.Schema.AllowedValues) != len(question.Options) {
			return false
		}
		for index := range question.Options {
			if question.Schema.AllowedValues[index] != question.Options[index] {
				return false
			}
		}
	}
	return true
}

func safeDisplayText(value string, maximum int) bool {
	value = strings.TrimSpace(value)
	lower := strings.ToLower(value)
	if value == "" || len(value) > maximum || strings.Contains(lower, "://") || containsSensitiveDisplayText(lower) || strings.HasPrefix(value, "/") || strings.HasPrefix(value, `\\`) {
		return false
	}
	for _, character := range value {
		if unicode.IsControl(character) {
			return false
		}
	}
	return !windowsAbsolutePathPattern.MatchString(value)
}

func containsSensitiveDisplayText(lower string) bool {
	for _, marker := range []string{"password", "passwd", "secret", "credential", "bearer ", "access token", "api key", "api_key", "api-key", "apikey", "--token", "token=", "token:", "token-"} {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

// ValidUserInputAnswer validates the provider-native answer envelope against
// the exact normalized question set before it can be durably recorded.
func ValidUserInputAnswer(input UserInputRequest, raw json.RawMessage) bool {
	if !ValidUserInputRequest(input) {
		return false
	}
	type answerValue struct {
		Answers string `json:"answers"`
	}
	var envelope struct {
		Answers map[string]answerValue `json:"answers"`
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&envelope) != nil || decoder.Decode(new(any)) != io.EOF || len(envelope.Answers) != len(input.Questions) {
		return false
	}
	for _, question := range input.Questions {
		answer, ok := envelope.Answers[question.ID]
		if !ok || !safeDisplayText(answer.Answers, 128) {
			return false
		}
		if len(question.Schema.AllowedValues) != 0 {
			allowed := false
			for _, value := range question.Schema.AllowedValues {
				if answer.Answers == value {
					allowed = true
					break
				}
			}
			if !allowed {
				return false
			}
		}
	}
	return true
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
