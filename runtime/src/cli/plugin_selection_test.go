package cli

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	nodets "darkstar/src/adapters/nodeextension/typescript"
	"darkstar/src/adapters/provider/codex"
	providerts "darkstar/src/adapters/provider/typescript"
	"darkstar/src/core/runexecution"
	"darkstar/src/core/workflow"
	"darkstar/src/ports/provider"
	"darkstar/src/ports/statestore"
)

func TestNewRunSelectsTypeScriptNodesAndProviderWithExactPins(t *testing.T) {
	nodePath, err := exec.LookPath("node")
	if err != nil {
		t.Skip("Node unavailable")
	}
	nodePath, err = filepath.Abs(nodePath)
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	evidence, err := codex.NewDirectoryEvidenceRecorder(filepath.Join(root, "evidence"))
	if err != nil {
		t.Fatal(err)
	}
	w := &daemonProviderWiring{executable: nodePath, projectRoot: root, evidence: evidence}
	w.configurePlugins(root)
	defer w.closePluginProviders()
	if w.pluginErr != nil || w.nodePluginErr != nil || w.providerPluginErr != nil {
		t.Fatalf("plugin composition: %v, %v, %v", w.pluginErr, w.nodePluginErr, w.providerPluginErr)
	}
	pins := w.ExtensionPins()
	if pins[builtinNodePin] != nodets.BuiltinRef() || pins["provider:codex"] != providerts.BuiltinRef() {
		t.Fatalf("new run pins = %#v", pins)
	}
	if err := w.ValidateExtensionPins(pins); err != nil {
		t.Fatal(err)
	}
	ref := pins["provider:codex"]
	request := runexecution.ProviderRequest{Provider: "codex", Scenario: runexecution.ScenarioWorkflow, AttemptID: "attempt-ts", Ref: &ref}
	adapter, err := w.Provider(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := adapter.(*providerts.Adapter); !ok {
		t.Fatalf("pinned provider = %T", adapter)
	}
	request.Ref = nil
	legacy, err := w.Provider(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := legacy.(*codex.Adapter); !ok {
		t.Fatalf("legacy provider = %T", legacy)
	}
	ref.Digest = strings.Repeat("0", 64)
	request.Ref = &ref
	if _, err := w.Provider(context.Background(), request); err == nil {
		t.Fatal("substituted provider digest accepted")
	}
	ctx := runexecution.AttemptRequestContext{
		Attempt: statestore.AttemptProjection{AttemptID: "attempt-ts", NodeID: "reason"},
		Project: statestore.ProjectProjection{Status: statestore.ProjectActive, SourceHash: fmt.Sprintf("%x", sha256.Sum256([]byte(filepath.Clean(root))))},
		Node: workflow.ReasoningNode{
			Common: workflow.NodeFields{
				Inputs:  map[workflow.Identifier]workflow.Binding{"task": workflow.RequiredBinding{Type: workflow.ValueTask}},
				Outputs: map[workflow.Identifier]workflow.OutputDeclaration{"summary": {Type: workflow.ValueString}},
			},
			Executor: workflow.ReasoningExecutor{Agent: "reader"},
		},
		NodeInputs:       map[workflow.Identifier]json.RawMessage{"task": json.RawMessage(`{"title":"Scoped task"}`), "unrelated": json.RawMessage(`"secret"`)},
		ExecutionContext: statestore.RunExecutionContext{ExtensionPins: pins},
	}
	built, err := w.buildScopedAttempt(context.Background(), ctx, "fixture")
	if err != nil {
		t.Fatal(err)
	}
	if built.Access != provider.AccessReadOnly || len(built.Inputs) != 1 || strings.Contains(built.Prompt, "secret") {
		t.Fatalf("plugin altered input or permission boundary: %#v", built)
	}
	nodeRef := pins[builtinNodePin]
	nodeRef.Digest = strings.Repeat("0", 64)
	pins[builtinNodePin] = nodeRef
	if _, err := w.buildScopedAttempt(context.Background(), ctx, "fixture"); err == nil {
		t.Fatal("substituted node digest accepted")
	}
}
