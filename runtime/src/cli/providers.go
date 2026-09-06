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
	fields := request.Node.Fields()
	agent, skills, tools := "", []string(nil), []string(nil)
	access := providerport.AccessReadOnly
	commandPolicy, filePolicy := providerport.InteractionDeny, providerport.InteractionDeny
	instruction := "Execute this exact installed workflow reasoning node."
	switch node := request.Node.(type) {
	case workflow.ReasoningNode:
		agent, skills, tools = node.Executor.Agent, append([]string(nil), node.Executor.Skills...), append([]string(nil), node.Executor.Tools...)
		if len(fields.Permissions) != 0 {
			return providerport.AttemptRequest{}, fmt.Errorf("workflow node %q names permission policies that are not configured: %s", request.Attempt.NodeID, strings.Join(fields.Permissions, ", "))
		}
	case workflow.PointExecutionNode:
		if !samePermissionSet(fields.Permissions, []string{"process.run", "workspace.write"}) {
			return providerport.AttemptRequest{}, fmt.Errorf("point-execution node %q requires exactly process.run and workspace.write permissions", request.Attempt.NodeID)
		}
		agent, access = "implementation-point", providerport.AccessWorkspaceWrite
		commandPolicy, filePolicy = providerport.InteractionAllow, providerport.InteractionAllow
		instruction = "Implement the requested work item in the supplied workspace. Make only the necessary repository changes. Do not claim completion unless the requested outcome exists on disk. Return changeset with summary, files, and validation; return progress with completed_points and remaining_points."
	default:
		return providerport.AttemptRequest{}, fmt.Errorf("workflow node %q is %s; it is not a Codex-backed executor", request.Attempt.NodeID, request.Node.Type())
	}
	outputSchema, err := workflowOutputSchema(request.Node, fields.Outputs)
	if err != nil {
		return providerport.AttemptRequest{}, err
	}
	promptContext := struct {
		WorkflowName    string                                                          `json:"workflowName"`
		WorkflowVersion string                                                          `json:"workflowVersion"`
		WorkflowDigest  string                                                          `json:"workflowDigest"`
		NodeID          string                                                          `json:"nodeId"`
		NodeType        string                                                          `json:"nodeType"`
		Agent           string                                                          `json:"agent"`
		Skills          []string                                                        `json:"skills"`
		Tools           []string                                                        `json:"tools"`
		WorkItemID      string                                                          `json:"workItemId"`
		WorkTitle       string                                                          `json:"workTitle"`
		ProjectID       string                                                          `json:"projectId"`
		ProjectName     string                                                          `json:"projectName"`
		RunInputs       map[workflow.Identifier]json.RawMessage                         `json:"runInputs"`
		NodeInputs      map[workflow.Identifier]json.RawMessage                         `json:"nodeInputs"`
		AcceptedOutputs map[workflow.Identifier]map[workflow.Identifier]json.RawMessage `json:"acceptedOutputs,omitempty"`
	}{
		WorkflowName: request.Workflow.Version.Name, WorkflowVersion: request.Workflow.Version.Version, WorkflowDigest: request.Workflow.Version.Digest,
		NodeID: request.Attempt.NodeID, NodeType: string(request.Node.Type()), Agent: agent, Skills: skills, Tools: tools,
		WorkItemID: request.WorkItem.WorkItemID, WorkTitle: request.WorkItem.Title, ProjectID: request.Project.ProjectID, ProjectName: request.Project.Name,
		RunInputs: request.RunInputs, NodeInputs: request.NodeInputs, AcceptedOutputs: request.AcceptedOutputs,
	}
	encodedContext, err := json.Marshal(promptContext)
	if err != nil {
		return providerport.AttemptRequest{}, fmt.Errorf("encode workflow attempt context: %w", err)
	}
	providerInputs := make([]providerport.Input, 0, len(request.NodeInputs))
	inputIDs := make([]string, 0, len(request.NodeInputs))
	for id := range request.NodeInputs {
		inputIDs = append(inputIDs, string(id))
	}
	sort.Strings(inputIDs)
	for _, id := range inputIDs {
		value := request.NodeInputs[workflow.Identifier(id)]
		digest := sha256.Sum256(value)
		providerInputs = append(providerInputs, providerport.Input{
			Kind: providerport.InputText, Name: id, MediaType: "application/json",
			Locator: "workflow-input:" + id, Digest: fmt.Sprintf("%x", digest), Text: string(value),
		})
	}
	return providerport.AttemptRequest{
		AttemptID: request.Attempt.AttemptID, RunID: request.Run.RunID, NodeID: request.Attempt.NodeID,
		IdempotencyKey: "start:" + request.Attempt.AttemptID, Workspace: workspace,
		Access: access, Network: providerport.NetworkDenied,
		CommandPolicy: commandPolicy, FilePolicy: filePolicy, ToolPolicy: providerport.InteractionDeny,
		Prompt: instruction + " Skills and tools are descriptive requirements only; do not assume unresolved capabilities. Return only JSON matching the supplied output schema. For implementation progress, set remaining_points to 0 only after the requested outcome is complete.\nContext: " + string(encodedContext),
		Inputs: providerInputs, OutputSchema: outputSchema, CapabilityFingerprint: capabilityFingerprint,
	}, nil
}

func samePermissionSet(actual, expected []string) bool {
	if len(actual) != len(expected) {
		return false
	}
	left, right := append([]string(nil), actual...), append([]string(nil), expected...)
	sort.Strings(left)
	sort.Strings(right)
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func workflowOutputSchema(node workflow.Node, outputs map[workflow.Identifier]workflow.OutputDeclaration) (json.RawMessage, error) {
	ids := make([]string, 0, len(outputs))
	for id := range outputs {
		ids = append(ids, string(id))
	}
	sort.Strings(ids)
	properties := make(map[string]any, len(ids))
	for _, id := range ids {
		declaration := outputs[workflow.Identifier(id)]
		property := map[string]any{"type": string(declaration.Type)}
		if declaration.Description != "" {
			property["description"] = declaration.Description
		}
		if declaration.Type == workflow.ValueObject {
			property["properties"] = map[string]any{}
			property["required"] = []string{}
			property["additionalProperties"] = false
		}
		if declaration.Type == workflow.ValueArray {
			property["items"] = map[string]any{"type": "string"}
		}
		properties[id] = property
	}
	if _, point := node.(workflow.PointExecutionNode); point {
		if _, exists := properties["changeset"]; exists {
			properties["changeset"] = map[string]any{
				"type": "object", "additionalProperties": false,
				"properties": map[string]any{
					"summary":    map[string]any{"type": "string"},
					"files":      map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
					"validation": map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
				},
				"required": []string{"summary", "files", "validation"},
			}
		}
		if _, exists := properties["progress"]; exists {
			properties["progress"] = map[string]any{
				"type": "object", "additionalProperties": false,
				"properties": map[string]any{
					"completed_points": map[string]any{"type": "integer"},
					"remaining_points": map[string]any{"type": "integer"},
				},
				"required": []string{"completed_points", "remaining_points"},
			}
		}
	}
	schema := struct {
		Type                 string         `json:"type"`
		Properties           map[string]any `json:"properties"`
		Required             []string       `json:"required"`
		AdditionalProperties bool           `json:"additionalProperties"`
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
