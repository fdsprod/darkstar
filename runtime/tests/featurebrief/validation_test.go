package featurebrief_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"darkstar/src/adapters/valueschema/jsonschema"
	"darkstar/src/core/featurebrief"
)

func fixture(t *testing.T) map[string]any {
	t.Helper()
	content, err := os.ReadFile(filepath.Join("..", "..", "..", "examples", "feature-planning", "brief.json"))
	if err != nil {
		t.Fatal(err)
	}
	var value map[string]any
	if err := json.Unmarshal(content, &value); err != nil {
		t.Fatal(err)
	}
	return value
}

func encode(t *testing.T, value any) json.RawMessage {
	t.Helper()
	content, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return content
}

func TestBriefRejectsInvalidContractsAndCatalogReferences(t *testing.T) {
	v := featurebrief.Validator{Schema: jsonschema.Validator{}, Load: func(context.Context, featurebrief.Reference) (json.RawMessage, error) {
		return nil, errors.New("unexpected artifact load")
	}}
	if err := v.Validate(t.Context(), "project-factory", encode(t, fixture(t))); err != nil {
		t.Fatal(err)
	}
	cases := map[string]func(map[string]any){
		"unknown field": func(value map[string]any) {
			value["instructionAuthority"] = "system"
		},
		"project mismatch": func(value map[string]any) {
			value["projectId"] = "another-project"
		},
		"duplicate requirement": func(value map[string]any) {
			items := value["requirements"].([]any)
			value["requirements"] = append(items, items[0])
		},
		"missing evidence": func(value map[string]any) {
			value["requirements"].([]any)[0].(map[string]any)["evidenceKeys"] = []string{"absent"}
		},
		"duplicate repository": func(value map[string]any) {
			items := value["repositories"].([]any)
			value["repositories"] = append(items, items[0])
		},
		"invalid resolved decision": func(value map[string]any) {
			value["decisions"].([]any)[0].(map[string]any)["state"] = map[string]any{"kind": "resolved", "resolution": "chosen", "evidenceKeys": []string{"absent"}}
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			value := fixture(t)
			mutate(value)
			if err := v.Validate(t.Context(), "project-factory", encode(t, value)); err == nil {
				t.Fatal("invalid feature brief was accepted")
			}
		})
	}
}

func TestBriefRevisionRequiresExactValidatedPredecessor(t *testing.T) {
	original := encode(t, fixture(t))
	digest := sha256.Sum256(original)
	reference := featurebrief.Reference{ArtifactID: "artifact_original", Version: 1, SHA256: hex.EncodeToString(digest[:])}
	value := fixture(t)
	value["lineage"] = map[string]any{"kind": "revision", "previous": reference}
	v := featurebrief.Validator{Schema: jsonschema.Validator{}, Load: func(_ context.Context, requested featurebrief.Reference) (json.RawMessage, error) {
		if requested != reference {
			t.Fatal("loaded a floating or different artifact version")
		}
		return original, nil
	}}
	if err := v.Validate(t.Context(), "project-factory", encode(t, value)); err != nil {
		t.Fatal(err)
	}
	value["featureKey"] = "DIFFERENT"
	if err := v.Validate(t.Context(), "project-factory", encode(t, value)); err == nil {
		t.Fatal("revision changed feature identity")
	}
	value["featureKey"] = "FEATURE-1"
	v.Load = func(context.Context, featurebrief.Reference) (json.RawMessage, error) {
		return append(original, ' '), nil
	}
	if err := v.Validate(t.Context(), "project-factory", encode(t, value)); err == nil {
		t.Fatal("changed predecessor bytes were accepted")
	}
}

func TestEmbeddedPlanningSchemasMatchCanonicalContracts(t *testing.T) {
	for _, name := range []string{"feature-planning-v1alpha1.schema.json", "planning-artifact-v1alpha1.schema.json"} {
		canonical, err := os.ReadFile(filepath.Join("..", "..", "..", "schemas", name))
		if err != nil {
			t.Fatal(err)
		}
		bundled, err := os.ReadFile(filepath.Join("..", "..", "src", "core", "featurebrief", name))
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(bytes.ReplaceAll(canonical, []byte("\r\n"), []byte("\n")), bytes.ReplaceAll(bundled, []byte("\r\n"), []byte("\n"))) {
			t.Fatalf("refresh runtime featurebrief copy of %s from canonical schema", name)
		}
	}
}
