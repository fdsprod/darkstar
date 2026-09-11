package workflow

import (
	_ "embed"
	"encoding/json"
	"fmt"
)

//go:embed value_schemas.json
var valueSchemasJSON []byte

// BuiltinValueSchemas returns independent copies of versioned nominal contracts.
func BuiltinValueSchemas() map[ValueType]json.RawMessage {
	var schemas map[ValueType]json.RawMessage
	if err := json.Unmarshal(valueSchemasJSON, &schemas); err != nil {
		panic(err)
	}
	return schemas
}

func ValueSchema(kind ValueType, explicit json.RawMessage) json.RawMessage {
	if builtin := BuiltinValueSchemas()[kind]; builtin != nil {
		return builtin
	}
	return explicit
}

// ValidateClosedValueSchema rejects unnamed nested shapes as well as unnamed
// ports. Typed dictionaries remain supported through an additionalProperties schema.
func ValidateClosedValueSchema(value any) error {
	schema, ok := value.(map[string]any)
	if !ok || len(schema) == 0 {
		return fmt.Errorf("nominal types require an explicit schema")
	}
	if schema["type"] == nil && schema["enum"] == nil && schema["const"] == nil && schema["$ref"] == nil && schema["oneOf"] == nil && schema["anyOf"] == nil && schema["allOf"] == nil {
		return fmt.Errorf("every schema must define a value type, finite value set, reference, or typed union")
	}
	if schema["type"] == "object" {
		additional, exists := schema["additionalProperties"]
		if !exists || additional == true {
			return fmt.Errorf("object schemas must define their fields or a typed dictionary")
		}
		if additional != false {
			if err := ValidateClosedValueSchema(additional); err != nil {
				return err
			}
		}
	}
	if schema["type"] == "array" {
		if err := ValidateClosedValueSchema(schema["items"]); err != nil {
			return fmt.Errorf("array items: %w", err)
		}
	}
	for _, key := range []string{"properties", "$defs"} {
		children, _ := schema[key].(map[string]any)
		for _, child := range children {
			if err := ValidateClosedValueSchema(child); err != nil {
				return err
			}
		}
	}
	for _, key := range []string{"oneOf", "anyOf", "allOf"} {
		children, _ := schema[key].([]any)
		for _, child := range children {
			if err := ValidateClosedValueSchema(child); err != nil {
				return err
			}
		}
	}
	return nil
}
