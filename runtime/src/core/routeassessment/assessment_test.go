package routeassessment_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"darkstar/src/core/routeassessment"
	"darkstar/src/core/workflow"
	"darkstar/src/ports/statestore"
)

func TestSubmissionUsesClosedFindingVariants(t *testing.T) {
	t.Parallel()
	submission := baseSubmission()
	submission.Findings = []routeassessment.Finding{
		routeassessment.InformationFinding{Code: "context_seen", Summary: "Context was supplied.", Evidence: evidence()},
		routeassessment.RecommendationFinding{Code: "research_useful", Summary: "Focused research may change the design.", Evidence: evidence(), RemedyCode: "add_research"},
		routeassessment.PolicyGateFinding{Code: "review_required", Summary: "Review is still required.", Evidence: evidence(), Policy: "owner_review", Status: routeassessment.GateUnsatisfied, RemedyCode: "add_research"},
		routeassessment.InvariantFinding{Code: "scope_preserved", Summary: "The requested terminal remains fixed.", Evidence: evidence(), Invariant: "Explicit scope remains authoritative.", Status: routeassessment.InvariantUpheld},
	}
	content, err := json.Marshal(submission)
	if err != nil {
		t.Fatal(err)
	}
	var decoded routeassessment.Submission
	if err := json.Unmarshal(content, &decoded); err != nil {
		t.Fatal(err)
	}
	levels := []routeassessment.Level{}
	for _, finding := range decoded.Findings {
		levels = append(levels, finding.Level())
	}
	want := []routeassessment.Level{routeassessment.LevelInformation, routeassessment.LevelRecommendation, routeassessment.LevelPolicyGate, routeassessment.LevelInvariant}
	if len(levels) != len(want) {
		t.Fatalf("levels = %#v", levels)
	}
	for index := range want {
		if levels[index] != want[index] {
			t.Fatalf("levels = %#v", levels)
		}
	}

	bad := strings.Replace(string(content), `"summary":"Context was supplied."`, `"summary":"Context was supplied.","unexpected":true`, 1)
	if err := json.Unmarshal([]byte(bad), &decoded); err == nil {
		t.Fatal("unknown finding field was accepted")
	}
}

func TestAssessDerivesDispositionFromExplicitFindingState(t *testing.T) {
	t.Parallel()
	document, state := patchableRouteState(t)
	tests := []struct {
		name     string
		finding  routeassessment.Finding
		expected routeassessment.Disposition
	}{
		{"information", routeassessment.InformationFinding{Code: "context_seen", Summary: "Context was supplied.", Evidence: evidence()}, routeassessment.DispositionReady},
		{"recommendation", routeassessment.RecommendationFinding{Code: "research_useful", Summary: "Research may change the result.", Evidence: evidence(), RemedyCode: "add_research"}, routeassessment.DispositionChoiceRequired},
		{"policy", routeassessment.PolicyGateFinding{Code: "review_required", Summary: "Owner review is missing.", Evidence: evidence(), Policy: "owner_review", Status: routeassessment.GateUnsatisfied, RemedyCode: "add_research"}, routeassessment.DispositionPolicyBlocked},
		{"advisory policy", routeassessment.PolicyGateFinding{Code: "risk_context", Summary: "More risk context would help.", Evidence: evidence(), Policy: "risk_advice", Status: routeassessment.GateUnsatisfied, RemedyCode: "add_research"}, routeassessment.DispositionChoiceRequired},
		{"invariant", routeassessment.InvariantFinding{Code: "scope_lost", Summary: "The explicit scope would be lost.", Evidence: evidence(), Invariant: "Explicit scope remains authoritative.", Status: routeassessment.InvariantViolated, RemedyCode: "add_research"}, routeassessment.DispositionInvariantBlocked},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			submission := baseSubmission()
			submission.Findings = []routeassessment.Finding{test.finding}
			assessment, err := routeassessment.Assess(document, state, workflow.RouteContext{}, strings.Repeat("a", 64), submission)
			if err != nil || assessment.Disposition() != test.expected {
				t.Fatalf("disposition = %s, error = %v", assessment.Disposition(), err)
			}
			if len(assessment.Snapshot().Digest) != 64 {
				t.Fatalf("snapshot = %#v", assessment.Snapshot())
			}
		})
	}
}

func TestAssessRejectsClaimsOutsideAuthoredReadinessContract(t *testing.T) {
	t.Parallel()
	document, state := patchableRouteState(t)
	tests := []routeassessment.Finding{
		routeassessment.RecommendationFinding{Code: "research_useful", Summary: "Research may help.", Evidence: evidence(), RemedyCode: "unknown"},
		routeassessment.PolicyGateFinding{Code: "gate", Summary: "A made-up policy failed.", Evidence: evidence(), Policy: "unknown", Status: routeassessment.GateUnsatisfied, RemedyCode: "add_research"},
		routeassessment.InvariantFinding{Code: "invariant", Summary: "A made-up invariant failed.", Evidence: evidence(), Invariant: "This was never authored.", Status: routeassessment.InvariantViolated, RemedyCode: "add_research"},
	}
	for _, finding := range tests {
		submission := baseSubmission()
		submission.Findings = []routeassessment.Finding{finding}
		_, err := routeassessment.Assess(document, state, workflow.RouteContext{}, strings.Repeat("a", 64), submission)
		var failure *routeassessment.Error
		if !errors.As(err, &failure) || failure.Code != routeassessment.ErrorContractMismatch {
			t.Fatalf("error = %v", err)
		}
	}
}

func TestHumanChoiceIsStoredBeforeRouteProposalIsReleased(t *testing.T) {
	t.Parallel()
	document, state := patchableRouteState(t)
	submission := baseSubmission()
	submission.Findings = []routeassessment.Finding{routeassessment.RecommendationFinding{
		Code: "research_useful", Summary: "Focused review may change the result.", Evidence: evidence(), RemedyCode: "add_research",
	}}
	patch := insertReviewPatch()
	submission.ProposedPatch = &patch
	assessment, err := routeassessment.Assess(document, state, workflow.RouteContext{}, strings.Repeat("a", 64), submission)
	if err != nil {
		t.Fatal(err)
	}
	recorder := &decisionRecorder{}
	decision, proposal, err := assessment.Decide(context.Background(), recorder, routeassessment.DecisionRequest{
		DecisionID: "decision_1", Choice: routeassessment.ChoiceAcceptRouteChange, Reason: "Add the focused review before continuing.",
		Actor: statestore.Actor{Type: statestore.ActorUser, ID: "operator_1"}, DecidedAt: time.Unix(10, 0),
	})
	if err != nil || proposal == nil || len(recorder.values) != 1 {
		t.Fatalf("decision = %#v, proposal = %#v, recorded = %#v, error = %v", decision, proposal, recorder.values, err)
	}
	if proposal.AuthorizationMode() != workflow.RoutePatchRequireApproval || decision.RouteScopeDigest != proposal.ScopeDigest() {
		t.Fatalf("semantic proposal bypassed approval: %#v", decision)
	}
}

func TestViewExposesValidatedRouteChangeAndSupplyInputUsesAuthoredRemedy(t *testing.T) {
	t.Parallel()
	document, state := patchableRouteState(t)
	document.Spec.Nodes["start"].Fields().Readiness.Remedies[0].Action = workflow.ReadinessSupplyInput
	submission := baseSubmission()
	submission.Findings = []routeassessment.Finding{routeassessment.RecommendationFinding{
		Code: "research_useful", Summary: "Focused input may change the result.", Evidence: evidence(), RemedyCode: "add_research",
	}}
	patch := insertReviewPatch()
	submission.ProposedPatch = &patch
	assessment, err := routeassessment.Assess(document, state, workflow.RouteContext{}, strings.Repeat("a", 64), submission)
	if err != nil {
		t.Fatal(err)
	}
	view := assessment.View()
	if view.RouteChange == nil || view.RouteChange.PatchID != patch.Metadata.ID || view.RouteChange.Reason != patch.Spec.Reason ||
		view.RouteChange.Candidate.Revision != 1 || len(view.RouteChange.Impact.AddedNodes) == 0 ||
		view.RouteChange.AuthorizationMode != workflow.RoutePatchRequireApproval || view.RouteChange.PolicyDigest != strings.Repeat("a", 64) ||
		len(view.RouteChange.ScopeDigest) != 64 || len(view.RouteChange.ValidationDigest) != 64 {
		t.Fatalf("route change view = %#v", view.RouteChange)
	}
	recorder := &decisionRecorder{}
	decision, proposal, err := assessment.Decide(context.Background(), recorder, routeassessment.DecisionRequest{
		DecisionID: "decision_1", Choice: routeassessment.ChoiceSupplyInput, RemedyCode: "add_research", Reason: "Supply the requested input.",
		Actor: statestore.Actor{Type: statestore.ActorUser, ID: "operator_1"}, DecidedAt: time.Unix(10, 0),
	})
	if err != nil || proposal != nil || decision.RemedyCode != "add_research" || len(recorder.values) != 1 {
		t.Fatalf("decision = %#v, proposal = %#v, recorded = %#v, err = %v", decision, proposal, recorder.values, err)
	}
	request := routeassessment.DecisionRequest{DecisionID: "decision_2", Choice: routeassessment.ChoiceSupplyInput, RemedyCode: "unknown", Reason: "Supply it.", Actor: statestore.Actor{Type: statestore.ActorUser, ID: "operator_1"}, DecidedAt: time.Unix(10, 0)}
	if _, _, err := assessment.Decide(context.Background(), &decisionRecorder{}, request); err == nil {
		t.Fatal("undeclared supply-input remedy was accepted")
	}
}

func TestStorageFailureAndBlockingFindingsReleaseNoRouteProposal(t *testing.T) {
	t.Parallel()
	document, state := patchableRouteState(t)
	submission := baseSubmission()
	submission.Findings = []routeassessment.Finding{routeassessment.PolicyGateFinding{
		Code: "review_required", Summary: "Owner review is missing.", Evidence: evidence(), Policy: "owner_review", Status: routeassessment.GateUnsatisfied, RemedyCode: "add_research",
	}}
	patch := insertReviewPatch()
	submission.ProposedPatch = &patch
	assessment, err := routeassessment.Assess(document, state, workflow.RouteContext{}, strings.Repeat("a", 64), submission)
	if err != nil {
		t.Fatal(err)
	}
	request := routeassessment.DecisionRequest{
		DecisionID: "decision_1", Choice: routeassessment.ChoiceContinue, Reason: "Continue anyway.",
		Actor: statestore.Actor{Type: statestore.ActorUser, ID: "operator_1"}, DecidedAt: time.Unix(10, 0),
	}
	if _, proposal, err := assessment.Decide(context.Background(), &decisionRecorder{}, request); err == nil || proposal != nil {
		t.Fatalf("blocking continue returned proposal %v, error %v", proposal, err)
	}
	request.Choice = routeassessment.ChoiceAcceptRouteChange
	if _, proposal, err := assessment.Decide(context.Background(), &decisionRecorder{err: errors.New("offline")}, request); err == nil || proposal != nil {
		t.Fatalf("failed recording returned proposal %v, error %v", proposal, err)
	}
}

func TestInformationalAssessmentCannotSmuggleRouteChange(t *testing.T) {
	t.Parallel()
	document, state := patchableRouteState(t)
	submission := baseSubmission()
	patch := insertReviewPatch()
	submission.ProposedPatch = &patch
	_, err := routeassessment.Assess(document, state, workflow.RouteContext{}, strings.Repeat("a", 64), submission)
	var failure *routeassessment.Error
	if !errors.As(err, &failure) || failure.Code != routeassessment.ErrorInvalid {
		t.Fatalf("error = %v", err)
	}
}

type decisionRecorder struct {
	values []routeassessment.Decision
	err    error
}

func (recorder *decisionRecorder) RecordRouteAssessmentDecision(_ context.Context, decision routeassessment.Decision) error {
	if recorder.err != nil {
		return recorder.err
	}
	recorder.values = append(recorder.values, decision)
	return nil
}

func baseSubmission() routeassessment.Submission {
	return routeassessment.Submission{
		AssessmentID: "assessment_1", RunID: "run_1", NodeID: "start",
		Scores:   []routeassessment.Score{{Name: "completeness", Value: 0.8, Evidence: evidence()}},
		Findings: []routeassessment.Finding{routeassessment.InformationFinding{Code: "context_seen", Summary: "Context was supplied.", Evidence: evidence()}},
	}
}

func evidence() []routeassessment.Evidence {
	return []routeassessment.Evidence{{Source: "work:request", Observation: "The requested outcome is explicit."}}
}

func patchableRouteState(t *testing.T) (workflow.Document, workflow.RouteState) {
	t.Helper()
	falseValue := false
	contract := &workflow.ReadinessContract{
		RecommendedEvidence: []workflow.EvidenceRequirement{},
		PolicyGates: []workflow.ReadinessPolicyGate{
			{Policy: "owner_review", Enforcement: workflow.ReadinessGateBlocking, Description: "Owner review is required."},
			{Policy: "risk_advice", Enforcement: workflow.ReadinessGateAdvisory, Description: "Risk context is recommended."},
		},
		Invariants: []string{"Explicit scope remains authoritative."},
		Remedies:   []workflow.ReadinessRemedy{{Code: "add_research", Target: "start", Action: workflow.ReadinessClarifyDecision, Description: "Add a focused review."}},
	}
	document := workflow.Document{
		APIVersion: workflow.APIVersionV1Alpha2, Kind: workflow.KindWorkflow,
		Metadata: workflow.Metadata{Name: "patchable", Version: "1.0.0"},
		Spec: workflow.Spec{
			RouteDefaults: workflow.RouteDefaults{Entry: "start", Terminals: []workflow.Identifier{"end"}},
			Nodes: map[workflow.Identifier]workflow.Node{
				"start": command(workflow.NodeFields{Entry: true, Inputs: map[workflow.Identifier]workflow.Binding{}, Outputs: map[workflow.Identifier]workflow.OutputDeclaration{}, Readiness: contract, TransitionMode: workflow.TransitionExclusive, Transitions: []workflow.Transition{
					normal("direct", "end"),
					workflow.NormalTransition{Common: workflow.TransitionFields{TransitionID: "to_review", To: "review", EnabledByDefault: &falseValue}},
				}}),
				"review": command(workflow.NodeFields{Inputs: map[workflow.Identifier]workflow.Binding{}, Outputs: map[workflow.Identifier]workflow.OutputDeclaration{}, TransitionMode: workflow.TransitionExclusive, Transitions: []workflow.Transition{
					workflow.NormalTransition{Common: workflow.TransitionFields{TransitionID: "review_to_end", To: "end", EnabledByDefault: &falseValue}},
				}}),
				"end": command(workflow.NodeFields{Terminal: true, Inputs: map[workflow.Identifier]workflow.Binding{}, Outputs: map[workflow.Identifier]workflow.OutputDeclaration{}, TransitionMode: workflow.TransitionExclusive, Join: &workflow.Join{Mode: workflow.JoinOne, From: []workflow.Identifier{"direct", "review_to_end"}}}),
			},
		},
	}
	route, issues := workflow.CreateRoute(document, workflow.RouteRequest{}, workflow.RouteContext{})
	if len(issues) != 0 {
		t.Fatal(issues)
	}
	state, err := workflow.NewRouteState("run_1", route)
	if err != nil {
		t.Fatal(err)
	}
	return document, state
}

func insertReviewPatch() workflow.RoutePatch {
	return workflow.RoutePatch{
		APIVersion: workflow.APIVersionV1Alpha1, Kind: workflow.KindRoutePatch,
		Metadata: workflow.RoutePatchMetadata{ID: "patch_1"},
		Spec: workflow.RoutePatchSpec{RunID: "run_1", ExpectedRouteRevision: 0, Reason: "Focused review is likely to change the result.", Operations: []workflow.RoutePatchOperation{
			workflow.DisableTransitionOperation{TransitionID: "direct"},
			workflow.EnableTransitionOperation{TransitionID: "to_review"},
			workflow.EnableTransitionOperation{TransitionID: "review_to_end"},
		}},
	}
}

func command(fields workflow.NodeFields) workflow.CommandNode {
	return workflow.CommandNode{Common: fields, Executor: workflow.CommandExecutor{Argv: []string{"true"}}}
}

func normal(id, target workflow.Identifier) workflow.NormalTransition {
	return workflow.NormalTransition{Common: workflow.TransitionFields{TransitionID: id, To: target}}
}
