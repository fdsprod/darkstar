package nodes

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"

	"darkstar/src/core/workflow"
)

type Implementation struct{ Node workflow.ImplementationNode }

func (Implementation) isHandler() {}

func (n Implementation) BuildTask(nodeID string) (AgentTask, error) {
	return workspaceTask(n.Node, nodeID, "implementation", "Implement the task in the connected input "+string(n.Node.Executor.TaskInput)+" in the supplied workspace. Read optional connected Markdown instructions and supporting inputs when present; a plan is not required. Modify the actual files and run relevant checks. Preserve unrelated work. Do not commit, push, publish, or deploy. Use inspect_workspace_changes to see file changes relative to this attempt's durable baseline. Return changeset with disposition (changed, unchanged, or blocked), summary, files (exact relative paths reported by inspect_workspace_changes), and validation (checks actually run and results). Use unchanged only if the request is already satisfied and no files changed. Use blocked to report a blocker; blocked is not a successful completion. The runtime verifies files against the workspace; merely returning proposed content does not implement the task. "+n.Node.Executor.Instructions)
}

func (Implementation) ConfigureOutputs(properties map[string]any) {
	if _, ok := properties["changeset"]; !ok {
		return
	}
	change := changesetSchema()
	change["properties"].(map[string]any)["disposition"] = map[string]any{"type": "string", "enum": []string{"changed", "unchanged", "blocked"}}
	change["required"] = []string{"disposition", "summary", "files", "validation"}
	properties["changeset"] = change
}

func (n Implementation) Prepare(ctx context.Context, inputs Inputs, workspace string, service AgentWorkspace) (Inputs, string, string, error) {
	inputs = maps.Clone(inputs)
	suffix := ""
	if id := n.Node.Executor.WorkspaceInput; id != "" {
		prepared, err := service.Resolve(ctx, inputs[id])
		if err != nil {
			return nil, "", "", err
		}
		raw, err := json.Marshal(prepared)
		if err != nil {
			return nil, "", "", err
		}
		inputs[id] = raw
		workspace = prepared.Path
		suffix = " Use only the connected prepared workspace. Do not switch branches or create another worktree."
	}
	if err := service.CaptureBaseline(ctx, workspace); err != nil {
		return nil, "", "", fmt.Errorf("capture implementation baseline: %w", err)
	}
	return inputs, workspace, suffix, nil
}

// ValidateImplementationResult runs in the daemon at submit_output. Evidence is
// read lazily so blocked/invalid submissions cannot become successful outputs.
func ValidateImplementationResult(ctx context.Context, raw json.RawMessage, changes func(context.Context) ([]string, error)) error {
	var result struct {
		Disposition string   `json:"disposition"`
		Summary     string   `json:"summary"`
		Files       []string `json:"files"`
		Validation  []string `json:"validation"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		return err
	}
	if strings.TrimSpace(result.Summary) == "" {
		return errors.New("implementation changeset requires a summary")
	}
	if result.Files == nil || result.Validation == nil {
		return errors.New("implementation changeset requires explicit files and validation arrays")
	}
	if result.Disposition == "blocked" {
		return fmt.Errorf("implementation blocked: %s; validation: %s", result.Summary, strings.Join(result.Validation, "; "))
	}
	if result.Disposition != "changed" && result.Disposition != "unchanged" {
		return errors.New("implementation disposition must be changed, unchanged, or blocked")
	}
	actual, err := changes(ctx)
	if err != nil {
		return err
	}
	reported := slices.Clone(result.Files)
	slices.Sort(reported)
	if !slices.Equal(actual, reported) {
		return fmt.Errorf("changeset.files must match actual workspace changes: %v", actual)
	}
	if (result.Disposition == "changed") != (len(actual) > 0) {
		return errors.New("implementation disposition does not match the actual workspace changes")
	}
	return nil
}
