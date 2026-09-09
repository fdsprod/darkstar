package cli

import (
	"context"
	"crypto/sha256"
	valueschemaadapter "darkstar/src/adapters/valueschema/jsonschema"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"darkstar/src/adapters/provider/codex"
	"darkstar/src/adapters/provider/workflowtools"
	"darkstar/src/core/config"
	"darkstar/src/core/configmutation"
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
	configuration   *configmutation.Service
	workflows       *workflow.Catalog
	configuredCodex string
	executable      string
	selectionErr    error
	projectRoot     string
	toolDatabase    string
	evidence        codex.EvidenceRecorder
}

// richArtifactSkill is generated from skills/builtin/rich-artifacts/SKILL.md.
//
//go:embed rich-artifacts.md
var richArtifactSkill string

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
	wiring.toolDatabase = filepath.Join(paths.Data, "workflow-tools.db")
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
			return nil, fmt.Errorf("codex provider does not support scenario %q", request.Scenario)
		}
	default:
		return nil, fmt.Errorf("unsupported durable provider %q", request.Provider)
	}
	if wiring.selectionErr != nil {
		return nil, fmt.Errorf("codex provider is unavailable: %w", wiring.selectionErr)
	}
	return wiring.codexProvider()
}

func (wiring *daemonProviderWiring) BuildAttemptRequest(ctx context.Context, request runexecution.AttemptRequestContext) (providerport.AttemptRequest, error) {
	if wiring.selectionErr != nil {
		return providerport.AttemptRequest{}, fmt.Errorf("codex provider is unavailable: %w", wiring.selectionErr)
	}
	adapter, err := wiring.codexProvider()
	if err != nil {
		return providerport.AttemptRequest{}, err
	}
	manifest, err := adapter.Capabilities(ctx)
	if err != nil {
		return providerport.AttemptRequest{}, fmt.Errorf("observe Codex capabilities for workflow attempt: %w", err)
	}
	request.NodeInputs = agentNodeInputs(request)
	built, err := buildWorkflowAttemptRequest(request, wiring.projectRoot, manifest.Fingerprint)
	if err != nil {
		return built, err
	}
	session := &workflowtools.Session{Database: wiring.toolDatabase, RunID: request.Run.RunID, AttemptID: request.Attempt.AttemptID, Node: request.Node, Inputs: request.NodeInputs}
	if request.Node.Type() == workflow.NodeImplementation {
		if node := request.Node.(workflow.ImplementationNode); node.Executor.WorkspaceInput != "" {
			prepared, resolveErr := wiring.resolvePreparedWorkspace(ctx, request, request.NodeInputs[node.Executor.WorkspaceInput])
			if resolveErr != nil {
				return providerport.AttemptRequest{}, resolveErr
			}
			built.Workspace = prepared.Path
			built.Prompt += " Use only the connected prepared workspace. Do not switch branches or create another worktree."
		}
		session.Workspace = built.Workspace
		if err := session.PrepareWorkspace(ctx); err != nil {
			return providerport.AttemptRequest{}, fmt.Errorf("capture implementation baseline: %w", err)
		}
	}
	session.Workspace = built.Workspace
	if err := session.PrepareMarkdown(ctx); err != nil {
		return providerport.AttemptRequest{}, fmt.Errorf("capture Markdown baseline: %w", err)
	}
	built.DynamicTools = session.Definitions()
	built.ToolHandler = session
	built.Prompt += " Use read_input to inspect connected inputs and templates. Submit each deliverable with submit_output and correct any validation errors before finishing. Use journal tools only when they are present in your tool list. After submitting all required outputs, finish with a concise human-readable summary. Do not repeat document contents or a JSON output envelope; the daemon assembles the validated submissions."
	return built, nil
}

func buildWorkflowAttemptRequest(request runexecution.AttemptRequestContext, workspace, capabilityFingerprint string) (providerport.AttemptRequest, error) {
	workspace = filepath.Clean(strings.TrimSpace(workspace))
	workspaceDigest := fmt.Sprintf("%x", sha256.Sum256([]byte(workspace)))
	if workspace == "." || !filepath.IsAbs(workspace) || request.Project.Status != statestore.ProjectActive || request.Project.SourceHash != workspaceDigest {
		return providerport.AttemptRequest{}, fmt.Errorf("workflow project %q is not authorized for daemon workspace %q", request.Project.ProjectID, workspace)
	}
	request.NodeInputs = agentNodeInputs(request)
	fields := request.Node.Fields()
	agent, skills, tools := "", []string(nil), []string(nil)
	access := providerport.AccessReadOnly
	commandPolicy, filePolicy := providerport.InteractionDeny, providerport.InteractionDeny
	instruction := "Complete the following task using only the supplied inputs."
	switch node := request.Node.(type) {
	case workflow.ReasoningNode:
		instruction += " " + node.Executor.Instructions
		agent, skills, tools = node.Executor.Agent, append([]string(nil), node.Executor.Skills...), append([]string(nil), node.Executor.Tools...)
		if len(fields.Permissions) != 0 {
			return providerport.AttemptRequest{}, fmt.Errorf("workflow node %q names permission policies that are not configured: %s", request.Attempt.NodeID, strings.Join(fields.Permissions, ", "))
		}
	case workflow.PointExecutionNode:
		agent = "implementation-point"
		instruction = "Read the connected Markdown implementation plan and carry out its points. Implement the requested work item in the supplied workspace. Make only the necessary repository changes. Do not claim completion unless the requested outcome exists on disk. Return changeset with summary, files, and validation; return progress with completed_points and remaining_points."
	case workflow.ImplementationNode:
		agent = "implementation"
		instruction = "Implement the task in the connected input " + string(node.Executor.TaskInput) + " in the supplied workspace. Read optional connected Markdown instructions and supporting inputs when present; a plan is not required. Modify the actual files and run relevant checks. Preserve unrelated work. Do not commit, push, publish, or deploy. Use inspect_workspace_changes to see file changes relative to this attempt's durable baseline. Return changeset with disposition (changed, unchanged, or blocked), summary, files (exact relative paths reported by inspect_workspace_changes), and validation (checks actually run and results). Use unchanged only if the request is already satisfied and no files changed. Use blocked to report a blocker; blocked is not a successful completion. The runtime verifies files against the workspace; merely returning proposed content does not implement the task. " + node.Executor.Instructions
	default:
		return providerport.AttemptRequest{}, fmt.Errorf("workflow node %q is %s; it is not a Codex-backed executor", request.Attempt.NodeID, request.Node.Type())
	}
	// Both workspace executors share one permission boundary. Point execution
	// adds its plan semantics; ordinary implementation needs only its task.
	if request.Node.Type() == workflow.NodeImplementation || request.Node.Type() == workflow.NodePointExecution {
		if !samePermissionSet(fields.Permissions, []string{"process.run", "workspace.write"}) {
			return providerport.AttemptRequest{}, fmt.Errorf("%s node %q requires exactly process.run and workspace.write permissions", request.Node.Type(), request.Attempt.NodeID)
		}
		access = providerport.AccessWorkspaceWrite
		commandPolicy, filePolicy = providerport.InteractionAllow, providerport.InteractionAllow
	}
	// Load trusted authoring guidance, never scheduler state, for Markdown outputs.
	for _, output := range fields.Outputs {
		if output.Type == workflow.ValueMarkdown || (output.Artifact != nil && strings.HasSuffix(strings.ToLower(output.Artifact.Filename), ".md")) {
			instruction += "\nLoaded authoring skill (applies to Markdown deliverables only):\n" + richArtifactSkill + "\nEnd of authoring skill.\n"
			break
		}
	}
	outputSchema, err := workflowOutputSchema(request.Node, fields.Outputs)
	if err != nil {
		return providerport.AttemptRequest{}, err
	}
	// Execution context is deliberately narrower than scheduler state. Do not add
	// run inputs, other nodes' outputs, routing, or workflow metadata here.
	promptContext := struct {
		Agent        string                                             `json:"agent"`
		Skills       []string                                           `json:"skills,omitempty"`
		Tools        []string                                           `json:"tools,omitempty"`
		Deliverables map[workflow.Identifier]workflow.OutputDeclaration `json:"deliverables"`
		Inputs       []string                                           `json:"availableInputs"`
	}{Agent: agent, Skills: skills, Tools: tools, Deliverables: fields.Outputs, Inputs: sortedInputNames(request.NodeInputs)}
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
		inputText := "Connected input " + id + ":\n" + string(value)
		digest := sha256.Sum256([]byte(inputText))
		providerInputs = append(providerInputs, providerport.Input{
			Kind: providerport.InputText, Name: id, MediaType: "application/json",
			Locator: "workflow-input:" + id, Digest: fmt.Sprintf("%x", digest), Text: inputText,
		})
	}
	return providerport.AttemptRequest{
		AttemptID: request.Attempt.AttemptID, RunID: request.Run.RunID, NodeID: request.Attempt.NodeID,
		IdempotencyKey: "start:" + request.Attempt.AttemptID, Workspace: workspace,
		Access: access, Network: providerport.NetworkDenied,
		CommandPolicy: commandPolicy, FilePolicy: filePolicy, ToolPolicy: providerport.InteractionDeny,
		Prompt: instruction + " Skills and tools are descriptive requirements only; do not assume unresolved capabilities. Produce every required deliverable separately under its exact output ID. For artifact outputs, the value is the complete Markdown content, not a path or summary. Follow the template supplied through artifact.templateInput for that output; do not mix templates or combine files. Submit values matching their output contracts with submit_output. If a point-progress output is declared, set remaining_points to 0 only after the requested outcome is complete.\nContext: " + string(encodedContext),
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
		property := map[string]any{"type": string(declaration.Type.StorageType())}
		if len(declaration.SchemaDefinition) != 0 {
			if err := json.Unmarshal(declaration.SchemaDefinition, &property); err != nil {
				return nil, err
			}
			properties[id] = property
			continue
		}
		if declaration.Description != "" {
			property["description"] = declaration.Description
		}
		if declaration.Type.StorageType() == workflow.ValueObject {
			property["properties"] = map[string]any{}
			property["required"] = []string{}
			property["additionalProperties"] = false
		}
		if declaration.Type == workflow.ValueArray {
			property["items"] = map[string]any{"type": "string"}
		}
		properties[id] = property
	}
	if node.Type() == workflow.NodePointExecution || node.Type() == workflow.NodeImplementation {
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
			if node.Type() == workflow.NodeImplementation {
				change := properties["changeset"].(map[string]any)
				change["properties"].(map[string]any)["disposition"] = map[string]any{"type": "string", "enum": []string{"changed", "unchanged", "blocked"}}
				change["required"] = []string{"disposition", "summary", "files", "validation"}
			}
		}
		if _, exists := properties["progress"]; exists && node.Type() == workflow.NodePointExecution {
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

func (wiring *daemonProviderWiring) ResolveWorkflowConfig(ctx context.Context, name, version, projectID string) (map[workflow.Identifier]json.RawMessage, error) {
	result := map[workflow.Identifier]json.RawMessage{}
	if wiring.workflows == nil {
		return result, nil
	}
	definition, err := wiring.workflows.Definition(ctx, name, version)
	if err != nil {
		return nil, err
	}
	needed := map[workflow.Identifier]workflow.ConfigResource{}
	for id, declaration := range definition.Document.Spec.Inputs {
		if declaration.Resource != nil {
			if resource, ok := declaration.Resource.Source.(workflow.ConfigResource); ok {
				needed[id] = resource
			}
		}
	}
	if len(needed) == 0 {
		return result, nil
	}
	if wiring.configuration == nil {
		return nil, errors.New("configuration resolution unavailable")
	}
	scope, err := config.ProjectMutationScope(projectID)
	if err != nil {
		return nil, err
	}
	state, err := wiring.configuration.State(ctx, scope)
	if err != nil {
		return nil, err
	}
	for id, resource := range needed {
		found := false
		for _, setting := range state.Effective {
			if setting.Key == resource.Key && setting.Value.Type() != config.SettingSecretReference {
				encoded, _ := json.Marshal(setting.Value.Value())
				declaration := definition.Document.Spec.Inputs[id]
				if err := (valueschemaadapter.Validator{}).Validate(declaration.SchemaDefinition, encoded); err != nil {
					return nil, err
				}
				result[id] = encoded
				found = true
				break
			}
		}
		if !found {
			return nil, fmt.Errorf("config key %q is unavailable or secret", resource.Key)
		}
	}
	return result, nil
}

// Preserve canonical work records in the daemon; expose task content to the model.
func agentNodeInputs(request runexecution.AttemptRequestContext) map[workflow.Identifier]json.RawMessage {
	inputs := make(map[workflow.Identifier]json.RawMessage, len(request.NodeInputs))
	for id, value := range request.NodeInputs {
		binding, declared := request.Node.Fields().Inputs[id]
		if !declared {
			continue
		}
		if declared && binding.ValueType() == workflow.ValueTask {
			var task map[string]json.RawMessage
			if json.Unmarshal(value, &task) == nil {
				delete(task, "id")
				delete(task, "projectId")
				if encoded, err := json.Marshal(task); err == nil {
					value = encoded
				}
			}
		}
		inputs[id] = value
	}
	return inputs
}
func sortedInputNames(inputs map[workflow.Identifier]json.RawMessage) []string {
	names := make([]string, 0, len(inputs))
	for id := range inputs {
		names = append(names, string(id))
	}
	sort.Strings(names)
	return names
}
