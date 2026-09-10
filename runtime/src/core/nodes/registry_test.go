package nodes

import (
	"darkstar/src/core/workflow"
	"testing"
)

func TestRegistrySeparatesExecutionKindsAndRejectsUnsupportedTypes(t *testing.T) {
	for _, tc := range []struct {
		node workflow.Node
		kind string
	}{
		{workflow.ReasoningNode{}, "agent"}, {workflow.ImplementationNode{}, "agent"}, {workflow.PointExecutionNode{}, "agent"},
		{workflow.GateNode{}, "deterministic"}, {workflow.CommandNode{}, "deterministic"}, {workflow.WorkspacePrepareNode{}, "deterministic"}, {workflow.WorkspaceValidateNode{}, "deterministic"},
		{workflow.ApprovalNode{}, "unsupported"}, {workflow.SubworkflowNode{}, "unsupported"}, {workflow.RoutingNode{}, "unsupported"},
	} {
		t.Run(string(tc.node.Type()), func(t *testing.T) {
			h, err := Lookup(tc.node)
			if tc.kind == "unsupported" {
				if err == nil || h != nil {
					t.Fatal("unsupported node became executable")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			_, agent := h.(AgentHandler)
			_, deterministic := h.(DeterministicHandler)
			if agent == deterministic || agent != (tc.kind == "agent") {
				t.Fatalf("ambiguous/wrong handler: %T", h)
			}
		})
	}
	if _, err := Lookup(nil); err == nil {
		t.Fatal("nil node accepted")
	}
}
