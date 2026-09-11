// Package typescript adapts node contributions to the daemon's scoped services.
package typescript

import (
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"time"

	pluginprocess "darkstar/src/adapters/plugin/process"
	"darkstar/src/core/nodes"
	"darkstar/src/core/workflow"
	"darkstar/src/ports/extension"
	"darkstar/src/ports/plugin"
	"darkstar/src/ports/provider"
)

//go:embed builtin.mjs
var bundle []byte

func BuiltinRef() extension.Ref {
	digest := sha256.Sum256(bundle)
	return extension.Ref{ID: "darkstar/builtin-nodes", Version: "1.0.0", Digest: hex.EncodeToString(digest[:])}
}
func MaterializeBuiltin(directory string) (string, error) {
	path := filepath.Join(directory, BuiltinRef().Digest, "plugin.mjs")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return "", err
	}
	if err := os.WriteFile(path, bundle, 0600); err != nil {
		return "", err
	}
	return path, nil
}

type Engine struct{ Runtime plugin.Runtime }

func New(executable, directory string) (*Engine, error) {
	path, err := MaterializeBuiltin(directory)
	if err != nil {
		return nil, err
	}
	host, err := pluginprocess.New(pluginprocess.Config{Executable: executable, Entrypoint: path, Ref: BuiltinRef(), GrantedCapabilities: []string{"delivery.commit", "delivery.push", "delivery.create_pr", "workspace.prepare", "workspace.resolve", "process.run"}, Timeout: 10 * time.Minute})
	if err != nil {
		return nil, err
	}
	return &Engine{Runtime: host}, nil
}
func contribution(node workflow.Node) (string, any, error) {
	switch n := node.(type) {
	case workflow.GitCommitNode:
		return "git-commit", n.Executor, nil
	case workflow.GitPushNode:
		return "git-push", n.Executor, nil
	case workflow.CreatePRNode:
		return "create-pr", n.Executor, nil
	case workflow.ReasoningNode:
		if n.Executor.Agent == "delivery-text" {
			return "delivery-text", n.Executor, nil
		}
		return "reasoning", n.Executor, nil
	case workflow.ImplementationNode:
		return "implementation", n.Executor, nil
	case workflow.PointExecutionNode:
		return "point-execution", n.Executor, nil
	case workflow.CommandNode:
		return "command", n.Executor, nil
	case workflow.WorkspacePrepareNode:
		return "workspace-prepare", n.Executor, nil
	case workflow.WorkspaceValidateNode:
		return "workspace-validate", n.Executor, nil
	default:
		return "", nil, fmt.Errorf("node %s has no TypeScript contribution", node.Type())
	}
}
func (e *Engine) invoke(ctx context.Context, node workflow.Node, operation string, extra map[string]any, services plugin.HostServices) (json.RawMessage, error) {
	id, config, err := contribution(node)
	if err != nil {
		return nil, err
	}
	if extra == nil {
		extra = map[string]any{}
	}
	extra["operation"], extra["configuration"] = operation, config
	raw, err := json.Marshal(extra)
	if err != nil {
		return nil, err
	}
	return e.Runtime.Invoke(ctx, plugin.Invocation{Contribution: id, Arguments: raw}, services)
}
func (e *Engine) BuildTask(ctx context.Context, node workflow.Node, nodeID string) (nodes.AgentTask, error) {
	permissions := append([]string{}, node.Fields().Permissions...)
	raw, err := e.invoke(ctx, node, "buildTask", map[string]any{"permissions": permissions}, nil)
	if err != nil {
		return nodes.AgentTask{}, fmt.Errorf("node %q: %w", nodeID, err)
	}
	var wire struct {
		Agent        string               `json:"agent"`
		Instructions string               `json:"instructions"`
		Skills       []string             `json:"skills"`
		Tools        []string             `json:"tools"`
		Access       provider.AccessClass `json:"access"`
	}
	if err = json.Unmarshal(raw, &wire); err != nil {
		return nodes.AgentTask{}, err
	}
	// Returned access is a requirement; this cannot grant workspace access.
	if wire.Access != provider.AccessReadOnly && wire.Access != provider.AccessWorkspaceWrite {
		return nodes.AgentTask{}, errors.New("invalid node access requirement")
	}
	return nodes.AgentTask{Agent: wire.Agent, Instructions: wire.Instructions, Skills: wire.Skills, Tools: wire.Tools, Access: wire.Access}, nil
}
func (e *Engine) ConfigureOutputs(ctx context.Context, node workflow.Node, properties map[string]any) error {
	raw, err := e.invoke(ctx, node, "configureOutputs", map[string]any{"properties": properties}, nil)
	if err != nil {
		return err
	}
	var result map[string]any
	if err = json.Unmarshal(raw, &result); err != nil {
		return err
	}
	// Node contributions refine declared contracts; they cannot add/remove outputs.
	if len(result) != len(properties) {
		return errors.New("node changed declared output names")
	}
	for name := range properties {
		if _, ok := result[name]; !ok {
			return errors.New("node changed declared output names")
		}
	}
	for name, value := range result {
		properties[name] = value
	}
	return nil
}
func (e *Engine) Execute(ctx context.Context, node workflow.Node, inputs nodes.Inputs, services nodes.BuiltinServices) (json.RawMessage, error) {
	// The historical command allowlist remains host authorization, not plugin policy.
	if n, ok := node.(workflow.CommandNode); ok {
		if services.LegacyCommand.WorkflowID != "darkstar/story-execution" || services.LegacyCommand.NodeID != "s6_validation" || !reflect.DeepEqual(n.Executor.Argv, []string{"darkstar-project", "validate", "--json"}) || n.Executor.CWD != "" {
			return nil, errors.New("command node is not an explicitly supported deterministic builtin")
		}
		timeout := uint64(30)
		if n.Executor.TimeoutSeconds != nil && *n.Executor.TimeoutSeconds < timeout {
			timeout = *n.Executor.TimeoutSeconds
		}
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, time.Duration(timeout)*time.Second)
		defer cancel()
	}
	scoped := nodes.Inputs{}
	for id := range node.Fields().Inputs {
		if value, ok := inputs[id]; ok {
			scoped[id] = append(json.RawMessage(nil), value...)
		}
	}
	host := &executionServices{node: node, inputs: scoped, services: services}
	result, err := e.invoke(ctx, node, "execute", map[string]any{"inputs": scoped}, host)
	if err != nil {
		return nil, err
	}
	switch node.(type) {
	case workflow.GitCommitNode, workflow.GitPushNode, workflow.CreatePRNode:
		if host.deliveryResult == nil {
			return nil, errors.New("delivery node omitted host operation")
		}
	}
	if host.deliveryResult != nil && !sameJSON(host.deliveryResult, result) {
		return nil, errors.New("delivery plugin output differs from observed host result")
	}
	// Plugin output cannot replace evidence that required host operations ran.
	switch n := node.(type) {
	case workflow.WorkspaceValidateNode:
		if host.workspace == nil || host.check != len(n.Executor.Checks) || len(n.Executor.Checks) == 0 {
			return nil, errors.New("node omitted required workspace validation")
		}
	case workflow.CommandNode:
		if host.check != 2 {
			return nil, errors.New("node omitted required command validation")
		}
	case workflow.WorkspacePrepareNode:
		if !host.prepared {
			return nil, errors.New("node omitted workspace preparation")
		}
	}
	return result, nil
}

type executionServices struct {
	deliveryResult json.RawMessage
	node           workflow.Node
	inputs         nodes.Inputs
	services       nodes.BuiltinServices
	workspace      *nodes.Workspace
	check          int
	prepared       bool
}

func sameJSON(a, b any) bool {
	left, _ := json.Marshal(a)
	right, _ := json.Marshal(b)
	var l, r any
	_ = json.Unmarshal(left, &l)
	_ = json.Unmarshal(right, &r)
	return reflect.DeepEqual(l, r)
}
func (s *executionServices) Call(ctx context.Context, method string, args json.RawMessage) (json.RawMessage, error) {
	switch n := s.node.(type) {
	case workflow.GitCommitNode:
		return s.delivery(ctx, method, "delivery.commit", args, n.Executor)
	case workflow.GitPushNode:
		return s.delivery(ctx, method, "delivery.push", args, n.Executor)
	case workflow.CreatePRNode:
		return s.delivery(ctx, method, "delivery.create_pr", args, n.Executor)
	case workflow.WorkspacePrepareNode:
		if method != "workspace.prepare" {
			break
		}
		var request struct {
			Repository json.RawMessage `json:"repository"`
			Checkout   json.RawMessage `json:"checkout"`
		}
		if err := json.Unmarshal(args, &request); err != nil {
			return nil, err
		}
		if !sameJSON(request.Repository, s.inputs[n.Executor.RepositoryInput]) || !sameJSON(request.Checkout, n.Executor.Checkout) {
			return nil, errors.New("workspace request exceeds bound node configuration")
		}
		raw, err := nodes.PrepareWorkspace(ctx, request.Repository, n.Executor.Checkout, s.services.Workspaces)
		if err != nil {
			return nil, err
		}
		var output map[string]json.RawMessage
		if err = json.Unmarshal(raw, &output); err != nil {
			return nil, err
		}
		s.prepared = true
		return output["workspace"], nil
	case workflow.WorkspaceValidateNode:
		if method == "workspace.resolve" {
			var request struct {
				Reference json.RawMessage `json:"reference"`
			}
			if err := json.Unmarshal(args, &request); err != nil {
				return nil, err
			}
			if !sameJSON(request.Reference, s.inputs[n.Executor.WorkspaceInput]) {
				return nil, errors.New("workspace reference is not connected")
			}
			workspace, err := s.services.Workspaces.Store.Resolve(ctx, request.Reference)
			if err != nil {
				return nil, err
			}
			s.workspace = &workspace
			return json.Marshal(map[string]string{"id": workspace.ID})
		}
		if method == "process.run" && s.workspace != nil {
			var request struct {
				Argv           []string `json:"argv"`
				TimeoutSeconds int      `json:"timeoutSeconds"`
			}
			if err := json.Unmarshal(args, &request); err != nil {
				return nil, err
			}
			if s.check >= len(n.Executor.Checks) || !reflect.DeepEqual(request.Argv, n.Executor.Checks[s.check]) || request.TimeoutSeconds != 120 {
				return nil, errors.New("command is not the next declared validation check")
			}
			output, err := s.services.Commands.Run(ctx, s.workspace.Path, request.Argv, 2*time.Minute)
			if err != nil {
				return nil, fmt.Errorf("required check %q failed: %w\n%s", request.Argv, err, output)
			}
			s.check++
			return json.Marshal(output)
		}
	case workflow.CommandNode:
		if method != "process.run" {
			break
		}
		var request struct {
			Check          string `json:"check"`
			TimeoutSeconds uint64 `json:"timeoutSeconds"`
		}
		if err := json.Unmarshal(args, &request); err != nil {
			return nil, err
		}
		timeout := uint64(30)
		if n.Executor.TimeoutSeconds != nil && *n.Executor.TimeoutSeconds < timeout {
			timeout = *n.Executor.TimeoutSeconds
		}
		checks := [][]string{{"diff", "--check", "HEAD"}, {"status", "--porcelain"}}
		names := []string{"diff", "status"}
		if s.check >= len(checks) || request.Check != names[s.check] || request.TimeoutSeconds != timeout {
			return nil, errors.New("command is not an authorized validation operation")
		}
		argv := append([]string{"git", "-C", s.services.LegacyCommand.Workspace}, checks[s.check]...)
		output, err := s.services.Commands.Run(ctx, s.services.LegacyCommand.Workspace, argv, time.Duration(timeout)*time.Second)
		if err != nil {
			return nil, fmt.Errorf("darkstar-project validation failed: %w: %s", err, output)
		}
		s.check++
		return json.Marshal(output)
	}
	return nil, fmt.Errorf("node host service %q is not granted", method)
}

func (s *executionServices) delivery(ctx context.Context, method, want string, raw json.RawMessage, configuration any) (json.RawMessage, error) {
	var request struct {
		Configuration json.RawMessage `json:"configuration"`
		Inputs        nodes.Inputs    `json:"inputs"`
	}
	if err := json.Unmarshal(raw, &request); err != nil {
		return nil, err
	}
	if method != want || !sameJSON(request.Configuration, configuration) || !sameJSON(request.Inputs, s.inputs) || s.services.Delivery == nil {
		return nil, errors.New("delivery request exceeds bound node inputs/configuration")
	}
	result, err := s.services.Delivery.Execute(ctx, s.node, s.inputs)
	if err == nil {
		s.deliveryResult = result
	}
	return result, err
}
