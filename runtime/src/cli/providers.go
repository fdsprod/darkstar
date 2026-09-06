package cli

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"darkstar/src/adapters/provider/codex"
	"darkstar/src/core/config"
	"darkstar/src/core/runexecution"
	"darkstar/src/core/workflow"
	daemonconfiguration "darkstar/src/daemon/configuration"
	"darkstar/src/doctor"
	platformport "darkstar/src/ports/platform"
	providerport "darkstar/src/ports/provider"
	"darkstar/src/ports/statestore"
)

// daemonProviderWiring keeps fake acceptance scenarios separate from the
// selected production provider. A failed or ambiguous Codex selection remains
// explicit so fake scenarios still run while real attempts fail closed.
type daemonProviderWiring struct {
	configuredCodex string
	executable      string
	selectionErr    error
	projectRoot     string
	evidence        codex.EvidenceRecorder
}

const fakeProviderName = "fake"

func newDaemonProviderWiring(paths platformport.Paths, projectRoot string) (*daemonProviderWiring, error) {
	return resolveDaemonProviderWiring(paths, projectRoot, doctor.ResolveCodexExecutable)
}

func resolveDaemonProviderWiring(paths platformport.Paths, projectRoot string, resolve func(string) (string, error)) (*daemonProviderWiring, error) {
	configured, err := configuredCodexExecutable(paths, projectRoot)
	if err != nil {
		return nil, err
	}
	wiring := &daemonProviderWiring{configuredCodex: configured, projectRoot: projectRoot}
	wiring.executable, wiring.selectionErr = resolve(configured)
	if wiring.selectionErr != nil {
		return wiring, nil
	}
	wiring.evidence, err = codex.NewDirectoryEvidenceRecorder(filepath.Join(paths.Data, "provider-evidence", "codex"))
	if err != nil {
		return nil, fmt.Errorf("configure Codex provider evidence: %w", err)
	}
	return wiring, nil
}

func configuredCodexExecutable(paths platformport.Paths, projectRoot string) (string, error) {
	locations, err := daemonconfiguration.ResolveFileLocations(paths, projectRoot)
	if err != nil {
		return "", err
	}
	defaults, err := config.Defaults(map[string]any{})
	if err != nil {
		return "", err
	}
	effective, err := daemonconfiguration.Resolve(defaults, locations)
	if err != nil {
		return "", fmt.Errorf("resolve provider configuration: %w", err)
	}
	value, found := effective.Lookup("provider", "codex", "executable")
	if !found {
		return "", nil
	}
	executable, ok := value.Value().(string)
	if !ok || strings.TrimSpace(executable) == "" {
		return "", errors.New("provider.codex.executable must be a non-empty path")
	}
	return strings.TrimSpace(executable), nil
}

func (wiring *daemonProviderWiring) doctorProvider() (providerport.Provider, error) {
	if wiring.selectionErr != nil {
		return nil, nil
	}
	return wiring.codexProvider()
}

func (wiring *daemonProviderWiring) Provider(_ context.Context, request runexecution.ProviderRequest) (providerport.Provider, error) {
	switch request.Provider {
	case fakeProviderName:
		if request.Scenario != runexecution.ScenarioSuccess && request.Scenario != runexecution.ScenarioRestart {
			return nil, runexecution.ErrInvalidScenario
		}
		return newFakeRunProvider(request.Scenario, request.AttemptID, request.Resume)
	case runexecution.ProviderCodex:
		if request.Scenario != runexecution.ScenarioWorkflow {
			return nil, fmt.Errorf("Codex provider does not support scenario %q", request.Scenario)
		}
	default:
		return nil, fmt.Errorf("unsupported durable provider %q", request.Provider)
	}
	if wiring.selectionErr != nil {
		return nil, fmt.Errorf("Codex provider is unavailable: %w", wiring.selectionErr)
	}
	return wiring.codexProvider()
}

func (wiring *daemonProviderWiring) BuildAttemptRequest(ctx context.Context, request runexecution.AttemptRequestContext) (providerport.AttemptRequest, error) {
	if wiring.selectionErr != nil {
		return providerport.AttemptRequest{}, fmt.Errorf("Codex provider is unavailable: %w", wiring.selectionErr)
	}
	adapter, err := wiring.codexProvider()
	if err != nil {
		return providerport.AttemptRequest{}, err
	}
	manifest, err := adapter.Capabilities(ctx)
	if err != nil {
		return providerport.AttemptRequest{}, fmt.Errorf("observe Codex capabilities for workflow attempt: %w", err)
	}
	return buildWorkflowAttemptRequest(request, wiring.projectRoot, manifest.Fingerprint)
}

func buildWorkflowAttemptRequest(request runexecution.AttemptRequestContext, workspace, capabilityFingerprint string) (providerport.AttemptRequest, error) {
	workspace = filepath.Clean(strings.TrimSpace(workspace))
	workspaceDigest := fmt.Sprintf("%x", sha256.Sum256([]byte(workspace)))
	if workspace == "." || !filepath.IsAbs(workspace) || request.Project.Status != statestore.ProjectActive || request.Project.SourceHash != workspaceDigest {
		return providerport.AttemptRequest{}, fmt.Errorf("workflow project %q is not authorized for daemon workspace %q", request.Project.ProjectID, workspace)
	}
	node, ok := request.Node.(workflow.ReasoningNode)
	if !ok {
		return providerport.AttemptRequest{}, fmt.Errorf("workflow node %q is %s; only reasoning nodes support Codex dispatch", request.Attempt.NodeID, request.Node.Type())
	}
	if len(node.Common.Permissions) != 0 {
		return providerport.AttemptRequest{}, fmt.Errorf("workflow node %q names permission policies that are not configured: %s", request.Attempt.NodeID, strings.Join(node.Common.Permissions, ", "))
	}
	outputSchema, err := reasoningOutputSchema(node.Common.Outputs)
	if err != nil {
		return providerport.AttemptRequest{}, err
	}
	promptContext := struct {
		WorkflowName    string   `json:"workflowName"`
		WorkflowVersion string   `json:"workflowVersion"`
		WorkflowDigest  string   `json:"workflowDigest"`
		NodeID          string   `json:"nodeId"`
		Agent           string   `json:"agent"`
		Skills          []string `json:"skills"`
		Tools           []string `json:"tools"`
		WorkItemID      string   `json:"workItemId"`
		WorkTitle       string   `json:"workTitle"`
		ProjectID       string   `json:"projectId"`
		ProjectName     string   `json:"projectName"`
	}{
		WorkflowName: request.Workflow.Version.Name, WorkflowVersion: request.Workflow.Version.Version, WorkflowDigest: request.Workflow.Version.Digest,
		NodeID: request.Attempt.NodeID, Agent: node.Executor.Agent, Skills: append([]string(nil), node.Executor.Skills...), Tools: append([]string(nil), node.Executor.Tools...),
		WorkItemID: request.WorkItem.WorkItemID, WorkTitle: request.WorkItem.Title, ProjectID: request.Project.ProjectID, ProjectName: request.Project.Name,
	}
	encodedContext, err := json.Marshal(promptContext)
	if err != nil {
		return providerport.AttemptRequest{}, fmt.Errorf("encode workflow attempt context: %w", err)
	}
	return providerport.AttemptRequest{
		AttemptID: request.Attempt.AttemptID, RunID: request.Run.RunID, NodeID: request.Attempt.NodeID,
		IdempotencyKey: "start:" + request.Attempt.AttemptID, Workspace: workspace,
		Access: providerport.AccessReadOnly, Network: providerport.NetworkDenied,
		CommandPolicy: providerport.InteractionDeny, FilePolicy: providerport.InteractionDeny, ToolPolicy: providerport.InteractionDeny,
		Prompt: "Execute this exact installed workflow reasoning node. Skills and tools are descriptive requirements only; do not assume unresolved capabilities. Return only JSON matching the supplied output schema.\nContext: " + string(encodedContext),
		Inputs: []providerport.Input{}, OutputSchema: outputSchema, CapabilityFingerprint: capabilityFingerprint,
	}, nil
}

func reasoningOutputSchema(outputs map[workflow.Identifier]workflow.OutputDeclaration) (json.RawMessage, error) {
	ids := make([]string, 0, len(outputs))
	for id := range outputs {
		ids = append(ids, string(id))
	}
	sort.Strings(ids)
	properties := make(map[string]map[string]string, len(ids))
	for _, id := range ids {
		declaration := outputs[workflow.Identifier(id)]
		property := map[string]string{"type": string(declaration.Type)}
		if declaration.Description != "" {
			property["description"] = declaration.Description
		}
		properties[id] = property
	}
	schema := struct {
		Type                 string                       `json:"type"`
		Properties           map[string]map[string]string `json:"properties"`
		Required             []string                     `json:"required"`
		AdditionalProperties bool                         `json:"additionalProperties"`
	}{Type: "object", Properties: properties, Required: ids, AdditionalProperties: false}
	encoded, err := json.Marshal(schema)
	if err != nil {
		return nil, fmt.Errorf("encode reasoning output schema: %w", err)
	}
	return encoded, nil
}

func (wiring *daemonProviderWiring) codexProvider() (providerport.Provider, error) {
	return codex.NewAdapter(codex.AdapterOptions{
		Executable: wiring.executable, ProjectRoot: wiring.projectRoot, EvidenceRecorder: wiring.evidence,
	})
}

var _ runexecution.WorkflowProviderFactory = (*daemonProviderWiring)(nil)
var _ runexecution.AttemptRequestBuilder = (*daemonProviderWiring)(nil)
