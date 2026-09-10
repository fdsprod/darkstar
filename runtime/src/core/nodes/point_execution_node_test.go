package nodes

import (
	"darkstar/src/core/workflow"
	"encoding/json"
	"strings"
	"testing"
)

func TestPointExecutionRetainsDistinctProgressContract(t *testing.T) {
	n := PointExecution{Node: workflow.PointExecutionNode{Common: workflow.NodeFields{Permissions: []string{"process.run", "workspace.write"}}}}
	task, err := n.BuildTask("points")
	if err != nil {
		t.Fatal(err)
	}
	if task.Agent != "implementation-point" {
		t.Fatal("wrong agent task")
	}
	properties := map[string]any{"changeset": nil, "progress": nil}
	n.ConfigureOutputs(properties)
	raw, _ := json.Marshal(properties)
	if strings.Contains(string(raw), "disposition") || !strings.Contains(string(raw), `"required":["completed_points","remaining_points"]`) {
		t.Fatalf("schema = %s", raw)
	}
}
