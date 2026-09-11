package workflow

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
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
	contracts := map[ValueType]any{}
	for kind, schema := range BuiltinValueSchemas() {
		var value any
		_ = json.Unmarshal(schema, &value)
		contracts[kind] = value
	}
	declare := func(kind ValueType, schema json.RawMessage, location string) {
		if !strings.HasPrefix(string(kind), "schema:") {
			return
		}
		if len(schema) == 0 {
			return
		}
		var value any
		if err := json.Unmarshal(schema, &value); err != nil {
			return
		}
		if err := ValidateClosedValueSchema(value); err != nil {
			issues = append(issues, ValidationError{Code: ValidationSchemaInvalid, Message: err.Error(), Location: location})
			return
		}
		if previous, exists := contracts[kind]; exists && !reflect.DeepEqual(previous, value) {
			issues = append(issues, ValidationError{Code: ValidationSchemaInvalid, Message: "A nominal type must have one consistent schema; publish a new type version to change it", Location: location})
			return
		}
		contracts[kind] = value
	}
	for id, input := range doc.Spec.Inputs {
		declare(input.Type, input.SchemaDefinition, fmt.Sprintf("/spec/inputs/%s/schemaDefinition", id))
	}
	for id, node := range doc.Spec.Nodes {
		for name, output := range node.Fields().Outputs {
			declare(output.Type, output.SchemaDefinition, fmt.Sprintf("/spec/nodes/%s/outputs/%s/schemaDefinition", id, name))
		}
	}
	check := func(kind ValueType, location string) {
		if kind == ValueObject || kind == ValueArray {
			issues = append(issues, ValidationError{Code: ValidationSchemaInvalid, Message: "Choose a known nominal type with a defined schema; untyped object and array ports cannot be published", Location: location})
		}
		if strings.HasPrefix(string(kind), "schema:") && contracts[kind] == nil {
			issues = append(issues, ValidationError{Code: ValidationSchemaInvalid, Message: "Unknown nominal type: provide its schema definition or use a registered built-in type", Location: location})
		}
	}
	for id, input := range doc.Spec.Inputs {
		check(input.Type, fmt.Sprintf("/spec/inputs/%s/type", id))
	}
	for id, node := range doc.Spec.Nodes {
		for name, input := range node.Fields().Inputs {
			check(input.ValueType(), fmt.Sprintf("/spec/nodes/%s/inputs/%s/type", id, name))
		}
		for name, output := range node.Fields().Outputs {
			check(output.Type, fmt.Sprintf("/spec/nodes/%s/outputs/%s/type", id, name))
		}
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
