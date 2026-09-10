package nodes

import "darkstar/src/core/workflow"

// PointExecution preserves the current agent-backed implementation. Point
// lifecycle and publishing policy must not be inferred from provider narration.
type PointExecution struct{ Node workflow.PointExecutionNode }

func (PointExecution) isHandler() {}

func (n PointExecution) BuildTask(nodeID string) (AgentTask, error) {
	return workspaceTask(n.Node, nodeID, "implementation-point", "Read the connected Markdown implementation plan and carry out its points. Implement the requested work item in the supplied workspace. Make only the necessary repository changes. Do not claim completion unless the requested outcome exists on disk. Return changeset with summary, files, and validation; return progress with completed_points and remaining_points.")
}

func (PointExecution) ConfigureOutputs(properties map[string]any) {
	if _, ok := properties["changeset"]; ok {
		properties["changeset"] = changesetSchema()
	}
	if _, ok := properties["progress"]; ok {
		properties["progress"] = map[string]any{
			"type": "object", "additionalProperties": false,
			"properties": map[string]any{"completed_points": map[string]any{"type": "integer"}, "remaining_points": map[string]any{"type": "integer"}},
			"required":   []string{"completed_points", "remaining_points"},
		}
	}
}
