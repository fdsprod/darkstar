package cli

import (
	"context"
	"crypto/sha256"
	"darkstar/src/core/runexecution"
	"darkstar/src/core/workflow"
	"darkstar/src/ports/statestore"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestPreparedWorktreeIsolationResumeAndRequiredChecks(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	git := func(args ...string) string {
		t.Helper()
		out, err := exec.Command("git", append([]string{"-C", root}, args...)...).CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
		return string(out)
	}
	git("init")
	git("config", "user.email", "test@example.invalid")
	git("config", "user.name", "Test")
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("original\n"), 0600); err != nil {
		t.Fatal(err)
	}
	git("add", "README.md")
	git("commit", "-m", "initial")
	w := &daemonProviderWiring{projectRoot: root, toolDatabase: filepath.Join(t.TempDir(), "tools.db")}
	project := statestore.ProjectProjection{ProjectID: "project", Status: statestore.ProjectActive, SourceHash: fmt.Sprintf("%x", sha256.Sum256([]byte(root)))}
	source, _ := json.Marshal(map[string]string{"projectId": project.ProjectID, "sourceHash": project.SourceHash})
	r := runexecution.AttemptRequestContext{Project: project, Run: statestore.RunProjection{RunID: "run_one"}, WorkItem: statestore.WorkItemProjection{WorkItemID: "work_one"}, Attempt: statestore.AttemptProjection{NodeID: "prepare"}, Node: workflow.WorkspacePrepareNode{Executor: workflow.WorkspacePrepareExecutor{RepositoryInput: "repository", Checkout: workflow.NewWorktree{BaseRef: "HEAD", Branch: "darkstar/{runId}"}}}, NodeInputs: map[workflow.Identifier]json.RawMessage{"repository": source}}
	raw, err := w.ExecuteWorkspaceNode(ctx, r)
	if err != nil {
		t.Fatal(err)
	}
	var outputs map[string]json.RawMessage
	if err = json.Unmarshal(raw, &outputs); err != nil {
		t.Fatal(err)
	}
	p, err := w.resolvePreparedWorkspace(ctx, r, outputs["workspace"])
	if err != nil {
		t.Fatal(err)
	}
	if p.Path == root || p.Branch != "darkstar/run_one" {
		t.Fatalf("workspace: %#v", p)
	}
	if err = os.WriteFile(filepath.Join(p.Path, "README.md"), []byte("implemented\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if original, _ := os.ReadFile(filepath.Join(root, "README.md")); string(original) != "original\n" {
		t.Fatal("modified original checkout")
	}
	again, err := w.ExecuteWorkspaceNode(ctx, r)
	if err != nil || string(again) != string(raw) {
		t.Fatalf("resume: %s %v", again, err)
	}
	validator := r
	validator.Node = workflow.WorkspaceValidateNode{Executor: workflow.WorkspaceValidateExecutor{WorkspaceInput: "workspace", Checks: [][]string{{"git", "diff", "--check"}}}}
	validator.NodeInputs = map[workflow.Identifier]json.RawMessage{"workspace": outputs["workspace"]}
	if _, err = w.ExecuteWorkspaceNode(ctx, validator); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(p.Path, "README.md"), []byte("bad trailing spaces   \n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = w.ExecuteWorkspaceNode(ctx, validator); err == nil {
		t.Fatal("failed check accepted as success")
	}
	foreign := validator
	foreign.Run.RunID = "other_run"
	if _, err = w.ExecuteWorkspaceNode(ctx, foreign); err == nil {
		t.Fatal("accepted another run's workspace")
	}
	collision := r
	collision.Run.RunID = "run_two"
	collision.Node = workflow.WorkspacePrepareNode{Executor: workflow.WorkspacePrepareExecutor{RepositoryInput: "repository", Checkout: workflow.NewWorktree{BaseRef: "HEAD", Branch: "darkstar/run_one"}}}
	if _, err = w.ExecuteWorkspaceNode(ctx, collision); err == nil {
		t.Fatal("branch collision accepted")
	}
	missing := r
	missing.Attempt.NodeID = "missing"
	missing.Node = workflow.WorkspacePrepareNode{Executor: workflow.WorkspacePrepareExecutor{RepositoryInput: "repository", Checkout: workflow.NewWorktree{BaseRef: "refs/heads/does-not-exist", Branch: "darkstar/missing"}}}
	if _, err = w.ExecuteWorkspaceNode(ctx, missing); err == nil {
		t.Fatal("missing base silently substituted")
	}
}
