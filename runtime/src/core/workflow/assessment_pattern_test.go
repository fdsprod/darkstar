package workflow

import (
	"encoding/json"
	"strings"
	"testing"
)

func assessmentFixture(t *testing.T) AssessmentRouterPattern {
	t.Helper()
	ids := []Identifier{"questions", "research", "design", "technical_design", "plan"}
	nodes := map[Identifier]Node{}
	for index, id := range ids {
		common := NodeFields{Entry: index == 0, Terminal: index == 4, Inputs: map[Identifier]Binding{}, Outputs: map[Identifier]OutputDeclaration{}, Checkpoint: NoCheckpoint{}}
		if index < 4 {
			common.Transitions = []Transition{NormalTransition{Common: TransitionFields{TransitionID: Identifier(string(id) + "_next"), To: ids[index+1]}}}
		}
		nodes[id] = ReasoningNode{Common: common, Executor: ReasoningExecutor{Agent: "writer"}}
	}
	doc := Document{APIVersion: APIVersionV1Alpha3, Kind: KindWorkflow, Metadata: Metadata{Name: "test/planning", Version: "1.0.0"}, Spec: Spec{
		Inputs:        map[Identifier]ValueDeclaration{"task": {Type: ValueTask}, "unrelated_secret": {Type: ValueString}},
		RouteDefaults: RouteDefaults{Entry: "questions", Terminals: []Identifier{"plan"}}, Nodes: nodes,
	}}
	encoded, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	return AssessmentRouterPattern{Document: encoded, Targets: AssessmentTargets{Questions: "questions", Research: "research", Design: "design", TechnicalDesign: "technical_design", Plan: "plan"}}
}

func TestAssessmentPatternPreservesScopedInputsAndRequiredHumanReviews(t *testing.T) {
	request := assessmentFixture(t)
	result, err := BuildAssessmentRouter(request)
	if err != nil {
		t.Fatal(err)
	}
	doc, err := Decode(result.Document)
	if err != nil {
		t.Fatal(err)
	}
	if issues := draftConnectionIssues(result.Document); len(issues) != 0 {
		t.Fatalf("assessment pattern has unpublishable ports: %v", issues)
	}
	assessment := doc.Spec.Nodes[result.AssessmentID]
	if len(assessment.Fields().Inputs) != 2 || assessment.Fields().Inputs["task"] == nil || assessment.Fields().Inputs["template"] == nil {
		t.Fatalf("unexpected assessment context: %#v", assessment.Fields().Inputs)
	}
	if len(assessment.Fields().Transitions) != 1 || doc.Spec.Nodes[assessment.Fields().Transitions[0].Target()].Type() != NodeGate {
		t.Fatal("assessment can route before a durable gate visit")
	}
	for _, target := range []Identifier{"design", "technical_design", "plan"} {
		if doc.Spec.Nodes[target].Fields().Checkpoint.Mode() != CheckpointApprove {
			t.Fatalf("%s lacks human review", target)
		}
	}
	if string(request.Document) == string(result.Document) || strings.Contains(string(request.Document), "assessment_questions_gate") {
		t.Fatal("source draft mutated")
	}
	for range 10 {
		repeated, err := BuildAssessmentRouter(request)
		if err != nil || string(repeated.Document) != string(result.Document) {
			t.Fatal("pattern generation is nondeterministic", err)
		}
	}
}

func TestAssessmentPatternRequiresDistinctTargetsAndUnusedIDs(t *testing.T) {
	request := assessmentFixture(t)
	request.Targets.Plan = "design"
	if _, err := BuildAssessmentRouter(request); err == nil {
		t.Fatal("duplicate stage target accepted")
	}
	request = assessmentFixture(t)
	request.ID = "questions"
	if _, err := BuildAssessmentRouter(request); err == nil {
		t.Fatal("existing node overwritten")
	}
	request = assessmentFixture(t)
	request.InputNames = []Identifier{"not_present"}
	if _, err := BuildAssessmentRouter(request); err == nil {
		t.Fatal("missing explicitly selected input accepted")
	}
}

func TestAssessmentPatternSelectsEarliestMissingStageAndHonorsHumanApproval(t *testing.T) {
	result, err := BuildAssessmentRouter(assessmentFixture(t))
	if err != nil {
		t.Fatal(err)
	}
	doc, err := Decode(result.Document)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name     string
		ready    [4]bool
		approved [2]bool
		want     Identifier
	}{
		{"questions", [4]bool{false, true, true, true}, [2]bool{true, true}, "questions"},
		{"research", [4]bool{true, false, true, true}, [2]bool{true, true}, "research"},
		{"product", [4]bool{true, true, false, true}, [2]bool{true, true}, "design"},
		{"technical", [4]bool{true, true, true, false}, [2]bool{true, true}, "technical_design"},
		{"plan", [4]bool{true, true, true, true}, [2]bool{true, true}, "plan"},
		{"model_cannot_approve_product", [4]bool{true, true, true, true}, [2]bool{false, true}, "design"},
		{"model_cannot_approve_technical", [4]bool{true, true, true, true}, [2]bool{true, false}, "technical_design"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			readiness, _ := json.Marshal(map[string]bool{"questions_ready": tc.ready[0], "research_ready": tc.ready[1], "design_ready": tc.ready[2], "technical_design_ready": tc.ready[3]})
			at := result.GateIDs[0]
			for index := 0; index < 4; index++ {
				gate, ok := doc.Spec.Nodes[at].(GateNode)
				if !ok {
					break
				}
				inputs := map[Identifier]json.RawMessage{"readiness": readiness}
				if index >= 2 {
					inputs["approved"], _ = json.Marshal(tc.approved[index-2])
				}
				passed, err := EvaluatePredicate(gate.Executor.Condition, PredicateValues{Inputs: inputs}, "gate")
				if err != nil {
					t.Fatal(err)
				}
				output, _ := json.Marshal(passed)
				matches := 0
				for _, raw := range gate.Common.Transitions {
					transition := raw.(NormalTransition)
					match, err := EvaluatePredicate(transition.Common.When, PredicateValues{Outputs: map[Identifier]json.RawMessage{"passed": output}}, "transition")
					if err != nil {
						t.Fatal(err)
					}
					if match {
						matches++
						at = transition.Target()
					}
				}
				if matches != 1 {
					t.Fatal("ambiguous route")
				}
			}
			if at != tc.want {
				t.Fatalf("route selected %s, want %s", at, tc.want)
			}
		})
	}
}
