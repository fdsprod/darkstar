package projection

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"darkstar/src/ports/provider"
	"darkstar/src/ports/statestore"
)

func ReduceProviderPermission(current *statestore.ProviderPermissionProjection, event statestore.Event) (statestore.ProviderPermissionProjection, bool, error) {
	if event.SchemaVersion != 1 {
		return statestore.ProviderPermissionProjection{}, false, &UnsupportedSchemaVersionError{EventID: event.ID, Version: event.SchemaVersion}
	}
	if event.AggregateType != statestore.AggregatePermission {
		return statestore.ProviderPermissionProjection{}, false, nil
	}
	if current == nil {
		if event.Kind != "permission.requested" {
			return statestore.ProviderPermissionProjection{}, true, fmt.Errorf("provider permission %s must begin with permission.requested", event.AggregateID)
		}
		var data struct {
			RunID, AttemptID, NodeID, ProviderThreadID, ProviderTurnID, ProviderRequestID, InteractionKind, ScopeDigest, PolicyDigest string
			Scope, Evidence                                                                                                           json.RawMessage
		}
		if err := decodeData(event, &data); err != nil {
			return statestore.ProviderPermissionProjection{}, true, err
		}
		var scope provider.InteractionScope
		if data.RunID == "" || data.AttemptID == "" || data.NodeID == "" || data.ProviderThreadID == "" || data.ProviderTurnID == "" || data.ProviderRequestID == "" ||
			event.CorrelationID != data.RunID || !validSourceHash(data.ScopeDigest) || !validSourceHash(data.PolicyDigest) || !permissionKind(data.InteractionKind) ||
			decodeClosedJSON(data.Scope, &scope) != nil || !provider.ValidInteractionScope(provider.InteractionKind(data.InteractionKind), scope) || !validPermissionEvidence(data.Evidence, data.InteractionKind) {
			return statestore.ProviderPermissionProjection{}, true, errors.New("permission.requested requires complete subject, scope, and redacted evidence")
		}
		return statestore.ProviderPermissionProjection{PermissionRequestID: event.AggregateID, RunID: data.RunID, AttemptID: data.AttemptID, NodeID: data.NodeID,
			ProviderThreadID: data.ProviderThreadID, ProviderTurnID: data.ProviderTurnID, ProviderRequestID: data.ProviderRequestID, InteractionKind: data.InteractionKind,
			Scope: statestore.JSONSnapshot(data.Scope), ScopeDigest: data.ScopeDigest, PolicyDigest: data.PolicyDigest,
			Evidence: statestore.JSONSnapshot(data.Evidence), Status: statestore.ProviderPermissionPending, ResourceVersion: event.AggregateRevision,
			LastGlobalPosition: event.GlobalPosition, CreatedAt: event.RecordedAt, UpdatedAt: event.RecordedAt}, true, nil
	}
	if current.PermissionRequestID != event.AggregateID || event.AggregateRevision != current.ResourceVersion+1 {
		return statestore.ProviderPermissionProjection{}, true, errors.New("provider permission aggregate revision conflict")
	}
	next := *current
	switch event.Kind {
	case "permission.decision_recorded":
		if current.Status != statestore.ProviderPermissionPending {
			return statestore.ProviderPermissionProjection{}, true, invalidTransition("provider permission", current.PermissionRequestID, string(current.Status), event.Kind)
		}
		var data struct{ ScopeDigest, Decision string }
		if err := decodeData(event, &data); err != nil {
			return statestore.ProviderPermissionProjection{}, true, err
		}
		if data.ScopeDigest != current.ScopeDigest || !permissionDecision(data.Decision) || (event.Actor.Type != statestore.ActorUser && event.Actor.Type != statestore.ActorExternal) {
			return statestore.ProviderPermissionProjection{}, true, errors.New("permission decision requires exact scope, legal action, and actor")
		}
		next.Status = statestore.ProviderPermissionDecisionRecorded
		next.Decision = &statestore.ProviderPermissionDecisionProjection{Decision: data.Decision, ActionKey: event.CommandID, Actor: event.Actor, RecordedAt: event.RecordedAt}
	case "permission.response_delivered":
		if current.Status != statestore.ProviderPermissionDecisionRecorded || current.Decision == nil {
			return statestore.ProviderPermissionProjection{}, true, invalidTransition("provider permission", current.PermissionRequestID, string(current.Status), event.Kind)
		}
		var data struct{ ProviderRequestID string }
		if err := decodeData(event, &data); err != nil {
			return statestore.ProviderPermissionProjection{}, true, err
		}
		if data.ProviderRequestID != current.ProviderRequestID || (event.Actor.Type != statestore.ActorProvider && event.Actor.Type != statestore.ActorSystem) {
			return statestore.ProviderPermissionProjection{}, true, errors.New("permission delivery requires matching provider identity")
		}
		next.Status = statestore.ProviderPermissionResponded
		next.Receipt = &statestore.ProviderPermissionReceiptProjection{ProviderRequestID: data.ProviderRequestID, DeliveredAt: event.RecordedAt}
	default:
		return statestore.ProviderPermissionProjection{}, true, invalidTransition("provider permission", current.PermissionRequestID, string(current.Status), event.Kind)
	}
	next.ResourceVersion, next.LastGlobalPosition, next.UpdatedAt = event.AggregateRevision, event.GlobalPosition, event.RecordedAt
	return next, true, nil
}

func decodeClosedJSON(raw json.RawMessage, destination any) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return errors.New("JSON value has trailing content")
	}
	return nil
}

func validPermissionEvidence(raw json.RawMessage, kind string) bool {
	var evidence struct {
		Kind               string `json:"kind"`
		Summary            string `json:"summary"`
		PayloadDigest      string `json:"payloadDigest"`
		ProviderItemDigest string `json:"providerItemDigest"`
	}
	return decodeClosedJSON(raw, &evidence) == nil && evidence.Kind == kind && evidence.Summary == "Provider requested a "+kind+" interaction" &&
		validSourceHash(evidence.PayloadDigest) && validSourceHash(evidence.ProviderItemDigest)
}

func permissionKind(value string) bool {
	switch value {
	case "command", "file", "network", "permission", "tool":
		return true
	}
	return false
}
func permissionDecision(value string) bool {
	switch value {
	case "allow_once", "deny", "cancel":
		return true
	}
	return false
}
