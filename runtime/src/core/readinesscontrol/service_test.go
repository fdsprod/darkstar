package readinesscontrol

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"darkstar/src/core/projection"
	"darkstar/src/core/routeassessment"
	"darkstar/src/core/workflow"
	"darkstar/src/ports/statestore"
)

func TestServiceFreezesValidationAndRecordsPendingDecisionEffect(t *testing.T) {
	ctx := context.Background()
	runID := "run_01K3Z1D0000000000000000000"
	digest := strings.Repeat("a", 64)
	document := readinessDocument()
	route := workflow.Route{Entry: "plan", Terminals: []workflow.Identifier{"done"}, Nodes: []workflow.RouteNode{{ID: "plan"}, {ID: "done"}}}
	routeJSON, _ := json.Marshal(route)
	store := &memoryStore{run: statestore.RunProjection{RunID: runID, WorkflowID: "delivery", WorkflowVersion: "1.0.0", WorkflowDigest: digest,
		RouteSnapshot: statestore.JSONSnapshot(routeJSON), Status: statestore.RunRunning}}
	authority := &staticAuthority{actor: statestore.Actor{Type: statestore.ActorProvider, ID: "readiness-provider"}}
	service, err := New(store, staticWorkflows{definition: workflow.Definition{Version: workflow.VersionSummary{Name: "delivery", Version: "1.0.0", Digest: digest}, Document: document}},
		staticValidation{policyDigest: strings.Repeat("b", 64)}, authority)
	if err != nil {
		t.Fatal(err)
	}
	when := time.Date(2026, 9, 3, 14, 0, 0, 0, time.UTC)
	service.now = func() time.Time { return when }
	submission := routeassessment.Submission{AssessmentID: "assessment_01K3Z1D0000000000000000000", RunID: runID, NodeID: "plan",
		Scores:   []routeassessment.Score{{Name: "completeness", Value: .5, Evidence: readinessEvidence()}},
		Findings: []routeassessment.Finding{routeassessment.RecommendationFinding{Code: "needs_requirements", Summary: "Requirements are incomplete.", Evidence: readinessEvidence(), RemedyCode: "requirements"}}}
	view, err := service.Submit(ctx, SubmitRequest{Submission: submission, IdempotencyKey: "submit-1"})
	if err != nil || view.Status != statestore.ReadinessAssessmentPending || len(view.AllowedActions) != 3 || view.ResourceVersion != 1 {
		t.Fatalf("submitted view = %#v, err=%v", view, err)
	}
	replayed, err := service.Submit(ctx, SubmitRequest{Submission: submission, IdempotencyKey: "submit-1"})
	if err != nil || replayed.ResourceVersion != 1 || len(store.events) != 1 {
		t.Fatalf("replayed view = %#v, events=%d, err=%v", replayed, len(store.events), err)
	}
	authority.actor = statestore.Actor{Type: statestore.ActorUser, ID: "operator"}
	if _, err := service.Decide(ctx, DecisionRequest{AssessmentID: submission.AssessmentID, ExpectedResourceVersion: 1,
		ExpectedDigest: strings.Repeat("c", 64), DecisionID: "decision_wrong", Choice: routeassessment.ChoiceContinue,
		Reason: "Use stale evidence.", IdempotencyKey: "decide-stale"}); !errors.Is(err, ErrAssessmentConflict) {
		t.Fatalf("stale digest error = %v", err)
	}
	decisionRequest := DecisionRequest{AssessmentID: submission.AssessmentID, ExpectedResourceVersion: 1,
		ExpectedDigest: view.Assessment.Digest, DecisionID: "decision_1", Choice: routeassessment.ChoiceSupplyInput,
		RemedyCode: "requirements", Reason: "Provide the missing requirements.", IdempotencyKey: "decide-1"}
	decided, err := service.Decide(ctx, decisionRequest)
	if err != nil || decided.Status != statestore.ReadinessAssessmentDecided || decided.Decision == nil ||
		decided.Decision.EffectStatus != statestore.ReadinessEffectPending || decided.Decision.Actor.ID != "operator" ||
		!decided.Decision.DecidedAt.Equal(when) || len(decided.AllowedActions) != 0 || store.run.Status != statestore.RunRunning {
		t.Fatalf("decided view = %#v, run=%#v, err=%v", decided, store.run, err)
	}
	if replayed, err := service.Decide(ctx, decisionRequest); err != nil || replayed.ResourceVersion != 2 || len(store.events) != 2 {
		t.Fatalf("exact decision replay = %#v, events=%d, err=%v", replayed, len(store.events), err)
	}
	changed := decisionRequest
	changed.Reason = "A different reason with the same key."
	if _, err := service.Decide(ctx, changed); !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("changed replay error = %v", err)
	}
	newKey := decisionRequest
	newKey.IdempotencyKey = "decide-2"
	if _, err := service.Decide(ctx, newKey); !errors.Is(err, ErrAlreadyDecided) {
		t.Fatalf("new decision key error = %v", err)
	}
}

func readinessDocument() workflow.Document {
	contract := &workflow.ReadinessContract{RecommendedEvidence: []workflow.EvidenceRequirement{}, PolicyGates: []workflow.ReadinessPolicyGate{}, Invariants: []string{},
		Remedies: []workflow.ReadinessRemedy{{Code: "requirements", Target: "plan", Action: workflow.ReadinessSupplyInput, Description: "Provide requirements."}}}
	fields := func() workflow.NodeFields {
		return workflow.NodeFields{Inputs: map[workflow.Identifier]workflow.Binding{}, Outputs: map[workflow.Identifier]workflow.OutputDeclaration{}, TransitionMode: workflow.TransitionExclusive}
	}
	plan := fields()
	plan.Entry, plan.Readiness = true, contract
	done := fields()
	done.Terminal = true
	return workflow.Document{APIVersion: workflow.APIVersionV1Alpha2, Kind: workflow.KindWorkflow,
		Metadata: workflow.Metadata{Name: "delivery", Version: "1.0.0"}, Spec: workflow.Spec{RouteDefaults: workflow.RouteDefaults{Entry: "plan", Terminals: []workflow.Identifier{"done"}},
			Nodes: map[workflow.Identifier]workflow.Node{"plan": workflow.CommandNode{Common: plan}, "done": workflow.CommandNode{Common: done}}}}
}

func readinessEvidence() []routeassessment.Evidence {
	return []routeassessment.Evidence{{Source: "work:request", Observation: "A requirement is missing."}}
}

type staticAuthority struct{ actor statestore.Actor }

func (value *staticAuthority) Actor(context.Context) (statestore.Actor, error) {
	return value.actor, nil
}

type staticValidation struct{ policyDigest string }

func (value staticValidation) ReadinessValidationContext(context.Context, statestore.RunProjection) (workflow.RouteContext, string, error) {
	return workflow.RouteContext{}, value.policyDigest, nil
}

type staticWorkflows struct{ definition workflow.Definition }

func (value staticWorkflows) Definition(context.Context, string, string) (workflow.Definition, error) {
	return value.definition, nil
}

type memoryStore struct {
	run        statestore.RunProjection
	assessment *statestore.ReadinessAssessmentProjection
	events     []statestore.Event
}

func (store *memoryStore) Run(context.Context, string) (statestore.RunProjection, error) {
	return store.run, nil
}
func (store *memoryStore) ReadinessAssessment(_ context.Context, id string) (statestore.ReadinessAssessmentProjection, error) {
	if store.assessment == nil || store.assessment.AssessmentID != id {
		return statestore.ReadinessAssessmentProjection{}, statestore.ErrNotFound
	}
	return *store.assessment, nil
}
func (store *memoryStore) LatestReadinessAssessmentForRun(context.Context, string) (statestore.ReadinessAssessmentProjection, error) {
	if store.assessment == nil {
		return statestore.ReadinessAssessmentProjection{}, statestore.ErrNotFound
	}
	return *store.assessment, nil
}
func (store *memoryStore) EventByCommand(_ context.Context, aggregateID, commandID string) (statestore.Event, error) {
	for _, event := range store.events {
		if event.AggregateID == aggregateID && event.CommandID == commandID {
			return event, nil
		}
	}
	return statestore.Event{}, statestore.ErrNotFound
}
func (store *memoryStore) Append(_ context.Context, pending ...statestore.PendingEvent) ([]statestore.Event, error) {
	committed := make([]statestore.Event, 0, len(pending))
	for _, item := range pending {
		for _, existing := range store.events {
			if existing.AggregateID == item.AggregateID && existing.CommandID == item.CommandID {
				return []statestore.Event{existing}, nil
			}
		}
		event := statestore.Event{SchemaVersion: item.SchemaVersion, ID: item.ID, AggregateType: item.AggregateType, AggregateID: item.AggregateID,
			AggregateRevision: item.ExpectedRevision + 1, GlobalPosition: uint64(len(store.events) + 1), Kind: item.Kind,
			OccurredAt: item.OccurredAt, RecordedAt: item.OccurredAt, CorrelationID: item.CorrelationID, CommandID: item.CommandID,
			Actor: item.Actor, Data: item.Data, Metadata: item.Metadata}
		next, _, err := projection.ReduceReadinessAssessment(store.assessment, event)
		if err != nil {
			return nil, err
		}
		store.assessment = &next
		store.events = append(store.events, event)
		committed = append(committed, event)
	}
	return committed, nil
}

var _ Store = (*memoryStore)(nil)
