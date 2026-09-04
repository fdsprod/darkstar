package sqlite

import (
	"context"
	"encoding/json"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"darkstar/src/ports/statestore"
)

func TestReadinessAssessmentPersistsQueriesAndRebuilds(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	database, err := Open(ctx, filepath.Join(t.TempDir(), "readiness.db"), Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = database.Close() }()
	assessmentID := "assessment_01K3Z1D0000000000000000000"
	runID := "run_01K3Z1D0000000000000000000"
	digest := strings.Repeat("a", 64)
	when := time.Date(2026, 9, 3, 12, 0, 0, 0, time.UTC)
	recordedData := json.RawMessage(`{"runId":"` + runID + `","nodeId":"plan","disposition":"choice_required","assessmentDigest":"` + digest + `","policyDigest":"` + strings.Repeat("b", 64) + `","submission":{"assessmentId":"source"},"routeContext":{}}`)
	_, err = database.Append(ctx, statestore.PendingEvent{SchemaVersion: 1, ID: "event_01K3Z1D0000000000000000000", AggregateType: statestore.AggregateAssessment,
		AggregateID: assessmentID, ExpectedRevision: 0, Kind: "readiness.assessment_recorded", OccurredAt: when, CorrelationID: runID,
		CommandID: "submit", Actor: statestore.Actor{Type: statestore.ActorProvider, ID: "provider"}, Data: recordedData, Metadata: json.RawMessage(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	decisionData := json.RawMessage(`{"decisionId":"decision_1","assessmentDigest":"` + digest + `","choice":"continue","reason":"Proceed with current route.","effectStatus":"pending"}`)
	_, err = database.Append(ctx, statestore.PendingEvent{SchemaVersion: 1, ID: "event_01K3Z1D0000000000000000001", AggregateType: statestore.AggregateAssessment,
		AggregateID: assessmentID, ExpectedRevision: 1, Kind: "readiness.decision_recorded", OccurredAt: when.Add(time.Second), CorrelationID: runID,
		CommandID: "decide", Actor: statestore.Actor{Type: statestore.ActorUser, ID: "operator"}, Data: decisionData, Metadata: json.RawMessage(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	want, err := database.ReadinessAssessment(ctx, assessmentID)
	if err != nil || want.Status != statestore.ReadinessAssessmentDecided || want.Decision == nil || want.Decision.EffectStatus != statestore.ReadinessEffectPending {
		t.Fatalf("assessment = %#v, err=%v", want, err)
	}
	latest, err := database.LatestReadinessAssessmentForRun(ctx, runID)
	if err != nil || !reflect.DeepEqual(latest, want) {
		t.Fatalf("latest = %#v, err=%v", latest, err)
	}
	if err := database.RebuildProjections(ctx); err != nil {
		t.Fatal(err)
	}
	rebuilt, err := database.ReadinessAssessment(ctx, assessmentID)
	if err != nil || !reflect.DeepEqual(rebuilt, want) {
		t.Fatalf("rebuilt = %#v, err=%v", rebuilt, err)
	}
}
