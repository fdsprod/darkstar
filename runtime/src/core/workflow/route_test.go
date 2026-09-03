package workflow_test

import (
	"encoding/json"
	"reflect"
	"testing"

	"darkstar/src/core/workflow"
)

func TestCreateRouteUsesWorkflowDefaults(t *testing.T) {
	t.Parallel()
	document := basicWorkflow()

	route, errors := workflow.CreateRoute(document, workflow.RouteRequest{}, workflow.RouteContext{})
	if len(errors) != 0 {
		t.Fatalf("CreateRoute() errors = %+v", errors)
	}
	if route.Entry != "start" || !reflect.DeepEqual(route.Terminals, []workflow.Identifier{"end"}) {
		t.Fatalf("route boundaries = %q -> %v", route.Entry, route.Terminals)
	}
	assertRouteNodeIDs(t, route, "end", "start")
	if !reflect.DeepEqual(route.Transitions, []workflow.RouteTransition{{ID: "finish", From: "start", To: "end"}}) {
		t.Fatalf("route transitions = %+v", route.Transitions)
	}
}

func TestCreateRouteSupportsMiddleEntryAndTerminalOnlyRoutes(t *testing.T) {
	t.Parallel()
	document := linearRouteWorkflow()

	route, errors := workflow.CreateRoute(document, workflow.RouteRequest{From: "middle", Until: []workflow.Identifier{"end"}}, workflow.RouteContext{})
	if len(errors) != 0 {
		t.Fatalf("middle-entry route errors = %+v", errors)
	}
	assertRouteNodeIDs(t, route, "end", "middle")
	assertExcluded(t, route, "start", workflow.ExclusionBeforeEntry)

	terminalOnly, errors := workflow.CreateRoute(document, workflow.RouteRequest{From: "middle", Until: []workflow.Identifier{"middle"}}, workflow.RouteContext{})
	if len(errors) != 0 {
		t.Fatalf("terminal-only route errors = %+v", errors)
	}
	assertRouteNodeIDs(t, terminalOnly, "middle")
	if len(terminalOnly.Transitions) != 0 {
		t.Fatalf("terminal-only transitions = %+v", terminalOnly.Transitions)
	}
	assertExcluded(t, terminalOnly, "start", workflow.ExclusionBeforeEntry)
	assertExcluded(t, terminalOnly, "end", workflow.ExclusionPastTerminal)
}

func TestShippedRouteProfilesPreviewThroughOrdinaryValidation(t *testing.T) {
	t.Parallel()

	tests := []struct {
		documentName string
		profile      workflow.Identifier
		entry        workflow.Identifier
		terminal     workflow.Identifier
	}{
		{"software-delivery.json", "idea_to_production", "p0_intake", "p17_verification"},
		{"software-delivery.json", "poc", "p3_poc", "p3_poc"},
		{"software-delivery.json", "prd_only", "p4_requirements", "p4_requirements"},
		{"software-delivery.json", "design_only", "p8_technical_design", "p8_technical_design"},
		{"software-delivery.json", "pr", "p13_pull_request", "p13_pull_request"},
		{"software-delivery.json", "release", "p15_release_readiness", "p17_verification"},
		{"story-execution.json", "accepted_story", "s0_readiness", "s6_validation"},
		{"story-execution.json", "implementation_only", "s5_implementation", "s6_validation"},
		{"story-execution.json", "bug", "s0_readiness", "s6_validation"},
		{"story-execution.json", "validation", "s6_validation", "s6_validation"},
	}
	for _, test := range tests {
		test := test
		t.Run(string(test.profile), func(t *testing.T) {
			document := decodeExample(t, test.documentName)
			route, errors := workflow.CreateProfileRoute(document, test.profile, workflow.RouteContext{})
			if len(errors) != 0 {
				t.Fatalf("profile route errors = %+v", errors)
			}
			if route.Entry != test.entry || !reflect.DeepEqual(route.Terminals, []workflow.Identifier{test.terminal}) {
				t.Fatalf("route boundaries = %s -> %v", route.Entry, route.Terminals)
			}
		})
	}
}

func TestCreateRouteValidatesExplicitBoundaries(t *testing.T) {
	t.Parallel()
	document := basicWorkflow()
	document.Spec.Nodes["isolated"] = command(workflow.NodeFields{
		Entry: true, Terminal: true, Inputs: map[workflow.Identifier]workflow.Binding{},
		Outputs: map[workflow.Identifier]workflow.OutputDeclaration{}, TransitionMode: workflow.TransitionExclusive,
	})

	tests := []struct {
		name    string
		request workflow.RouteRequest
		code    workflow.ValidationCode
	}{
		{name: "missing entry", request: workflow.RouteRequest{From: "ghost", Until: []workflow.Identifier{"end"}}, code: workflow.ValidationRouteEntryInvalid},
		{name: "ineligible entry", request: workflow.RouteRequest{From: "end", Until: []workflow.Identifier{"end"}}, code: workflow.ValidationRouteEntryInvalid},
		{name: "missing terminal", request: workflow.RouteRequest{From: "start", Until: []workflow.Identifier{"ghost"}}, code: workflow.ValidationRouteTerminalInvalid},
		{name: "ineligible terminal", request: workflow.RouteRequest{From: "start", Until: []workflow.Identifier{"start"}}, code: workflow.ValidationRouteTerminalInvalid},
		{name: "unreachable terminal", request: workflow.RouteRequest{From: "start", Until: []workflow.Identifier{"isolated"}}, code: workflow.ValidationRouteTerminalInvalid},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			_, errors := workflow.CreateRoute(document, test.request, workflow.RouteContext{})
			assertValidationCode(t, errors, test.code)
		})
	}
}

func TestCreateRouteRejectsEnabledBranchPastRequestedBoundary(t *testing.T) {
	t.Parallel()
	document := basicWorkflow()
	start := document.Spec.Nodes["start"].(workflow.CommandNode)
	start.Common.TransitionMode = workflow.TransitionFanout
	start.Common.Transitions = []workflow.Transition{normal("to_end", "end"), normal("to_dead", "dead")}
	document.Spec.Nodes["start"] = start
	document.Spec.Nodes["dead"] = command(workflow.NodeFields{
		Terminal: true, Inputs: map[workflow.Identifier]workflow.Binding{},
		Outputs: map[workflow.Identifier]workflow.OutputDeclaration{}, TransitionMode: workflow.TransitionExclusive,
	})
	document.Spec.RouteDefaults.Terminals = []workflow.Identifier{"dead", "end"}

	_, errors := workflow.CreateRoute(document, workflow.RouteRequest{Until: []workflow.Identifier{"end"}}, workflow.RouteContext{})
	assertValidationCode(t, errors, workflow.ValidationRoutePathIncomplete)
	if got := errors[0].Details["nodeIds"]; !reflect.DeepEqual(got, []string{"dead"}) {
		t.Fatalf("stranded nodes = %v", got)
	}
}

func TestCreateRouteIgnoresDisabledTransitions(t *testing.T) {
	t.Parallel()
	document := basicWorkflow()
	start := document.Spec.Nodes["start"].(workflow.CommandNode)
	disabled := false
	start.Common.Transitions = append(start.Common.Transitions, workflow.NormalTransition{Common: workflow.TransitionFields{
		TransitionID: "optional", To: "optional", EnabledByDefault: &disabled,
	}})
	document.Spec.Nodes["start"] = start
	document.Spec.Nodes["optional"] = command(workflow.NodeFields{
		Terminal: true, Inputs: map[workflow.Identifier]workflow.Binding{},
		Outputs: map[workflow.Identifier]workflow.OutputDeclaration{}, TransitionMode: workflow.TransitionExclusive,
	})

	route, errors := workflow.CreateRoute(document, workflow.RouteRequest{}, workflow.RouteContext{})
	if len(errors) != 0 {
		t.Fatalf("CreateRoute() errors = %+v", errors)
	}
	assertRouteNodeIDs(t, route, "end", "start")
	assertExcluded(t, route, "optional", workflow.ExclusionNotConnected)
}

func TestCreateRouteProjectsJoinsOntoIncludedTransitions(t *testing.T) {
	t.Parallel()
	document := joinWorkflow(workflow.TransitionFanout, workflow.JoinAll, true)
	left := document.Spec.Nodes["left"].(workflow.CommandNode)
	left.Common.Entry = true
	document.Spec.Nodes["left"] = left

	route, errors := workflow.CreateRoute(document, workflow.RouteRequest{From: "left"}, workflow.RouteContext{})
	if len(errors) != 0 {
		t.Fatalf("CreateRoute() errors = %+v", errors)
	}
	end := routeNode(t, route, "end")
	if end.Join == nil || end.Join.Mode != workflow.JoinAll || !reflect.DeepEqual(end.Join.From, []workflow.Identifier{"first"}) {
		t.Fatalf("projected join = %+v", end.Join)
	}
}

func TestCreateRouteReturnsStructuredMissingInputs(t *testing.T) {
	t.Parallel()
	document := linearRouteWorkflow()
	middle := document.Spec.Nodes["middle"].(workflow.CommandNode)
	middle.Common.Inputs["prior"] = workflow.RequiredBinding{From: "node.start.output.result", Type: workflow.ValueBoolean}
	document.Spec.Nodes["middle"] = middle

	route, errors := workflow.CreateRoute(document, workflow.RouteRequest{From: "middle"}, workflow.RouteContext{})
	if len(errors) != 0 {
		t.Fatalf("CreateRoute() errors = %+v", errors)
	}
	want := []workflow.InputRequirement{{
		Code: workflow.ValidationRunInputRequired, Node: "middle", Input: "prior", Source: "node.start.output.result",
	}}
	if !reflect.DeepEqual(route.InputRequirements, want) {
		t.Fatalf("input requirements = %+v, want %+v", route.InputRequirements, want)
	}

	context := workflow.RouteContext{AcceptedOutputs: map[workflow.Identifier]map[workflow.Identifier]json.RawMessage{
		"start": {"result": json.RawMessage("true")},
	}}
	route, errors = workflow.CreateRoute(document, workflow.RouteRequest{From: "middle"}, context)
	if len(errors) != 0 || len(route.InputRequirements) != 0 {
		t.Fatalf("accepted output route = %+v, errors = %+v", route, errors)
	}
}

func TestCreateRouteEnforcesRequiredNodePolicy(t *testing.T) {
	t.Parallel()
	document := linearRouteWorkflow()

	_, errors := workflow.CreateRoute(document, workflow.RouteRequest{From: "middle"}, workflow.RouteContext{
		RequiredNodes: []workflow.Identifier{"start"},
	})
	assertValidationCode(t, errors, workflow.ValidationRoutePolicyViolation)
}

func TestCreateRouteIsDeterministicAndPartitionsEveryNode(t *testing.T) {
	t.Parallel()
	document := linearRouteWorkflow()
	document.Spec.Nodes["detached"] = command(workflow.NodeFields{
		Entry: true, Terminal: true, Inputs: map[workflow.Identifier]workflow.Binding{},
		Outputs: map[workflow.Identifier]workflow.OutputDeclaration{}, TransitionMode: workflow.TransitionExclusive,
	})

	first, firstErrors := workflow.CreateRoute(document, workflow.RouteRequest{Until: []workflow.Identifier{"middle"}}, workflow.RouteContext{})
	second, secondErrors := workflow.CreateRoute(document, workflow.RouteRequest{Until: []workflow.Identifier{"middle"}}, workflow.RouteContext{})
	if len(firstErrors) != 0 || len(secondErrors) != 0 || !reflect.DeepEqual(first, second) {
		t.Fatalf("route results differ:\nfirst=%+v errors=%+v\nsecond=%+v errors=%+v", first, firstErrors, second, secondErrors)
	}
	seen := make(map[workflow.Identifier]bool)
	for _, node := range first.Nodes {
		seen[node.ID] = true
	}
	for _, node := range first.ExcludedNodes {
		if seen[node.ID] {
			t.Fatalf("node %q is both executable and excluded", node.ID)
		}
		seen[node.ID] = true
	}
	if len(seen) != len(document.Spec.Nodes) {
		t.Fatalf("partition contains %d nodes, want %d", len(seen), len(document.Spec.Nodes))
	}
}

func linearRouteWorkflow() workflow.Document {
	document := basicWorkflow()
	start := document.Spec.Nodes["start"].(workflow.CommandNode)
	start.Common.Transitions = []workflow.Transition{normal("to_middle", "middle")}
	document.Spec.Nodes["start"] = start
	document.Spec.Nodes["middle"] = command(workflow.NodeFields{
		Entry: true, Terminal: true, Inputs: map[workflow.Identifier]workflow.Binding{},
		Outputs: map[workflow.Identifier]workflow.OutputDeclaration{}, TransitionMode: workflow.TransitionExclusive,
		Transitions: []workflow.Transition{normal("to_end", "end")},
	})
	return document
}

func assertRouteNodeIDs(t *testing.T, route workflow.Route, want ...workflow.Identifier) {
	t.Helper()
	got := make([]workflow.Identifier, len(route.Nodes))
	for index, node := range route.Nodes {
		got[index] = node.ID
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("route nodes = %v, want %v", got, want)
	}
}

func assertExcluded(t *testing.T, route workflow.Route, id workflow.Identifier, reason workflow.ExclusionReason) {
	t.Helper()
	for _, node := range route.ExcludedNodes {
		if node.ID == id {
			if node.Reason != reason {
				t.Fatalf("excluded node %q reason = %q, want %q", id, node.Reason, reason)
			}
			return
		}
	}
	t.Fatalf("excluded node %q not found in %+v", id, route.ExcludedNodes)
}

func routeNode(t *testing.T, route workflow.Route, id workflow.Identifier) workflow.RouteNode {
	t.Helper()
	for _, node := range route.Nodes {
		if node.ID == id {
			return node
		}
	}
	t.Fatalf("route node %q not found", id)
	return workflow.RouteNode{}
}
