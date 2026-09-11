package cli

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"darkstar/src/core/contentlibrary"
	"darkstar/src/core/runexecution"
	"darkstar/src/core/workflow"
	"darkstar/src/ports/contentstore"
	"darkstar/src/ports/statestore"
)

func TestLinkedPromptUsesFrozenVersionAndDistinguishesAbsentEmptyUnavailable(t *testing.T) {
	workspace := t.TempDir()
	workspaceDigest := fmt.Sprintf("%x", sha256.Sum256([]byte(filepath.Clean(workspace))))
	version := contentlibrary.BuiltinItems()[0].Versions[0]
	if version.Document.Kind != "prompt" {
		t.Fatal("fixture must use a prompt")
	}
	node := workflow.ReasoningNode{Common: workflow.NodeFields{
		Prompt:  &version.Reference,
		Inputs:  map[workflow.Identifier]workflow.Binding{},
		Outputs: map[workflow.Identifier]workflow.OutputDeclaration{"result": {Type: workflow.ValueString}},
	}, Executor: workflow.ReasoningExecutor{Agent: "writer"}}
	request := runexecution.AttemptRequestContext{
		Attempt:          statestore.AttemptProjection{NodeID: "write"},
		Project:          statestore.ProjectProjection{SourceHash: workspaceDigest, Status: statestore.ProjectActive},
		Node:             node,
		NodeInputs:       map[workflow.Identifier]json.RawMessage{},
		ExecutionContext: statestore.RunExecutionContext{PromptSnapshots: map[string]contentstore.Version{"write": version}},
	}
	built, err := buildWorkflowAttemptRequest(request, workspace, "")
	if err != nil || !strings.Contains(built.Prompt, "Open items is not connected") {
		t.Fatalf("absent input guidance: %v", err)
	}
	node.Common.Inputs["open_items"] = workflow.OptionalBinding{From: "run.input.open_items", Type: workflow.ValueOpenItems}
	request.Node = node
	if _, err := buildWorkflowAttemptRequest(request, workspace, ""); err == nil || !strings.Contains(err.Error(), "unavailable") {
		t.Fatal("unavailable linked input must fail rather than appear absent")
	}
	request.NodeInputs["open_items"] = json.RawMessage(`{"items":[]}`)
	built, err = buildWorkflowAttemptRequest(request, workspace, "")
	if err != nil || !strings.Contains(built.Prompt, "Open items is connected") || strings.Contains(built.Prompt, "Open items is not connected") {
		t.Fatalf("empty linked collection guidance: %v", err)
	}
	if len(built.Inputs) != 1 || strings.Contains(built.Prompt, `"items":[]`) {
		t.Fatal("input value must be delivered separately once")
	}
	request.Revision = true
	built, err = buildWorkflowAttemptRequest(request, workspace, "")
	if err != nil || !strings.Contains(built.Prompt, "This is a revision") {
		t.Fatalf("revision condition not assembled: %v", err)
	}
	if !strings.Contains(built.Prompt, "do not invent an undeclared findings output") || !strings.Contains(built.Prompt, "Submit only the currently declared revised deliverable") {
		t.Fatal("revision requires outputs outside its narrowed contract")
	}
	version.Document.Instructions = "Replaced after run creation"
	request.ExecutionContext.PromptSnapshots["write"] = version
	if _, err := buildWorkflowAttemptRequest(request, workspace, ""); err == nil {
		t.Fatal("changed pinned snapshot must be rejected")
	}
	delete(request.ExecutionContext.PromptSnapshots, "write")
	if _, err := buildWorkflowAttemptRequest(request, workspace, ""); err == nil {
		t.Fatal("missing frozen snapshot must be rejected")
	}
}

func TestCustomConditionalInputCannotSilentlyDisappear(t *testing.T) {
	workspace := t.TempDir()
	workspaceDigest := fmt.Sprintf("%x", sha256.Sum256([]byte(filepath.Clean(workspace))))
	for _, condition := range []string{"input_linked", "input_absent"} {
		t.Run(condition, func(t *testing.T) {
			document := contentstore.Document{Kind: "prompt", Instructions: "Scoped task", Sections: []contentstore.Section{{ID: "research", When: contentstore.Condition{Kind: condition, Input: "research"}, Instructions: "Research condition matched."}}}
			ref := contentstore.Reference{ID: "custom-prompt", Version: "1.0.0", Digest: contentlibrary.Digest(document)}
			node := workflow.ReasoningNode{Common: workflow.NodeFields{Prompt: &ref, Inputs: map[workflow.Identifier]workflow.Binding{"research": workflow.OptionalBinding{From: "run.input.research", Type: workflow.ValueMarkdown}}, Outputs: map[workflow.Identifier]workflow.OutputDeclaration{"result": {Type: workflow.ValueString}}}, Executor: workflow.ReasoningExecutor{Agent: "writer"}}
			request := runexecution.AttemptRequestContext{
				Attempt: statestore.AttemptProjection{NodeID: "write"}, Node: node,
				Project:          statestore.ProjectProjection{SourceHash: workspaceDigest, Status: statestore.ProjectActive},
				NodeInputs:       map[workflow.Identifier]json.RawMessage{},
				ExecutionContext: statestore.RunExecutionContext{PromptSnapshots: map[string]contentstore.Version{"write": {Reference: ref, Document: document}}},
			}
			if _, err := buildWorkflowAttemptRequest(request, workspace, ""); err == nil || !strings.Contains(err.Error(), "research is unavailable") {
				t.Fatalf("missing custom conditional input accepted: %v", err)
			}
			request.NodeInputs["research"] = json.RawMessage(`""`)
			if _, err := buildWorkflowAttemptRequest(request, workspace, ""); err != nil {
				t.Fatalf("explicit empty input rejected: %v", err)
			}
			delete(node.Common.Inputs, "research")
			request.Node = node
			delete(request.NodeInputs, "research")
			if _, err := buildWorkflowAttemptRequest(request, workspace, ""); err != nil {
				t.Fatalf("intentionally disconnected input rejected: %v", err)
			}
		})
	}
}

func TestTracerBulletsInstructionsLoadOnlyWhenDeclared(t *testing.T) {
	workspace := t.TempDir()
	workspaceDigest := fmt.Sprintf("%x", sha256.Sum256([]byte(filepath.Clean(workspace))))
	node := workflow.ReasoningNode{Common: workflow.NodeFields{
		Inputs:  map[workflow.Identifier]workflow.Binding{},
		Outputs: map[workflow.Identifier]workflow.OutputDeclaration{"result": {Type: workflow.ValueString}},
	}, Executor: workflow.ReasoningExecutor{Agent: "planner"}}
	request := runexecution.AttemptRequestContext{
		Attempt: statestore.AttemptProjection{NodeID: "plan"}, Node: node,
		Project:    statestore.ProjectProjection{SourceHash: workspaceDigest, Status: statestore.ProjectActive},
		NodeInputs: map[workflow.Identifier]json.RawMessage{},
	}
	plain, err := buildWorkflowAttemptRequest(request, workspace, "")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(plain.Prompt, tracerBulletsSkill) {
		t.Fatal("undeclared planning skill entered the attempt")
	}
	node.Executor.Skills = []string{"darkstar:tracer-bullets", "darkstar:tracer-bullets"}
	request.Node = node
	loaded, err := buildWorkflowAttemptRequest(request, workspace, "")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(loaded.Prompt, tracerBulletsSkill) != 1 {
		t.Fatal("declared skill must supply its actual instructions exactly once")
	}
}
