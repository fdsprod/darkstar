package nodes

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"

	"darkstar/src/core/workflow"
)

type commandCall struct {
	workspace string
	argv      []string
	timeout   time.Duration
}
type recordingRunner struct {
	calls   []commandCall
	outputs []string
	failAt  int
}

func (r *recordingRunner) Run(ctx context.Context, path string, argv []string, timeout time.Duration) (string, error) {
	r.calls = append(r.calls, commandCall{path, append([]string(nil), argv...), timeout})
	out := ""
	if len(r.outputs) >= len(r.calls) {
		out = r.outputs[len(r.calls)-1]
	}
	if len(r.calls) == r.failAt {
		return out, errors.New("exit status 1")
	}
	return out, nil
}

type workspaceStoreFake struct {
	record              Workspace
	loadErr, resolveErr error
	created             int
}

func (s *workspaceStoreFake) Load(context.Context, string) (Workspace, error) {
	return s.record, s.loadErr
}
func (s *workspaceStoreFake) Create(_ context.Context, p Workspace) (Workspace, error) {
	s.created++
	s.record = p
	s.loadErr = nil
	return p, nil
}
func (s *workspaceStoreFake) Resolve(context.Context, []byte) (Workspace, error) {
	return s.record, s.resolveErr
}

func TestWorkspaceValidationUsesBoundWorkspaceAndStopsAtFailure(t *testing.T) {
	checks := [][]string{{"checker", "argument with spaces"}, {"second"}, {"must-not-run"}}
	n := WorkspaceValidate{Node: workflow.WorkspaceValidateNode{Executor: workflow.WorkspaceValidateExecutor{WorkspaceInput: "workspace", Checks: checks}}}
	store := &workspaceStoreFake{record: Workspace{ID: "ws", Path: "isolated-worktree"}}
	runner := &recordingRunner{failAt: 2, outputs: []string{"first passed", "second failed"}}
	services := BuiltinServices{Workspaces: WorkspaceServices{Store: store}, Commands: runner}
	raw, err := n.Execute(t.Context(), Inputs{"workspace": json.RawMessage(`{"id":"ws"}`)}, services)
	if err == nil || raw != nil || len(runner.calls) != 2 {
		t.Fatalf("failure advanced: %s %v", raw, err)
	}
	if runner.calls[0].workspace != "isolated-worktree" || runner.calls[0].timeout != 2*time.Minute || !reflect.DeepEqual(runner.calls[0].argv, checks[0]) {
		t.Fatal("check escaped workspace or changed arguments")
	}
	runner.calls = nil
	runner.failAt = 0
	raw, err = n.Execute(t.Context(), nil, services)
	if err != nil {
		t.Fatal(err)
	}
	var output struct {
		Validation struct {
			Passed      bool   `json:"passed"`
			WorkspaceID string `json:"workspaceId"`
			Checks      []any  `json:"checks"`
		} `json:"validation"`
	}
	if err = json.Unmarshal(raw, &output); err != nil {
		t.Fatal(err)
	}
	if !output.Validation.Passed || output.Validation.WorkspaceID != "ws" || len(output.Validation.Checks) != 3 {
		t.Fatalf("missing evidence: %s", raw)
	}
	store.resolveErr = errors.New("wrong owner")
	runner.calls = nil
	if _, err = n.Execute(t.Context(), nil, services); err == nil || len(runner.calls) != 0 {
		t.Fatal("executed against unresolved workspace")
	}
}

func TestWorkspaceValidationRejectsMissingOrEmptyChecks(t *testing.T) {
	for _, checks := range [][][]string{nil, {{}}, {{" "}}} {
		runner := &recordingRunner{}
		n := WorkspaceValidate{Node: workflow.WorkspaceValidateNode{Executor: workflow.WorkspaceValidateExecutor{Checks: checks}}}
		if _, err := n.Execute(t.Context(), nil, BuiltinServices{Workspaces: WorkspaceServices{Store: &workspaceStoreFake{}}, Commands: runner}); err == nil || len(runner.calls) != 0 {
			t.Fatal("invalid checks ran")
		}
	}
}
