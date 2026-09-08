package jsonschema

import (
	"encoding/json"
	"testing"
)

func TestSchemaValidationFailsClosed(t *testing.T) {
	validator := Validator{}
	schema := json.RawMessage(`{"type":"object","required":["ready"],"properties":{"ready":{"type":"boolean"}},"additionalProperties":false}`)
	if err := validator.Validate(schema, json.RawMessage(`{"ready":true}`)); err != nil {
		t.Fatal(err)
	}
	for _, value := range []string{`{}`, `{"ready":"yes"}`, `{"ready":true,"extra":1}`} {
		if validator.Validate(schema, json.RawMessage(value)) == nil {
			t.Fatalf("accepted %s", value)
		}
	}
	if validator.Validate(json.RawMessage(`{"type":"unknown"}`), nil) == nil {
		t.Fatal("invalid schema compiled")
	}
}
