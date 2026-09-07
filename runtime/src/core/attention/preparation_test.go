package attention

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"darkstar/src/core/preparation"
	"darkstar/src/core/workflow"
	"darkstar/src/ports/routeadvisor"
	"darkstar/src/ports/statestore"
)

type preparationAttentionSource struct{ *attentionSource }

func (source preparationAttentionSource) Runs(context.Context) ([]statestore.RunProjection, error) {
	runs := []statestore.RunProjection{}
	for _, run := range source.runs {
		runs = append(runs, run)
	}
	return runs, nil
}

func TestPreparationAttentionTracksWaitingRunWithoutProviderOwner(t *testing.T) {
	work := statestore.WorkItemProjection{WorkItemID: "work_1", ProjectID: "project_1", Title: "Clarify deliverable"}
	input := preparation.Input{
		Work: work, WorkflowDigest: strings.Repeat("a", 64), Policy: preparation.Policy{Version: "test"},
		Workflow: workflow.Document{APIVersion: workflow.APIVersionV1Alpha2, Kind: workflow.KindWorkflow, Metadata: workflow.Metadata{Name: "test", Version: "1.0.0"}, Spec: workflow.Spec{RouteDefaults: workflow.RouteDefaults{Entry: "review", Terminals: []workflow.Identifier{"review"}}, Nodes: map[workflow.Identifier]workflow.Node{"review": workflow.ReasoningNode{Common: workflow.NodeFields{Entry: true, Terminal: true, Inputs: map[workflow.Identifier]workflow.Binding{}, Outputs: map[workflow.Identifier]workflow.OutputDeclaration{}, Checkpoint: workflow.NoCheckpoint{}}, Executor: workflow.ReasoningExecutor{Agent: "test"}}}}},
	}
	assessment, err := preparation.Assess(input, routeadvisor.Advice{Confidence: "high", Candidates: []routeadvisor.CandidateAdvice{{Entry: "review", Terminals: []string{"review"}, Disposition: "input_required", Rationale: "Acceptance criteria missing", Questions: []routeadvisor.Question{{ID: "acceptance", Prompt: "Which acceptance criteria apply?"}}}}})
	if err != nil {
		t.Fatal(err)
	}
	route := assessment.Route
	route.Assessment, err = json.Marshal(assessment)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(route)
	if err != nil {
		t.Fatal(err)
	}
	source := preparationAttentionSource{&attentionSource{
		works: map[string]statestore.WorkItemProjection{work.WorkItemID: work}, projects: map[string]statestore.ProjectProjection{"project_1": {ProjectID: "project_1", Name: "Test"}},
		runs: map[string]statestore.RunProjection{"run_1": {RunID: "run_1", WorkItemID: work.WorkItemID, Status: statestore.RunWaiting, RouteSnapshot: statestore.JSONSnapshot(encoded), ResourceVersion: 3}},
	}}
	service, err := New(source)
	if err != nil {
		t.Fatal(err)
	}
	page, err := service.List(context.Background(), ListRequest{IncludePreparation: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 1 {
		t.Fatalf("attention=%#v", page)
	}
	legacy, err := service.List(context.Background(), ListRequest{})
	if err != nil || len(legacy.Items) != 0 || legacy.NextCursor != "" {
		t.Fatalf("legacy attention exposed new variant: %#v, %v", legacy, err)
	}
	item, ok := page.Items[0].(PreparationInputRequired)
	if !ok || item.Subject.AssessmentDigest != assessment.Digest || item.Subject.Questions[0].ID != "acceptance" || item.DeepLink != "/work/work_1" || len(item.AllowedActions) != 1 || item.AllowedActions[0] != PreparationInputPrepare {
		t.Fatalf("item=%#v", page.Items[0])
	}
	wire, err := json.Marshal(page)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(wire), "attemptId") || strings.Contains(string(wire), "providerRequestId") || !strings.Contains(string(wire), `"source":"route_preparation"`) {
		t.Fatalf("incorrect preparation shape: %s", wire)
	}
	var restored Page
	if err := json.Unmarshal(wire, &restored); err != nil {
		t.Fatal(err)
	}
	if _, ok := restored.Items[0].(*PreparationInputRequired); !ok {
		t.Fatalf("decoded item %T", restored.Items[0])
	}
	filtered, err := service.List(context.Background(), ListRequest{IncludePreparation: true, WorkItemID: "work_other"})
	if err != nil || len(filtered.Items) != 0 {
		t.Fatalf("filtered=%#v %v", filtered, err)
	}
	for _, status := range []statestore.RunStatus{statestore.RunCancelled, statestore.RunReady, statestore.RunRunning, statestore.RunCompleted} {
		run := source.runs["run_1"]
		run.Status = status
		source.runs["run_1"] = run
		page, err = service.List(context.Background(), ListRequest{IncludePreparation: true})
		if err != nil || len(page.Items) != 0 {
			t.Fatalf("resolved %s still pending: %#v %v", status, page, err)
		}
	}
	run := source.runs["run_1"]
	run.Status = statestore.RunWaiting
	run.RouteSnapshot = statestore.JSONSnapshot(strings.Replace(string(encoded), "Acceptance criteria missing", "Tampered assessment", 1))
	source.runs["run_1"] = run
	if _, err = service.List(context.Background(), ListRequest{IncludePreparation: true}); err == nil {
		t.Fatal("tampered preparation surfaced as trusted attention")
	}
}

func TestPreparationAttentionRejectsUnknownInputSource(t *testing.T) {
	var items Checkpoints
	if err := json.Unmarshal([]byte(`[{"kind":"input_required","subject":{"source":"unknown"}}]`), &items); err == nil {
		t.Fatal("unknown sibling accepted")
	}
}
