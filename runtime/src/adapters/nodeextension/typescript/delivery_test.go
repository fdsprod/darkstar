package typescript

import (
	"context"
	"darkstar/src/core/nodes"
	"darkstar/src/core/workflow"
	"encoding/json"
	"testing"
)

type deliveryRecorder struct {
	calls  int
	node   workflow.Node
	inputs nodes.Inputs
}

func (s *deliveryRecorder) Execute(_ context.Context, node workflow.Node, inputs nodes.Inputs) (json.RawMessage, error) {
	s.calls++
	s.node = node
	s.inputs = inputs
	return json.RawMessage(`{"observed":"host result"}`), nil
}

func TestDeliveryPluginsCallOnlyTheirScopedHostOperation(t *testing.T) {
	engine := realEngine(t)
	for _, node := range []workflow.Node{
		workflow.GitCommitNode{Executor: workflow.GitCommitExecutor{WorkspaceInput: "workspace", ChangesetInput: "changeset", TextInput: "text"}},
		workflow.GitPushNode{Executor: workflow.GitPushExecutor{WorkspaceInput: "workspace", CommitInput: "commit", Remote: "origin"}},
		workflow.CreatePRNode{Executor: workflow.CreatePRExecutor{WorkspaceInput: "workspace", BranchInput: "branch", TextInput: "text", Base: "remote_default"}},
	} {
		service := &deliveryRecorder{}
		output, err := engine.Execute(t.Context(), node, nodes.Inputs{}, nodes.BuiltinServices{Delivery: service})
		if err != nil {
			t.Fatal(err)
		}
		if service.calls != 1 || service.node.Type() != node.Type() || string(output) != `{"observed":"host result"}` {
			t.Fatalf("wrong scoped operation for %s: %s", node.Type(), output)
		}
	}
}
