package projection_test

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"darkstar/src/core/projection"
	"darkstar/src/ports/statestore"
)

func TestReadinessAssessmentReducerHasClosedPendingDecidedLifecycle(t *testing.T) {
	t.Parallel()
	when := time.Date(2026, 9, 3, 12, 0, 0, 0, time.UTC)
	id := "assessment_01K3Z1D0000000000000000000"
	recorded := statestore.Event{SchemaVersion: 1, AggregateType: statestore.AggregateAssessment, AggregateID: id, AggregateRevision: 1,
		Kind: "readiness.assessment_recorded", CorrelationID: "run_01K3Z1D0000000000000000000", GlobalPosition: 4, RecordedAt: when,
		Data: json.RawMessage(`{"runId":"run_01K3Z1D0000000000000000000","nodeId":"plan","disposition":"choice_required","assessmentDigest":"` + strings.Repeat("a", 64) + `","policyDigest":"` + strings.Repeat("b", 64) + `","submission":{},"routeContext":{}}`)}
	current, applies, err := projection.ReduceReadinessAssessment(nil, recorded)
	if err != nil || !applies || current.Status != statestore.ReadinessAssessmentPending || current.Decision != nil {
		t.Fatalf("recorded projection = %#v, applies=%v, err=%v", current, applies, err)
	}
	decided := statestore.Event{SchemaVersion: 1, AggregateType: statestore.AggregateAssessment, AggregateID: id, AggregateRevision: 2,
		Kind: "readiness.decision_recorded", CorrelationID: current.RunID, GlobalPosition: 5, RecordedAt: when.Add(time.Second),
		Actor: statestore.Actor{Type: statestore.ActorUser, ID: "operator"},
		Data:  json.RawMessage(`{"decisionId":"decision_1","assessmentDigest":"` + strings.Repeat("a", 64) + `","choice":"supply_input","remedyCode":"requirements","reason":"Provide requirements.","effectStatus":"pending"}`)}
	next, applies, err := projection.ReduceReadinessAssessment(&current, decided)
	if err != nil || !applies || next.Status != statestore.ReadinessAssessmentDecided || next.Decision == nil ||
		next.Decision.Choice != "supply_input" || next.Decision.EffectStatus != statestore.ReadinessEffectPending {
		t.Fatalf("decided projection = %#v, applies=%v, err=%v", next, applies, err)
	}
	if _, _, err := projection.ReduceReadinessAssessment(&next, decided); err == nil {
		t.Fatal("second decision was accepted")
	}
}
