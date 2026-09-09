package workflowtools

import (
	"context"
	"darkstar/src/core/workflow"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestImplementationRequiresRealWorkspaceEditsAndRetainsBaseline(t *testing.T) {
	ctx := context.Background()
	workspace := t.TempDir()
	if output, err := exec.Command("git", "-C", workspace, "init").CombinedOutput(); err != nil {
		t.Fatalf("git init: %s %v", output, err)
	}
	readme := filepath.Join(workspace, "README.md")
	if err := os.WriteFile(readme, []byte("# Existing README\n"), 0600); err != nil {
		t.Fatal(err)
	}
	s := &Session{Database: filepath.Join(t.TempDir(), "tools.db"), Workspace: workspace, RunID: "run", AttemptID: "attempt", Node: workflow.ImplementationNode{Common: workflow.NodeFields{Outputs: map[workflow.Identifier]workflow.OutputDeclaration{"changeset": {Type: workflow.ValueObject}}}}}
	if err := s.PrepareWorkspace(ctx); err != nil {
		t.Fatal(err)
	}
	value := json.RawMessage(`{"disposition":"changed","summary":"Updated README","files":["README.md"],"validation":["Read updated README"]}`)
	if err := s.validateWorkspaceResult(ctx, value); err == nil {
		t.Fatal("proposed content was accepted as completed work")
	}
	if err := os.WriteFile(readme, []byte("# Updated README\n\nNew usage instructions.\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := s.PrepareWorkspace(ctx); err != nil {
		t.Fatal(err)
	} // reconnect must not reset baseline
	if err := s.validateWorkspaceResult(ctx, value); err != nil {
		t.Fatal(err)
	}
	result, err := s.Call(ctx, "inspect", "inspect_workspace_changes", json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	if string(result) != `{"files":["README.md"]}` {
		t.Fatalf("evidence = %s", result)
	}
	if _, err = s.Call(ctx, "submit", "submit_output", append(append([]byte(`{"id":"changeset","value":`), value...), '}')); err != nil {
		t.Fatal(err)
	}
	final := append(append([]byte(`{"changeset":`), value...), '}')
	if err = s.ValidateFinal(final); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(readme, []byte("# Existing README\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err = s.ValidateFinal(final); err == nil {
		t.Fatal("reverted edits passed completion verification")
	}
	if err = s.validateWorkspaceResult(ctx, json.RawMessage(`{"disposition":"blocked","summary":"Cannot access required file","files":[],"validation":[]}`)); err == nil {
		t.Fatal("blocked was accepted as success")
	}
}

func TestImplementationEvidenceIncludesAddsAndDeletesButNotPreexistingDirt(t *testing.T) {
	workspace := t.TempDir()
	if err := exec.Command("git", "-C", workspace, "init").Run(); err != nil {
		t.Fatal(err)
	}
	old := filepath.Join(workspace, "old.md")
	if err := os.WriteFile(old, []byte("old"), 0600); err != nil {
		t.Fatal(err)
	}
	s := &Session{Database: filepath.Join(t.TempDir(), "tools.db"), Workspace: workspace, RunID: "r", AttemptID: "a", Node: workflow.ImplementationNode{}}
	ctx := t.Context()
	if err := s.PrepareWorkspace(ctx); err != nil {
		t.Fatal(err)
	}
	if changes, err := s.workspaceChanges(ctx); err != nil || len(changes) != 0 {
		t.Fatalf("preexisting files counted: %v %v", changes, err)
	}
	if err := os.Remove(old); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "new.md"), []byte("new"), 0600); err != nil {
		t.Fatal(err)
	}
	if changes, err := s.workspaceChanges(ctx); err != nil || len(changes) != 2 || changes[0] != "new.md" || changes[1] != "old.md" {
		t.Fatalf("changes: %v %v", changes, err)
	}
}
