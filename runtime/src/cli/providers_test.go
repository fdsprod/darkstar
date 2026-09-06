package cli

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"darkstar/src/adapters/provider/codex"
	"darkstar/src/adapters/provider/fake"
	"darkstar/src/core/runexecution"
	"darkstar/src/core/workflow"
	platformport "darkstar/src/ports/platform"
	providerport "darkstar/src/ports/provider"
	"darkstar/src/ports/statestore"
)

func TestDaemonProviderWiringUsesConfiguredCodexAndPreservesFakeScenarios(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	paths := providerTestPaths(root)
	if err := os.MkdirAll(paths.Config, 0o700); err != nil {
		t.Fatal(err)
	}
	configured := filepath.ToSlash(filepath.Join(root, "packaged", "codex.exe"))
	content := "provider:\n  codex:\n    executable: " + configured + "\n"
	if err := os.WriteFile(filepath.Join(paths.Config, "config.yaml"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	var resolved string
	wiring, err := resolveDaemonProviderWiring(paths, root, func(value string) (string, error) {
		resolved = value
		return filepath.Clean(value), nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if resolved != configured || wiring.configuredCodex != configured || wiring.executable != filepath.Clean(configured) {
		t.Fatalf("Codex selection = resolved %q configured %q executable %q", resolved, wiring.configuredCodex, wiring.executable)
	}
	real, err := wiring.Provider(context.Background(), runexecution.ProviderRequest{Provider: runexecution.ProviderCodex, Scenario: runexecution.ScenarioWorkflow, AttemptID: "attempt-real"})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := real.(*codex.Adapter); !ok {
		t.Fatalf("workflow provider = %T, want *codex.Adapter", real)
	}
	fixture, err := wiring.Provider(context.Background(), runexecution.ProviderRequest{Provider: fakeProviderName, Scenario: runexecution.ScenarioSuccess, AttemptID: "attempt-fake"})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := fixture.(*fake.Fake); !ok {
		t.Fatalf("fake scenario provider = %T, want *fake.Fake", fixture)
	}
}

func TestDaemonProviderWiringFailsRealAttemptsClosedWhenFallbackIsAmbiguous(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	wiring, err := resolveDaemonProviderWiring(providerTestPaths(root), root, func(string) (string, error) {
		return "", errors.New("ambiguous fallback: two Codex executables")
	})
	if err != nil {
		t.Fatal(err)
	}
	if provider, err := wiring.doctorProvider(); err != nil || provider != nil {
		t.Fatalf("doctor provider = %T, %v; want nil, nil", provider, err)
	}
	if _, err := wiring.Provider(context.Background(), runexecution.ProviderRequest{Provider: runexecution.ProviderCodex, Scenario: runexecution.ScenarioWorkflow, AttemptID: "attempt-real"}); err == nil || !strings.Contains(err.Error(), "ambiguous fallback") {
		t.Fatalf("workflow provider error = %v", err)
	}
	if _, err := wiring.Provider(context.Background(), runexecution.ProviderRequest{Provider: fakeProviderName, Scenario: runexecution.ScenarioSuccess, AttemptID: "attempt-fake"}); err != nil {
		t.Fatalf("fake scenario was blocked by Codex ambiguity: %v", err)
	}
}

func TestWorkflowAttemptBuilderUsesExactContextAndFailClosedPolicies(t *testing.T) {
	t.Parallel()
	workspace := t.TempDir()
	workspaceDigest := fmt.Sprintf("%x", sha256.Sum256([]byte(filepath.Clean(workspace))))
	node := workflow.ReasoningNode{Common: workflow.NodeFields{Outputs: map[workflow.Identifier]workflow.OutputDeclaration{
		"summary": {Type: workflow.ValueString, Description: "Concise result"}, "evidence": {Type: workflow.ValueObject},
	}}, Executor: workflow.ReasoningExecutor{Agent: "technical-designer", Skills: []string{"design"}, Tools: []string{"repository-search"}}}
	request := runexecution.AttemptRequestContext{
		Attempt: statestore.AttemptProjection{AttemptID: "attempt-1", RunID: "run-1", NodeID: "design"},
		Run:     statestore.RunProjection{RunID: "run-1"}, WorkItem: statestore.WorkItemProjection{WorkItemID: "work-1", Title: "Design feature"},
		Project:  statestore.ProjectProjection{ProjectID: "project-1", Name: "Factory", SourceHash: workspaceDigest, Status: statestore.ProjectActive},
		Workflow: workflow.Definition{Version: workflow.VersionSummary{Name: "delivery", Version: "1.2.3", Digest: strings.Repeat("a", 64)}}, Node: node,
	}
	built, err := buildWorkflowAttemptRequest(request, workspace, strings.Repeat("b", 64))
	if err != nil {
		t.Fatal(err)
	}
	if built.AttemptID != "attempt-1" || built.RunID != "run-1" || built.NodeID != "design" || built.IdempotencyKey != "start:attempt-1" {
		t.Fatalf("attempt identity = %#v", built)
	}
	if built.Access != providerport.AccessReadOnly || built.Network != providerport.NetworkDenied || built.CommandPolicy != providerport.InteractionDeny || built.FilePolicy != providerport.InteractionDeny || built.ToolPolicy != providerport.InteractionDeny {
		t.Fatalf("attempt policy = %#v", built)
	}
	if len(built.Inputs) != 0 || !strings.Contains(built.Prompt, `"skills":["design"]`) || !strings.Contains(built.Prompt, `"tools":["repository-search"]`) || !strings.Contains(built.Prompt, `"workflowDigest":"`+strings.Repeat("a", 64)+`"`) {
		t.Fatalf("prompt/input context = %q / %#v", built.Prompt, built.Inputs)
	}
	var schema struct {
		Type                 string                     `json:"type"`
		Properties           map[string]json.RawMessage `json:"properties"`
		Required             []string                   `json:"required"`
		AdditionalProperties bool                       `json:"additionalProperties"`
	}
	if err := json.Unmarshal(built.OutputSchema, &schema); err != nil {
		t.Fatal(err)
	}
	if schema.Type != "object" || schema.AdditionalProperties || strings.Join(schema.Required, ",") != "evidence,summary" || len(schema.Properties) != 2 {
		t.Fatalf("output schema = %s", built.OutputSchema)
	}
	node.Common.Permissions = []string{"workspace-write"}
	request.Node = node
	if _, err := buildWorkflowAttemptRequest(request, workspace, strings.Repeat("b", 64)); err == nil || !strings.Contains(err.Error(), "not configured") {
		t.Fatalf("named permission policy error = %v", err)
	}
	request.Project.SourceHash = strings.Repeat("c", 64)
	if _, err := buildWorkflowAttemptRequest(request, workspace, strings.Repeat("b", 64)); err == nil || !strings.Contains(err.Error(), "not authorized") {
		t.Fatalf("workspace authorization error = %v", err)
	}
}

func TestWorkflowAttemptBuilderDigestsPreparedNodeInputs(t *testing.T) {
	t.Parallel()
	workspace := t.TempDir()
	workspaceDigest := fmt.Sprintf("%x", sha256.Sum256([]byte(filepath.Clean(workspace))))
	story := json.RawMessage(`{"title":"Create README"}`)
	node := workflow.ReasoningNode{Common: workflow.NodeFields{Outputs: map[workflow.Identifier]workflow.OutputDeclaration{"summary": {Type: workflow.ValueString}}}, Executor: workflow.ReasoningExecutor{Agent: "reader"}}
	request := runexecution.AttemptRequestContext{
		Attempt: statestore.AttemptProjection{AttemptID: "attempt-1", RunID: "run-1", NodeID: "read"},
		Run:     statestore.RunProjection{RunID: "run-1"}, WorkItem: statestore.WorkItemProjection{WorkItemID: "work-1", Title: "Create README"},
		Project:  statestore.ProjectProjection{ProjectID: "project-1", Name: "Factory", SourceHash: workspaceDigest, Status: statestore.ProjectActive},
		Workflow: workflow.Definition{Version: workflow.VersionSummary{Name: "delivery", Version: "1.2.3", Digest: strings.Repeat("a", 64)}}, Node: node,
		NodeInputs: map[workflow.Identifier]json.RawMessage{"story": story},
	}
	built, err := buildWorkflowAttemptRequest(request, workspace, strings.Repeat("b", 64))
	if err != nil {
		t.Fatal(err)
	}
	wantDigest := fmt.Sprintf("%x", sha256.Sum256(story))
	if len(built.Inputs) != 1 || built.Inputs[0].Name != "story" || built.Inputs[0].Digest != wantDigest || built.Inputs[0].Text != string(story) {
		t.Fatalf("prepared inputs = %#v", built.Inputs)
	}
}

func TestPointExecutionOutputSchemaIsStrictAtEveryObjectBoundary(t *testing.T) {
	t.Parallel()
	node := workflow.PointExecutionNode{Common: workflow.NodeFields{Outputs: map[workflow.Identifier]workflow.OutputDeclaration{
		"changeset": {Type: workflow.ValueObject},
		"progress":  {Type: workflow.ValueObject},
	}}}
	raw, err := workflowOutputSchema(node, node.Common.Outputs)
	if err != nil {
		t.Fatal(err)
	}
	var schema map[string]any
	if err := json.Unmarshal(raw, &schema); err != nil {
		t.Fatal(err)
	}
	properties := schema["properties"].(map[string]any)
	changeset := properties["changeset"].(map[string]any)
	progress := properties["progress"].(map[string]any)
	if schema["additionalProperties"] != false || changeset["additionalProperties"] != false || progress["additionalProperties"] != false {
		t.Fatalf("point output schema permits undeclared fields: %s", raw)
	}
	if fmt.Sprint(changeset["required"]) != "[summary files validation]" || fmt.Sprint(progress["required"]) != "[completed_points remaining_points]" {
		t.Fatalf("point output schema omits required fields: %s", raw)
	}
}

func providerTestPaths(root string) platformport.Paths {
	return platformport.Paths{Config: filepath.Join(root, "config"), Data: filepath.Join(root, "data"), Cache: filepath.Join(root, "cache"), Logs: filepath.Join(root, "logs"), Runtime: filepath.Join(root, "runtime")}
}
