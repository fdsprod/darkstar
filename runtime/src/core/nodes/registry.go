// Package nodes owns node-specific execution behavior. The daemon owns input
// binding, permission grants, attempt lifecycle, persistence, and transitions.
package nodes

import (
	"context"
	"encoding/json"
	"fmt"

	"darkstar/src/core/workflow"
)

type Inputs = map[workflow.Identifier]json.RawMessage

// Handler is closed to this package. Agent and deterministic handlers have
// different contracts; a handler cannot accidentally be dispatched as both.
type Handler interface{ isHandler() }

type AgentHandler interface {
	Handler
	BuildTask(nodeID string) (AgentTask, error)
	ConfigureOutputs(map[string]any)
}

type DeterministicHandler interface {
	Handler
	Execute(context.Context, Inputs, BuiltinServices) (json.RawMessage, error)
}

// Lookup is the single executable support registry. Schema support alone is
// not execution support. Unsupported types fail without invoking a provider.
func Lookup(node workflow.Node) (Handler, error) {
	switch n := node.(type) {
	case workflow.GitCommitNode, workflow.GitPushNode, workflow.CreatePRNode:
		return Delivery{Node: node}, nil
	case workflow.ExtensionNode:
		return Extension{Node: n}, nil
	case workflow.ReasoningNode:
		return Reasoning{Node: n}, nil
	case workflow.ImplementationNode:
		return Implementation{Node: n}, nil
	case workflow.PointExecutionNode:
		return PointExecution{Node: n}, nil
	case workflow.GateNode:
		return Gate{Node: n}, nil
	case workflow.CommandNode:
		return Command{Node: n}, nil
	case workflow.WorkspacePrepareNode:
		return WorkspacePrepare{Node: n}, nil
	case workflow.WorkspaceValidateNode:
		return WorkspaceValidate{Node: n}, nil
	case workflow.ApprovalNode:
		return nil, approvalUnsupported()
	case workflow.SubworkflowNode:
		return nil, subworkflowUnsupported()
	case workflow.RoutingNode:
		return nil, routingUnsupported()
	default:
		return nil, fmt.Errorf("unsupported workflow node %T", node)
	}
}
