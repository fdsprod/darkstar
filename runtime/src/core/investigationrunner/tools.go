package investigationrunner

import (
	"darkstar/src/ports/provider"
	_ "embed"
	"encoding/json"
	"sort"
)

//go:embed output_schemas.json
var outputSchemas []byte

// OutputSchema returns the published, self-contained result contract.
func OutputSchema(kind string) json.RawMessage {
	var schemas map[string]json.RawMessage
	if json.Unmarshal(outputSchemas, &schemas) != nil {
		return json.RawMessage("false")
	}
	value, ok := schemas[kind]
	if !ok {
		return json.RawMessage("false")
	}
	return append(json.RawMessage{}, value...)
}

func (t *tools) definitions() []provider.ToolDefinition {
	schema := OutputSchema(t.execution.Kind)
	text := map[string]any{"type": "string", "minLength": 1}
	result := []provider.ToolDefinition{{Type: "function", Name: "submit_output", Description: "Submit the final structured investigation exactly once. The daemon validates citations and persists the immutable artifact before acknowledging success.", InputSchema: schema}}
	if t.execution.Kind == "repository" {
		schema, _ := json.Marshal(objectSchema(map[string]any{"path": text}))
		result = append(result, provider.ToolDefinition{Type: "function", Name: "read_repository_file", Description: "Read one exact committed file from the assigned immutable repository manifest. No other filesystem paths are granted.", InputSchema: schema})
	}
	return result
}

func objectSchema(properties map[string]any) map[string]any {
	required := make([]string, 0, len(properties))
	for key := range properties {
		required = append(required, key)
	}
	// Schema bytes participate in context identity; map iteration must not.
	sort.Strings(required)
	return map[string]any{"type": "object", "additionalProperties": false, "properties": properties, "required": required}
}
