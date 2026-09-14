package workflowtools

import (
	"encoding/json"

	"darkstar/src/core/workflow"
)

// ProjectInput exposes useful document coordinates without daemon record IDs or
// authorization snapshots. The caller retains the original canonical input.
func ProjectInput(kind workflow.ValueType, value json.RawMessage) (json.RawMessage, error) {
	var fields []string
	switch kind {
	case workflow.ValueRepository:
		fields = []string{"name"}
	case workflow.ValueWorkspace:
		fields = []string{"path", "branch", "headSha", "baseSha", "baseRef", "mode"}
	default:
		return value, nil
	}
	var canonical map[string]json.RawMessage
	if err := json.Unmarshal(value, &canonical); err != nil {
		return nil, err
	}
	projection := make(map[string]json.RawMessage)
	for _, field := range fields {
		if value, exists := canonical[field]; exists {
			projection[field] = value
		}
	}
	return json.Marshal(projection)
}
