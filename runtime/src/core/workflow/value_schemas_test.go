package workflow

import (
	"encoding/json"
	"strings"
	"testing"
)

const nominalDraft = `{"apiVersion":"darkstar.local/v1alpha3","kind":"Workflow","metadata":{"name":"example/typed","version":"1.0.0"},"spec":{"routeDefaults":{"entry":"text","terminals":["text"]},"nodes":{"text":{"type":"reasoning","entry":true,"terminal":true,"reasoning":{"agent":"writer","instructions":"Describe the connected changes."},"inputs":{},"outputs":{"text":{"type":"schema:delivery_text_v1"}},"transitions":[]}}}}`

func TestDraftRequiresKnownConsistentNominalTypes(t *testing.T) {
	if _, err := Decode(json.RawMessage(nominalDraft)); err != nil {
		t.Fatal(err)
	}
	if issues := draftConnectionIssues(json.RawMessage(nominalDraft)); len(issues) != 0 {
		t.Fatal(issues)
	}
	for _, kind := range []string{"object", "array", "schema:unknown_v1"} {
		raw := strings.Replace(nominalDraft, "schema:delivery_text_v1", kind, 1)
		if len(draftConnectionIssues(json.RawMessage(raw))) == 0 {
			t.Fatalf("accepted unregistered type %s", kind)
		}
	}
	conflicting := strings.Replace(nominalDraft, `"type":"schema:delivery_text_v1"`, `"type":"schema:delivery_text_v1","schemaDefinition":{"type":"object"}`, 1)
	if len(draftConnectionIssues(json.RawMessage(conflicting))) == 0 {
		t.Fatal("accepted replacement schema for built-in identity")
	}
}

func TestBuiltinSchemaRegistryReturnsCopies(t *testing.T) {
	schemas := BuiltinValueSchemas()
	schemas["schema:changeset_v1"][0] = 'x'
	if !json.Valid(ValueSchema("schema:changeset_v1", nil)) {
		t.Fatal("caller mutated global schema")
	}
}
