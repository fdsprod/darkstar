package preparation_test

import (
	"encoding/json"
	"strings"
	"testing"

	"darkstar/src/core/preparation"
	"darkstar/src/core/workflow"
	"darkstar/src/ports/routeadvisor"
	"darkstar/src/ports/statestore"
)

func fixture() preparation.Input {
	nodes := map[workflow.Identifier]workflow.Node{}
	for _, id := range []workflow.Identifier{"plan", "build", "review"} {
		fields := workflow.NodeFields{Entry: true, Terminal: id == "review", Inputs: map[workflow.Identifier]workflow.Binding{}, Outputs: map[workflow.Identifier]workflow.OutputDeclaration{}, Checkpoint: workflow.NoCheckpoint{}}
		if id != "review" {
			next := workflow.Identifier("review")
			if id == "plan" {
				next = "build"
			}
			fields.Transitions = []workflow.Transition{workflow.NormalTransition{Common: workflow.TransitionFields{TransitionID: workflow.Identifier(string(id) + "_next"), To: next}}}
		}
		nodes[id] = workflow.ReasoningNode{Common: fields, Executor: workflow.ReasoningExecutor{Agent: "test"}}
	}
	return preparation.Input{Work: statestore.WorkItemProjection{Title: "Implement and review the accepted plan", Details: "Acceptance: feature works and review passes", ResourceVersion: 2}, Workflow: workflow.Document{APIVersion: workflow.APIVersionV1Alpha2, Kind: workflow.KindWorkflow, Metadata: workflow.Metadata{Name: "test/flow", Version: "1.0.0"}, Spec: workflow.Spec{Nodes: nodes, RouteDefaults: workflow.RouteDefaults{Entry: "plan", Terminals: []workflow.Identifier{"review"}}}}, WorkflowDigest: strings.Repeat("a", 64), Policy: preparation.Policy{Version: "test-v1"}, Evidence: []routeadvisor.Evidence{}}
}

func advice(input preparation.Input) routeadvisor.Advice {
	request, _ := preparation.Candidates(input)
	result := routeadvisor.Advice{Confidence: "high", EvidenceUsed: []string{}}
	for _, candidate := range request.Candidates {
		disposition := "suitable"
		if candidate.Entry == "review" {
			disposition = "unsuitable"
		}
		result.Candidates = append(result.Candidates, routeadvisor.CandidateAdvice{Entry: candidate.Entry, Terminals: candidate.Terminals, Disposition: disposition, Rationale: "Implementation is required before review.", Questions: []routeadvisor.Question{}, Assumptions: []string{}})
	}
	return result
}

func TestSmallestSafeOutcomeCompleteRouteAndReproduction(t *testing.T) {
	input := fixture()
	first, err := preparation.Assess(input, advice(input))
	if err != nil {
		t.Fatal(err)
	}
	if first.Route.Entry != "build" || first.Readiness() != "ready" {
		t.Fatalf("route = %s, readiness %s", first.Route.Entry, first.Readiness())
	}
	content, _ := json.Marshal(first)
	var restored preparation.Assessment
	if err := json.Unmarshal(content, &restored); err != nil {
		t.Fatal(err)
	}
	if err := preparation.Verify(restored); err != nil {
		t.Fatal(err)
	}
	second, _ := preparation.Assess(input, advice(input))
	if first.Digest != second.Digest {
		t.Fatal("same frozen inputs changed decision")
	}
	input.Work.ResourceVersion++
	third, _ := preparation.Assess(input, advice(input))
	if third.Digest == first.Digest {
		t.Fatal("work revision did not bind decision")
	}
}

func TestProviderCannotAuthorizeInvalidOrUnreadyRoute(t *testing.T) {
	input := fixture()
	input.Policy.RequiredNodes = []workflow.Identifier{"plan"}
	malicious := advice(fixture())
	if _, err := preparation.Assess(input, malicious); err == nil {
		t.Fatal("provider bypassed required project node")
	}
	input = fixture()
	node := input.Workflow.Spec.Nodes["build"].(workflow.ReasoningNode)
	node.Common.Inputs["approved_plan"] = workflow.RequiredBinding{From: "run.input.approved_plan", Type: workflow.ValueObject}
	input.Workflow.Spec.Nodes["build"] = node
	if _, err := preparation.Assess(input, malicious); err == nil {
		t.Fatal("provider bypassed missing required input")
	}
}

func TestMissingInputProducesTargetedQuestion(t *testing.T) {
	input := fixture()
	result := advice(input)
	for i := range result.Candidates {
		result.Candidates[i].Disposition = "input_required"
		result.Candidates[i].Questions = []routeadvisor.Question{{ID: "acceptance", Prompt: "Which acceptance tests must pass?"}}
	}
	assessment, err := preparation.Assess(input, result)
	if err != nil {
		t.Fatal(err)
	}
	if assessment.Readiness() != "input_required" || assessment.Questions[0].ID != "acceptance" {
		t.Fatalf("questions=%#v", assessment.Questions)
	}
}

func TestConfirmationAndTamperDetection(t *testing.T) {
	input := fixture()
	input.Policy.ConsequentialNodes = []workflow.Identifier{"build"}
	result := advice(input)
	result.Confidence = "medium"
	for i := range result.Candidates {
		if result.Candidates[i].Entry == "build" {
			result.Candidates[i].Assumptions = []string{"Deployment is authorized"}
		}
	}
	assessment, err := preparation.Assess(input, result)
	if err != nil {
		t.Fatal(err)
	}
	if assessment.Readiness() != "confirmation_required" || len(assessment.ConfirmationReasons) != 3 {
		t.Fatalf("confirmation=%#v", assessment.ConfirmationReasons)
	}
	assessment.ConfirmationReasons = nil
	if preparation.Verify(assessment) == nil {
		t.Fatal("tampered readiness accepted")
	}
}

func TestOverrideWinsUnlessProhibited(t *testing.T) {
	input := fixture()
	input.Override = &workflow.RouteRequest{From: "plan", Until: []workflow.Identifier{"review"}}
	assessment, err := preparation.Assess(input, routeadvisor.Advice{})
	if err != nil || assessment.Route.Entry != "plan" {
		t.Fatalf("override=%s %v", assessment.Route.Entry, err)
	}
	input.Override.From = "ghost"
	if _, err := preparation.Assess(input, routeadvisor.Advice{}); err == nil {
		t.Fatal("invalid override accepted")
	}
	input.Override.From = "build"
	input.Policy.RequiredNodes = []workflow.Identifier{"plan"}
	if _, err := preparation.Assess(input, routeadvisor.Advice{}); err == nil {
		t.Fatal("prohibited override accepted")
	}
}

func TestUnresolvedEvidenceCannotBeCited(t *testing.T) {
	input := fixture()
	input.Evidence = []routeadvisor.Evidence{{Reference: "https://example.com/plan"}}
	result := advice(input)
	result.EvidenceUsed = []string{input.Evidence[0].Reference}
	if _, err := preparation.Assess(input, result); err == nil {
		t.Fatal("unread reference counted as evidence")
	}
}

func TestAdviceMustNameExactTerminalSet(t *testing.T) {
	input := fixture()
	result := advice(input)
	result.Candidates[0].Terminals = nil
	if _, err := preparation.Assess(input, result); err == nil {
		t.Fatal("missing terminal identity silently defaulted")
	}
}

func TestImmutableAssessmentRejectsEveryDecisionContentChange(t *testing.T) {
	input := fixture()
	original, err := preparation.Assess(input, advice(input))
	if err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*preparation.Assessment){
		"route": func(a *preparation.Assessment) { a.Route.Entry = "review" },
		"question": func(a *preparation.Assessment) {
			a.Questions = []routeadvisor.Question{{ID: "new", Prompt: "Unrecorded question"}}
		},
		"confirmation": func(a *preparation.Assessment) { a.ConfirmationReasons = []string{"Unrecorded authority"} },
		"rationale":    func(a *preparation.Assessment) { a.Rationale = "Unrecorded reason" },
	} {
		t.Run(name, func(t *testing.T) {
			encoded, err := json.Marshal(original)
			if err != nil {
				t.Fatal(err)
			}
			var changed preparation.Assessment
			if err := json.Unmarshal(encoded, &changed); err != nil {
				t.Fatal(err)
			}
			mutate(&changed)
			if preparation.Verify(changed) == nil {
				t.Fatal("tampered immutable content verified")
			}
			// Even recomputing the envelope digest cannot authorize a route
			// or readiness result the deterministic assessment did not derive.
			changed.Digest = ""
			changed.Digest = preparation.Digest(changed)
			if preparation.Verify(changed) == nil {
				t.Fatal("rehashed unauthorized result verified")
			}
		})
	}
}

func TestAssessmentBindsEvidencePolicyAndWorkflowIdentity(t *testing.T) {
	base := fixture()
	base.Evidence = []routeadvisor.Evidence{{Reference: "approved-plan", Digest: strings.Repeat("b", 64), Content: "Approved implementation plan"}}
	assess := func(input preparation.Input) preparation.Assessment {
		result := advice(input)
		result.EvidenceUsed = []string{"approved-plan"}
		a, err := preparation.Assess(input, result)
		if err != nil {
			t.Fatal(err)
		}
		return a
	}
	original := assess(base)
	for name, mutate := range map[string]func(*preparation.Input){
		"policy":   func(i *preparation.Input) { i.Policy.Version = "review-policy-v2" },
		"evidence": func(i *preparation.Input) { i.Evidence[0].Digest = strings.Repeat("c", 64) },
		"workflow": func(i *preparation.Input) { i.WorkflowDigest = strings.Repeat("d", 64) },
	} {
		t.Run(name, func(t *testing.T) {
			changed := base
			changed.Evidence = append([]routeadvisor.Evidence{}, base.Evidence...)
			mutate(&changed)
			next := assess(changed)
			if next.InputDigest == original.InputDigest || next.Digest == original.Digest {
				t.Fatal("changed audit identity reused decision digest")
			}
			if next.Route.Entry != "build" {
				t.Fatal("audit identity changed semantic outcome")
			}
		})
	}
}

func TestUnknownModelCandidateAndLocalGateVersusGlobalPolicy(t *testing.T) {
	input := fixture()
	unknown := advice(input)
	unknown.Candidates = append(unknown.Candidates, routeadvisor.CandidateAdvice{Entry: "nonexistent", Disposition: "suitable", Rationale: "Model invented entry"})
	if _, err := preparation.Assess(input, unknown); err == nil {
		t.Fatal("model invented executable entry")
	}
	plan := input.Workflow.Spec.Nodes["plan"].(workflow.ReasoningNode)
	plan.Common.Readiness = &workflow.ReadinessContract{PolicyGates: []workflow.ReadinessPolicyGate{{Policy: "plan_review", Enforcement: workflow.ReadinessGateBlocking, Description: "Review when planning executes"}}}
	input.Workflow.Spec.Nodes["plan"] = plan
	selected, err := preparation.Assess(input, advice(input))
	if err != nil {
		t.Fatal(err)
	}
	if selected.Route.Entry != "build" {
		t.Fatal("local early-stage gate became unrelated global requirement")
	}
	input.Policy.RequiredNodes = []workflow.Identifier{"plan"}
	selected, err = preparation.Assess(input, advice(input))
	if err != nil {
		t.Fatal(err)
	}
	if selected.Route.Entry != "plan" {
		t.Fatal("global policy node was bypassed")
	}
}

func TestAdvisorReceivesResolvedProjectAndWorkflowContext(t *testing.T) {
	input := fixture()
	input.Project.ProjectID = "project_known"
	input.Project.Name = "Known repository"
	input.Context.RunInputs = map[workflow.Identifier]json.RawMessage{"repository": json.RawMessage(`{"projectId":"project_known"}`), "needs_design": json.RawMessage("false")}
	request, _ := preparation.Candidates(input)
	if request.Context.ProjectID != "project_known" || request.Context.DefaultEntry != "plan" || string(request.Context.RunInputs["needs_design"]) != "false" {
		t.Fatalf("missing context: %#v", request.Context)
	}
	if !strings.Contains(string(request.Candidates[0].Contracts), "execution") {
		t.Fatal("stage execution omitted")
	}
}
func TestClarificationsBelongToOneProposedRoute(t *testing.T) {
	input := fixture()
	a := advice(input)
	for i := range a.Candidates {
		a.Candidates[i].Disposition = "input_required"
		a.Candidates[i].Questions = []routeadvisor.Question{{ID: a.Candidates[i].Entry, Prompt: "Question for " + a.Candidates[i].Entry}}
	}
	result, err := preparation.Assess(input, a)
	if err != nil {
		t.Fatal(err)
	}
	if result.Route.Entry != "plan" || len(result.Questions) != 1 || result.Questions[0].ID != "plan" {
		t.Fatalf("mixed candidate questions: %#v", result.Questions)
	}
}
