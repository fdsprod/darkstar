package cli

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"darkstar/src/adapters/provider/workflowtools"
	"darkstar/src/core/runexecution"
	"darkstar/src/core/workflow"
	"darkstar/src/ports/statestore"
)

func TestStandaloneCommitNeedsOnlyConnectedInputsAndReconcilesRetry(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	git := func(args ...string) string {
		t.Helper()
		output, err := exec.Command("git", append([]string{"-C", root}, args...)...).CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v %s", args, err, output)
		}
		return string(output)
	}
	git("init")
	git("config", "user.name", "Test")
	git("config", "user.email", "test@example.invalid")
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("before\n"), 0600); err != nil {
		t.Fatal(err)
	}
	git("add", "README.md")
	git("commit", "-m", "initial")
	w := &daemonProviderWiring{projectRoot: root, toolDatabase: filepath.Join(t.TempDir(), "tools.db")}
	project := statestore.ProjectProjection{ProjectID: "project", Status: statestore.ProjectActive, SourceHash: fmt.Sprintf("%x", sha256.Sum256([]byte(root)))}
	source, _ := json.Marshal(map[string]string{"projectId": project.ProjectID, "sourceHash": project.SourceHash})
	r := runexecution.AttemptRequestContext{Project: project, Run: statestore.RunProjection{RunID: "run"}, WorkItem: statestore.WorkItemProjection{WorkItemID: "work"}, Attempt: statestore.AttemptProjection{NodeID: "prepare"}, Node: workflow.WorkspacePrepareNode{Executor: workflow.WorkspacePrepareExecutor{RepositoryInput: "repository", Checkout: workflow.CurrentCheckout{}}}, NodeInputs: map[workflow.Identifier]json.RawMessage{"repository": source}}
	raw, err := w.ExecuteWorkspaceNode(ctx, r)
	if err != nil {
		t.Fatal(err)
	}
	var prepared map[string]json.RawMessage
	if err = json.Unmarshal(raw, &prepared); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(root, "README.md"), []byte("after\n"), 0600); err != nil {
		t.Fatal(err)
	}
	digest, err := workflowtools.WorkspaceDigest(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	changes, _ := json.Marshal(map[string]any{"disposition": "changed", "summary": "Update README", "files": []string{"README.md"}, "validation": []string{}, "snapshotDigest": digest})
	text, _ := json.Marshal(deliveryText{CommitSubject: "Update README", CommitBody: "Explain the change.", PRTitle: "Update README", PRBody: "Update documentation.", ChangesetSnapshot: digest})
	r.Node = workflow.GitCommitNode{Executor: workflow.GitCommitExecutor{WorkspaceInput: "workspace", ChangesetInput: "changeset", TextInput: "text"}}
	r.Attempt.NodeID = "commit"
	r.Attempt.VisitID = "visit_commit"
	r.NodeInputs = map[workflow.Identifier]json.RawMessage{"workspace": prepared["workspace"], "changeset": changes, "text": text}
	// No point acceptance, check results, or approval evidence exists.
	first, err := w.ExecuteDeliveryNode(ctx, r)
	if err != nil {
		t.Fatal(err)
	}
	head := git("rev-parse", "HEAD")
	second, err := w.ExecuteDeliveryNode(ctx, r)
	if err != nil {
		t.Fatal(err)
	}
	if string(first) != string(second) || git("rev-parse", "HEAD") != head {
		t.Fatal("retry created another commit or changed output")
	}
	if _, err = w.resolvePreparedWorkspace(ctx, r, prepared["workspace"]); err != nil {
		t.Fatalf("prepared workspace lost its accepted head: %v", err)
	}
	if err = os.WriteFile(filepath.Join(root, "README.md"), []byte("later\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = w.ExecuteDeliveryNode(ctx, r); err == nil {
		t.Fatal("accepted stale changeset")
	}
	if git("rev-parse", "HEAD") != head {
		t.Fatal("stale input mutated HEAD")
	}
}
