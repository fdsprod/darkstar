package workflow

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestResourceUnionRejectsContradictorySources(t *testing.T) {
	for _, raw := range []string{`{"kind":"task","key":"other"}`, `{"kind":"template","content":"Design"}`, `{"kind":"constant"}`, `{"kind":"unknown"}`} {
		var resource Resource
		if json.Unmarshal([]byte(raw), &resource) == nil {
			t.Fatalf("accepted %s", raw)
		}
	}
	var resource Resource
	if err := json.Unmarshal([]byte(`{"kind":"template","version":"1.0.0","content":"# Design","requiredHeadings":["Approach"]}`), &resource); err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(resource)
	if err != nil || !strings.Contains(string(encoded), `"kind":"template"`) {
		t.Fatalf("%s %v", encoded, err)
	}
}

func TestDeliverablesRespectIndependentTemplatesAndSchemas(t *testing.T) {
	node := ReasoningNode{Common: NodeFields{Outputs: map[Identifier]OutputDeclaration{
		"design":     {Type: ValueString, Artifact: &ArtifactContract{Filename: "design.md", TemplateInput: "template"}},
		"assessment": {Type: ValueObject, SchemaDefinition: json.RawMessage(`{"type":"object","required":["ready"],"properties":{"ready":{"type":"boolean"}},"additionalProperties":false}`)},
	}}}
	inputs := map[Identifier]json.RawMessage{"template": json.RawMessage(`{"version":"1.0.0","content":"# Design","requiredHeadings":["Approach"]}`)}
	if err := ValidateDeliverable(node, "design", json.RawMessage(`"# Design\n## Approach\nUse typed inputs."`), inputs); err != nil {
		t.Fatal(err)
	}
	for id, value := range map[Identifier]string{"design": `"# Design"`, "assessment": `{"ready":"yes"}`, "undeclared": `"text"`} {
		if ValidateDeliverable(node, id, json.RawMessage(value), inputs) == nil {
			t.Fatalf("accepted %s", id)
		}
	}
	if ValidateArtifact(ArtifactContract{Filename: "../design.md"}, "content", nil) == nil {
		t.Fatal("path escape accepted")
	}
}

func TestNominalTypesHaveDistinctConnectionsAndValidStorage(t *testing.T) {
	types := []ValueType{ValueTask, ValueRepository, ValueTemplate, ValueMarkdown, ValueOpenItems, ValueDecisionLog}
	for _, source := range types {
		raw := json.RawMessage(`{}`)
		if source == ValueMarkdown {
			raw = json.RawMessage(`"# Design"`)
		}
		if !rawMessageHasType(raw, source) || !literalMatchesType(raw, source) {
			t.Fatalf("valid %s storage rejected", source)
		}
		for _, target := range types {
			if typesCompatible(source, target) != (source == target) {
				t.Fatalf("incorrect connection %s to %s", source, target)
			}
		}
		if typesCompatible(source, source.StorageType()) {
			t.Fatalf("nominal %s silently converts to %s", source, source.StorageType())
		}
	}
}
