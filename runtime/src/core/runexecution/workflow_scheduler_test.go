package runexecution

import (
	"encoding/json"
	"strings"
	"testing"

	"darkstar/src/core/workflow"
	"darkstar/src/ports/statestore"
)

func TestResolveNodeInputsUsesRunAndAcceptedOutputBindings(t *testing.T) {
	node := workflow.GateNode{Common: workflow.NodeFields{Inputs: map[workflow.Identifier]workflow.Binding{
		"story":    workflow.RequiredBinding{From: "run.input.story", Type: workflow.ValueObject},
		"progress": workflow.RequiredBinding{From: "node.implementation.output.progress", Pointer: "/detail", Type: workflow.ValueObject},
		"optional": workflow.OptionalBinding{From: "node.missing.output.value", Type: workflow.ValueString, Default: json.RawMessage(`"fallback"`)},
	}}}
	inputs, err := resolveNodeInputs(node,
		map[workflow.Identifier]json.RawMessage{"story": json.RawMessage(`{"title":"test"}`)},
		map[workflow.Identifier]map[workflow.Identifier]json.RawMessage{"implementation": {"progress": json.RawMessage(`{"detail":{"remaining_points":0}}`)}},
	)
	if err != nil {
		t.Fatal(err)
	}
	if string(inputs["story"]) != `{"title":"test"}` || string(inputs["progress"]) != `{"remaining_points":0}` || string(inputs["optional"]) != `"fallback"` {
		t.Fatalf("resolved inputs = %#v", inputs)
	}
}

func TestNamedWorkflowValuesValidateTheirStorageShapeAtExecution(t *testing.T) {
	for _, kind := range []workflow.ValueType{workflow.ValueMarkdown, workflow.ValueTask, workflow.ValueRepository, workflow.ValueTemplate, workflow.ValueOpenItems, workflow.ValueDecisionLog, "schema:review_evidence", workflow.ValueNumber} {
		t.Run(string(kind), func(t *testing.T) {
			raw := json.RawMessage(`{}`)
			if kind == workflow.ValueMarkdown {
				raw = json.RawMessage(`"# Design"`)
			}
			if kind == workflow.ValueNumber {
				raw = json.RawMessage(`3`)
			}
			node := workflow.ReasoningNode{Common: workflow.NodeFields{Inputs: map[workflow.Identifier]workflow.Binding{"value": workflow.RequiredBinding{From: "run.input.value", Type: kind}}, Outputs: map[workflow.Identifier]workflow.OutputDeclaration{"value": {Type: kind}}}}
			if _, err := resolveNodeInputs(node, map[workflow.Identifier]json.RawMessage{"value": raw}, nil); err != nil {
				t.Fatal(err)
			}
			encoded, _ := json.Marshal(map[string]json.RawMessage{"value": raw})
			if _, err := decodeNodeOutputs(node, encoded); err != nil {
				t.Fatal(err)
			}
			if _, err := resolveNodeInputs(node, map[workflow.Identifier]json.RawMessage{"value": json.RawMessage(`true`)}, nil); err == nil {
				t.Fatal("wrong storage shape accepted")
			}
			if _, err := decodeNodeOutputs(node, json.RawMessage(`{"value":true}`)); err == nil {
				t.Fatal("wrong output storage shape accepted")
			}
		})
	}
}

func TestPrepareWorkflowAdvancePersistsOutputAndReplaysCommittedTransition(t *testing.T) {
	transition := workflow.NormalTransition{Common: workflow.TransitionFields{TransitionID: "implementation_to_gate", To: "gate"}}
	node := workflow.PointExecutionNode{Common: workflow.NodeFields{
		Outputs:     map[workflow.Identifier]workflow.OutputDeclaration{"changeset": {Type: workflow.ValueObject}, "progress": {Type: workflow.ValueObject}},
		Transitions: []workflow.Transition{transition}, TransitionMode: workflow.TransitionExclusive,
	}}
	route := workflow.Route{Entry: "implementation", Terminals: []workflow.Identifier{"validation"},
		Nodes:       []workflow.RouteNode{{ID: "implementation"}, {ID: "gate"}, {ID: "validation"}},
		Transitions: []workflow.RouteTransition{{ID: transition.ID(), From: "implementation", To: "gate"}},
	}
	snapshot := workflow.FrameSnapshot{ID: "frame-1", Workflow: workflow.WorkflowIdentity{Name: "test", Version: "1.0.0", Digest: strings.Repeat("a", 64)}, Origin: workflow.RootFrameOrigin{RunID: "run-1"}, Route: route, Inputs: map[workflow.Identifier]json.RawMessage{}}
	frameJSON, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	dispatch := AttemptRequestContext{
		Attempt: statestore.AttemptProjection{AttemptID: "attempt-1", VisitID: "visit-1", NodeID: "implementation"}, Node: node, FrozenRoute: route,
		RunInputs: map[workflow.Identifier]json.RawMessage{}, AcceptedOutputs: map[workflow.Identifier]map[workflow.Identifier]json.RawMessage{}, NodeInputs: map[workflow.Identifier]json.RawMessage{}, FrameSnapshot: snapshot,
		ExecutionContext: statestore.RunExecutionContext{SchemaVersion: 1, RunID: "run-1", RunInputs: map[string]json.RawMessage{}, AcceptedOutputs: map[string]map[string]json.RawMessage{}, FrameSnapshot: frameJSON, Revision: 1},
	}
	output := map[workflow.Identifier]json.RawMessage{"changeset": json.RawMessage(`{"file":"README.md"}`), "progress": json.RawMessage(`{"remaining_points":0}`)}
	advance, err := prepareWorkflowAdvance(dispatch, output)
	if err != nil {
		t.Fatal(err)
	}
	if !advance.persist || advance.successor != "gate" || len(advance.context.AcceptedOutputs["implementation"]) != 2 {
		t.Fatalf("advance = %#v", advance)
	}
	var advancedSnapshot workflow.FrameSnapshot
	if err := json.Unmarshal(advance.context.FrameSnapshot, &advancedSnapshot); err != nil {
		t.Fatal(err)
	}
	if len(advancedSnapshot.Tokens) != 1 || advancedSnapshot.Tokens[0].Key.SourceVisitID != "visit-1" {
		t.Fatalf("tokens = %#v", advancedSnapshot.Tokens)
	}

	dispatch.AcceptedOutputs = map[workflow.Identifier]map[workflow.Identifier]json.RawMessage{"implementation": output}
	dispatch.FrameSnapshot = advancedSnapshot
	dispatch.ExecutionContext = advance.context
	dispatch.ExecutionContext.Revision = 2
	replayed, err := prepareWorkflowAdvance(dispatch, output)
	if err != nil {
		t.Fatal(err)
	}
	if replayed.persist || replayed.successor != "gate" {
		t.Fatalf("replayed advance = %#v", replayed)
	}
}

func TestBuiltinGateProducesEvidenceUsedByTransition(t *testing.T) {
	condition := workflow.ComparisonPredicate{Operator: workflow.CompareEqual, Args: [2]workflow.Operand{
		workflow.ReferenceOperand{Ref: "input.progress.remaining_points"}, workflow.LiteralOperand{Literal: json.RawMessage(`0`)},
	}}
	node := workflow.GateNode{Executor: workflow.GateExecutor{Policy: "points-complete", Condition: condition}}
	raw, err := builtinGateOutput(node, map[workflow.Identifier]json.RawMessage{"progress": json.RawMessage(`{"remaining_points":0}`)}, nil)
	if err != nil {
		t.Fatal(err)
	}
	var output struct {
		Passed   bool `json:"passed"`
		Evidence struct {
			Policy string `json:"policy"`
			Passed bool   `json:"passed"`
		} `json:"gate_evidence"`
	}
	if err := json.Unmarshal(raw, &output); err != nil {
		t.Fatal(err)
	}
	if !output.Passed || !output.Evidence.Passed || output.Evidence.Policy != "points-complete" {
		t.Fatalf("gate output = %s", raw)
	}
}

func TestBuiltinValidationRejectsUnrecognizedCommandNode(t *testing.T) {
	node := workflow.CommandNode{Executor: workflow.CommandExecutor{Argv: []string{"echo", "success"}}}
	if _, err := builtinValidationOutput(t.Context(), DefaultWorkflowID, DefaultWorkflowVersion, "s6_validation", t.TempDir(), node); err == nil || !strings.Contains(err.Error(), "not an explicitly supported") {
		t.Fatalf("builtin validation error = %v", err)
	}
}
