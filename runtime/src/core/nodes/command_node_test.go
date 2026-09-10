package nodes

import (
	"darkstar/src/core/workflow"
	"strings"
	"testing"
	"time"
)

func TestLegacyCommandRejectsUnconfiguredScopeBeforeProcessExecution(t *testing.T) {
	for _, tc := range []struct {
		workflow, node, cwd string
		argv                []string
	}{
		{"other", "s6_validation", "", []string{"darkstar-project", "validate", "--json"}},
		{"darkstar/story-execution", "other", "", []string{"darkstar-project", "validate", "--json"}},
		{"darkstar/story-execution", "s6_validation", "elsewhere", []string{"darkstar-project", "validate", "--json"}},
		{"darkstar/story-execution", "s6_validation", "", []string{"arbitrary-command"}},
	} {
		runner := &recordingRunner{}
		_, err := CommandOutput(t.Context(), tc.workflow, tc.node, "workspace", workflow.CommandNode{Executor: workflow.CommandExecutor{Argv: tc.argv, CWD: tc.cwd}}, runner)
		if err == nil || len(runner.calls) != 0 {
			t.Fatal("legacy command allowlist widened")
		}
	}
}

func TestLegacyCommandPreservesChecksAndFailureEvidence(t *testing.T) {
	timeout := uint64(5)
	n := Command{Node: workflow.CommandNode{Executor: workflow.CommandExecutor{Argv: []string{"darkstar-project", "validate", "--json"}, TimeoutSeconds: &timeout}}}
	for _, tc := range []struct {
		name    string
		outputs []string
		failAt  int
		want    string
	}{
		{"changes", []string{"", " M README.md"}, 0, ""},
		{"empty", []string{"", ""}, 0, "no repository changes"},
		{"whitespace", []string{"trailing spaces"}, 1, "trailing spaces"},
		{"status error", []string{"", "permission denied"}, 2, "permission denied"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			runner := &recordingRunner{outputs: tc.outputs, failAt: tc.failAt}
			raw, err := n.Execute(t.Context(), nil, BuiltinServices{Commands: runner, LegacyCommand: LegacyCommandScope{WorkflowID: "darkstar/story-execution", NodeID: "s6_validation", Workspace: "authorized"}})
			if tc.want == "" {
				if err != nil || !strings.Contains(string(raw), `"passed":true`) {
					t.Fatalf("%s %v", raw, err)
				}
			} else if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v", err)
			}
			if runner.calls[0].timeout != 5*time.Second || runner.calls[0].workspace != "authorized" {
				t.Fatal("scope or timeout changed")
			}
		})
	}
}
