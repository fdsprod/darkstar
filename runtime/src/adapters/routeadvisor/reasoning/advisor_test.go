package reasoning

import (
	valueschema "darkstar/src/adapters/valueschema/jsonschema"
	"darkstar/src/ports/routeadvisor"
	"encoding/json"
	"testing"
)

func TestAdviceSchemaRejectsInventedEvidence(t *testing.T) {
	for _, evidence := range [][]routeadvisor.Evidence{nil, {{Reference: "artifact:design", Content: "design", Digest: "abc"}, {Reference: "unresolved"}}} {
		schema := adviceOutputSchema(routeadvisor.Request{Evidence: evidence})
		validator := valueschema.Validator{}
		for _, ref := range []string{"Supplied outcome: update README", "unresolved"} {
			value, _ := json.Marshal(map[string]any{"confidence": "high", "candidates": []any{}, "evidenceUsed": []string{ref}})
			if validator.Validate(schema, value) == nil {
				t.Fatalf("accepted invented evidence %q", ref)
			}
		}
		refs := []string{}
		if len(evidence) > 0 {
			refs = []string{"artifact:design"}
		}
		value, _ := json.Marshal(map[string]any{"confidence": "high", "candidates": []any{}, "evidenceUsed": refs})
		if err := validator.Validate(schema, value); err != nil {
			t.Fatal(err)
		}
	}
}
