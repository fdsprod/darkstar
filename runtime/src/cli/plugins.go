package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	nodets "darkstar/src/adapters/nodeextension/typescript"
	"darkstar/src/adapters/plugin/process"
	"darkstar/src/adapters/provider/codex"
	providerts "darkstar/src/adapters/provider/typescript"
	"darkstar/src/adapters/provider/workflowtools"
	"darkstar/src/core/pluginworkspace"
	"darkstar/src/core/runexecution"
	"darkstar/src/ports/plugin"
	"darkstar/src/ports/provider"
	"darkstar/src/ports/tool"
)

const builtinPluginPin = "plugin:darkstar/builtin-resources"
const builtinNodePin = "plugin:darkstar/builtin-nodes"

// configurePlugins composes the trusted bundled implementation. Missing Node
// blocks plugin-backed attempts while keeping daemon inspection/repair available.
// There is no workflow-supplied executable or ambient package discovery.
func (w *daemonProviderWiring) configurePlugins(dataDirectory string) {
	w.pluginConfigured = true
	w.pluginRef = pluginprocess.BuiltinRef()
	entrypoint, err := pluginprocess.MaterializeBuiltin(filepath.Join(dataDirectory, "plugins"))
	if err != nil {
		w.pluginErr = err
		return
	}
	node, err := exec.LookPath("node")
	if err != nil {
		w.pluginErr = fmt.Errorf("Node.js is required for TypeScript plugins: %w", err)
		return
	}
	node, err = filepath.Abs(node)
	if err != nil {
		w.pluginErr = err
		return
	}
	w.nodePlugin, w.nodePluginErr = nodets.New(node, filepath.Join(dataDirectory, "plugins"))
	providerPath, providerErr := providerts.MaterializeBuiltin(filepath.Join(dataDirectory, "plugins"))
	w.providerPluginErr = providerErr
	if providerErr == nil {
		w.providerPluginConfig = &providerts.Config{NodeExecutable: node, Entrypoint: providerPath, ProviderExecutable: w.executable, ProjectRoot: w.projectRoot, Provider: runexecution.ProviderCodex, Environment: w.environment, Ref: providerts.BuiltinRef(), RecordEvidence: func(ctx context.Context, r providerts.EvidenceRecord) (provider.Evidence, error) {
			if w.evidence == nil {
				return provider.Evidence{}, fmt.Errorf("provider evidence storage unavailable")
			}
			return w.evidence.Record(ctx, codex.EvidenceRecord{AttemptID: r.AttemptID, Sequence: r.Sequence, Kind: r.Kind, MediaType: r.MediaType, Data: r.Data})
		}}
	}
	runtime, err := pluginprocess.New(pluginprocess.Config{
		Executable: node, Entrypoint: entrypoint, Ref: w.pluginRef,
		GrantedCapabilities: []string{"journal.read", "journal.mutate", "workspace.read", "workspace.write", "workspace.publish_artifact"},
		Timeout:             30 * time.Second,
	})
	if err != nil {
		w.pluginErr = err
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	descriptor, err := runtime.Describe(ctx)
	if err != nil {
		w.pluginErr = err
		return
	}
	w.resourcePlugin = &workflowtools.ResourcePlugin{Runtime: runtime, Descriptor: descriptor}
}

func (w *daemonProviderWiring) pinnedNodeEngine(request runexecution.AttemptRequestContext) (*nodets.Engine, error) {
	ref, ok := request.ExecutionContext.ExtensionPins[builtinNodePin]
	if !ok {
		return nil, nil
	}
	if ref != nodets.BuiltinRef() {
		return nil, fmt.Errorf("EXTENSION_UNAVAILABLE: exact node package is required")
	}
	if w.nodePluginErr != nil {
		return nil, w.nodePluginErr
	}
	if w.nodePlugin == nil {
		return nil, fmt.Errorf("node plugin runtime is unavailable")
	}
	return w.nodePlugin, nil
}
func (w *daemonProviderWiring) buildScopedAttempt(ctx context.Context, request runexecution.AttemptRequestContext, fingerprint string) (provider.AttemptRequest, error) {
	engine, err := w.pinnedNodeEngine(request)
	if err != nil {
		return provider.AttemptRequest{}, err
	}
	return buildWorkflowAttemptRequestWithNodes(ctx, request, w.projectRoot, fingerprint, engine)
}
func (w *daemonProviderWiring) pluginProvider(request runexecution.ProviderRequest) (provider.Provider, error) {
	if w.providerPluginErr != nil {
		return nil, w.providerPluginErr
	}
	if w.providerPluginConfig == nil {
		return nil, fmt.Errorf("TypeScript provider plugin unavailable")
	}
	w.pluginProvidersMu.Lock()
	defer w.pluginProvidersMu.Unlock()
	if w.pluginProviders == nil {
		w.pluginProviders = map[string]*providerts.Adapter{}
	}
	if existing := w.pluginProviders[request.AttemptID]; existing != nil {
		return existing, nil
	}
	adapter, err := providerts.New(*w.providerPluginConfig)
	if err != nil {
		return nil, err
	}
	w.pluginProviders[request.AttemptID] = adapter
	return adapter, nil
}
func (w *daemonProviderWiring) closePluginProviders() {
	w.pluginProvidersMu.Lock()
	values := w.pluginProviders
	w.pluginProviders = nil
	w.pluginProvidersMu.Unlock()
	for _, adapter := range values {
		_ = adapter.Close()
	}
}

func (w *daemonProviderWiring) ReleaseProvider(attemptID string) {
	w.pluginProvidersMu.Lock()
	adapter := w.pluginProviders[attemptID]
	delete(w.pluginProviders, attemptID)
	w.pluginProvidersMu.Unlock()
	if adapter != nil {
		_ = adapter.Close()
	}
}

func (w *daemonProviderWiring) bindPluginTools(session *workflowtools.Session, request runexecution.AttemptRequestContext) error {
	if !w.pluginConfigured {
		return nil
	} // Explicitly constructed legacy/test wiring.
	ref, pinned := request.ExecutionContext.ExtensionPins[builtinPluginPin]
	if !pinned {
		return nil // Legacy runs retain their original tools and grants.
	}
	if ref != w.pluginRef {
		return fmt.Errorf("EXTENSION_UNAVAILABLE: run requires exact plugin %s@%s (%s)", ref.ID, ref.Version, ref.Digest)
	}
	if w.pluginErr != nil {
		return fmt.Errorf("PLUGIN_UNAVAILABLE: %w", w.pluginErr)
	}
	if w.resourcePlugin == nil {
		return fmt.Errorf("PLUGIN_UNAVAILABLE: built-in tool plugin is not configured")
	}
	host, err := pluginworkspace.New(w.workspaces, w.pluginArtifacts, pluginworkspace.Scope{
		WorkItemID: request.WorkItem.WorkItemID, RunID: request.Run.RunID,
		NodeID: request.Attempt.NodeID, AttemptID: request.Attempt.AttemptID,
		Plugin: w.resourcePlugin.Descriptor.Ref,
	})
	if err != nil {
		return err
	}
	session.ResourcePlugin = w.resourcePlugin
	for _, contribution := range w.resourcePlugin.Descriptor.Tools {
		// Function names are transport aliases; the full contribution identity
		// remains in the invocation and pinned package descriptor.
		name := strings.ReplaceAll(contribution.ID, ".", "_")
		session.AdditionalTools = append(session.AdditionalTools, tool.Tool{
			Definition:   provider.ToolDefinition{Type: "function", Name: name, Description: contribution.Description, InputSchema: contribution.InputSchema},
			ResultSchema: contribution.ResultSchema,
			Invoke: func(ctx context.Context, _ string, args json.RawMessage) (json.RawMessage, error) {
				return w.resourcePlugin.Runtime.Invoke(ctx, plugin.Invocation{Contribution: contribution.ID, Arguments: args}, host)
			},
		})
	}
	return nil
}
