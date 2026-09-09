package cli

import (
	"crypto/sha256"
	"darkstar/src/core/runexecution"
	"darkstar/src/core/workflow"
	"darkstar/src/ports/provider"
	"darkstar/src/ports/statestore"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func TestImplementationBuildsWritableAttemptWithoutPointPlan(t *testing.T) {
	workspace := t.TempDir()
	node := workflow.ImplementationNode{Common: workflow.NodeFields{Permissions: []string{"process.run", "workspace.write"}, Inputs: map[workflow.Identifier]workflow.Binding{"task": workflow.RequiredBinding{From: "run.input.task", Type: workflow.ValueTask}}, Outputs: map[workflow.Identifier]workflow.OutputDeclaration{"changeset": {Type: workflow.ValueObject}}}, Executor: workflow.ImplementationExecutor{TaskInput: "task", Instructions: "Update the README on disk."}}
	request := runexecution.AttemptRequestContext{Project: statestore.ProjectProjection{Status: statestore.ProjectActive, SourceHash: fmt.Sprintf("%x", sha256.Sum256([]byte(workspace)))}, Node: node, NodeInputs: map[workflow.Identifier]json.RawMessage{"task": json.RawMessage(`{"title":"Update the README"}`)}}
	built, err := buildWorkflowAttemptRequest(request, workspace, strings.Repeat("b", 64))
	if err != nil {
		t.Fatal(err)
	}
	if built.Access != provider.AccessWorkspaceWrite || built.CommandPolicy != provider.InteractionAllow || built.FilePolicy != provider.InteractionAllow || built.Network != provider.NetworkDenied {
		t.Fatalf("wrong policy: %#v", built)
	}
	if !strings.Contains(built.Prompt, "Modify the actual files") || !strings.Contains(built.Prompt, "Update the README on disk.") {
		t.Fatal("implementation instructions missing")
	}
	var schema map[string]any
	if err = json.Unmarshal(built.OutputSchema, &schema); err != nil {
		t.Fatal(err)
	}
	properties := schema["properties"].(map[string]any)
	if _, exists := properties["progress"]; exists {
		t.Fatal("implementation inherited a point plan/progress contract")
	}
	changeset := properties["changeset"].(map[string]any)
	if fmt.Sprint(changeset["required"]) != "[disposition summary files validation]" {
		t.Fatalf("changeset schema: %#v", changeset)
	}
	node.Common.Permissions = nil
	request.Node = node
	if _, err = buildWorkflowAttemptRequest(request, workspace, strings.Repeat("b", 64)); err == nil {
		t.Fatal("workspace writes granted without declared permissions")
	}
}

func TestExecutionPromptCannotReadUnconnectedSchedulerContext(t *testing.T) {
	workspace := t.TempDir()
	node := workflow.ReasoningNode{Common: workflow.NodeFields{Inputs: map[workflow.Identifier]workflow.Binding{"document": workflow.RequiredBinding{Type: workflow.ValueMarkdown}}, Outputs: map[workflow.Identifier]workflow.OutputDeclaration{"answer": {Type: workflow.ValueString}}}, Executor: workflow.ReasoningExecutor{Instructions: "Summarize the supplied document."}}
	request := runexecution.AttemptRequestContext{Project: statestore.ProjectProjection{Status: statestore.ProjectActive, SourceHash: fmt.Sprintf("%x", sha256.Sum256([]byte(workspace)))}, Node: node, NodeInputs: map[workflow.Identifier]json.RawMessage{"document": json.RawMessage(`"connected document"`)}, RunInputs: map[workflow.Identifier]json.RawMessage{"private": json.RawMessage(`"unconnected run value"`)}, AcceptedOutputs: map[workflow.Identifier]map[workflow.Identifier]json.RawMessage{"elsewhere": {"private": json.RawMessage(`"unconnected prior output"`)}}}
	built, err := buildWorkflowAttemptRequest(request, workspace, strings.Repeat("b", 64))
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"unconnected run value", "unconnected prior output", "workflowName", "workflowDigest", "acceptedOutputs", "runInputs", "Execute this exact installed workflow"} {
		if strings.Contains(built.Prompt, forbidden) {
			t.Fatalf("scheduler context leaked: %s", forbidden)
		}
	}
	if len(built.Inputs) == 0 || !strings.Contains(built.Inputs[0].Text, "connected document") || len(built.Inputs) != 1 || built.Inputs[0].Name != "document" {
		t.Fatal("scoped input missing")
	}
	// Changing unrelated scheduler state must not change the model request.
	request.RunInputs = nil
	request.AcceptedOutputs = nil
	again, err := buildWorkflowAttemptRequest(request, workspace, strings.Repeat("b", 64))
	if err != nil || built.Prompt != again.Prompt {
		t.Fatal("prompt depends on unconnected state")
	}
}
