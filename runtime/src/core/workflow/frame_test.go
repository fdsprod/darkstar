package workflow_test

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"darkstar/src/core/workflow"
)

func TestBoundedTransitionIsIdempotentAndFailsAtItsFrameLimit(t *testing.T) {
	t.Parallel()
	frame := rootFrame(t, "frame_root")
	transition := workflow.BoundedTransition{
		Common:        workflow.TransitionFields{TransitionID: "repair", To: "implement"},
		MaxTraversals: 2,
	}

	first, replayed, err := frame.FireTransition("visit_validate_1", "validate", 1, transition)
	if err != nil || replayed || first.Traversal != 1 {
		t.Fatalf("first traversal = %#v, replayed %t, error %v", first, replayed, err)
	}
	replay, replayed, err := frame.FireTransition("visit_validate_1", "validate", 1, transition)
	if err != nil || !replayed || replay != first {
		t.Fatalf("replay = %#v, replayed %t, error %v", replay, replayed, err)
	}
	changedLimit := transition
	changedLimit.MaxTraversals = 3
	if _, _, err := frame.FireTransition("visit_validate_1", "validate", 1, changedLimit); err == nil {
		t.Fatal("replay with a changed traversal limit was accepted")
	}
	excluded := workflow.NormalTransition{Common: workflow.TransitionFields{TransitionID: "unfrozen", To: "implement"}}
	if _, _, err := frame.FireTransition("visit_validate_1", "validate", 1, excluded); err == nil {
		t.Fatal("transition outside the frozen route was accepted")
	}
	second, replayed, err := frame.FireTransition("visit_validate_2", "validate", 2, transition)
	if err != nil || replayed || second.Traversal != 2 {
		t.Fatalf("second traversal = %#v, replayed %t, error %v", second, replayed, err)
	}

	_, _, err = frame.FireTransition("visit_validate_3", "validate", 3, transition)
	var frameError *workflow.FrameError
	if !errors.As(err, &frameError) || frameError.Code != workflow.RunLoopLimitExhausted || frameError.TransitionID != "repair" {
		t.Fatalf("exhaustion error = %#v", err)
	}
	if got := frame.TraversalCount("repair"); got != 2 {
		t.Fatalf("traversal count after exhaustion = %d, want 2", got)
	}
}

func TestFrameSnapshotRestoresTokensWithoutDoubleConsumption(t *testing.T) {
	t.Parallel()
	frame := rootFrame(t, "frame_root")
	transition := workflow.BoundedTransition{
		Common: workflow.TransitionFields{TransitionID: "repair", To: "implement"}, MaxTraversals: 2,
	}
	if _, _, err := frame.FireTransition("visit_validate_1", "validate", 1, transition); err != nil {
		t.Fatal(err)
	}
	restored, err := workflow.RestoreFrame(frame.Snapshot())
	if err != nil {
		t.Fatal(err)
	}
	if _, replayed, err := restored.FireTransition("visit_validate_1", "validate", 1, transition); err != nil || !replayed {
		t.Fatalf("restored replay: replayed %t, error %v", replayed, err)
	}
	if token, replayed, err := restored.FireTransition("visit_validate_2", "validate", 2, transition); err != nil || replayed || token.Traversal != 2 {
		t.Fatalf("restored second traversal = %#v, replayed %t, error %v", token, replayed, err)
	}

	broken := frame.Snapshot()
	broken.Tokens[0].Traversal = 2
	if _, err := workflow.RestoreFrame(broken); err == nil {
		t.Fatal("snapshot with a gap in traversal ordinals was accepted")
	}
}

func TestFrameSnapshotJSONPreservesTheClosedOrigin(t *testing.T) {
	t.Parallel()
	snapshot := rootFrame(t, "frame_root").Snapshot()
	encoded, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	var decoded workflow.FrameSnapshot
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	origin, ok := decoded.Origin.(workflow.RootFrameOrigin)
	if !ok || origin.RunID != "run_1" {
		t.Fatalf("decoded origin = %#v", decoded.Origin)
	}
	if _, err := workflow.RestoreFrame(decoded); err != nil {
		t.Fatal(err)
	}

	contradictory := strings.Replace(string(encoded), `"origin":"root"`, `"origin":"child","parentFrameId":"parent","parentVisitId":"visit"`, 1)
	if err := json.Unmarshal([]byte(contradictory), &decoded); err == nil {
		t.Fatal("contradictory root/child origin was accepted")
	}
}

func TestSubworkflowFramesCopyInputsAndKeepSiblingBudgetsIsolated(t *testing.T) {
	t.Parallel()
	parent := rootFrame(t, "frame_root")
	child := childDefinition()
	call := childNode()
	parentInputs := map[workflow.Identifier]json.RawMessage{"story": json.RawMessage(`{"id":"DAR-41"}`)}
	route := childRoute()

	first, err := parent.StartSubworkflow("frame_child_1", "visit_story_1", call, child, route, parentInputs)
	if err != nil {
		t.Fatal(err)
	}
	second, err := parent.StartSubworkflow("frame_child_2", "visit_story_2", call, child, route, parentInputs)
	if err != nil {
		t.Fatal(err)
	}
	parentInputs["story"][7] = 'X'
	if got := string(first.Snapshot().Inputs["request"]); got != `{"id":"DAR-41"}` {
		t.Fatalf("child input changed through parent alias: %s", got)
	}

	repair := workflow.BoundedTransition{Common: workflow.TransitionFields{TransitionID: "repair", To: "implement"}, MaxTraversals: 1}
	if _, _, err := first.FireTransition("visit_validate_1", "validate", 1, repair); err != nil {
		t.Fatal(err)
	}
	if _, _, err := first.FireTransition("visit_validate_2", "validate", 2, repair); err == nil {
		t.Fatal("first child exceeded its budget without error")
	}
	if token, _, err := second.FireTransition("visit_validate_1", "validate", 1, repair); err != nil || token.Traversal != 1 {
		t.Fatalf("sibling frame inherited traversal count: token %#v, error %v", token, err)
	}
}

func TestSubworkflowRejectsIdentityMismatchAndRecursion(t *testing.T) {
	t.Parallel()
	parent := rootFrame(t, "frame_root")
	child := childDefinition()
	call := childNode()
	route := childRoute()
	inputs := map[workflow.Identifier]json.RawMessage{"story": json.RawMessage(`{}`)}

	mismatch := child
	mismatch.Digest = strings.Repeat("b", 64)
	_, err := parent.StartSubworkflow("frame_child", "visit_story", call, mismatch, route, inputs)
	assertFrameCode(t, err, workflow.WorkflowReferenceError)

	childFrame, err := parent.StartSubworkflow("frame_child", "visit_story", call, child, route, inputs)
	if err != nil {
		t.Fatal(err)
	}
	recursiveCall := call
	recursiveCall.Call.Workflow = workflow.WorkflowReference{Name: child.Document.Metadata.Name, Version: child.Document.Metadata.Version, Digest: child.Digest}
	_, err = childFrame.StartSubworkflow("frame_recursive", "visit_recursive", recursiveCall, child, route, inputs)
	assertFrameCode(t, err, workflow.WorkflowRecursionError)
}

func TestSubworkflowMapsDeclaredOutputsByValue(t *testing.T) {
	t.Parallel()
	child := childDefinition()
	node := childNode()
	value := json.RawMessage(`{"passed":true}`)
	candidate, err := workflow.MapSubworkflowOutputs(node, child.Document, map[workflow.Identifier]map[workflow.Identifier]json.RawMessage{
		"validate": {"report": value},
	})
	if err != nil {
		t.Fatal(err)
	}
	value[2] = 'X'
	if got := string(candidate["result"]); got != `{"passed":true}` {
		t.Fatalf("mapped output changed through child alias: %s", got)
	}
	_, err = workflow.MapSubworkflowOutputs(node, child.Document, map[workflow.Identifier]map[workflow.Identifier]json.RawMessage{})
	assertFrameCode(t, err, workflow.RunOutputInvalid)
}

func rootFrame(t *testing.T, id string) *workflow.Frame {
	t.Helper()
	document := workflow.Document{
		APIVersion: workflow.APIVersionV1Alpha1, Kind: workflow.KindWorkflow,
		Metadata: workflow.Metadata{Name: "delivery", Version: "1.0.0"},
	}
	frame, err := workflow.NewRootFrame(id, "run_1", workflow.LoadedDefinition{
		Document: document, Digest: strings.Repeat("a", 64),
	}, workflow.Route{
		Entry: "start", Terminals: []workflow.Identifier{"finish"},
		Transitions: []workflow.RouteTransition{{ID: "repair", From: "validate", To: "implement"}},
	}, map[workflow.Identifier]json.RawMessage{})
	if err != nil {
		t.Fatal(err)
	}
	return frame
}

func childRoute() workflow.Route {
	return workflow.Route{
		Entry: "implement", Terminals: []workflow.Identifier{"validate"},
		Transitions: []workflow.RouteTransition{{ID: "repair", From: "validate", To: "implement"}},
	}
}

func childDefinition() workflow.LoadedDefinition {
	return workflow.LoadedDefinition{
		Digest: strings.Repeat("c", 64),
		Document: workflow.Document{
			APIVersion: workflow.APIVersionV1Alpha1, Kind: workflow.KindWorkflow,
			Metadata: workflow.Metadata{Name: "story", Version: "1.0.0"},
			Spec: workflow.Spec{
				Inputs: map[workflow.Identifier]workflow.ValueDeclaration{"request": {Type: workflow.ValueObject}},
				Nodes: map[workflow.Identifier]workflow.Node{
					"validate": workflow.CommandNode{Common: workflow.NodeFields{Outputs: map[workflow.Identifier]workflow.OutputDeclaration{
						"report": {Type: workflow.ValueObject},
					}}},
				},
			},
		},
	}
}

func childNode() workflow.SubworkflowNode {
	return workflow.SubworkflowNode{
		Common: workflow.NodeFields{
			Inputs:  map[workflow.Identifier]workflow.Binding{"story": workflow.RequiredBinding{From: "run.input.story", Type: workflow.ValueObject}},
			Outputs: map[workflow.Identifier]workflow.OutputDeclaration{"result": {Type: workflow.ValueObject}},
		},
		Call: workflow.SubworkflowCall{
			Workflow: workflow.WorkflowReference{Name: "story", Version: "1.0.0", Digest: strings.Repeat("c", 64)},
			Entry:    "implement", Terminals: []workflow.Identifier{"validate"},
			Inputs:  map[workflow.Identifier]workflow.Identifier{"request": "story"},
			Outputs: map[workflow.Identifier]string{"result": "node.validate.output.report"},
		},
	}
}

func assertFrameCode(t *testing.T, err error, code string) {
	t.Helper()
	var frameError *workflow.FrameError
	if !errors.As(err, &frameError) || frameError.Code != code {
		t.Fatalf("error = %#v, want frame code %s", err, code)
	}
}
