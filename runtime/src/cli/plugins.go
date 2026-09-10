package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"darkstar/src/adapters/plugin/process"
	"darkstar/src/adapters/provider/workflowtools"
	"darkstar/src/core/pluginworkspace"
	"darkstar/src/core/runexecution"
	"darkstar/src/ports/plugin"
	"darkstar/src/ports/provider"
	"darkstar/src/ports/tool"
)

const builtinPluginPin = "plugin:darkstar/builtin-resources"

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
