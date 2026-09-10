package nodes

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"darkstar/src/core/workflow"
	"darkstar/src/ports/provider"
)

func TestImplementationRequiresExactPermissionsAndProducesStrictChangeset(t *testing.T) {
	n := Implementation{Node: workflow.ImplementationNode{Executor: workflow.ImplementationExecutor{TaskInput: "task", Instructions: "Update README."}}}
	for _, permissions := range [][]string{nil, {"workspace.write"}, {"process.run", "workspace.write", "network"}, {"process.run", "process.run"}} {
		n.Node.Common.Permissions = permissions
		if _, err := n.BuildTask("edit"); err == nil {
			t.Fatalf("accepted %v", permissions)
		}
	}
	n.Node.Common.Permissions = []string{"workspace.write", "process.run"}
	task, err := n.BuildTask("edit")
	if err != nil {
		t.Fatal(err)
	}
	if task.Access != provider.AccessWorkspaceWrite || !strings.Contains(task.Instructions, "Do not commit, push, publish, or deploy.") || !strings.HasSuffix(task.Instructions, "Update README.") {
		t.Fatal("implementation boundary changed")
	}
	properties := map[string]any{"changeset": nil, "notes": map[string]any{"type": "string"}}
	n.ConfigureOutputs(properties)
	raw, _ := json.Marshal(properties)
	if !strings.Contains(string(raw), `"enum":["changed","unchanged","blocked"]`) || !strings.Contains(string(raw), `"required":["disposition","summary","files","validation"]`) {
		t.Fatalf("schema = %s", raw)
	}
	if properties["progress"] != nil {
		t.Fatal("implementation acquired point execution outputs")
	}
}

type agentWorkspaceFake struct {
	workspace Workspace
	err       error
	captured  []string
}

func (s *agentWorkspaceFake) Resolve(context.Context, []byte) (Workspace, error) {
	return s.workspace, s.err
}
func (s *agentWorkspaceFake) CaptureBaseline(_ context.Context, path string) error {
	s.captured = append(s.captured, path)
	return s.err
}

func TestImplementationRefreshesWorkspaceWithoutRewritingPriorInputs(t *testing.T) {
	n := Implementation{Node: workflow.ImplementationNode{Executor: workflow.ImplementationExecutor{WorkspaceInput: "workspace"}}}
	inputs := Inputs{"workspace": json.RawMessage(`{"id":"ws","path":"old"}`), "task": json.RawMessage(`{"title":"edit"}`)}
	service := &agentWorkspaceFake{workspace: Workspace{ID: "ws", Path: "relocated"}}
	resolved, path, suffix, err := n.Prepare(t.Context(), inputs, "original", service)
	if err != nil {
		t.Fatal(err)
	}
	if path != "relocated" || len(service.captured) != 1 || service.captured[0] != path || suffix == "" {
		t.Fatal("baseline did not bind to resolved workspace")
	}
	if string(inputs["workspace"]) != `{"id":"ws","path":"old"}` || string(resolved["workspace"]) == string(inputs["workspace"]) {
		t.Fatal("workspace history was changed or not refreshed")
	}
	service.err = errors.New("workspace owned by another run")
	service.captured = nil
	if _, _, _, err = n.Prepare(t.Context(), inputs, "original", service); err == nil || len(service.captured) != 0 {
		t.Fatal("failed resolution fell back to original checkout")
	}
}

func TestImplementationResultUsesEvidenceRatherThanSuccessNarration(t *testing.T) {
	for _, tc := range []struct {
		name, raw string
		files     []string
		want      string
		reads     int
	}{
		{"changed", `{"disposition":"changed","summary":"done","files":["b","a"],"validation":[]}`, []string{"a", "b"}, "", 1},
		{"unchanged", `{"disposition":"unchanged","summary":"already done","files":[],"validation":[]}`, nil, "", 1},
		{"blocked", `{"disposition":"blocked","summary":"missing tools","files":[],"validation":["bootstrap failed"]}`, nil, "implementation blocked", 0},
		{"invented files", `{"disposition":"changed","summary":"done","files":["imaginary"],"validation":[]}`, []string{"real"}, "must match actual", 1},
		{"false changed", `{"disposition":"changed","summary":"done","files":[],"validation":[]}`, nil, "does not match", 1},
		{"false unchanged", `{"disposition":"unchanged","summary":"done","files":["a"],"validation":[]}`, []string{"a"}, "does not match", 1},
		{"missing arrays", `{"disposition":"changed","summary":"done"}`, nil, "explicit files", 0},
		{"invalid validation", `{"disposition":"changed","summary":"done","files":[],"validation":"pass"}`, nil, "cannot unmarshal", 0},
		{"unknown disposition", `{"disposition":"success","summary":"done","files":[],"validation":[]}`, nil, "disposition must be", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reads := 0
			err := ValidateImplementationResult(t.Context(), json.RawMessage(tc.raw), func(context.Context) ([]string, error) {
				reads++
				return tc.files, nil
			})
			if tc.want == "" && err != nil {
				t.Fatal(err)
			}
			if tc.want != "" && (err == nil || !strings.Contains(err.Error(), tc.want)) {
				t.Fatalf("error = %v", err)
			}
			if reads != tc.reads {
				t.Fatalf("evidence reads = %d", reads)
			}
		})
	}
	want := errors.New("evidence unavailable")
	err := ValidateImplementationResult(t.Context(), json.RawMessage(`{"disposition":"changed","summary":"done","files":[],"validation":[]}`), func(context.Context) ([]string, error) {
		return nil, want
	})
	if !errors.Is(err, want) {
		t.Fatal("lost evidence failure")
	}
}
