package projection

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"darkstar/src/ports/provider"
	"darkstar/src/ports/statestore"
)

// ReduceInputRequest applies the closed pending -> answer_recorded -> answered
// lifecycle for provider user-input questions.
func ReduceInputRequest(current *statestore.InputRequestProjection, event statestore.Event) (statestore.InputRequestProjection, bool, error) {
	if event.SchemaVersion != 1 {
		return statestore.InputRequestProjection{}, false, &UnsupportedSchemaVersionError{EventID: event.ID, Version: event.SchemaVersion}
	}
	if event.AggregateType != statestore.AggregateInput {
		return statestore.InputRequestProjection{}, false, nil
	}
	if current == nil {
		if event.Kind != "input.requested" {
			return statestore.InputRequestProjection{}, true, fmt.Errorf("input request %s first event is %s, want input.requested", event.AggregateID, event.Kind)
		}
		var data struct {
			RunID             string          `json:"runId"`
			AttemptID         string          `json:"attemptId"`
			NodeID            string          `json:"nodeId"`
			ProviderThreadID  string          `json:"providerThreadId"`
			ProviderRequestID string          `json:"providerRequestId"`
			ScopeDigest       string          `json:"scopeDigest"`
			Request           json.RawMessage `json:"request"`
		}
		if err := decodeData(event, &data); err != nil {
			return statestore.InputRequestProjection{}, true, err
		}
		var request provider.UserInputRequest
		if data.RunID == "" || data.AttemptID == "" || data.NodeID == "" || data.ProviderThreadID == "" ||
			strings.TrimSpace(data.ProviderRequestID) == "" || data.ProviderRequestID != strings.TrimSpace(data.ProviderRequestID) ||
			event.CorrelationID != data.RunID || !validSourceHash(data.ScopeDigest) || decodeClosedJSON(data.Request, &request) != nil || !provider.ValidUserInputRequest(request) {
			return statestore.InputRequestProjection{}, true, errors.New("input.requested requires correlated run, attempt, node, provider identities, scope digest, and an object request")
		}
		return statestore.InputRequestProjection{
			InputRequestID: event.AggregateID, RunID: data.RunID, AttemptID: data.AttemptID, NodeID: data.NodeID,
			ProviderThreadID: data.ProviderThreadID, ProviderRequestID: data.ProviderRequestID,
			ScopeDigest: data.ScopeDigest, Request: statestore.JSONSnapshot(data.Request), Status: statestore.InputRequestPending,
			ResourceVersion: event.AggregateRevision, LastGlobalPosition: event.GlobalPosition,
			CreatedAt: event.RecordedAt, UpdatedAt: event.RecordedAt,
		}, true, nil
	}
	if current.InputRequestID != event.AggregateID || event.AggregateRevision != current.ResourceVersion+1 {
		return statestore.InputRequestProjection{}, true, fmt.Errorf("input request %s cannot apply aggregate %s revision %d", current.InputRequestID, event.AggregateID, event.AggregateRevision)
	}
	next := *current
	switch event.Kind {
	case "input.answer_recorded":
		if current.Status != statestore.InputRequestPending {
			return statestore.InputRequestProjection{}, true, invalidTransition("input request", current.InputRequestID, string(current.Status), event.Kind)
		}
		var data struct {
			ScopeDigest string          `json:"scopeDigest"`
			Answer      json.RawMessage `json:"answer"`
		}
		if err := decodeData(event, &data); err != nil {
			return statestore.InputRequestProjection{}, true, err
		}
		if data.ScopeDigest != current.ScopeDigest || len(data.Answer) == 0 || !json.Valid(data.Answer) ||
			(event.Actor.Type != statestore.ActorUser && event.Actor.Type != statestore.ActorExternal) {
			return statestore.InputRequestProjection{}, true, errors.New("input.answer_recorded requires exact scope, valid JSON, and attributable actor")
		}
		next.Status = statestore.InputRequestAnswerRecorded
		next.Answer = &statestore.InputAnswerProjection{Answer: statestore.JSONSnapshot(data.Answer), ActionKey: event.CommandID, Actor: event.Actor, RecordedAt: event.RecordedAt}
	case "input.answer_delivered":
		if current.Status != statestore.InputRequestAnswerRecorded || current.Answer == nil {
			return statestore.InputRequestProjection{}, true, invalidTransition("input request", current.InputRequestID, string(current.Status), event.Kind)
		}
		var data struct {
			ProviderRequestID string `json:"providerRequestId"`
		}
		if err := decodeData(event, &data); err != nil {
			return statestore.InputRequestProjection{}, true, err
		}
		if data.ProviderRequestID != current.ProviderRequestID || (event.Actor.Type != statestore.ActorProvider && event.Actor.Type != statestore.ActorSystem) {
			return statestore.InputRequestProjection{}, true, errors.New("input.answer_delivered requires matching provider request and provider/system actor")
		}
		next.Status = statestore.InputRequestAnswered
		next.Receipt = &statestore.InputReceiptProjection{ProviderRequestID: data.ProviderRequestID, DeliveredAt: event.RecordedAt}
	default:
		return statestore.InputRequestProjection{}, true, invalidTransition("input request", current.InputRequestID, string(current.Status), event.Kind)
	}
	next.ResourceVersion = event.AggregateRevision
	next.LastGlobalPosition = event.GlobalPosition
	next.UpdatedAt = event.RecordedAt
	return next, true, nil
}
