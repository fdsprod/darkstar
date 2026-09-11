package runexecution

import (
	"encoding/json"
	"testing"

	"darkstar/src/core/contentlibrary"
	"darkstar/src/core/workflow"
	"darkstar/src/ports/contentstore"
	"darkstar/src/ports/statestore"
)

func TestInlinePromptArtifactReviewRetainsIndependentTemplateValidation(t *testing.T) {
	request := AttemptRequestContext{
		Node: workflow.ReasoningNode{Common: workflow.NodeFields{
			Inputs:  map[workflow.Identifier]workflow.Binding{"template": workflow.RequiredBinding{From: "run.input.design_template", Type: workflow.ValueTemplate}},
			Outputs: map[workflow.Identifier]workflow.OutputDeclaration{"design": {Type: workflow.ValueMarkdown, Artifact: &workflow.ArtifactContract{Filename: "design.md", TemplateInput: "template"}}},
		}, Executor: workflow.ReasoningExecutor{Instructions: "Write the supplied design.", Skills: []string{"darkstar:tracer-bullets"}}},
		NodeInputs: map[workflow.Identifier]json.RawMessage{"template": json.RawMessage(`{"content":"# Design\n## Scope","version":"1.0.0","requiredHeadings":["Scope"]}`)},
	}
	revision := buildReviewTask(request, "design", json.RawMessage(`"# Design\n## Scope\nOriginal"`), json.RawMessage(`{"instruction":"Clarify scope"}`))
	if skills := revision.Node.(workflow.ReasoningNode).Executor.Skills; len(skills) != 1 || skills[0] != "darkstar:tracer-bullets" {
		t.Fatal("declared skill lost during artifact revision")
	}
	if revision.Node.Fields().Prompt != nil || revision.Node.Fields().Outputs["design"].Artifact.TemplateInput != "template" || len(revision.NodeInputs["template"]) == 0 {
		t.Fatal("independent template link lost during inline-prompt revision")
	}
	if err := workflow.ValidateDeliverable(revision.Node, "design", json.RawMessage(`"# Design\nScope accidentally removed"`), revision.NodeInputs); err == nil {
		t.Fatal("revision bypassed template required headings")
	}
	if err := workflow.ValidateDeliverable(revision.Node, "design", json.RawMessage(`"# Design\n## Scope\nClarified"`), revision.NodeInputs); err != nil {
		t.Fatal(err)
	}
	delete(revision.NodeInputs, "template")
	if err := workflow.ValidateDeliverable(revision.Node, "design", json.RawMessage(`"# Design\n## Scope\nClarified"`), revision.NodeInputs); err == nil {
		t.Fatal("unavailable linked template accepted during review")
	}
}

func TestReviewRetainsCustomConditionalInputConnection(t *testing.T) {
	document := contentstore.Document{Kind: "prompt", Instructions: "Write design", Sections: []contentstore.Section{{ID: "research", When: contentstore.Condition{Kind: "input_linked", Input: "research"}, Instructions: "Use research."}}}
	ref := contentstore.Reference{ID: "custom", Version: "1.0.0", Digest: contentlibrary.Digest(document)}
	request := AttemptRequestContext{
		Attempt:          statestore.AttemptProjection{NodeID: "design"},
		Node:             workflow.ReasoningNode{Common: workflow.NodeFields{Prompt: &ref, Inputs: map[workflow.Identifier]workflow.Binding{"research": workflow.OptionalBinding{From: "run.input.research", Type: workflow.ValueMarkdown}}, Outputs: map[workflow.Identifier]workflow.OutputDeclaration{"design": {Type: workflow.ValueMarkdown}}}},
		NodeInputs:       map[workflow.Identifier]json.RawMessage{"research": json.RawMessage(`"Supplied research"`)},
		ExecutionContext: statestore.RunExecutionContext{PromptSnapshots: map[string]contentstore.Version{"design": {Reference: ref, Document: document}}},
	}
	revision := buildReviewTask(request, "design", json.RawMessage(`"Candidate"`), json.RawMessage(`{"instruction":"Revise"}`))
	if _, exists := revision.Node.Fields().Inputs["research"]; !exists || string(revision.NodeInputs["research"]) != `"Supplied research"` {
		t.Fatal("custom conditional connection lost during revision")
	}
}
