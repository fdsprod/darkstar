package codex

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/fdsprod/darkstar/runtime/src/ports/provider"
)

// NormalizerOptions binds one ordered native message stream to one DARKSTAR attempt.
type NormalizerOptions struct {
	AttemptID       string
	ProviderVersion string
	InitialSequence uint64
	Clock           func() time.Time
	EvidenceRef     func(sequence uint64, method string) string
}

// EventNormalizer is stateful because sequence numbers and resolved server
// requests are meaningful only within one provider-backed attempt.
type EventNormalizer struct {
	mu              sync.Mutex
	attemptID       string
	providerVersion string
	clock           func() time.Time
	evidenceRef     func(uint64, string) string
	sequence        uint64
	interactions    map[string]provider.InteractionCheckpoint
}

// NewEventNormalizer creates an attempt-scoped normalizer.
func NewEventNormalizer(options NormalizerOptions) (*EventNormalizer, error) {
	if strings.TrimSpace(options.AttemptID) == "" {
		return nil, errors.New("codex event normalizer attempt ID is required")
	}
	if strings.TrimSpace(options.ProviderVersion) == "" {
		return nil, errors.New("codex event normalizer provider version is required")
	}
	if options.Clock == nil {
		options.Clock = time.Now
	}
	return &EventNormalizer{
		attemptID:       options.AttemptID,
		providerVersion: options.ProviderVersion,
		clock:           options.Clock,
		evidenceRef:     options.EvidenceRef,
		sequence:        options.InitialSequence,
		interactions:    make(map[string]provider.InteractionCheckpoint),
	}, nil
}

// Normalize converts one request or notification while preserving wire order.
func (normalizer *EventNormalizer) Normalize(message IncomingMessage) (provider.Event, error) {
	normalizer.mu.Lock()
	defer normalizer.mu.Unlock()

	switch message := message.(type) {
	case ServerNotification:
		return normalizer.normalizeNotification(message)
	case ServerRequest:
		return normalizer.normalizeRequest(message)
	default:
		return provider.Event{}, fmt.Errorf("unsupported Codex incoming message %T", message)
	}
}

// Emit appends one adapter-derived event to the same ordered sequence as native
// messages. It is used for facts such as schema-validated structured output.
func (normalizer *EventNormalizer) Emit(kind provider.EventKind, payload json.RawMessage, threadID, turnID, itemID string) (provider.Event, error) {
	normalizer.mu.Lock()
	defer normalizer.mu.Unlock()
	if !kind.IsCanonical() || kind == provider.EventUnknownProvider {
		return provider.Event{}, fmt.Errorf("invalid derived Codex event kind %q", kind)
	}
	if len(bytes.TrimSpace(payload)) == 0 || !json.Valid(payload) {
		return provider.Event{}, errors.New("derived Codex event payload must be valid JSON")
	}
	params := map[string]json.RawMessage{
		"threadId": mustRawString(threadID),
		"turnId":   mustRawString(turnID),
		"itemId":   mustRawString(itemID),
	}
	return normalizer.event("darkstar/derived", cloneRaw(payload), nil, kind, normalizer.clock().UTC(), params)
}

func (normalizer *EventNormalizer) normalizeNotification(notification ServerNotification) (provider.Event, error) {
	params, err := objectParams(notification.Params)
	if err != nil {
		return provider.Event{}, fmt.Errorf("normalize Codex %s notification: %w", notification.Method, err)
	}
	kind := notificationKind(notification.Method, params)
	var checkpoint *provider.InteractionCheckpoint
	if notification.Method == "serverRequest/resolved" {
		kind, checkpoint = normalizer.resolvedKind(params)
	}
	occurredAt := normalizer.clock().UTC()
	if notification.EmittedAtMS > 0 {
		occurredAt = time.UnixMilli(notification.EmittedAtMS).UTC()
	}
	payload, err := encodeNativePayload(notification.Method, nil, notification.Params, checkpoint)
	if err != nil {
		return provider.Event{}, err
	}
	return normalizer.event(notification.Method, payload, nil, kind, occurredAt, params)
}

func (normalizer *EventNormalizer) normalizeRequest(request ServerRequest) (provider.Event, error) {
	params, err := objectParams(request.Params)
	if err != nil {
		return provider.Event{}, fmt.Errorf("normalize Codex %s request: %w", request.Method, err)
	}
	interaction, kind := requestKind(request.Method, params)
	var checkpoint *provider.InteractionCheckpoint
	if interaction != "" {
		value, digestErr := interactionCheckpoint(request.ID, request.Method, interaction, request.Params)
		if digestErr != nil {
			return provider.Event{}, fmt.Errorf("digest normalized Codex request: %w", digestErr)
		}
		checkpoint = &value
		normalizer.interactions[requestIDKey(request.ID)] = value
	}
	payload, err := encodeNativePayload(request.Method, request.ID, request.Params, checkpoint)
	if err != nil {
		return provider.Event{}, fmt.Errorf("encode normalized Codex request: %w", err)
	}
	return normalizer.event(request.Method, payload, request.ID, kind, normalizer.clock().UTC(), params)
}

func encodeNativePayload(method string, requestID, params json.RawMessage, checkpoint *provider.InteractionCheckpoint) (json.RawMessage, error) {
	if len(bytes.TrimSpace(params)) == 0 || bytes.Equal(bytes.TrimSpace(params), []byte("null")) {
		params = json.RawMessage(`{}`)
	}
	payload, err := json.Marshal(struct {
		ProviderMethod string                          `json:"providerMethod"`
		RequestID      json.RawMessage                 `json:"requestId,omitempty"`
		Checkpoint     *provider.InteractionCheckpoint `json:"checkpoint,omitempty"`
		Params         json.RawMessage                 `json:"params"`
	}{ProviderMethod: method, RequestID: cloneRaw(requestID), Checkpoint: checkpoint, Params: cloneRaw(params)})
	if err != nil {
		return nil, fmt.Errorf("encode normalized Codex payload: %w", err)
	}
	return payload, nil
}

func (normalizer *EventNormalizer) event(
	method string,
	payload json.RawMessage,
	requestID json.RawMessage,
	kind provider.EventKind,
	occurredAt time.Time,
	params map[string]json.RawMessage,
) (provider.Event, error) {
	normalizer.sequence++
	threadID := rawString(params["threadId"])
	turnID := rawString(params["turnId"])
	itemID := rawString(params["itemId"])
	if threadID == "" {
		threadID = rawString(params["conversationId"])
	}
	if itemID == "" {
		itemID = rawString(params["callId"])
	}
	if item, ok := rawObject(params["item"]); ok {
		if itemID == "" {
			itemID = rawString(item["id"])
		}
	}
	if turn, ok := rawObject(params["turn"]); ok && turnID == "" {
		turnID = rawString(turn["id"])
	}
	if thread, ok := rawObject(params["thread"]); ok && threadID == "" {
		threadID = rawString(thread["id"])
	}
	if len(requestID) > 0 && itemID == "" {
		itemID = requestIDKey(requestID)
	}

	event := provider.Event{
		SchemaVersion:    1,
		AttemptID:        normalizer.attemptID,
		Sequence:         normalizer.sequence,
		OccurredAt:       occurredAt,
		Kind:             kind,
		Provider:         "codex",
		ProviderVersion:  normalizer.providerVersion,
		ProviderThreadID: threadID,
		ProviderTurnID:   turnID,
		ProviderItemID:   itemID,
		Payload:          cloneRaw(payload),
	}
	if normalizer.evidenceRef != nil {
		event.RawEvidenceRef = normalizer.evidenceRef(event.Sequence, method)
	}
	if err := event.Validate(); err != nil {
		return provider.Event{}, err
	}
	return event, nil
}

func notificationKind(method string, params map[string]json.RawMessage) provider.EventKind {
	switch method {
	case "thread/started":
		return provider.EventAttemptStarted
	case "turn/started":
		return provider.EventTurnStarted
	case "turn/completed":
		if turn, ok := rawObject(params["turn"]); ok && rawString(turn["status"]) == "interrupted" {
			return provider.EventTurnInterrupted
		}
		return provider.EventTurnCompleted
	case "item/agentMessage/delta":
		return provider.EventMessageDelta
	case "item/commandExecution/outputDelta":
		return provider.EventCommandOutput
	case "turn/plan/updated", "item/plan/delta":
		return provider.EventPlanUpdated
	case "thread/tokenUsage/updated":
		return provider.EventUsageUpdated
	case "error", "thread/realtime/error":
		return provider.EventError
	case "warning", "configWarning", "guardianWarning", "windows/worldWritableWarning", "deprecationNotice":
		return provider.EventWarning
	case "serverRequest/resolved":
		return provider.EventUnknownProvider
	case "thread/status/changed":
		if status, ok := rawObject(params["status"]); ok && len(bytes.TrimSpace(status["activeFlags"])) > 0 && string(bytes.TrimSpace(status["activeFlags"])) != "null" {
			return provider.EventAttemptWaiting
		}
		return provider.EventUnknownProvider
	case "item/started":
		return itemKind(params, true)
	case "item/completed":
		return itemKind(params, false)
	default:
		return provider.EventUnknownProvider
	}
}

func itemKind(params map[string]json.RawMessage, started bool) provider.EventKind {
	item, ok := rawObject(params["item"])
	if !ok {
		return provider.EventUnknownProvider
	}
	switch rawString(item["type"]) {
	case "agentMessage":
		if started {
			return provider.EventUnknownProvider
		}
		return provider.EventMessageCompleted
	case "commandExecution":
		if started {
			return provider.EventCommandStarted
		}
		return provider.EventCommandCompleted
	case "fileChange":
		if started {
			return provider.EventFileChangeStarted
		}
		return provider.EventFileChangeCompleted
	case "plan":
		return provider.EventPlanUpdated
	case "usageLimitExceeded":
		return provider.EventError
	case "mcpToolCall", "dynamicToolCall", "webSearch", "imageGeneration", "collabAgentToolCall", "findInPage", "listFiles", "read", "search", "openPage":
		if started {
			return provider.EventToolStarted
		}
		return provider.EventToolCompleted
	default:
		return provider.EventUnknownProvider
	}
}

func requestKind(method string, params map[string]json.RawMessage) (provider.InteractionKind, provider.EventKind) {
	switch method {
	case "execCommandApproval":
		return provider.InteractionCommand, provider.EventPermissionRequested
	case "item/commandExecution/requestApproval":
		if context, present := params["networkApprovalContext"]; present && len(bytes.TrimSpace(context)) > 0 && string(bytes.TrimSpace(context)) != "null" {
			return provider.InteractionNetwork, provider.EventPermissionRequested
		}
		return provider.InteractionCommand, provider.EventPermissionRequested
	case "applyPatchApproval", "item/fileChange/requestApproval":
		return provider.InteractionFile, provider.EventPermissionRequested
	case "item/permissions/requestApproval":
		return provider.InteractionPermission, provider.EventPermissionRequested
	case "item/tool/requestUserInput", "mcpServer/elicitation/request":
		return provider.InteractionUser, provider.EventUserInputRequested
	case "item/tool/call":
		return provider.InteractionTool, provider.EventToolStarted
	default:
		return "", provider.EventUnknownProvider
	}
}

func (normalizer *EventNormalizer) resolvedKind(params map[string]json.RawMessage) (provider.EventKind, *provider.InteractionCheckpoint) {
	key := rawIDKey(params["requestId"])
	checkpoint, ok := normalizer.interactions[key]
	delete(normalizer.interactions, key)
	if !ok {
		return provider.EventUnknownProvider, nil
	}
	switch checkpoint.Kind {
	case provider.InteractionCommand, provider.InteractionFile, provider.InteractionNetwork, provider.InteractionPermission:
		return provider.EventPermissionResponseRecorded, &checkpoint
	case provider.InteractionUser:
		return provider.EventUserInputResponseRecorded, &checkpoint
	case provider.InteractionTool:
		return provider.EventToolCompleted, &checkpoint
	default:
		return provider.EventUnknownProvider, nil
	}
}

func interactionCheckpoint(requestID json.RawMessage, method string, kind provider.InteractionKind, params json.RawMessage) (provider.InteractionCheckpoint, error) {
	providerRequestID := requestIDKey(requestID)
	if providerRequestID == "" || providerRequestID == "null" {
		return provider.InteractionCheckpoint{}, errors.New("provider request ID is required")
	}
	payload, err := json.Marshal(struct {
		Kind           provider.InteractionKind `json:"kind"`
		ProviderMethod string                   `json:"providerMethod"`
		Params         json.RawMessage          `json:"params"`
	}{Kind: kind, ProviderMethod: method, Params: cloneRaw(params)})
	if err != nil {
		return provider.InteractionCheckpoint{}, err
	}
	digest := sha256.Sum256(payload)
	return provider.InteractionCheckpoint{
		Kind: kind, ProviderRequestID: providerRequestID, ScopeDigest: hex.EncodeToString(digest[:]),
	}, nil
}

func objectParams(raw json.RawMessage) (map[string]json.RawMessage, error) {
	if len(bytes.TrimSpace(raw)) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return map[string]json.RawMessage{}, nil
	}
	var result map[string]json.RawMessage
	if err := json.Unmarshal(raw, &result); err != nil {
		return nil, errors.New("params must be a JSON object")
	}
	if result == nil {
		return map[string]json.RawMessage{}, nil
	}
	return result, nil
}

func rawObject(raw json.RawMessage) (map[string]json.RawMessage, bool) {
	var result map[string]json.RawMessage
	if len(raw) == 0 || json.Unmarshal(raw, &result) != nil || result == nil {
		return nil, false
	}
	return result, true
}

func rawString(raw json.RawMessage) string {
	var result string
	_ = json.Unmarshal(raw, &result)
	return result
}

func requestIDKey(raw json.RawMessage) string { return string(bytes.TrimSpace(raw)) }

func rawIDKey(raw json.RawMessage) string {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return ""
	}
	var text string
	if json.Unmarshal(trimmed, &text) == nil {
		encoded, _ := json.Marshal(text)
		return string(encoded)
	}
	return string(trimmed)
}

func mustRawString(value string) json.RawMessage {
	payload, _ := json.Marshal(value)
	return payload
}
