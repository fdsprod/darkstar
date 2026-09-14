package workflowtools

import (
	"encoding/json"
	"testing"

	"darkstar/src/core/workflow"
)

func TestRepositoryInputProjectsContentWithoutDaemonAuthority(t *testing.T) {
	canonical := json.RawMessage(`{"projectId":"private-project","sourceHash":"private-hash","name":"Application","repositoryBinding":{"membershipRevision":4,"repository":{"id":"private-repository"}}}`)
	session := &Session{Node: workflow.ReasoningNode{Common: workflow.NodeFields{Inputs: map[workflow.Identifier]workflow.Binding{"repository": workflow.RequiredBinding{Type: workflow.ValueRepository}}}}, Inputs: map[workflow.Identifier]json.RawMessage{"repository": canonical}}
	value, err := session.readInput(t.Context(), "call", json.RawMessage(`{"id":"repository"}`))
	if err != nil {
		t.Fatal(err)
	}
	if string(value) != `{"name":"Application"}` {
		t.Fatalf("repository projection leaked authority: %s", value)
	}
	if string(session.Inputs["repository"]) != string(canonical) {
		t.Fatal("canonical repository history changed")
	}
}
