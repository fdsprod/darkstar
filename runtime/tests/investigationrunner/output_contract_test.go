package investigationrunner_test

import (
	"encoding/json"
	"testing"

	"darkstar/src/adapters/valueschema/jsonschema"
	"darkstar/src/core/investigationrunner"
)

func TestPublishedInvestigationOutputSchemasRejectMissingNullAndMixedVariants(t *testing.T) {
	validator := jsonschema.Validator{}
	repository := json.RawMessage(`{"schemaVersion":1,"repositoryId":"repo_one","commitSha":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","quality":"missing","summary":"No source evidence available.","codeReferences":[],"affectedInterfaces":[],"reusablePatterns":[],"constraints":[],"risks":[],"unresolvedQuestions":[],"limitations":["Evidence unavailable"]}`)
	synthesis := json.RawMessage(`{"schemaVersion":1,"quality":"missing","evidenceStatus":"no_repository_evidence","summary":"No repositories selected.","findings":[],"missing":[],"crossRepositoryImplications":[],"constraints":[],"risks":[],"unresolvedQuestions":[],"limitations":["No repositories selected"]}`)
	for kind, valid := range map[string]json.RawMessage{"repository": repository, "synthesis": synthesis} {
		t.Run(kind, func(t *testing.T) {
			schema := investigationrunner.OutputSchema(kind)
			if err := validator.Validate(schema, valid); err != nil {
				t.Fatal(err)
			}
			var value map[string]json.RawMessage
			if err := json.Unmarshal(valid, &value); err != nil {
				t.Fatal(err)
			}
			for key := range value {
				clone := map[string]json.RawMessage{}
				for field, content := range value {
					clone[field] = content
				}
				delete(clone, key)
				raw, _ := json.Marshal(clone)
				if err := validator.Validate(schema, raw); err == nil {
					t.Errorf("omitted required field %s accepted", key)
				}
				clone[key] = json.RawMessage("null")
				raw, _ = json.Marshal(clone)
				if err := validator.Validate(schema, raw); err == nil {
					t.Errorf("null required field %s accepted", key)
				}
			}
			value["runId"] = json.RawMessage(`"fake-run"`)
			raw, _ := json.Marshal(value)
			if err := validator.Validate(schema, raw); err == nil {
				t.Fatal("unknown authority field accepted")
			}
		})
	}
	if validator.Validate(investigationrunner.OutputSchema("repository"), synthesis) == nil || validator.Validate(investigationrunner.OutputSchema("synthesis"), repository) == nil {
		t.Fatal("output variants were interchangeable")
	}
}
