package workflow

import (
	"errors"
	"strings"
	"testing"
)

func TestCanonicalizeSortsNestedLiteralKeysAndRejectsDuplicates(t *testing.T) {
	t.Parallel()
	content := strings.Replace(validCatalogWorkflow(), `"default":null`, `"default":{"z":1,"a":2}`, 1)
	_, canonical, digest, err := Canonicalize([]byte(content))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(canonical), `"default":{"a":2,"z":1}`) {
		t.Fatalf("canonical JSON did not sort nested literal keys: %s", canonical)
	}
	if len(digest) != 64 {
		t.Fatalf("digest length = %d, want 64", len(digest))
	}

	duplicate := strings.Replace(validCatalogWorkflow(), `"name":"catalog-test"`, `"name":"catalog-test","name":"duplicate"`, 1)
	if _, _, _, err := Canonicalize([]byte(duplicate)); err == nil || !strings.Contains(err.Error(), "duplicate JSON object key") {
		t.Fatalf("duplicate key error = %v", err)
	}
}

func TestSemanticVersionOrderingUsesNumericAndPrereleasePrecedence(t *testing.T) {
	t.Parallel()
	for _, test := range []struct{ older, newer string }{
		{"2.0.0", "10.0.0"},
		{"1.0.0-alpha.2", "1.0.0-alpha.10"},
		{"1.0.0-rc.1", "1.0.0"},
	} {
		if !semanticVersionLess(test.older, test.newer) || semanticVersionLess(test.newer, test.older) {
			t.Errorf("semantic order %s < %s was not preserved", test.older, test.newer)
		}
	}
}

func validCatalogWorkflow() string {
	return `{"apiVersion":"darkstar.local/v1alpha1","kind":"Workflow","metadata":{"name":"catalog-test","version":"1.0.0"},"spec":{"inputs":{"request":{"type":"object"}},"routeDefaults":{"entry":"finish","terminals":["finish"]},"nodes":{"finish":{"type":"reasoning","entry":true,"terminal":true,"inputs":{"request":{"from":"run.input.request","type":"object","required":false,"default":null}},"outputs":{},"reasoning":{"agent":"fake"},"checkpoint":{"mode":"none"},"transitions":[]}}}}`
}

func TestAuthoringFindingsUseExplicitManualValidationLocations(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		document  string
		location  string
		nodeID    Identifier
		fieldName string
	}{
		{name: "route default entry", document: strings.Replace(validCatalogWorkflow(), `"entry":"finish"`, `"entry":"bad-id"`, 1), location: "/spec/routeDefaults/entry"},
		{name: "binding source", document: strings.Replace(validCatalogWorkflow(), `"from":"run.input.request"`, `"from":"invalid"`, 1), location: "/spec/nodes/finish/inputs/request/from", nodeID: "finish", fieldName: "inputs.request.from"},
		{name: "output type", document: strings.Replace(validCatalogWorkflow(), `"outputs":{}`, `"outputs":{"result":{"type":"future"}}`, 1), location: "/spec/nodes/finish/outputs/result/type", nodeID: "finish", fieldName: "outputs.result.type"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := Decode([]byte(test.document))
			var decoded *DecodeError
			if !errors.As(err, &decoded) {
				t.Fatalf("Decode() error = %v, want DecodeError", err)
			}
			if decoded.Location != test.location {
				t.Fatalf("location = %q, want %q", decoded.Location, test.location)
			}
			finding := authoringFinding(ValidationError{Code: ValidationSchemaInvalid, Message: err.Error(), Location: decoded.Location}, []byte(test.document))
			if finding.NodeID != test.nodeID || finding.Field != test.fieldName {
				t.Fatalf("finding target = node %q field %q, want node %q field %q", finding.NodeID, finding.Field, test.nodeID, test.fieldName)
			}
		})
	}
}
