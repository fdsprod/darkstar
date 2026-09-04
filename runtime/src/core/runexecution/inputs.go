package runexecution

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"darkstar/src/core/identity"
	"darkstar/src/ports/provider"
	"darkstar/src/ports/statestore"
)

var (
	ErrInputInvalidRequest      = errors.New("invalid input-request answer")
	ErrInputScopeConflict       = errors.New("input-request scope conflict")
	ErrInputAlreadyAnswered     = errors.New("input request is already answered")
	ErrInputAnswerInProgress    = errors.New("a different input answer is already recorded")
	ErrInputDeliveryUnavailable = errors.New("input-request provider delivery is unavailable")
)

var (
	inputRequestIDPattern = regexp.MustCompile(`^input_[0-9A-HJKMNP-TV-Z]{26}$`)
	inputDigestPattern    = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

type InputRequestList struct {
	SchemaVersion int                `json:"schemaVersion"`
	Items         []InputRequestView `json:"items"`
}

type InputRequestAction string

const (
	InputRequestActionAnswer        InputRequestAction = "answer"
	InputRequestActionRetryDelivery InputRequestAction = "retry_delivery"
)

// InputRequestView exposes a request without leaking the durable provider
// idempotency key used to deliver its answer.
type InputRequestView struct {
	ID                 string                             `json:"id"`
	RunID              string                             `json:"runId"`
	AttemptID          string                             `json:"attemptId"`
	NodeID             string                             `json:"nodeId"`
	ProviderThreadID   string                             `json:"providerThreadId"`
	ProviderRequestID  string                             `json:"providerRequestId"`
	ScopeDigest        string                             `json:"scopeDigest"`
	Request            statestore.JSONSnapshot            `json:"request"`
	Status             statestore.InputRequestStatus      `json:"status"`
	Answer             *statestore.InputAnswerProjection  `json:"answer,omitempty"`
	Receipt            *statestore.InputReceiptProjection `json:"receipt,omitempty"`
	AllowedActions     []InputRequestAction               `json:"allowedActions"`
	ResourceVersion    uint64                             `json:"resourceVersion"`
	LastGlobalPosition uint64                             `json:"lastGlobalPosition"`
	CreatedAt          time.Time                          `json:"createdAt"`
	UpdatedAt          time.Time                          `json:"updatedAt"`
}

type AnswerInputRequest struct {
	InputRequestID          string
	ExpectedResourceVersion uint64
	ScopeDigest             string
	Answer                  json.RawMessage
	IdempotencyKey          string
	Actor                   statestore.Actor
}

func (s *Service) InputRequest(ctx context.Context, inputRequestID string) (InputRequestView, error) {
	value, err := s.store.InputRequest(ctx, inputRequestID)
	if err != nil {
		return InputRequestView{}, err
	}
	return inputRequestView(value), nil
}

func (s *Service) InputRequests(ctx context.Context, status statestore.InputRequestStatus) (InputRequestList, error) {
	if status == "" {
		status = statestore.InputRequestPending
	}
	if status != statestore.InputRequestPending && status != statestore.InputRequestAnswerRecorded && status != statestore.InputRequestAnswered {
		return InputRequestList{}, ErrInputInvalidRequest
	}
	values, err := s.store.InputRequests(ctx, status)
	if err != nil {
		return InputRequestList{}, err
	}
	return InputRequestList{SchemaVersion: 1, Items: inputRequestViews(values)}, nil
}

func (s *Service) InputRequestsForRun(ctx context.Context, runID string) (InputRequestList, error) {
	values, err := s.store.InputRequestsForRun(ctx, runID)
	if err != nil {
		return InputRequestList{}, err
	}
	if values == nil {
		values = []statestore.InputRequestProjection{}
	}
	return InputRequestList{SchemaVersion: 1, Items: inputRequestViews(values)}, nil
}

func (s *Service) InputRequestsForAttempt(ctx context.Context, attemptID string) (InputRequestList, error) {
	values, err := s.store.InputRequestsForAttempt(ctx, attemptID)
	if err != nil {
		return InputRequestList{}, err
	}
	if values == nil {
		values = []statestore.InputRequestProjection{}
	}
	return InputRequestList{SchemaVersion: 1, Items: inputRequestViews(values)}, nil
}

// AnswerInput durably records the exact answer before delivering it to the
// live provider. A delivery failure leaves answer_recorded state for a safe
// retry with the same action key and payload.
func (s *Service) AnswerInput(ctx context.Context, request AnswerInputRequest) (InputRequestView, error) {
	if !inputRequestIDPattern.MatchString(request.InputRequestID) || request.ExpectedResourceVersion == 0 ||
		!inputDigestPattern.MatchString(request.ScopeDigest) || len(request.Answer) == 0 || !json.Valid(request.Answer) ||
		strings.TrimSpace(request.IdempotencyKey) == "" || !validInputActor(request.Actor) {
		return InputRequestView{}, ErrInputInvalidRequest
	}
	input, err := s.store.InputRequest(ctx, request.InputRequestID)
	if err != nil {
		return InputRequestView{}, err
	}
	if request.ScopeDigest != input.ScopeDigest {
		return InputRequestView{}, ErrInputScopeConflict
	}
	if input.Answer != nil {
		same := input.Answer.ActionKey == request.IdempotencyKey && bytes.Equal([]byte(input.Answer.Answer), request.Answer) && input.Answer.Actor == request.Actor
		if !same {
			if input.Status == statestore.InputRequestAnswered {
				return InputRequestView{}, ErrInputAlreadyAnswered
			}
			return InputRequestView{}, ErrInputAnswerInProgress
		}
		if input.Status == statestore.InputRequestAnswered {
			return inputRequestView(input), nil
		}
	} else {
		if input.Status != statestore.InputRequestPending || input.ResourceVersion != request.ExpectedResourceVersion {
			return InputRequestView{}, ErrInputScopeConflict
		}
		_, err = s.store.Append(ctx, pendingEvent("input.answer_recorded", statestore.AggregateInput, input.InputRequestID,
			input.ResourceVersion, input.RunID, request.IdempotencyKey, request.Actor.Type, request.Actor.ID, s.now(), map[string]any{
				"scopeDigest": request.ScopeDigest, "answer": json.RawMessage(request.Answer),
			}))
		if err != nil {
			return InputRequestView{}, fmt.Errorf("record input answer: %w", err)
		}
		input, err = s.store.InputRequest(ctx, input.InputRequestID)
		if err != nil {
			return InputRequestView{}, err
		}
	}
	return s.deliverInputAnswer(ctx, input)
}

// RetryInputDelivery retries only the external delivery of an already durable
// answer. The server reuses the hidden provider action key, so a reloaded client
// never needs to recover it and repeated calls remain provider-idempotent.
func (s *Service) RetryInputDelivery(ctx context.Context, inputRequestID string, expectedResourceVersion uint64) (InputRequestView, error) {
	if !inputRequestIDPattern.MatchString(inputRequestID) || expectedResourceVersion == 0 {
		return InputRequestView{}, ErrInputInvalidRequest
	}
	input, err := s.store.InputRequest(ctx, inputRequestID)
	if err != nil {
		return InputRequestView{}, err
	}
	if input.ResourceVersion != expectedResourceVersion {
		return InputRequestView{}, ErrInputScopeConflict
	}
	if input.Status == statestore.InputRequestAnswered {
		return inputRequestView(input), nil
	}
	if input.Status != statestore.InputRequestAnswerRecorded || input.Answer == nil {
		return InputRequestView{}, ErrInputInvalidRequest
	}
	return s.deliverInputAnswer(ctx, input)
}

func (s *Service) deliverInputAnswer(ctx context.Context, input statestore.InputRequestProjection) (InputRequestView, error) {

	s.mu.Lock()
	active := s.workers[input.AttemptID]
	var adapter provider.Provider
	var handle provider.AttemptHandle
	if active != nil {
		adapter, handle = active.adapter, active.handle
	}
	s.mu.Unlock()
	if adapter == nil || handle.AttemptID != input.AttemptID {
		return inputRequestView(input), ErrInputDeliveryUnavailable
	}
	receipt, err := adapter.Respond(ctx, provider.AnswerResponse{InteractionContext: provider.InteractionContext{
		AttemptID: input.AttemptID, ProviderThreadID: input.ProviderThreadID, ProviderRequestID: input.ProviderRequestID,
		IdempotencyKey: input.Answer.ActionKey, ScopeDigest: input.ScopeDigest,
	}, Answer: append(json.RawMessage(nil), input.Answer.Answer...)})
	if err != nil {
		return inputRequestView(input), fmt.Errorf("%w: %v", ErrInputDeliveryUnavailable, err)
	}
	if receipt.ProviderRequestID != input.ProviderRequestID {
		return inputRequestView(input), fmt.Errorf("%w: provider receipt identity changed", ErrInputDeliveryUnavailable)
	}
	_, err = s.store.Append(ctx, pendingEvent("input.answer_delivered", statestore.AggregateInput, input.InputRequestID,
		input.ResourceVersion, input.RunID, "deliver:"+input.Answer.ActionKey, statestore.ActorSystem, "daemon", s.now(),
		map[string]any{"providerRequestId": receipt.ProviderRequestID}))
	if err != nil {
		return inputRequestView(input), fmt.Errorf("record input answer delivery: %w", err)
	}
	value, err := s.store.InputRequest(ctx, input.InputRequestID)
	if err != nil {
		return InputRequestView{}, err
	}
	return inputRequestView(value), nil
}

func inputRequestViews(values []statestore.InputRequestProjection) []InputRequestView {
	views := make([]InputRequestView, len(values))
	for index := range values {
		views[index] = inputRequestView(values[index])
	}
	return views
}

func inputRequestView(value statestore.InputRequestProjection) InputRequestView {
	actions := []InputRequestAction{}
	switch value.Status {
	case statestore.InputRequestPending:
		actions = append(actions, InputRequestActionAnswer)
	case statestore.InputRequestAnswerRecorded:
		actions = append(actions, InputRequestActionRetryDelivery)
	}
	return InputRequestView{ID: value.InputRequestID, RunID: value.RunID, AttemptID: value.AttemptID, NodeID: value.NodeID,
		ProviderThreadID: value.ProviderThreadID, ProviderRequestID: value.ProviderRequestID, ScopeDigest: value.ScopeDigest,
		Request: value.Request, Status: value.Status, Answer: value.Answer, Receipt: value.Receipt, AllowedActions: actions,
		ResourceVersion: value.ResourceVersion, LastGlobalPosition: value.LastGlobalPosition, CreatedAt: value.CreatedAt, UpdatedAt: value.UpdatedAt}
}

func inputRequestedEvent(attempt statestore.AttemptProjection, handle provider.AttemptHandle, event provider.Event, checkpoint provider.InteractionCheckpoint) statestore.PendingEvent {
	id := identity.Deterministic("input_", attempt.AttemptID+"\x00"+checkpoint.ProviderRequestID)
	return pendingEvent("input.requested", statestore.AggregateInput, id, 0, attempt.RunID,
		fmt.Sprintf("input:%s:%d", attempt.AttemptID, event.Sequence), statestore.ActorProvider, event.Provider, event.OccurredAt,
		map[string]any{"runId": attempt.RunID, "attemptId": attempt.AttemptID, "nodeId": attempt.NodeID,
			"providerThreadId": handle.ProviderThreadID, "providerRequestId": checkpoint.ProviderRequestID,
			"scopeDigest": checkpoint.ScopeDigest, "request": json.RawMessage(event.Payload)})
}

func validInputActor(actor statestore.Actor) bool {
	return (actor.Type == statestore.ActorUser || actor.Type == statestore.ActorExternal) &&
		strings.TrimSpace(actor.ID) != "" && actor.ID == strings.TrimSpace(actor.ID)
}
