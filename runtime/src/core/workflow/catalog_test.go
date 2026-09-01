package workflow

import (
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

func validCatalogWorkflow() string {
	return `{"apiVersion":"darkstar.local/v1alpha1","kind":"Workflow","metadata":{"name":"catalog-test","version":"1.0.0"},"spec":{"inputs":{"request":{"type":"object"}},"routeDefaults":{"entry":"finish","terminals":["finish"]},"nodes":{"finish":{"type":"reasoning","entry":true,"terminal":true,"inputs":{"request":{"from":"run.input.request","type":"object","required":false,"default":null}},"outputs":{},"reasoning":{"agent":"fake"},"checkpoint":{"mode":"none"},"transitions":[]}}}}`
}
