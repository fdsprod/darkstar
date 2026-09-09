package workflowtools

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"darkstar/src/core/workflow"
)

func TestMultipleTemplatedOutputsAndAppendOnlyJournal(t *testing.T) {
	s := &Session{Database: filepath.Join(t.TempDir(), "tools.db"), RunID: "run1", AttemptID: "attempt1", Inputs: map[workflow.Identifier]json.RawMessage{
		"design_template": json.RawMessage(`{"content":"# Design\n## Approach","version":"1.0.0","requiredHeadings":["Approach"]}`),
		"test_template":   json.RawMessage(`{"content":"# Tests\n## Coverage","version":"1.0.0","requiredHeadings":["Coverage"]}`),
		"open":            json.RawMessage(`{"kind":"open_items","resourceId":"open"}`),
		"decisions":       json.RawMessage(`{"kind":"decision_log","resourceId":"decisions"}`),
	}, Node: workflow.ReasoningNode{Common: workflow.NodeFields{Outputs: map[workflow.Identifier]workflow.OutputDeclaration{
		"design":  {Type: workflow.ValueString, Artifact: &workflow.ArtifactContract{Filename: "design.md", TemplateInput: "design_template"}},
		"tests":   {Type: workflow.ValueString, Artifact: &workflow.ArtifactContract{Filename: "tests.md", TemplateInput: "test_template"}},
		"summary": {Type: workflow.ValueString, Artifact: &workflow.ArtifactContract{Filename: "summary.md"}},
	}}}}
	ctx := t.Context()
	call := func(id, name, body string) json.RawMessage {
		t.Helper()
		value, err := s.Call(ctx, id, name, json.RawMessage(body))
		if err != nil {
			t.Fatal(err)
		}
		return value
	}
	if _, err := s.Call(ctx, "bad", "submit_output", json.RawMessage(`{"id":"design","value":"# Wrong"}`)); err == nil {
		t.Fatal("missing heading accepted")
	}
	call("d", "submit_output", `{"id":"design","value":"# Design\n## Approach\nUse an adapter."}`)
	call("t", "submit_output", `{"id":"tests","value":"# Tests\n## Coverage\nExercise the boundary."}`)
	call("s", "submit_output", `{"id":"summary","value":"# Summary\nThree deliverables."}`)
	if err := s.ValidateFinal(json.RawMessage(`{"design":"# Design\n## Approach\nUse an adapter.","tests":"# Tests\n## Coverage\nExercise the boundary.","summary":"# Summary\nThree deliverables."}`)); err != nil {
		t.Fatal(err)
	}
	if err := s.ValidateFinal(json.RawMessage(`{"design":"changed","tests":"changed","summary":"changed"}`)); err == nil {
		t.Fatal("unsubmitted final output accepted")
	}
	created := call("a", "journal_open", `{"operation":"add","entryId":"","text":"Choose timeout","key":"timeout"}`)
	var entry struct {
		ID string `json:"entryId"`
	}
	_ = json.Unmarshal(created, &entry)
	call("retry", "journal_open", `{"operation":"add","entryId":"","text":"Choose timeout","key":"timeout"}`)
	call("r", "journal_open", `{"operation":"resolve","entryId":"`+entry.ID+`","text":"Use 30 seconds","key":"resolve-timeout"}`)
	if _, err := s.Call(ctx, "rewrite", "journal_open", json.RawMessage(`{"operation":"delete","entryId":"`+entry.ID+`","text":"","key":"delete"}`)); err == nil {
		t.Fatal("journal deletion allowed")
	}
	if _, err := s.Call(ctx, "conflict", "journal_open", json.RawMessage(`{"operation":"add","entryId":"","text":"Changed history","key":"timeout"}`)); err == nil {
		t.Fatal("conflicting retry allowed")
	}
	read := string(call("read", "journal_open", `{"operation":"read","entryId":"","text":"","key":""}`))
	if strings.Count(read, "Choose timeout") != 1 || !strings.Contains(read, "Use 30 seconds") {
		t.Fatal(read)
	}
	s.RunID = "run2"
	if strings.Contains(string(call("read2", "journal_open", `{"operation":"read"}`)), "Choose timeout") {
		t.Fatal("journal leaked between runs")
	}
}

func TestSubmittedOutputsAreDurableAndRequiredWithoutFinalEcho(t *testing.T) {
	optional := false
	node := workflow.ReasoningNode{Common: workflow.NodeFields{Outputs: map[workflow.Identifier]workflow.OutputDeclaration{"research": {Type: workflow.ValueMarkdown}, "design": {Type: workflow.ValueMarkdown}, "optional": {Type: workflow.ValueString, Required: &optional}}}}
	session := &Session{Database: filepath.Join(t.TempDir(), "tools.db"), RunID: "run", AttemptID: "attempt", Node: node}
	if _, err := session.ResolveSubmittedOutputs(t.Context()); err == nil {
		t.Fatal("missing deliverables accepted")
	}
	for _, id := range []string{"research", "design"} {
		body, _ := json.Marshal(map[string]any{"id": id, "value": "# " + id + "\n\nBody"})
		if _, err := session.Call(t.Context(), id, "submit_output", body); err != nil {
			t.Fatal(err)
		}
	}
	recovered := &Session{Database: session.Database, RunID: "run", AttemptID: "attempt", Node: node}
	raw, err := recovered.ResolveSubmittedOutputs(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	var result map[string]string
	if err = json.Unmarshal(raw, &result); err != nil || len(result) != 2 || result["design"] != "# design\n\nBody" {
		t.Fatalf("bad durable result %s: %v", raw, err)
	}
	recovered.AttemptID = "other"
	if _, err = recovered.ResolveSubmittedOutputs(t.Context()); err == nil {
		t.Fatal("other attempt's submissions leaked")
	}
}
