package cli

import (
	"context"
	"encoding/json"
	"errors"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"darkstar/src/adapters/plugin/process"
	"darkstar/src/adapters/provider/workflowtools"
	workspacefolder "darkstar/src/adapters/workspace/folder"
	"darkstar/src/core/artifactingest"
	"darkstar/src/core/artifactops"
	"darkstar/src/core/runexecution"
	"darkstar/src/core/workflow"
	"darkstar/src/ports/artifactbinding"
	"darkstar/src/ports/extension"
	"darkstar/src/ports/statestore"
)

type noPublication struct{}

func (noPublication) Ingest(context.Context, artifactops.IngestInput, string) (artifactingest.Result, error) {
	return artifactingest.Result{}, errors.New("unexpected publication")
}
func (noPublication) Attach(context.Context, artifactops.AttachInput, string) (artifactbinding.Version, error) {
	return artifactbinding.Version{}, errors.New("unexpected attachment")
}

func TestDaemonBindsTypeScriptWorkspaceToolsToPinnedAttempt(t *testing.T) {
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("Node unavailable")
	}
	root := t.TempDir()
	w := &daemonProviderWiring{}
	w.configurePlugins(root)
	if w.pluginErr != nil {
		t.Fatal(w.pluginErr)
	}
	manager, err := workspacefolder.New(filepath.Join(root, "workspaces"), workspacefolder.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close()
	w.workspaces = manager
	w.pluginArtifacts = noPublication{}
	request := runexecution.AttemptRequestContext{
		WorkItem: statestore.WorkItemProjection{WorkItemID: "work_one"}, Run: statestore.RunProjection{RunID: "run_one"}, Attempt: statestore.AttemptProjection{AttemptID: "attempt_one", NodeID: "notes"},
		ExecutionContext: statestore.RunExecutionContext{ExtensionPins: map[string]extension.Ref{builtinPluginPin: w.pluginRef}},
	}
	session := &workflowtools.Session{Node: workflow.ReasoningNode{}}
	if err := w.bindPluginTools(session, request); err != nil {
		t.Fatal(err)
	}
	if err := session.Validate(); err != nil {
		t.Fatal(err)
	}
	write := json.RawMessage(`{"area":"staged","path":"notes.md","content":{"encoding":"utf8","text":"saved by the TypeScript tool"}}`)
	result, err := session.Call(context.Background(), "call-1", "workspace_write", write)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(result), `"digest"`) {
		t.Fatalf("missing revision: %s", result)
	}
	result, err = session.Call(context.Background(), "call-2", "workspace_read", json.RawMessage(`{"area":"staged","path":"notes.md"}`))
	if err != nil || !strings.Contains(string(result), "saved by the TypeScript tool") {
		t.Fatalf("read=%s %v", result, err)
	}
	var snapshot struct {
		Digest string `json:"digest"`
	}
	if err := json.Unmarshal(result, &snapshot); err != nil {
		t.Fatal(err)
	}
	replacement, _ := json.Marshal(map[string]any{"area": "staged", "path": "notes.md", "expectedDigest": snapshot.Digest, "content": map[string]string{"encoding": "utf8", "text": "corrected"}})
	if _, err := session.Call(context.Background(), "call-replace", "workspace_write", replacement); err != nil {
		t.Fatal(err)
	}
	stale, _ := json.Marshal(map[string]any{"area": "staged", "path": "notes.md", "expectedDigest": snapshot.Digest, "content": map[string]string{"encoding": "utf8", "text": "stale correction"}})
	if _, err := session.Call(context.Background(), "call-stale", "workspace_write", stale); err == nil {
		t.Fatal("stale replacement succeeded")
	}
	request.WorkItem.WorkItemID = "work_other"
	other := &workflowtools.Session{Node: workflow.ReasoningNode{}}
	if err := w.bindPluginTools(other, request); err != nil {
		t.Fatal(err)
	}
	if _, err := other.Call(context.Background(), "call-3", "workspace_read", json.RawMessage(`{"area":"staged","path":"notes.md"}`)); err == nil {
		t.Fatal("cross-work-item read succeeded")
	}
	if _, err := session.Call(context.Background(), "call-4", "workspace_read", json.RawMessage(`{"area":"staged","path":"notes.md","workItemId":"work_other"}`)); err == nil {
		t.Fatal("owner override accepted")
	}
}

func TestPluginRecoveryAndLegacyToolsDoNotSilentlyChange(t *testing.T) {
	w := &daemonProviderWiring{pluginConfigured: true, pluginRef: pluginprocess.BuiltinRef(), pluginErr: errors.New("node unavailable")}
	legacy := &workflowtools.Session{Node: workflow.ReasoningNode{}}
	request := runexecution.AttemptRequestContext{}
	if err := w.bindPluginTools(legacy, request); err != nil {
		t.Fatal(err)
	}
	if legacy.ResourcePlugin != nil || len(legacy.AdditionalTools) != 0 {
		t.Fatal("legacy run acquired new plugins")
	}
	pins := w.ExtensionPins()
	if pins[builtinPluginPin] != w.pluginRef {
		t.Fatal("new run missing plugin pin")
	}
	request.ExecutionContext.ExtensionPins = pins
	if err := w.bindPluginTools(legacy, request); err == nil || !strings.Contains(err.Error(), "node unavailable") {
		t.Fatalf("missing runtime error=%v", err)
	}
	ref := w.pluginRef
	ref.Digest = strings.Repeat("b", 64)
	pins[builtinPluginPin] = ref
	if err := w.ValidateExtensionPins(pins); err == nil {
		t.Fatal("replaced plugin accepted during recovery")
	}
	if err := w.bindPluginTools(legacy, request); err == nil {
		t.Fatal("replaced plugin accepted at tool binding")
	}
}
