package nodes

import (
	"darkstar/src/core/workflow"
	"darkstar/src/ports/provider"
	"testing"
)

func TestReasoningIsReadOnlyAndDoesNotAliasConfiguration(t *testing.T) {
	n := Reasoning{Node: workflow.ReasoningNode{Executor: workflow.ReasoningExecutor{Agent: "researcher", Instructions: "Compare the supplied evidence.", Skills: []string{"writing"}, Tools: []string{"search"}}}}
	task, err := n.BuildTask("research")
	if err != nil {
		t.Fatal(err)
	}
	if task.Access != provider.AccessReadOnly || task.Agent != "researcher" || task.Instructions != "Complete the following task using only the supplied inputs. Compare the supplied evidence." {
		t.Fatalf("task = %#v", task)
	}
	task.Skills[0] = "changed"
	task.Tools[0] = "changed"
	if n.Node.Executor.Skills[0] != "writing" || n.Node.Executor.Tools[0] != "search" {
		t.Fatal("task mutated frozen node configuration")
	}
	n.Node.Common.Permissions = []string{"workspace.write"}
	if _, err = n.BuildTask("research"); err == nil {
		t.Fatal("reasoning requested write access")
	}
}
