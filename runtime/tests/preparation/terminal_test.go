package preparation_test

import (
	"reflect"
	"testing"

	"darkstar/src/core/preparation"
	"darkstar/src/core/workflow"
	"darkstar/src/ports/routeadvisor"
)

func terminalFixture() preparation.Input {
	input := fixture()
	plan := input.Workflow.Spec.Nodes["plan"].(workflow.ReasoningNode)
	plan.Common.Terminal = true
	input.Workflow.Spec.Nodes["plan"] = plan
	return input
}

func terminalAdvice(input preparation.Input, suitable func(routeadvisor.Candidate) bool) routeadvisor.Advice {
	request, _ := preparation.Candidates(input)
	result := routeadvisor.Advice{Confidence: "high", EvidenceUsed: []string{}}
	for _, candidate := range request.Candidates {
		disposition := "unsuitable"
		if suitable(candidate) {
			disposition = "suitable"
		}
		result.Candidates = append(result.Candidates, routeadvisor.CandidateAdvice{Entry: candidate.Entry, Terminals: append([]string(nil), candidate.Terminals...), Disposition: disposition, Rationale: "Assessed the complete requested outcome for this entry and terminal boundary.", Questions: []routeadvisor.Question{}, Assumptions: []string{}})
	}
	return result
}

func TestTerminalSelectionSeparatesDesignOnlyAndDeliveryOutcomes(t *testing.T) {
	for _, terminal := range []string{"plan", "review"} {
		t.Run(terminal, func(t *testing.T) {
			input := terminalFixture()
			input.Work.Title = "Complete outcome through " + terminal
			result := terminalAdvice(input, func(candidate routeadvisor.Candidate) bool {
				return candidate.Entry == "plan" && reflect.DeepEqual(candidate.Terminals, []string{terminal})
			})
			assessment, err := preparation.Assess(input, result)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(assessment.Route.Terminals, []workflow.Identifier{workflow.Identifier(terminal)}) {
				t.Fatalf("selected terminal=%v", assessment.Route.Terminals)
			}
			expectedNodes := 1
			if terminal == "review" {
				expectedNodes = 3
			}
			if len(assessment.Route.Nodes) != expectedNodes {
				t.Fatalf("outcome %s included %d nodes", terminal, len(assessment.Route.Nodes))
			}
			if err := preparation.Verify(assessment); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestTerminalAdviceCannotNameUnknownBoundaryOrOmitPairs(t *testing.T) {
	input := terminalFixture()
	result := terminalAdvice(input, func(routeadvisor.Candidate) bool {
		return true
	})
	result.Candidates[0].Terminals = []string{"ghost"}
	if _, err := preparation.Assess(input, result); err == nil {
		t.Fatal("unknown terminal advice accepted")
	}
	result = terminalAdvice(input, func(routeadvisor.Candidate) bool {
		return true
	})
	result.Candidates = result.Candidates[1:]
	if _, err := preparation.Assess(input, result); err == nil {
		t.Fatal("unassessed entry-terminal pair accepted")
	}
}

func TestTerminalTieBreakIsIndependentOfAdviceOrder(t *testing.T) {
	input := terminalFixture()
	result := terminalAdvice(input, func(routeadvisor.Candidate) bool {
		return true
	})
	first, err := preparation.Assess(input, result)
	if err != nil {
		t.Fatal(err)
	}
	for i, j := 0, len(result.Candidates)-1; i < j; i, j = i+1, j-1 {
		result.Candidates[i], result.Candidates[j] = result.Candidates[j], result.Candidates[i]
	}
	second, err := preparation.Assess(input, result)
	if err != nil {
		t.Fatal(err)
	}
	if first.Route.Entry != "plan" || !reflect.DeepEqual(first.Route.Terminals, []workflow.Identifier{"plan"}) || !reflect.DeepEqual(first.Route, second.Route) {
		t.Fatalf("unstable smallest-route tie: %#v / %#v", first.Route, second.Route)
	}
}

func TestProfileMultipleTerminalsAreEnumeratedAndSelectedExactly(t *testing.T) {
	input := terminalFixture()
	build := input.Workflow.Spec.Nodes["build"].(workflow.ReasoningNode)
	build.Common.TransitionMode = workflow.TransitionFanout
	build.Common.Transitions = append(build.Common.Transitions, workflow.NormalTransition{Common: workflow.TransitionFields{TransitionID: "publish_next", To: "publish"}})
	input.Workflow.Spec.Nodes["build"] = build
	input.Workflow.Spec.Nodes["publish"] = workflow.ReasoningNode{Common: workflow.NodeFields{Terminal: true, Inputs: map[workflow.Identifier]workflow.Binding{}, Outputs: map[workflow.Identifier]workflow.OutputDeclaration{}, Checkpoint: workflow.NoCheckpoint{}}, Executor: workflow.ReasoningExecutor{Agent: "test"}}
	input.Workflow.Spec.Profiles = map[workflow.Identifier]workflow.RouteProfile{"complete": {Entry: "plan", Terminals: []workflow.Identifier{"review", "publish"}}}
	request, _ := preparation.Candidates(input)
	count := 0
	for _, candidate := range request.Candidates {
		if candidate.Entry == "plan" && reflect.DeepEqual(candidate.Terminals, []string{"publish", "review"}) {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("multi-terminal profile candidate count=%d", count)
	}
	result := terminalAdvice(input, func(candidate routeadvisor.Candidate) bool {
		return candidate.Entry == "plan" && reflect.DeepEqual(candidate.Terminals, []string{"publish", "review"})
	})
	assessment, err := preparation.Assess(input, result)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(assessment.Route.Terminals, []workflow.Identifier{"publish", "review"}) || len(assessment.Route.Nodes) != 4 {
		t.Fatalf("profile route=%#v", assessment.Route)
	}
}
