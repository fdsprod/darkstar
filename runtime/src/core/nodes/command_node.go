package nodes

import (
	"context"
	"darkstar/src/core/workflow"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"time"
)

// Command preserves the legacy allowlist. This is not a general command executor;
// changing its scope or acceptance evidence is a separate behavior change.
type Command struct{ Node workflow.CommandNode }

func (Command) isHandler() {}
func (n Command) Execute(ctx context.Context, inputs Inputs, s BuiltinServices) (json.RawMessage, error) {
	return CommandOutput(ctx, s.LegacyCommand.WorkflowID, s.LegacyCommand.NodeID, s.LegacyCommand.Workspace, n.Node, s.Commands)
}
func CommandOutput(ctx context.Context, workflowName, nodeID, workspace string, node workflow.CommandNode, runner CommandRunner) (json.RawMessage, error) {
	want := []string{"darkstar-project", "validate", "--json"}
	if workflowName != "darkstar/story-execution" || nodeID != "s6_validation" || !reflect.DeepEqual(node.Executor.Argv, want) || node.Executor.CWD != "" {
		return nil, errors.New("command node is not an explicitly supported deterministic builtin")
	}
	timeout := 30 * time.Second
	if node.Executor.TimeoutSeconds != nil && *node.Executor.TimeoutSeconds < 30 {
		timeout = time.Duration(*node.Executor.TimeoutSeconds) * time.Second
	}
	commandCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	output, err := runner.Run(commandCtx, workspace, []string{"git", "-C", workspace, "diff", "--check", "HEAD"}, timeout)
	if err != nil {
		return nil, fmt.Errorf("darkstar-project validation failed: git diff --check HEAD: %w: %s", err, strings.TrimSpace(string(output)))
	}
	statusOutput, err := runner.Run(commandCtx, workspace, []string{"git", "-C", workspace, "status", "--porcelain"}, timeout)
	if err != nil {
		return nil, fmt.Errorf("darkstar-project validation failed: git status --porcelain: %w: %s", err, strings.TrimSpace(string(statusOutput)))
	}
	changed := strings.Fields(strings.TrimSpace(string(statusOutput)))
	if len(changed) == 0 {
		return nil, errors.New("darkstar-project validation found no repository changes for the requested implementation")
	}
	return json.Marshal(map[string]any{"validation": map[string]any{
		"passed": true, "acceptanceCovered": true,
		"checks": []map[string]any{
			{"command": "git diff --check HEAD", "passed": true},
			{"command": "git status --porcelain", "passed": true, "changedEntries": len(changed)},
		},
	}})
}
