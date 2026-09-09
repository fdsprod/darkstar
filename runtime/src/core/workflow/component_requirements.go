package workflow

import (
	_ "embed"
	"encoding/json"
	"fmt"
)

//go:embed component_requirements.json
var componentRequirementsJSON []byte

type ComponentConnection struct {
	Direction      string     `json:"direction"`
	Field          string     `json:"field,omitempty"`
	ID             Identifier `json:"id,omitempty"`
	Type           ValueType  `json:"type"`
	LegacyOptional bool       `json:"legacyOptional,omitempty"`
}

// Legacy installed versions remain executable. Draft authoring must make the
// workspace explicit before a new version can be published.
func draftConnectionIssues(raw json.RawMessage) ValidationErrors {
	doc, err := Decode(raw)
	if err != nil {
		return nil
	}
	var issues ValidationErrors
	for id, node := range doc.Spec.Nodes {
		if n, ok := node.(ImplementationNode); ok && n.Executor.WorkspaceInput == "" {
			issues = append(issues, ValidationError{Code: ValidationReferenceMissing, Message: "Connect Prepare workspace to Implementation and set workspaceInput before publishing this draft", Location: fmt.Sprintf("/spec/nodes/%s/implementation/workspaceInput", id)})
		}
	}
	sortValidationErrors(issues)
	return issues
}

type ComponentRequirement struct {
	Instructions string                `json:"instructions"`
	Connections  []ComponentConnection `json:"connections"`
}

// ComponentRequirements is also consumed directly by the dashboard; it is not
// a second independently maintained list of required connector types.
func ComponentRequirements() map[NodeType]ComponentRequirement {
	var result map[NodeType]ComponentRequirement
	if err := json.Unmarshal(componentRequirementsJSON, &result); err != nil {
		panic(err)
	}
	return result
}
