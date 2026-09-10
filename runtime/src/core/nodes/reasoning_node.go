package nodes

import (
	"fmt"
	"slices"
	"strings"

	"darkstar/src/core/workflow"
	"darkstar/src/ports/provider"
)

type Reasoning struct{ Node workflow.ReasoningNode }

func (Reasoning) isHandler() {}

func (n Reasoning) BuildTask(nodeID string) (AgentTask, error) {
	if len(n.Node.Common.Permissions) != 0 {
		return AgentTask{}, fmt.Errorf("workflow node %q names permission policies that are not configured: %s", nodeID, strings.Join(n.Node.Common.Permissions, ", "))
	}
	return AgentTask{
		Agent:        n.Node.Executor.Agent,
		Instructions: "Complete the following task using only the supplied inputs. " + n.Node.Executor.Instructions,
		Skills:       slices.Clone(n.Node.Executor.Skills), Tools: slices.Clone(n.Node.Executor.Tools),
		Access: provider.AccessReadOnly,
	}, nil
}

// Reasoning outputs use the workflow's declared contracts without overrides.
func (Reasoning) ConfigureOutputs(map[string]any) {}
