package planningartifacts_test

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	jsonschema "github.com/santhosh-tekuri/jsonschema/v6"
)

func TestPlanningArtifactSchemaAcceptsEveryClosedVariant(t *testing.T) {
	t.Parallel()

	content, err := os.ReadFile(filepath.Join(repositoryRoot(t), "schemas", "planning-artifact-v1alpha1.schema.json"))
	if err != nil {
		t.Fatal(err)
	}
	document, err := jsonschema.UnmarshalJSON(bytes.NewReader(content))
	if err != nil {
		t.Fatal(err)
	}
	compiler := jsonschema.NewCompiler()
	if err := compiler.AddResource("planning-artifact.json", document); err != nil {
		t.Fatal(err)
	}
	compiled, err := compiler.Compile("planning-artifact.json")
	if err != nil {
		t.Fatal(err)
	}

	root := document.(map[string]any)
	definitions := root["$defs"].(map[string]any)
	for _, variant := range root["oneOf"].([]any) {
		reference := variant.(map[string]any)["$ref"].(string)
		definition := resolvePlanningDefinition(t, definitions, reference)
		instance := planningExample(t, definitions, definition)
		artifactType := instance["artifactType"].(string)
		t.Run(artifactType, func(t *testing.T) {
			if err := compiled.Validate(instance); err != nil {
				t.Fatalf("valid %s example: %v", artifactType, err)
			}
			instance["unexpectedSiblingField"] = "not permitted"
			if err := compiled.Validate(instance); err == nil {
				t.Fatalf("%s accepted an unrelated sibling field", artifactType)
			}
		})
	}
}

func planningExample(t *testing.T, definitions map[string]any, schema map[string]any) map[string]any {
	t.Helper()
	properties := schema["properties"].(map[string]any)
	instance := make(map[string]any)
	for property, value := range properties {
		propertySchema := value.(map[string]any)
		if _, exists := propertySchema["const"]; exists {
			instance[property] = planningValue(t, definitions, propertySchema)
		}
	}
	for _, name := range schema["required"].([]any) {
		property := name.(string)
		instance[property] = planningValue(t, definitions, properties[property].(map[string]any))
	}
	return instance
}

func planningValue(t *testing.T, definitions map[string]any, schema map[string]any) any {
	t.Helper()
	if reference, ok := schema["$ref"].(string); ok {
		return planningValue(t, definitions, resolvePlanningDefinition(t, definitions, reference))
	}
	if value, ok := schema["const"]; ok {
		return value
	}
	if values, ok := schema["enum"].([]any); ok {
		return values[0]
	}
	switch schema["type"] {
	case "string":
		return "value"
	case "integer", "number":
		return float64(1)
	case "boolean":
		return true
	case "array":
		return []any{planningValue(t, definitions, schema["items"].(map[string]any))}
	case "object":
		return planningExample(t, definitions, schema)
	default:
		t.Fatalf("cannot create example for schema %#v", schema)
		return nil
	}
}

func resolvePlanningDefinition(t *testing.T, definitions map[string]any, reference string) map[string]any {
	t.Helper()
	const prefix = "#/$defs/"
	if len(reference) <= len(prefix) || reference[:len(prefix)] != prefix {
		t.Fatalf("unsupported reference %q", reference)
	}
	definition, ok := definitions[reference[len(prefix):]].(map[string]any)
	if !ok {
		t.Fatalf("missing definition %q", reference)
	}
	return definition
}

func repositoryRoot(t *testing.T) string {
	t.Helper()
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve test filename")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(filename), "..", "..", ".."))
}
