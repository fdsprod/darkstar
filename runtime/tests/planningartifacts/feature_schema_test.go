package planningartifacts_test

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	jsonschema "github.com/santhosh-tekuri/jsonschema/v6"
)

func featureSchema(t *testing.T) *jsonschema.Schema {
	t.Helper()
	content, err := os.ReadFile(filepath.Join(repositoryRoot(t), "schemas", "feature-planning-v1alpha1.schema.json"))
	if err != nil {
		t.Fatal(err)
	}
	document, err := jsonschema.UnmarshalJSON(bytes.NewReader(content))
	if err != nil {
		t.Fatal(err)
	}
	compiler := jsonschema.NewCompiler()
	compiler.AssertFormat()
	if err := compiler.AddResource("feature-planning.json", document); err != nil {
		t.Fatal(err)
	}
	compiled, err := compiler.Compile("feature-planning.json")
	if err != nil {
		t.Fatal(err)
	}
	return compiled
}

func featureExample(t *testing.T, name string) map[string]any {
	t.Helper()
	content, err := os.ReadFile(filepath.Join(repositoryRoot(t), "examples", "feature-planning", name+".json"))
	if err != nil {
		t.Fatal(err)
	}
	value, err := jsonschema.UnmarshalJSON(bytes.NewReader(content))
	if err != nil {
		t.Fatal(err)
	}
	return value.(map[string]any)
}

func firstFeatureStory(value map[string]any) map[string]any {
	return value["stories"].([]any)[0].(map[string]any)
}

func TestFeaturePlanningSchemaExamplesAndClosedVariants(t *testing.T) {
	t.Parallel()
	schema := featureSchema(t)
	for _, name := range []string{"brief", "backlog", "links", "handoff"} {
		t.Run(name, func(t *testing.T) {
			value := featureExample(t, name)
			if err := schema.Validate(value); err != nil {
				t.Fatal(err)
			}
			value["unexpected"] = true
			if schema.Validate(value) == nil {
				t.Fatal("unknown root field accepted")
			}
		})
	}
	for _, impact := range []map[string]any{
		{"kind": "unknown", "reason": "Investigation pending"},
		{"kind": "none", "rationale": "No repository changes"},
		{"kind": "repositories", "repositoryIds": []any{"repo-runtime"}},
	} {
		value := featureExample(t, "backlog")
		firstFeatureStory(value)["repositoryImpact"] = impact
		if err := schema.Validate(value); err != nil {
			t.Fatal(err)
		}
	}
	for _, lifecycle := range []map[string]any{
		{"kind": "active"},
		{"kind": "retired", "reason": "No longer needed"},
		{"kind": "split", "reason": "Separate outcomes", "replacementKeys": []any{"STORY-3", "STORY-4"}},
	} {
		value := featureExample(t, "backlog")
		firstFeatureStory(value)["lifecycle"] = lifecycle
		if err := schema.Validate(value); err != nil {
			t.Fatal(err)
		}
	}
	for _, milestone := range []map[string]any{
		{"kind": "tests_passed", "repositoryId": "repo-runtime", "commit": "abc", "validationProfile": "unit"},
		{"kind": "implementation_validated", "repositoryId": "repo-runtime", "commit": "abc", "validationProfile": "required"},
		{"kind": "pull_request_created", "repositoryId": "repo-runtime", "commit": "abc", "pullRequestId": "pr-1"},
		{"kind": "pull_request_accepted", "repositoryId": "repo-runtime", "commit": "abc", "pullRequestId": "pr-1"},
		{"kind": "deployed", "environment": "staging", "releaseId": "release-1", "commit": "abc"},
	} {
		value := featureExample(t, "handoff")
		value["milestone"] = milestone
		value["authority"] = map[string]any{"kind": "external", "connectorId": "ci", "accountId": "account", "nativeObservationId": "check-1", "nativeRevision": "v1"}
		if err := schema.Validate(value); err != nil {
			t.Fatal(err)
		}
	}
	value := featureExample(t, "links")
	value["destination"] = map[string]any{"kind": "github_issues", "connectorId": "github", "accountId": "account", "host": "github.com", "repositoryId": "native-repo-id"}
	if err := schema.Validate(value); err != nil {
		t.Fatal(err)
	}
	for _, state := range []map[string]any{
		{"kind": "open", "blocking": true},
		{"kind": "resolved", "resolution": "Use the selected destination", "evidenceKeys": []any{"E-1"}},
		{"kind": "withdrawn", "reason": "No longer in scope"},
	} {
		value := featureExample(t, "brief")
		value["decisions"].([]any)[0].(map[string]any)["state"] = state
		if err := schema.Validate(value); err != nil {
			t.Fatal(err)
		}
	}
	for _, lineage := range []map[string]any{
		{"kind": "revision", "previous": featureExample(t, "backlog")["brief"]},
		{"kind": "migration", "source": featureExample(t, "backlog")["brief"], "sourceSchema": "planning-artifact-v1alpha1", "keyMappings": []any{}, "notes": []any{"Retain original evidence"}},
	} {
		value := featureExample(t, "brief")
		value["lineage"] = lineage
		if err := schema.Validate(value); err != nil {
			t.Fatal(err)
		}
	}
}

func TestFeaturePlanningSchemaRejectsContradictoryAndUnknownFields(t *testing.T) {
	t.Parallel()
	schema := featureSchema(t)
	cases := []struct {
		name   string
		file   string
		mutate func(map[string]any)
	}{
		{"unknown impact", "backlog", func(value map[string]any) {
			firstFeatureStory(value)["repositoryImpact"] = map[string]any{"kind": "maybe"}
		}},
		{"none with repositories", "backlog", func(value map[string]any) {
			firstFeatureStory(value)["repositoryImpact"] = map[string]any{"kind": "none", "rationale": "None", "repositoryIds": []any{"repo"}}
		}},
		{"empty selected repositories", "backlog", func(value map[string]any) {
			firstFeatureStory(value)["repositoryImpact"] = map[string]any{"kind": "repositories", "repositoryIds": []any{}}
		}},
		{"unknown without reason", "backlog", func(value map[string]any) {
			firstFeatureStory(value)["repositoryImpact"] = map[string]any{"kind": "unknown"}
		}},
		{"external identifier on story", "backlog", func(value map[string]any) {
			firstFeatureStory(value)["linearId"] = "DAR-1"
		}},
		{"active with replacements", "backlog", func(value map[string]any) {
			firstFeatureStory(value)["lifecycle"] = map[string]any{"kind": "active", "replacementKeys": []any{"STORY-2", "STORY-3"}}
		}},
		{"split with one replacement", "backlog", func(value map[string]any) {
			firstFeatureStory(value)["lifecycle"] = map[string]any{"kind": "split", "reason": "Split", "replacementKeys": []any{"STORY-2"}}
		}},
		{"unknown acceptance field", "brief", func(value map[string]any) {
			value["requirements"].([]any)[0].(map[string]any)["passed"] = true
		}},
		{"mixed destination", "links", func(value map[string]any) {
			value["destination"].(map[string]any)["repositoryId"] = "repo"
		}},
		{"generic completion", "handoff", func(value map[string]any) {
			value["milestone"] = map[string]any{"kind": "completed"}
		}},
		{"handoff missing evidence", "handoff", func(value map[string]any) {
			value["evidence"] = []any{}
		}},
		{"approval asserts deployment", "handoff", func(value map[string]any) {
			value["milestone"].(map[string]any)["environment"] = "production"
		}},
		{"invalid timestamp", "handoff", func(value map[string]any) {
			value["observedAt"] = "yesterday"
		}},
		{"unknown reference field", "backlog", func(value map[string]any) {
			value["brief"].(map[string]any)["latest"] = true
		}},
		{"unversioned reference", "backlog", func(value map[string]any) {
			value["brief"].(map[string]any)["version"] = 0
		}},
		{"initial with predecessor", "brief", func(value map[string]any) {
			value["lineage"].(map[string]any)["previous"] = featureExample(t, "backlog")["brief"]
		}},
		{"resolved with blocking flag", "brief", func(value map[string]any) {
			value["decisions"].([]any)[0].(map[string]any)["state"] = map[string]any{"kind": "resolved", "resolution": "Decided", "evidenceKeys": []any{"E-1"}, "blocking": true}
		}},
		{"resolved without evidence", "brief", func(value map[string]any) {
			value["decisions"].([]any)[0].(map[string]any)["state"] = map[string]any{"kind": "resolved", "resolution": "Decided", "evidenceKeys": []any{}}
		}},
	}
	for _, entry := range cases {
		t.Run(entry.name, func(t *testing.T) {
			value := featureExample(t, entry.file)
			entry.mutate(value)
			if schema.Validate(value) == nil {
				t.Fatal("invalid value accepted")
			}
		})
	}
}
