package nodes

import (
	"context"
	"fmt"
	"slices"

	"darkstar/src/core/workflow"
	"darkstar/src/ports/provider"
)

// AgentTask contains node requirements, not permission grants or workflow state.
// The daemon projects these requirements into an authorized provider request.
type AgentTask struct {
	Agent        string
	Instructions string
	Skills       []string
	Tools        []string
	Access       provider.AccessClass
}

type AgentWorkspace interface {
	Resolve(context.Context, []byte) (Workspace, error)
	CaptureBaseline(context.Context, string) error
}

// AgentPreparation is implemented only by nodes with workspace preparation.
// Inputs are copied when a durable workspace reference must be refreshed.
type AgentPreparation interface {
	Prepare(context.Context, Inputs, string, AgentWorkspace) (Inputs, string, string, error)
}

func workspaceTask(node workflow.Node, nodeID, agent, instruction string) (AgentTask, error) {
	permissions := slices.Clone(node.Fields().Permissions)
	slices.Sort(permissions)
	if !slices.Equal(permissions, []string{"process.run", "workspace.write"}) {
		return AgentTask{}, fmt.Errorf("%s node %q requires exactly process.run and workspace.write permissions", node.Type(), nodeID)
	}
	return AgentTask{Agent: agent, Instructions: instruction, Access: provider.AccessWorkspaceWrite}, nil
}

func changesetSchema() map[string]any {
	return map[string]any{
		"type": "object", "additionalProperties": false,
		"properties": map[string]any{
			"summary":    map[string]any{"type": "string"},
			"files":      map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
			"validation": map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
		},
		"required": []string{"summary", "files", "validation"},
	}
}
