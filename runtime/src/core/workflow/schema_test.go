package workflow_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"darkstar/src/core/workflow"
)

func TestShippedWorkflowsDecodeIntoClosedNodeVariants(t *testing.T) {
	t.Parallel()

	directory := filepath.Join(repositoryRoot(t), "examples", "workflows")
	entries, err := os.ReadDir(directory)
	if err != nil {
		t.Fatalf("read workflow examples: %v", err)
	}

	found := make(map[workflow.NodeType]bool)
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		entry := entry
		t.Run(entry.Name(), func(t *testing.T) {
			t.Parallel()

			path := filepath.Join(directory, entry.Name())
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read %s: %v", path, err)
			}
			document, err := workflow.Decode(data)
			if err != nil {
				t.Fatalf("decode %s: %v", path, err)
			}
			for id, node := range document.Spec.Nodes {
				assertConcreteNodeType(t, id, node)
			}

			encoded, err := workflow.Encode(document)
			if err != nil {
				t.Fatalf("encode %s: %v", path, err)
			}
			if _, err := workflow.Decode(encoded); err != nil {
				t.Fatalf("decode encoded %s: %v", path, err)
			}
		})
	}

	// Subtests above are parallel, so verify coverage in a non-parallel pass.
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		data, err := os.ReadFile(filepath.Join(directory, entry.Name()))
		if err != nil {
			t.Fatal(err)
		}
		document, err := workflow.Decode(data)
		if err != nil {
			t.Fatal(err)
		}
		for _, node := range document.Spec.Nodes {
			found[node.Type()] = true
		}
	}
	for _, nodeType := range []workflow.NodeType{
		workflow.NodeReasoning,
		workflow.NodeGate,
		workflow.NodeCommand,
		workflow.NodeApproval,
		workflow.NodeSubworkflow,
		workflow.NodePointExecution,
	} {
		if !found[nodeType] {
			t.Errorf("shipped examples do not cover %q", nodeType)
		}
	}
}

func TestPublishedEnumsMatchTypedContract(t *testing.T) {
	t.Parallel()

	data, err := os.ReadFile(filepath.Join(repositoryRoot(t), "schemas", "workflow-v1alpha1.schema.json"))
	if err != nil {
		t.Fatal(err)
	}
	var schema map[string]any
	if err := json.Unmarshal(data, &schema); err != nil {
		t.Fatal(err)
	}

	assertStringSet(t, schemaEnum(t, schema, "$defs", "node", "properties", "type", "enum"), []string{
		string(workflow.NodeReasoning), string(workflow.NodeGate), string(workflow.NodeCommand),
		string(workflow.NodeApproval), string(workflow.NodeSubworkflow),
		string(workflow.NodePointExecution),
	})
	assertStringSet(t, schemaEnum(t, schema, "$defs", "valueType", "enum"), []string{
		string(workflow.ValueNull), string(workflow.ValueBoolean), string(workflow.ValueInteger),
		string(workflow.ValueNumber), string(workflow.ValueString), string(workflow.ValueArray),
		string(workflow.ValueObject),
	})
	assertStringSet(t, schemaEnum(t, schema, "$defs", "retry", "properties", "on", "items", "enum"), []string{
		string(workflow.RetryProviderUnavailable), string(workflow.RetryProviderRateLimit),
		string(workflow.RetryProcessFailure), string(workflow.RetryValidatorFailure),
		string(workflow.RetryTimeout), string(workflow.RetryInterrupted),
	})
	assertStringSet(t, schemaEnum(t, schema, "$defs", "readiness", "properties", "policyGates", "items", "properties", "enforcement", "enum"), []string{
		string(workflow.ReadinessGateAdvisory), string(workflow.ReadinessGateBlocking), string(workflow.ReadinessGateExternal),
	})
	assertStringSet(t, schemaEnum(t, schema, "$defs", "readiness", "properties", "remedies", "items", "properties", "action", "enum"), []string{
		string(workflow.ReadinessSupplyInput), string(workflow.ReadinessReviseArtifact), string(workflow.ReadinessClarifyDecision),
		string(workflow.ReadinessInstallCapability), string(workflow.ReadinessRerunValidation),
	})
	assertStringSet(t, schemaEnum(t, schema, "$defs", "pointExecutionExecutor", "properties", "approval", "enum"), []string{
		string(workflow.PointApprovalNone), string(workflow.PointApprovalEvery), string(workflow.PointApprovalRisk), string(workflow.PointApprovalCombined),
	})
	assertStringSet(t, schemaEnum(t, schema, "$defs", "pointExecutionExecutor", "properties", "validation", "enum"), []string{
		string(workflow.PointValidationEach), string(workflow.PointValidationCombined), string(workflow.PointValidationEachAndCombined),
	})
	assertStringSet(t, schemaEnum(t, schema, "$defs", "pointExecutionExecutor", "properties", "publishing", "enum"), []string{
		string(workflow.PointPublishingAfterStory), string(workflow.PointPublishingAfterEach),
	})
}

func TestGatePredicatesAndBoundedTransitionsAreTyped(t *testing.T) {
	t.Parallel()

	document := decodeExample(t, "software-delivery.json")
	gate, ok := document.Spec.Nodes["p1_route_gate"].(workflow.GateNode)
	if !ok {
		t.Fatalf("p1_route_gate has type %T", document.Spec.Nodes["p1_route_gate"])
	}
	comparison, ok := gate.Executor.Condition.(workflow.ComparisonPredicate)
	if !ok {
		t.Fatalf("gate condition has type %T", gate.Executor.Condition)
	}
	if comparison.Operator != workflow.CompareGreaterOrEqual {
		t.Fatalf("gate operator = %q", comparison.Operator)
	}
	if _, ok := comparison.Args[0].(workflow.ReferenceOperand); !ok {
		t.Fatalf("left operand has type %T", comparison.Args[0])
	}

	story := decodeExample(t, "story-execution.json")
	progress := story.Spec.Nodes["s5_progress_gate"].(workflow.GateNode)
	transitions := progress.Fields().Transitions
	if len(transitions) != 2 {
		t.Fatalf("progress transitions = %d", len(transitions))
	}
	bounded, ok := transitions[0].(workflow.BoundedTransition)
	if !ok {
		t.Fatalf("repair transition has type %T", transitions[0])
	}
	if bounded.MaxTraversals != 20 {
		t.Fatalf("max traversals = %d", bounded.MaxTraversals)
	}
}

func TestShippedDefaultNodesHaveTypedReadinessContracts(t *testing.T) {
	t.Parallel()

	for _, name := range []string{"software-delivery.json", "story-execution.json"} {
		document := decodeExample(t, name)
		for nodeID, node := range document.Spec.Nodes {
			readiness := node.Fields().Readiness
			if readiness == nil {
				t.Errorf("%s node %s has no readiness contract", name, nodeID)
				continue
			}
			if readiness.RecommendedEvidence == nil || readiness.PolicyGates == nil || readiness.Invariants == nil || readiness.Remedies == nil {
				t.Errorf("%s node %s readiness categories are not explicit", name, nodeID)
			}
			for inputID, binding := range node.Fields().Inputs {
				switch binding.(type) {
				case workflow.RequiredBinding, workflow.OptionalBinding:
				default:
					t.Errorf("%s node %s input %s has untyped requirement state %T", name, nodeID, inputID, binding)
				}
			}
		}
	}
}

func TestPostPRNodesUseStructuredExternalEvidence(t *testing.T) {
	t.Parallel()

	document := decodeExample(t, "software-delivery.json")
	for _, nodeID := range []workflow.Identifier{"p14_review_ci", "p16_release", "p17_verification"} {
		node, ok := document.Spec.Nodes[nodeID].(workflow.ApprovalNode)
		if !ok {
			t.Fatalf("%s has type %T", nodeID, document.Spec.Nodes[nodeID])
		}
		external, ok := node.Executor.(workflow.ExternalApproval)
		if !ok {
			t.Fatalf("%s executor has type %T", nodeID, node.Executor)
		}
		output := node.Common.Outputs[external.EvidenceOutput]
		if output.Type != workflow.ValueObject || !strings.HasPrefix(output.Schema, "schemas/delivery-evidence-v1alpha1.schema.json#/") {
			t.Errorf("%s evidence output = %#v", nodeID, output)
		}
		foundValidator := false
		for _, validator := range node.Common.Validators {
			if schema, ok := validator.(workflow.SchemaValidator); ok && schema.Output == external.EvidenceOutput && schema.Schema == output.Schema {
				foundValidator = true
			}
		}
		if !foundValidator {
			t.Errorf("%s has no matching evidence schema validator", nodeID)
		}
	}
}

func TestStoryWorkflowUsesTypedPointExecution(t *testing.T) {
	t.Parallel()

	document := decodeExample(t, "story-execution.json")
	node, ok := document.Spec.Nodes["s5_implementation"].(workflow.PointExecutionNode)
	if !ok {
		t.Fatalf("s5_implementation has type %T", document.Spec.Nodes["s5_implementation"])
	}
	if node.Executor.PlanInput != "implementation_plan" || node.Executor.Approval != workflow.PointApprovalNone ||
		node.Executor.Validation != workflow.PointValidationEachAndCombined || node.Executor.Publishing != workflow.PointPublishingAfterStory {
		t.Fatalf("point execution policy = %#v", node.Executor)
	}
}

func TestBindingCheckpointAndValidatorVariantsAreTyped(t *testing.T) {
	t.Parallel()

	object := exampleObject(t, "mvp-walking-skeleton.json")
	node(object, "technical_design")["validators"] = []any{
		map[string]any{"output": "technical_design", "schema": "schemas/technical-design-v1.json"},
		map[string]any{"command": []any{"validate-design", "--strict"}},
	}
	data, err := json.Marshal(object)
	if err != nil {
		t.Fatal(err)
	}
	document, err := workflow.Decode(data)
	if err != nil {
		t.Fatal(err)
	}
	node := document.Spec.Nodes["technical_design"].(workflow.ReasoningNode)
	if _, ok := node.Common.Inputs["work_item"].(workflow.RequiredBinding); !ok {
		t.Fatalf("work_item binding has type %T", node.Common.Inputs["work_item"])
	}
	checkpoint, ok := node.Common.Checkpoint.(workflow.ApproveCheckpoint)
	if !ok {
		t.Fatalf("checkpoint has type %T", node.Common.Checkpoint)
	}
	if checkpoint.MaxRevisions == nil || *checkpoint.MaxRevisions != 5 {
		t.Fatalf("checkpoint max revisions = %v", checkpoint.MaxRevisions)
	}
	if len(node.Common.Validators) != 2 {
		t.Fatalf("validators = %d", len(node.Common.Validators))
	}
	if _, ok := node.Common.Validators[0].(workflow.SchemaValidator); !ok {
		t.Fatalf("validator has type %T", node.Common.Validators[0])
	}
	if _, ok := node.Common.Validators[1].(workflow.CommandValidator); !ok {
		t.Fatalf("validator has type %T", node.Common.Validators[1])
	}
}

func TestContradictoryTaggedShapesFailDuringDecode(t *testing.T) {
	t.Parallel()

	base := exampleObject(t, "mvp-walking-skeleton.json")
	tests := []struct {
		name string
		want string
		edit func(map[string]any)
	}{
		{
			name: "mixed executors",
			want: "command settings are invalid",
			edit: func(document map[string]any) {
				node(document, "technical_design")["command"] = map[string]any{"argv": []any{"false"}}
			},
		},
		{
			name: "default on required binding",
			want: "default requires required=false",
			edit: func(document map[string]any) {
				binding(document, "technical_design", "work_item")["default"] = map[string]any{}
			},
		},
		{
			name: "checkpoint sibling fields",
			want: "unknown field",
			edit: func(document map[string]any) {
				node(document, "technical_design")["checkpoint"] = map[string]any{"mode": "none", "externalCondition": "never"}
			},
		},
		{
			name: "mixed validator",
			want: "exactly one schema or command validator",
			edit: func(document map[string]any) {
				node(document, "technical_design")["validators"] = []any{
					map[string]any{"output": "technical_design", "schema": "schema.json", "command": []any{"validate"}},
				}
			},
		},
		{
			name: "external approval without evidence output",
			want: "external actor requires evidenceOutput",
			edit: func(document map[string]any) {
				node(document, "technical_design")["type"] = "approval"
				delete(node(document, "technical_design"), "reasoning")
				node(document, "technical_design")["approval"] = map[string]any{"actor": "external", "externalCondition": "checks pass"}
			},
		},
		{
			name: "unbounded traversal count",
			want: "maxTraversals is valid only for a bounded transition",
			edit: func(document map[string]any) {
				node(document, "technical_design")["transitions"] = []any{
					map[string]any{"id": "repeat", "to": "technical_design", "maxTraversals": float64(2)},
				}
			},
		},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			document := cloneObject(t, base)
			test.edit(document)
			data, err := json.Marshal(document)
			if err != nil {
				t.Fatal(err)
			}
			_, err = workflow.Decode(data)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want substring %q", err, test.want)
			}
		})
	}
}

func assertConcreteNodeType(t *testing.T, id workflow.Identifier, node workflow.Node) {
	t.Helper()
	switch node.Type() {
	case workflow.NodeReasoning:
		if _, ok := node.(workflow.ReasoningNode); !ok {
			t.Errorf("node %s discriminator reasoning has concrete type %T", id, node)
		}
	case workflow.NodeGate:
		if _, ok := node.(workflow.GateNode); !ok {
			t.Errorf("node %s discriminator gate has concrete type %T", id, node)
		}
	case workflow.NodeCommand:
		if _, ok := node.(workflow.CommandNode); !ok {
			t.Errorf("node %s discriminator command has concrete type %T", id, node)
		}
	case workflow.NodeApproval:
		if _, ok := node.(workflow.ApprovalNode); !ok {
			t.Errorf("node %s discriminator approval has concrete type %T", id, node)
		}
	case workflow.NodeSubworkflow:
		if _, ok := node.(workflow.SubworkflowNode); !ok {
			t.Errorf("node %s discriminator subworkflow has concrete type %T", id, node)
		}
	case workflow.NodePointExecution:
		if _, ok := node.(workflow.PointExecutionNode); !ok {
			t.Errorf("node %s discriminator point_execution has concrete type %T", id, node)
		}
	default:
		t.Errorf("node %s has unsupported type %q", id, node.Type())
	}
}

func decodeExample(t *testing.T, name string) workflow.Document {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(repositoryRoot(t), "examples", "workflows", name))
	if err != nil {
		t.Fatal(err)
	}
	document, err := workflow.Decode(data)
	if err != nil {
		t.Fatal(err)
	}
	return document
}

func exampleObject(t *testing.T, name string) map[string]any {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(repositoryRoot(t), "examples", "workflows", name))
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]any
	if err := json.Unmarshal(data, &document); err != nil {
		t.Fatal(err)
	}
	return document
}

func cloneObject(t *testing.T, value map[string]any) map[string]any {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	var clone map[string]any
	if err := json.Unmarshal(data, &clone); err != nil {
		t.Fatal(err)
	}
	return clone
}

func node(document map[string]any, name string) map[string]any {
	return document["spec"].(map[string]any)["nodes"].(map[string]any)[name].(map[string]any)
}

func binding(document map[string]any, nodeName, inputName string) map[string]any {
	return node(document, nodeName)["inputs"].(map[string]any)[inputName].(map[string]any)
}

func schemaEnum(t *testing.T, schema map[string]any, path ...string) []string {
	t.Helper()
	var current any = schema
	for _, segment := range path {
		object, ok := current.(map[string]any)
		if !ok {
			t.Fatalf("schema path %s is not an object", strings.Join(path, "."))
		}
		current, ok = object[segment]
		if !ok {
			t.Fatalf("schema path %s is missing", strings.Join(path, "."))
		}
	}
	values, ok := current.([]any)
	if !ok {
		t.Fatalf("schema path %s is not an array", strings.Join(path, "."))
	}
	result := make([]string, len(values))
	for index, value := range values {
		result[index], ok = value.(string)
		if !ok {
			t.Fatalf("schema path %s item %d is not a string", strings.Join(path, "."), index)
		}
	}
	return result
}

func assertStringSet(t *testing.T, actual, expected []string) {
	t.Helper()
	actualSet := make(map[string]struct{}, len(actual))
	for _, value := range actual {
		actualSet[value] = struct{}{}
	}
	if len(actualSet) != len(expected) {
		t.Fatalf("published enum = %v, typed values = %v", actual, expected)
	}
	for _, value := range expected {
		if _, exists := actualSet[value]; !exists {
			t.Fatalf("published enum %v is missing typed value %q", actual, value)
		}
	}
}

func repositoryRoot(t *testing.T) string {
	t.Helper()
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve test filename")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(filename), "..", "..", "..", ".."))
}
