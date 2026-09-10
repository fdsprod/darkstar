package workflowtools

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"

	valueschemaadapter "darkstar/src/adapters/valueschema/jsonschema"
	"darkstar/src/core/workflow"
	"darkstar/src/ports/plugin"
	"darkstar/src/ports/provider"
	toolport "darkstar/src/ports/tool"
)

type Tool = toolport.Tool

// ResourcePlugin binds verified contribution metadata to its executable runtime.
// No filesystem or database location is disclosed to the plugin.
type ResourcePlugin struct {
	Runtime    plugin.Runtime
	Descriptor plugin.Descriptor
}

func objectSchema(properties map[string]any, required ...string) json.RawMessage {
	if required == nil {
		required = []string{}
	}
	raw, _ := json.Marshal(map[string]any{"type": "object", "additionalProperties": false, "properties": properties, "required": required})
	return raw
}

func (s *Session) toolCatalog() ([]Tool, error) {
	text := map[string]any{"type": "string"}
	makeTool := func(name, description string, schema json.RawMessage, invoke func(context.Context, string, json.RawMessage) (json.RawMessage, error)) Tool {
		return Tool{Definition: provider.ToolDefinition{Type: "function", Name: name, Description: description, InputSchema: schema}, ResultSchema: json.RawMessage(`{}`), Invoke: invoke}
	}
	entries := []Tool{
		makeTool("read_input", "Read an input or template by its exact input ID.", objectSchema(map[string]any{"id": text}, "id"), s.readInput),
		makeTool("submit_output", "Validate and stage one output. Correct any reported errors and submit again. The daemon collects validated submissions; do not repeat their contents in your final message.", objectSchema(map[string]any{"id": text, "value": map[string]any{}}, "id", "value"), s.submitOutput),
	}
	if s.Node.Type() == workflow.NodeImplementation {
		entries = append(entries, makeTool("inspect_workspace_changes", "Read the actual added, modified, and deleted files since this attempt began. Use these exact paths in changeset.files.", objectSchema(map[string]any{}), func(ctx context.Context, _ string, _ json.RawMessage) (json.RawMessage, error) {
			changes, err := s.workspaceChanges(ctx)
			if err != nil {
				return nil, err
			}
			return json.Marshal(map[string]any{"files": changes})
		}))
	}
	resources := builtinResources()
	if s.ResourcePlugin != nil {
		if s.ResourcePlugin.Runtime == nil {
			return nil, errors.New("resource plugin runtime is missing")
		}
		resources = s.ResourcePlugin.Descriptor.Resources
	}
	byKind := map[string]plugin.Resource{}
	for _, resource := range resources {
		if resource.Kind == "" || resource.Tool.ID == "" || resource.CreateOperation == "" || resource.CreateOperation == "read" {
			return nil, errors.New("invalid resource contribution")
		}
		if _, exists := byKind[resource.Kind]; exists {
			return nil, fmt.Errorf("duplicate resource kind %s", resource.Kind)
		}
		byKind[resource.Kind] = resource
	}
	ids := make([]string, 0, len(s.Inputs))
	for id := range s.Inputs {
		ids = append(ids, string(id))
	}
	sort.Strings(ids)
	for _, id := range ids {
		var ref struct {
			Kind       string `json:"kind"`
			ResourceID string `json:"resourceId"`
		}
		if json.Unmarshal(s.Inputs[workflow.Identifier(id)], &ref) != nil || ref.ResourceID == "" {
			continue
		}
		resource, ok := byKind[ref.Kind]
		if !ok {
			continue
		}
		binding := ref.ResourceID
		if strings.HasPrefix(binding, "output:") || strings.HasPrefix(binding, "rejected_output:") {
			return nil, errors.New("journal resource uses a reserved host namespace")
		}
		entry := makeTool("journal_"+id, resource.Tool.Description, resource.Tool.InputSchema, func(ctx context.Context, _ string, args json.RawMessage) (json.RawMessage, error) {
			host := plugin.HostServicesFunc(func(ctx context.Context, method string, body json.RawMessage) (json.RawMessage, error) {
				if !slices.Contains(resource.Tool.RequiredCapabilities, method) {
					return nil, fmt.Errorf("resource capability %s is not declared", method)
				}
				return s.resourceCall(ctx, resource, binding, method, body)
			})
			if s.ResourcePlugin != nil {
				return s.ResourcePlugin.Runtime.Invoke(ctx, plugin.Invocation{Contribution: resource.Tool.ID, Arguments: args}, host)
			}
			var operation struct {
				Operation string `json:"operation"`
			}
			if err := json.Unmarshal(args, &operation); err != nil {
				return nil, err
			}
			if operation.Operation == "read" {
				return host.Call(ctx, "journal.read", json.RawMessage(`{}`))
			}
			return host.Call(ctx, "journal.mutate", args)
		})
		entry.ResultSchema = resource.Tool.ResultSchema
		entries = append(entries, entry)
	}
	entries = append(entries, s.AdditionalTools...)
	seen := map[string]bool{}
	for _, entry := range entries {
		if entry.Definition.Name == "" || entry.Invoke == nil {
			return nil, errors.New("invalid tool registration")
		}
		if seen[entry.Definition.Name] {
			return nil, fmt.Errorf("duplicate tool %s", entry.Definition.Name)
		}
		if len(entry.Definition.InputSchema) == 0 || len(entry.ResultSchema) == 0 {
			return nil, fmt.Errorf("tool %s requires input and result schemas", entry.Definition.Name)
		}
		for _, schema := range []json.RawMessage{entry.Definition.InputSchema, entry.ResultSchema} {
			if err := (valueschemaadapter.Validator{}).Validate(schema, nil); err != nil {
				return nil, fmt.Errorf("tool %s schema: %w", entry.Definition.Name, err)
			}
		}
		seen[entry.Definition.Name] = true
	}
	return entries, nil
}

// Validate fails before handing definitions to a provider when composition is invalid.
func (s *Session) Validate() error { _, err := s.toolCatalog(); return err }
func (s *Session) Definitions() []provider.ToolDefinition {
	entries, err := s.toolCatalog()
	if err != nil {
		return nil
	}
	result := make([]provider.ToolDefinition, 0, len(entries))
	for _, entry := range entries {
		result = append(result, entry.Definition)
	}
	return result
}
func (s *Session) Call(ctx context.Context, callID, name string, raw json.RawMessage) (json.RawMessage, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if len(raw) > 1024*1024 {
		return nil, errors.New("tool arguments exceed 1 MiB")
	}
	if !json.Valid(raw) {
		return nil, errors.New("tool arguments must be JSON")
	}
	entries, err := s.toolCatalog()
	if err != nil {
		return nil, err
	}
	for _, entry := range entries {
		if entry.Definition.Name != name {
			continue
		}
		validator := valueschemaadapter.Validator{}
		if err := validator.Validate(entry.Definition.InputSchema, raw); err != nil {
			return nil, fmt.Errorf("invalid tool arguments: %w", err)
		}
		result, err := entry.Invoke(ctx, callID, raw)
		if err != nil {
			return nil, err
		}
		if !json.Valid(result) {
			return nil, errors.New("tool returned invalid JSON")
		}
		if err := validator.Validate(entry.ResultSchema, result); err != nil {
			return nil, fmt.Errorf("invalid tool result: %w", err)
		}
		return result, nil
	}
	return nil, errors.New("tool is not connected to this node")
}

func builtinResources() []plugin.Resource {
	text := map[string]any{"type": "string"}
	resources := []plugin.Resource{
		{Kind: "open_items", CreateOperation: "add", UpdateOperations: []string{"resolve", "defer"}, Tool: plugin.Tool{ID: "darkstar/open-items"}},
		{Kind: "decision_log", CreateOperation: "record", UpdateOperations: []string{"supersede"}, Tool: plugin.Tool{ID: "darkstar/decision-log"}},
	}
	for i := range resources {
		resource := &resources[i]
		operations := append([]string{"read", resource.CreateOperation}, resource.UpdateOperations...)
		resource.Tool.Description = "Use this append-only " + resource.Kind + " journal. Use entryId for an existing item and an empty entryId to create one. key is a stable operation key, reused on retry. text records the item, decision, or resolution rationale."
		resource.Tool.InputSchema = objectSchema(map[string]any{"operation": map[string]any{"type": "string", "enum": operations}, "entryId": text, "text": text, "key": text, "expectedRevision": map[string]any{"type": "integer", "minimum": 0}}, "operation")
		resource.Tool.ResultSchema = json.RawMessage(`{"type":"object"}`)
		resource.Tool.RequiredCapabilities = []string{"journal.read", "journal.mutate"}
	}
	return resources
}

func (s *Session) resourceCall(ctx context.Context, resource plugin.Resource, binding, method string, raw json.RawMessage) (json.RawMessage, error) {
	if len(raw) > 1024*1024 || !json.Valid(raw) {
		return nil, errors.New("invalid resource arguments")
	}
	if method == "journal.read" {
		if err := (valueschemaadapter.Validator{}).Validate(objectSchema(map[string]any{}), raw); err != nil {
			return nil, err
		}
		return s.read(ctx, binding)
	}
	if method != "journal.mutate" {
		return nil, errors.New("host service is not granted")
	}
	if err := (valueschemaadapter.Validator{}).Validate(resource.Tool.InputSchema, raw); err != nil {
		return nil, err
	}
	var args struct {
		Operation        string  `json:"operation"`
		EntryID          string  `json:"entryId"`
		Text             string  `json:"text"`
		Key              string  `json:"key"`
		ExpectedRevision *uint64 `json:"expectedRevision"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return nil, err
	}
	if s.ResourcePlugin != nil && args.ExpectedRevision == nil {
		return nil, errors.New("journal mutation requires expectedRevision from the latest read")
	}
	create := args.Operation == resource.CreateOperation
	update := slices.Contains(resource.UpdateOperations, args.Operation)
	if !create && !update {
		return nil, errors.New("operation is not allowed for this journal")
	}
	if strings.TrimSpace(args.Text) == "" || strings.TrimSpace(args.Key) == "" {
		return nil, errors.New("text and a stable operation key are required")
	}
	if create {
		if args.EntryID != "" {
			return nil, errors.New("new items must not supply entryId")
		}
		args.EntryID = fmt.Sprintf("item_%x", sha256.Sum256([]byte(s.RunID+"\x00"+binding+"\x00"+args.Key)))[:29]
	} else if args.EntryID == "" {
		return nil, errors.New("entryId is required")
	}
	return s.appendEvent(ctx, binding, args.Operation, args.EntryID, args.Key, args.Text, update, args.ExpectedRevision, resource.CreateOperation)
}
