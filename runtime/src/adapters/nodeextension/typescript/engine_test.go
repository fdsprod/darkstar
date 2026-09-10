package typescript

import (
	"context"
	"encoding/json"
	"errors"
	"os/exec"
	"reflect"
	"testing"
	"time"

	"darkstar/src/core/nodes"
	"darkstar/src/core/workflow"
	"darkstar/src/ports/plugin"
)

func realEngine(t *testing.T) *Engine {
	t.Helper()
	executable, err := exec.LookPath("node")
	if err != nil {
		t.Fatal("Node.js is required for node plugin integration tests")
	}
	engine, err := New(executable, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return engine
}
func TestAgentNodeBehaviorMatchesLegacyThroughRealProcess(t *testing.T) {
	engine := realEngine(t)
	for _, node := range []workflow.Node{
		workflow.ReasoningNode{Executor: workflow.ReasoningExecutor{Agent: "researcher", Instructions: "Compare evidence", Skills: []string{"writing"}, Tools: []string{"search"}}},
		workflow.ImplementationNode{Common: workflow.NodeFields{Permissions: []string{"workspace.write", "process.run"}}, Executor: workflow.ImplementationExecutor{TaskInput: "task", Instructions: "Keep changes small"}},
		workflow.PointExecutionNode{Common: workflow.NodeFields{Permissions: []string{"process.run", "workspace.write"}}},
	} {
		t.Run(string(node.Type()), func(t *testing.T) {
			handler, err := nodes.Lookup(node)
			if err != nil {
				t.Fatal(err)
			}
			agent := handler.(nodes.AgentHandler)
			want, err := agent.BuildTask("node")
			if err != nil {
				t.Fatal(err)
			}
			got, err := engine.BuildTask(t.Context(), node, "node")
			if err != nil {
				t.Fatal(err)
			}
			if got.Agent != want.Agent || got.Instructions != want.Instructions || got.Access != want.Access || !sameJSON(append([]string{}, got.Skills...), append([]string{}, want.Skills...)) || !sameJSON(append([]string{}, got.Tools...), append([]string{}, want.Tools...)) {
				t.Fatalf("task changed: %#v != %#v", got, want)
			}
			before := map[string]any{"changeset": map[string]any{"type": "object"}, "progress": map[string]any{"type": "object"}, "custom": map[string]any{"type": "string"}}
			expected := map[string]any{}
			for k, v := range before {
				expected[k] = v
			}
			agent.ConfigureOutputs(expected)
			if err = engine.ConfigureOutputs(t.Context(), node, before); err != nil {
				t.Fatal(err)
			}
			if !sameJSON(before, expected) {
				t.Fatalf("output contracts changed: %#v", before)
			}
		})
	}
	if _, err := engine.BuildTask(t.Context(), workflow.ReasoningNode{Common: workflow.NodeFields{Permissions: []string{"workspace.write"}}}, "bad"); err == nil {
		t.Fatal("reasoning accepted write permissions")
	}
}

type store struct{ workspace nodes.Workspace }

func (s *store) Load(context.Context, string) (nodes.Workspace, error) {
	return s.workspace, nil
}
func (s *store) Create(context.Context, nodes.Workspace) (nodes.Workspace, error) {
	return s.workspace, nil
}
func (s *store) Resolve(context.Context, []byte) (nodes.Workspace, error) {
	return s.workspace, nil
}

type runner struct {
	calls   [][]string
	paths   []string
	failAt  int
	outputs []string
}

func (r *runner) Run(_ context.Context, path string, argv []string, _ time.Duration) (string, error) {
	r.calls = append(r.calls, argv)
	r.paths = append(r.paths, path)
	if len(r.calls) == r.failAt {
		return "failed", errors.New("check failed")
	}
	if len(r.outputs) >= len(r.calls) {
		return r.outputs[len(r.calls)-1], nil
	}
	return "passed", nil
}
func TestWorkspaceValidationRealProcessStopsOnFailedCheck(t *testing.T) {
	engine := realEngine(t)
	checks := [][]string{{"first", "argument with spaces"}, {"second"}, {"never"}}
	node := workflow.WorkspaceValidateNode{Common: workflow.NodeFields{Inputs: map[workflow.Identifier]workflow.Binding{"workspace": workflow.RequiredBinding{}}}, Executor: workflow.WorkspaceValidateExecutor{WorkspaceInput: "workspace", Checks: checks}}
	run := &runner{failAt: 2}
	services := nodes.BuiltinServices{Commands: run, Workspaces: nodes.WorkspaceServices{Store: &store{workspace: nodes.Workspace{ID: "workspace", Path: "owned"}}}}
	if _, err := engine.Execute(t.Context(), node, nodes.Inputs{"workspace": json.RawMessage(`{"id":"workspace"}`)}, services); err == nil {
		t.Fatal("failed check succeeded")
	}
	if len(run.calls) != 2 || run.paths[0] != "owned" || !reflect.DeepEqual(run.calls[0], checks[0]) {
		t.Fatalf("unexpected calls %#v", run)
	}
	run.failAt = 0
	run.calls = nil
	run.paths = nil
	raw, err := engine.Execute(t.Context(), node, nodes.Inputs{"workspace": json.RawMessage(`{"id":"workspace"}`)}, services)
	if err != nil {
		t.Fatal(err)
	}
	if len(run.calls) != 3 || !json.Valid(raw) {
		t.Fatalf("missing validation evidence %s", raw)
	}
}

type captureRuntime struct{ args map[string]json.RawMessage }

func (c *captureRuntime) Describe(context.Context) (plugin.Descriptor, error) {
	return plugin.Descriptor{}, nil
}
func (c *captureRuntime) Invoke(_ context.Context, request plugin.Invocation, _ plugin.HostServices) (json.RawMessage, error) {
	_ = json.Unmarshal(request.Arguments, &c.args)
	return json.RawMessage(`{}`), nil
}
func TestPluginRequestExcludesUndeclaredInputsAndExecutionMetadata(t *testing.T) {
	runtime := &captureRuntime{}
	engine := &Engine{Runtime: runtime}
	node := workflow.WorkspaceValidateNode{Common: workflow.NodeFields{Inputs: map[workflow.Identifier]workflow.Binding{"workspace": workflow.RequiredBinding{}}}, Executor: workflow.WorkspaceValidateExecutor{WorkspaceInput: "workspace"}}
	_, err := engine.Execute(t.Context(), node, nodes.Inputs{"workspace": json.RawMessage(`{"id":"bound"}`), "secret": json.RawMessage(`"private"`)}, nodes.BuiltinServices{RunInputs: map[string]json.RawMessage{"hidden": json.RawMessage(`true`)}, LegacyCommand: nodes.LegacyCommandScope{WorkflowID: "private-graph"}})
	if err == nil {
		t.Fatal("plugin skipped mandatory validation without rejection")
	}
	if len(runtime.args) != 3 {
		t.Fatalf("unexpected request fields %v", runtime.args)
	}
	var inputs map[string]any
	_ = json.Unmarshal(runtime.args["inputs"], &inputs)
	if len(inputs) != 1 || inputs["workspace"] == nil {
		t.Fatal("plugin received undeclared data")
	}
}
func TestHostRejectsUndeclaredCommands(t *testing.T) {
	node := workflow.WorkspaceValidateNode{Executor: workflow.WorkspaceValidateExecutor{Checks: [][]string{{"safe"}}}}
	run := &runner{}
	host := &executionServices{node: node, services: nodes.BuiltinServices{Commands: run}, workspace: &nodes.Workspace{Path: "owned"}}
	if _, err := host.Call(t.Context(), "process.run", json.RawMessage(`{"argv":["different"],"timeoutSeconds":120}`)); err == nil {
		t.Fatal("undeclared executable ran")
	}
	if _, err := host.Call(t.Context(), "approval.grant", json.RawMessage(`{}`)); err == nil {
		t.Fatal("approval callback exists")
	}
	if len(run.calls) != 0 {
		t.Fatal("unauthorized command reached runner")
	}
}

func TestWorkspacePrepareRealProcessUsesBoundRepository(t *testing.T) {
	engine := realEngine(t)
	node := workflow.WorkspacePrepareNode{Common: workflow.NodeFields{Inputs: map[workflow.Identifier]workflow.Binding{"repository": workflow.RequiredBinding{}}}, Executor: workflow.WorkspacePrepareExecutor{RepositoryInput: "repository", Checkout: workflow.CurrentCheckout{}}}
	workspace := nodes.Workspace{ID: "owned", Path: "project", ProjectID: "project", RunID: "run", Mode: "current_checkout"}
	services := nodes.BuiltinServices{Workspaces: nodes.WorkspaceServices{Identity: nodes.WorkspaceIdentity{ProjectID: "project", SourceHash: "source", RunID: "run", NodeID: "prepare", Root: "project"}, Store: &store{workspace: workspace}}}
	inputs := nodes.Inputs{"repository": json.RawMessage(`{"projectId":"project","sourceHash":"source"}`)}
	got, err := engine.Execute(t.Context(), node, inputs, services)
	if err != nil {
		t.Fatal(err)
	}
	want, err := (nodes.WorkspacePrepare{Node: node}).Execute(t.Context(), inputs, services)
	if err != nil || !sameJSON(got, want) {
		t.Fatalf("workspace behavior changed: %s / %s: %v", got, want, err)
	}
	inputs["repository"] = json.RawMessage(`{"projectId":"other","sourceHash":"source"}`)
	if _, err = engine.Execute(t.Context(), node, inputs, services); err == nil {
		t.Fatal("prepared an unowned repository")
	}
}

func TestCommandRealProcessMatchesLegacyEvidenceAndRejectsOtherScope(t *testing.T) {
	engine := realEngine(t)
	node := workflow.CommandNode{Executor: workflow.CommandExecutor{Argv: []string{"darkstar-project", "validate", "--json"}}}
	run := &runner{outputs: []string{"", " M tracked.txt\n?? new.txt\n"}}
	services := nodes.BuiltinServices{Commands: run, LegacyCommand: nodes.LegacyCommandScope{WorkflowID: "darkstar/story-execution", NodeID: "s6_validation", Workspace: "owned"}}
	got, err := engine.Execute(t.Context(), node, nil, services)
	if err != nil {
		t.Fatal(err)
	}
	legacy := &runner{outputs: run.outputs}
	want, err := nodes.CommandOutput(t.Context(), services.LegacyCommand.WorkflowID, services.LegacyCommand.NodeID, "owned", node, legacy)
	if err != nil || !sameJSON(got, want) || !reflect.DeepEqual(run.calls, legacy.calls) {
		t.Fatalf("command behavior changed: %s / %s: %v", got, want, err)
	}
	services.LegacyCommand.WorkflowID = "other"
	if _, err = engine.Execute(t.Context(), node, nil, services); err == nil {
		t.Fatal("command escaped its authorized workflow scope")
	}
}
