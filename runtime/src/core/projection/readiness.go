package projection

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"darkstar/src/ports/statestore"
)

// ReduceReadinessAssessment applies readiness-control events to their distinct
// aggregate projection.
func ReduceReadinessAssessment(current *statestore.ReadinessAssessmentProjection, event statestore.Event) (statestore.ReadinessAssessmentProjection, bool, error) {
	if event.SchemaVersion != 1 {
		return statestore.ReadinessAssessmentProjection{}, false, &UnsupportedSchemaVersionError{EventID: event.ID, Version: event.SchemaVersion}
	}
	if event.AggregateType != statestore.AggregateAssessment {
		return statestore.ReadinessAssessmentProjection{}, false, nil
	}
	if current == nil {
		if event.Kind != "readiness.assessment_recorded" {
			return statestore.ReadinessAssessmentProjection{}, true, fmt.Errorf("readiness assessment %s first event is %s, want readiness.assessment_recorded", event.AggregateID, event.Kind)
		}
		var data struct {
			RunID            string          `json:"runId"`
			NodeID           string          `json:"nodeId"`
			Disposition      string          `json:"disposition"`
			AssessmentDigest string          `json:"assessmentDigest"`
			PolicyDigest     string          `json:"policyDigest"`
			Submission       json.RawMessage `json:"submission"`
			RouteContext     json.RawMessage `json:"routeContext"`
		}
		if err := decodeData(event, &data); err != nil {
			return statestore.ReadinessAssessmentProjection{}, true, err
		}
		if data.RunID == "" || data.NodeID == "" || event.CorrelationID != data.RunID ||
			!validReadinessDisposition(data.Disposition) || !validSourceHash(data.AssessmentDigest) ||
			!validSourceHash(data.PolicyDigest) || !jsonObjectValue(data.Submission) || !jsonObjectValue(data.RouteContext) {
			return statestore.ReadinessAssessmentProjection{}, true, errors.New("readiness.assessment_recorded requires correlated run/node identity, valid disposition and digests, and object snapshots")
		}
		return statestore.ReadinessAssessmentProjection{
			AssessmentID: event.AggregateID, RunID: data.RunID, NodeID: data.NodeID,
			Disposition: data.Disposition, AssessmentDigest: data.AssessmentDigest, PolicyDigest: data.PolicyDigest,
			Submission: statestore.JSONSnapshot(data.Submission), RouteContext: statestore.JSONSnapshot(data.RouteContext),
			Status: statestore.ReadinessAssessmentPending, ResourceVersion: event.AggregateRevision,
			LastGlobalPosition: event.GlobalPosition, CreatedAt: event.RecordedAt, UpdatedAt: event.RecordedAt,
		}, true, nil
	}
	if current.AssessmentID != event.AggregateID {
		return statestore.ReadinessAssessmentProjection{}, true, fmt.Errorf("readiness assessment projection %s cannot apply event for %s", current.AssessmentID, event.AggregateID)
	}
	if event.AggregateRevision != current.ResourceVersion+1 {
		return statestore.ReadinessAssessmentProjection{}, true, fmt.Errorf("readiness assessment %s projection revision %d cannot apply revision %d", current.AssessmentID, current.ResourceVersion, event.AggregateRevision)
	}
	if event.Kind != "readiness.decision_recorded" || current.Status != statestore.ReadinessAssessmentPending {
		return statestore.ReadinessAssessmentProjection{}, true, invalidTransition("readiness assessment", current.AssessmentID, string(current.Status), event.Kind)
	}
	var data struct {
		DecisionID       string                           `json:"decisionId"`
		AssessmentDigest string                           `json:"assessmentDigest"`
		Choice           string                           `json:"choice"`
		RemedyCode       string                           `json:"remedyCode"`
		Reason           string                           `json:"reason"`
		EffectStatus     statestore.ReadinessEffectStatus `json:"effectStatus"`
	}
	if err := decodeData(event, &data); err != nil {
		return statestore.ReadinessAssessmentProjection{}, true, err
	}
	if event.CorrelationID != current.RunID || data.DecisionID == "" || data.AssessmentDigest != current.AssessmentDigest ||
		!validReadinessChoice(data.Choice) || strings.TrimSpace(data.Reason) == "" || data.Reason != strings.TrimSpace(data.Reason) ||
		data.EffectStatus != statestore.ReadinessEffectPending ||
		(event.Actor.Type != statestore.ActorUser && event.Actor.Type != statestore.ActorExternal) {
		return statestore.ReadinessAssessmentProjection{}, true, errors.New("readiness.decision_recorded requires a correlated attributable pending decision bound to the assessment digest")
	}
	if (data.Choice == "supply_input") != (data.RemedyCode != "") {
		return statestore.ReadinessAssessmentProjection{}, true, errors.New("readiness supply_input decisions require exactly one remedyCode")
	}
	next := *current
	next.Status = statestore.ReadinessAssessmentDecided
	next.Decision = &statestore.ReadinessDecisionProjection{
		DecisionID: data.DecisionID, Choice: data.Choice, RemedyCode: data.RemedyCode, Reason: data.Reason,
		EffectStatus: data.EffectStatus, Actor: event.Actor, DecidedAt: event.RecordedAt,
	}
	next.ResourceVersion = event.AggregateRevision
	next.LastGlobalPosition = event.GlobalPosition
	next.UpdatedAt = event.RecordedAt
	return next, true, nil
}

func validReadinessDisposition(value string) bool {
	switch value {
	case "ready", "choice_required", "policy_blocked", "invariant_blocked":
		return true
	default:
		return false
	}
}

func validReadinessChoice(value string) bool {
	switch value {
	case "continue", "accept_route_change", "supply_input", "cancel":
		return true
	default:
		return false
	}
}

func jsonObjectValue(value json.RawMessage) bool {
	var decoded any
	if len(value) == 0 || json.Unmarshal(value, &decoded) != nil {
		return false
	}
	_, ok := decoded.(map[string]any)
	return ok
}
