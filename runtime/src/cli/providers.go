package cli

import (
	"context"
	"crypto/sha256"
	valueschemaadapter "darkstar/src/adapters/valueschema/jsonschema"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"

	nodets "darkstar/src/adapters/nodeextension/typescript"
	"darkstar/src/adapters/provider/codex"
	providerts "darkstar/src/adapters/provider/typescript"
	"darkstar/src/adapters/provider/workflowtools"
	"darkstar/src/core/config"
	"darkstar/src/core/configmutation"
	"darkstar/src/core/extensions"
	"darkstar/src/core/nodes"
	"darkstar/src/core/pluginworkspace"
	"darkstar/src/core/runexecution"
	"darkstar/src/core/workflow"
	daemonconfiguration "darkstar/src/daemon/configuration"
	"darkstar/src/doctor"
	"darkstar/src/platform/toolchain"
	"darkstar/src/ports/extension"
	"darkstar/src/ports/nodeextension"
	"darkstar/src/ports/outputvalidator"
	platformport "darkstar/src/ports/platform"
	providerport "darkstar/src/ports/provider"
	"darkstar/src/ports/statestore"
	"darkstar/src/ports/workspace"
)

// daemonProviderWiring keeps fake acceptance scenarios separate from the
// selected production provider. A failed or ambiguous Codex selection remains
// explicit so fake scenarios still run while real attempts fail closed.
type daemonProviderWiring struct {
	nodePlugin           *nodets.Engine
	nodePluginErr        error
	providerPluginConfig *providerts.Config
	providerPluginErr    error
	pluginProvidersMu    sync.Mutex
	pluginProviders      map[string]*providerts.Adapter
	workspaces           workspace.Manager
	pluginArtifacts      pluginworkspace.Artifacts
	resourcePlugin       *workflowtools.ResourcePlugin
	pluginErr            error
	pluginConfigured     bool
	pluginRef            extension.Ref
	validators           *extensions.Catalog[outputvalidator.Validator]
	nodeExtensions       nodeextension.Resolver
	providers            *runexecution.ProviderCatalog
	defaultProvider      string
	configuration        *configmutation.Service
	workflows            *workflow.Catalog
	configuredCodex      string
	executable           string
	environment          []string
	selectionErr         error
	projectRoot          string
	toolDatabase         string
	evidence             codex.EvidenceRecorder
}

// richArtifactSkill is generated from skills/builtin/rich-artifacts/SKILL.md.
//
//go:embed rich-artifacts.md
var richArtifactSkill string

const fakeProviderName = "fake"

func newDaemonProviderWiring(paths platformport.Paths, projectRoot string) (*daemonProviderWiring, error) {
	wiring, err := resolveDaemonProviderWiring(paths, projectRoot, doctor.ResolveCodexExecutable)
	if err == nil && wiring.selectionErr == nil {
		err = toolchain.Activate(wiring.environment)
	}
	if err == nil {
		wiring.configurePlugins(paths.Data)
	}
	return wiring, err
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
	wiring.environment, wiring.selectionErr = toolchain.Environment(context.Background(), projectRoot, os.Environ())
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

func (wiring *daemonProviderWiring) DefaultWorkflowProvider() string {
	if wiring.defaultProvider != "" {
		return wiring.defaultProvider
	}
	return runexecution.ProviderCodex
}

func (wiring *daemonProviderWiring) providerCatalog() (*runexecution.ProviderCatalog, error) {
	if wiring.providers != nil {
		return wiring.providers, nil
	}
	var builtinRef *extension.Ref
	if wiring.pluginConfigured {
		ref := providerts.BuiltinRef()
		builtinRef = &ref
	}
	return runexecution.NewProviderCatalog(
		runexecution.ProviderRegistration{Name: fakeProviderName, Factory: runexecution.WorkflowProviderFactoryFunc(func(_ context.Context, request runexecution.ProviderRequest) (providerport.Provider, error) {
			if request.Scenario != runexecution.ScenarioSuccess && request.Scenario != runexecution.ScenarioRestart {
				return nil, runexecution.ErrInvalidScenario
			}
			return newFakeRunProvider(request.Scenario, request.AttemptID, request.Resume)
		})},
		runexecution.ProviderRegistration{Name: runexecution.ProviderCodex, Ref: builtinRef, Factory: runexecution.WorkflowProviderFactoryFunc(func(_ context.Context, request runexecution.ProviderRequest) (providerport.Provider, error) {
			if request.Scenario != runexecution.ScenarioWorkflow {
				return nil, fmt.Errorf("codex provider does not support scenario %q", request.Scenario)
			}
			if wiring.selectionErr != nil {
				return nil, fmt.Errorf("codex provider is unavailable: %w", wiring.selectionErr)
			}
			if request.Ref != nil {
				return wiring.pluginProvider(request)
			}
			return wiring.codexProvider()
		})},
	)
}

func (wiring *daemonProviderWiring) Provider(ctx context.Context, request runexecution.ProviderRequest) (providerport.Provider, error) {
	catalog, err := wiring.providerCatalog()
	if err != nil {
		return nil, err
	}
	return catalog.Provider(ctx, request)
}

func (wiring *daemonProviderWiring) BuildAttemptRequest(ctx context.Context, request runexecution.AttemptRequestContext) (providerport.AttemptRequest, error) {
	providerName := request.Attempt.Provider
	if providerName == "" {
		providerName = wiring.DefaultWorkflowProvider()
	}
	var ref *extension.Ref
	if pin, ok := request.ExecutionContext.ExtensionPins["provider:"+providerName]; ok {
		ref = &pin
	}
	adapter, err := wiring.Provider(ctx, runexecution.ProviderRequest{Ref: ref, Provider: providerName, Scenario: runexecution.ScenarioWorkflow, AttemptID: request.Attempt.AttemptID})
	if err != nil {
		return providerport.AttemptRequest{}, err
	}
	manifest, err := adapter.Capabilities(ctx)
	if err != nil {
		return providerport.AttemptRequest{}, fmt.Errorf("observe provider capabilities for workflow attempt: %w", err)
	}
	request.NodeInputs = agentNodeInputs(request)

	handler, err := nodes.Lookup(request.Node)
	if err != nil {
		return providerport.AttemptRequest{}, err
	}
	session := &workflowtools.Session{Database: wiring.toolDatabase, RunID: request.Run.RunID, AttemptID: request.Attempt.AttemptID, Node: request.Node, Inputs: request.NodeInputs}
	// Build and authorize the task before touching the prepared workspace.
	built, err := wiring.buildScopedAttempt(ctx, request, manifest.Fingerprint)
	if err != nil {
		return built, err
	}
	if preparation, ok := handler.(nodes.AgentPreparation); ok {
		inputs, path, suffix, prepareErr := preparation.Prepare(ctx, request.NodeInputs, built.Workspace, agentWorkspaceServices{workspaceNodeServices{wiring, request}, session})
		if prepareErr != nil {
			return providerport.AttemptRequest{}, prepareErr
		}
		request.NodeInputs = inputs
		session.Inputs = inputs
		// Refresh input projections from the durable workspace; old history is retained.
		built, err = wiring.buildScopedAttempt(ctx, request, manifest.Fingerprint)
		if err != nil {
			return built, err
		}
		built.Workspace = path
		built.Prompt += suffix
	}
	session.Workspace = built.Workspace
	if err := wiring.bindPluginTools(session, request); err != nil {
		return providerport.AttemptRequest{}, err
	}
	if err := session.Validate(); err != nil {
		return providerport.AttemptRequest{}, fmt.Errorf("validate attempt tools: %w", err)
	}
	if err := session.PrepareMarkdown(ctx); err != nil {
		return providerport.AttemptRequest{}, fmt.Errorf("capture Markdown baseline: %w", err)
	}
	built.DynamicTools = session.Definitions()
	built.ToolHandler = session
	built.Prompt += " Use read_input to inspect connected inputs and templates. Submit each deliverable with submit_output and correct any validation errors before finishing. Use journal tools only when they are present in your tool list. After submitting all required outputs, finish with a concise human-readable summary. Do not repeat document contents or a JSON output envelope; the daemon assembles the validated submissions."
	return built, nil
}

func buildWorkflowAttemptRequest(request runexecution.AttemptRequestContext, workspace, capabilityFingerprint string) (providerport.AttemptRequest, error) {
	return buildWorkflowAttemptRequestWithNodes(context.Background(), request, workspace, capabilityFingerprint, nil)
}

func buildWorkflowAttemptRequestWithNodes(ctx context.Context, request runexecution.AttemptRequestContext, workspace, capabilityFingerprint string, engine *nodets.Engine) (providerport.AttemptRequest, error) {
	workspace = filepath.Clean(strings.TrimSpace(workspace))
	workspaceDigest := fmt.Sprintf("%x", sha256.Sum256([]byte(workspace)))
	if workspace == "." || !filepath.IsAbs(workspace) || request.Project.Status != statestore.ProjectActive || request.Project.SourceHash != workspaceDigest {
		return providerport.AttemptRequest{}, fmt.Errorf("workflow project %q is not authorized for daemon workspace %q", request.Project.ProjectID, workspace)
	}
	request.NodeInputs = agentNodeInputs(request)
	fields := request.Node.Fields()

	handler, err := nodes.Lookup(request.Node)
	if err != nil {
		return providerport.AttemptRequest{}, err
	}
	agentHandler, ok := handler.(nodes.AgentHandler)
	if !ok {
		return providerport.AttemptRequest{}, fmt.Errorf("workflow node %q is %s; it is not an agent-backed executor", request.Attempt.NodeID, request.Node.Type())
	}
	var task nodes.AgentTask
	if engine != nil {
		task, err = engine.BuildTask(ctx, request.Node, request.Attempt.NodeID)
	} else {
		task, err = agentHandler.BuildTask(request.Attempt.NodeID)
	}
	if err != nil {
		return providerport.AttemptRequest{}, err
	}
	if engine != nil {
		permissions := slices.Clone(fields.Permissions)
		slices.Sort(permissions)
		switch request.Node.(type) {
		case workflow.ReasoningNode:
			if len(permissions) != 0 || task.Access != providerport.AccessReadOnly {
				return providerport.AttemptRequest{}, errors.New("reasoning plugin requested ungranted access")
			}
		case workflow.ImplementationNode, workflow.PointExecutionNode:
			if !slices.Equal(permissions, []string{"process.run", "workspace.write"}) || task.Access != providerport.AccessWorkspaceWrite {
				return providerport.AttemptRequest{}, errors.New("implementation plugin access exceeds the declared permission contract")
			}
		default:
			return providerport.AttemptRequest{}, errors.New("unsupported plugin agent access contract")
		}
	}
	agent, skills, tools, instruction := task.Agent, task.Skills, task.Tools, task.Instructions
	access := task.Access
	commandPolicy, filePolicy := providerport.InteractionDeny, providerport.InteractionDeny
	if access == providerport.AccessWorkspaceWrite {
		commandPolicy, filePolicy = providerport.InteractionAllow, providerport.InteractionAllow
	}
	// Load trusted authoring guidance, never scheduler state, for Markdown outputs.
	for _, output := range fields.Outputs {
		if output.Type == workflow.ValueMarkdown || (output.Artifact != nil && strings.HasSuffix(strings.ToLower(output.Artifact.Filename), ".md")) {
			instruction += "\nLoaded authoring skill (applies to Markdown deliverables only):\n" + richArtifactSkill + "\nEnd of authoring skill.\n"
			break
		}
	}
	outputSchema, err := workflowOutputSchemaWithNodes(ctx, request.Node, fields.Outputs, engine)
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

func workflowOutputSchema(node workflow.Node, outputs map[workflow.Identifier]workflow.OutputDeclaration) (json.RawMessage, error) {
	return workflowOutputSchemaWithNodes(context.Background(), node, outputs, nil)
}
func workflowOutputSchemaWithNodes(ctx context.Context, node workflow.Node, outputs map[workflow.Identifier]workflow.OutputDeclaration, engine *nodets.Engine) (json.RawMessage, error) {
	ids := make([]string, 0, len(outputs))
	for id := range outputs {
		ids = append(ids, string(id))
	}
	sort.Strings(ids)
	properties := make(map[string]any, len(ids))
	for _, id := range ids {
		declaration := outputs[workflow.Identifier(id)]
		property := map[string]any{"type": string(declaration.Type.StorageType())}
		declaration.SchemaDefinition = workflow.ValueSchema(declaration.Type, declaration.SchemaDefinition)
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

	handler, err := nodes.Lookup(node)
	if err != nil {
		return nil, err
	}
	if engine != nil {
		if err := engine.ConfigureOutputs(ctx, node, properties); err != nil {
			return nil, err
		}
	} else if agent, ok := handler.(nodes.AgentHandler); ok {
		agent.ConfigureOutputs(properties)
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
	environment, err := toolchain.Environment(context.Background(), wiring.projectRoot, os.Environ())
	if err != nil {
		return nil, err
	}
	return codex.NewAdapter(codex.AdapterOptions{
		Executable: wiring.executable, ProjectRoot: wiring.projectRoot, EvidenceRecorder: wiring.evidence,
		Client: codex.AppServerOptions{Environment: environment},
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

// agentWorkspaceServices supplies storage operations without exposing scheduling.
type agentWorkspaceServices struct {
	workspaceNodeServices
	session *workflowtools.Session
}

func (s agentWorkspaceServices) CaptureBaseline(ctx context.Context, path string) error {
	s.session.Workspace = path
	return s.session.PrepareWorkspace(ctx)
}

func (w *daemonProviderWiring) ValidateExtensions(ctx context.Context, checks []extensions.Check, inputs, outputs map[workflow.Identifier]json.RawMessage) ([]extensions.ValidationEvidence, error) {
	in, out := map[string]json.RawMessage{}, map[string]json.RawMessage{}
	for k, v := range inputs {
		in[string(k)] = v
	}
	for k, v := range outputs {
		out[string(k)] = v
	}
	return extensions.Validate(ctx, w.validators, valueschemaadapter.Validator{}, checks, in, out)
}

func (w *daemonProviderWiring) ExtensionPins() map[string]extension.Ref {
	c, err := w.providerCatalog()
	if err != nil {
		return nil
	}
	pins := map[string]extension.Ref{}
	key := "provider:" + w.DefaultWorkflowProvider()
	if ref, ok := c.ExtensionPins()[key]; ok {
		pins[key] = ref
	}
	if w.pluginConfigured {
		pins[builtinPluginPin] = w.pluginRef
		pins[builtinNodePin] = nodets.BuiltinRef()
	}
	return pins
}
func (w *daemonProviderWiring) ValidateExtensionPins(pins map[string]extension.Ref) error {
	for name, ref := range pins {
		matches := name == builtinPluginPin && w.pluginRef == ref || name == builtinNodePin && nodets.BuiltinRef() == ref
		if strings.HasPrefix(name, "plugin:") && (!w.pluginConfigured || !matches) {
			return fmt.Errorf("EXTENSION_UNAVAILABLE: exact plugin %s@%s (%s) is required", ref.ID, ref.Version, ref.Digest)
		}
	}
	c, err := w.providerCatalog()
	if err != nil {
		return err
	}
	return c.ValidateExtensionPins(pins)
}
