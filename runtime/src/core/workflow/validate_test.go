package workflow_test

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/fdsprod/darkstar/runtime/src/core/workflow"
)

func TestShippedWorkflowsPassStaticValidation(t *testing.T) {
	t.Parallel()

	directory := filepath.Join(repositoryRoot(t), "examples", "workflows")
	entries, err := os.ReadDir(directory)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		entry := entry
		t.Run(entry.Name(), func(t *testing.T) {
			t.Parallel()
			data, err := os.ReadFile(filepath.Join(directory, entry.Name()))
			if err != nil {
				t.Fatal(err)
			}
			document, err := workflow.Decode(data)
			if err != nil {
				t.Fatal(err)
			}
			if validationErrors := workflow.Validate(document); len(validationErrors) != 0 {
				t.Fatalf("validation errors: %+v", validationErrors)
			}
		})
	}
}

func TestStaticValidationFindsGraphAndBindingErrors(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		edit func(*workflow.Document)
		code workflow.ValidationCode
	}{
		{
			name: "missing transition target",
			code: workflow.ValidationReferenceMissing,
			edit: func(document *workflow.Document) {
				start := document.Spec.Nodes["start"].(workflow.CommandNode)
				start.Common.Transitions = []workflow.Transition{normal("missing", "ghost")}
				document.Spec.Nodes["start"] = start
			},
		},
		{
			name: "incompatible binding",
			code: workflow.ValidationBindingIncompatible,
			edit: func(document *workflow.Document) {
				end := document.Spec.Nodes["end"].(workflow.CommandNode)
				end.Common.Inputs["result"] = workflow.RequiredBinding{
					From: "node.start.output.result", Type: workflow.ValueString,
				}
				document.Spec.Nodes["end"] = end
			},
		},
		{
			name: "unreachable node",
			code: workflow.ValidationUnreachableNode,
			edit: func(document *workflow.Document) {
				document.Spec.Nodes["orphan"] = command(workflow.NodeFields{
					Entry: false, Terminal: true, Inputs: map[workflow.Identifier]workflow.Binding{},
					Outputs:        map[workflow.Identifier]workflow.OutputDeclaration{},
					TransitionMode: workflow.TransitionExclusive,
				})
			},
		},
		{
			name: "unbounded cycle",
			code: workflow.ValidationUnboundedCycle,
			edit: func(document *workflow.Document) {
				end := document.Spec.Nodes["end"].(workflow.CommandNode)
				end.Common.Transitions = []workflow.Transition{normal("again", "start")}
				document.Spec.Nodes["end"] = end
			},
		},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			document := basicWorkflow()
			test.edit(&document)
			assertValidationCode(t, workflow.Validate(document), test.code)
		})
	}
}

func TestBoundedEdgeBreaksEveryCycle(t *testing.T) {
	t.Parallel()
	document := basicWorkflow()
	end := document.Spec.Nodes["end"].(workflow.CommandNode)
	end.Common.Transitions = []workflow.Transition{workflow.BoundedTransition{
		Common: workflow.TransitionFields{TransitionID: "again", To: "start"}, MaxTraversals: 2,
	}}
	document.Spec.Nodes["end"] = end

	for _, validationError := range workflow.Validate(document) {
		if validationError.Code == workflow.ValidationUnboundedCycle {
			t.Fatalf("bounded cycle reported as unbounded: %+v", validationError)
		}
	}
}

func TestStaticValidationRejectsProvablyImpossibleJoins(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name         string
		sourceMode   workflow.TransitionMode
		joinMode     workflow.JoinMode
		intermediate bool
	}{
		{name: "all after exclusive split", sourceMode: workflow.TransitionExclusive, joinMode: workflow.JoinAll},
		{name: "one after unconditional fanout", sourceMode: workflow.TransitionFanout, joinMode: workflow.JoinOne},
		{name: "all after indirect exclusive split", sourceMode: workflow.TransitionExclusive, joinMode: workflow.JoinAll, intermediate: true},
		{name: "one after indirect unconditional fanout", sourceMode: workflow.TransitionFanout, joinMode: workflow.JoinOne, intermediate: true},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			document := joinWorkflow(test.sourceMode, test.joinMode, test.intermediate)
			assertValidationCode(t, workflow.Validate(document), workflow.ValidationJoinInvalid)
		})
	}
}

func TestCapabilityValidationKeepsSkillAndToolKindsDistinct(t *testing.T) {
	t.Parallel()
	document := decodeExample(t, "mvp-walking-skeleton.json")
	snapshot := workflow.NewCapabilitySnapshot(
		workflow.CapabilityReference{Kind: workflow.CapabilitySkill, Name: "darkstar:technical-design"},
		workflow.CapabilityReference{Kind: workflow.CapabilityTool, Name: "darkstar:repository-search"},
		workflow.CapabilityReference{Kind: workflow.CapabilitySkill, Name: "darkstar:artifact-store"},
	)
	errors := workflow.NewStaticValidator(snapshot).Validate(document)

	if len(errors) != 1 || errors[0].Code != workflow.ValidationCapabilityMissing ||
		errors[0].Location != "/spec/nodes/technical_design/reasoning/tools/1" {
		t.Fatalf("capability errors = %+v", errors)
	}
}

func TestValidationResultsAreDeterministicallySorted(t *testing.T) {
	t.Parallel()
	document := basicWorkflow()
	start := document.Spec.Nodes["start"].(workflow.CommandNode)
	start.Common.Transitions = []workflow.Transition{
		normal("z_edge", "missing_z"),
		normal("a_edge", "missing_a"),
	}
	document.Spec.Nodes["start"] = start

	first := workflow.Validate(document)
	second := workflow.Validate(document)
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("validation order changed:\nfirst=%+v\nsecond=%+v", first, second)
	}
	for index := 1; index < len(first); index++ {
		if first[index-1].Location > first[index].Location {
			t.Fatalf("errors are not location sorted: %+v", first)
		}
	}
}

func TestCanonicalizeRejectsSemanticWorkflowErrors(t *testing.T) {
	t.Parallel()
	document := basicWorkflow()
	start := document.Spec.Nodes["start"].(workflow.CommandNode)
	start.Common.Transitions = []workflow.Transition{normal("missing", "ghost")}
	document.Spec.Nodes["start"] = start
	content, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	_, _, _, err = workflow.Canonicalize(content)
	var validationErrors workflow.ValidationErrors
	if !errors.As(err, &validationErrors) || len(validationErrors) == 0 {
		t.Fatalf("Canonicalize error = %v, want ValidationErrors", err)
	}
}

func basicWorkflow() workflow.Document {
	return workflow.Document{
		APIVersion: workflow.APIVersionV1Alpha1,
		Kind:       workflow.KindWorkflow,
		Metadata:   workflow.Metadata{Name: "static-validation", Version: "1.0.0"},
		Spec: workflow.Spec{
			Inputs:        map[workflow.Identifier]workflow.ValueDeclaration{},
			RouteDefaults: workflow.RouteDefaults{Entry: "start", Terminals: []workflow.Identifier{"end"}},
			Nodes: map[workflow.Identifier]workflow.Node{
				"start": command(workflow.NodeFields{
					Entry: true, Terminal: false,
					Inputs: map[workflow.Identifier]workflow.Binding{},
					Outputs: map[workflow.Identifier]workflow.OutputDeclaration{
						"result": {Type: workflow.ValueBoolean},
					},
					TransitionMode: workflow.TransitionExclusive,
					Transitions:    []workflow.Transition{normal("finish", "end")},
				}),
				"end": command(workflow.NodeFields{
					Entry: false, Terminal: true,
					Inputs: map[workflow.Identifier]workflow.Binding{}, Outputs: map[workflow.Identifier]workflow.OutputDeclaration{},
					TransitionMode: workflow.TransitionExclusive,
				}),
			},
		},
	}
}

func joinWorkflow(sourceMode workflow.TransitionMode, joinMode workflow.JoinMode, intermediate bool) workflow.Document {
	document := basicWorkflow()
	start := document.Spec.Nodes["start"].(workflow.CommandNode)
	start.Common.TransitionMode = sourceMode
	if intermediate {
		start.Common.Transitions = []workflow.Transition{normal("to_left", "left"), normal("to_right", "right")}
		document.Spec.Nodes["left"] = command(workflow.NodeFields{
			Inputs: map[workflow.Identifier]workflow.Binding{}, Outputs: map[workflow.Identifier]workflow.OutputDeclaration{},
			TransitionMode: workflow.TransitionExclusive, Transitions: []workflow.Transition{normal("first", "end")},
		})
		document.Spec.Nodes["right"] = command(workflow.NodeFields{
			Inputs: map[workflow.Identifier]workflow.Binding{}, Outputs: map[workflow.Identifier]workflow.OutputDeclaration{},
			TransitionMode: workflow.TransitionExclusive, Transitions: []workflow.Transition{normal("second", "end")},
		})
	} else {
		start.Common.Transitions = []workflow.Transition{normal("first", "end"), normal("second", "end")}
	}
	document.Spec.Nodes["start"] = start
	end := document.Spec.Nodes["end"].(workflow.CommandNode)
	end.Common.Join = &workflow.Join{Mode: joinMode, From: []workflow.Identifier{"first", "second"}}
	document.Spec.Nodes["end"] = end
	return document
}

func command(fields workflow.NodeFields) workflow.CommandNode {
	return workflow.CommandNode{Common: fields, Executor: workflow.CommandExecutor{Argv: []string{"true"}}}
}

func normal(id, target workflow.Identifier) workflow.NormalTransition {
	return workflow.NormalTransition{Common: workflow.TransitionFields{TransitionID: id, To: target}}
}

func assertValidationCode(t *testing.T, errors workflow.ValidationErrors, code workflow.ValidationCode) {
	t.Helper()
	for _, validationError := range errors {
		if validationError.Code == code {
			return
		}
	}
	t.Fatalf("validation errors = %+v, want code %s", errors, code)
}
