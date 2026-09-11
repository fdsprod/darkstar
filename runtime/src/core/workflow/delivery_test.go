package workflow

import (
	"encoding/json"
	"testing"
)

func TestStandaloneDeliveryContractsDoNotRequireValidationOrApproval(t *testing.T) {
	for _, example := range []struct {
		kind, configuration, output string
		inputs                      map[string]string
	}{
		{"git_commit", `"gitCommit":{"workspaceInput":"workspace","changesetInput":"changeset","textInput":"text"}`, `"commit":{"type":"schema:commit_v1"}`, map[string]string{"workspace": "workspace", "changeset": "schema:changeset_v1", "text": "schema:delivery_text_v1"}},
		{"git_push", `"gitPush":{"workspaceInput":"workspace","commitInput":"commit","remote":"origin"}`, `"branch":{"type":"schema:published_branch_v1"}`, map[string]string{"workspace": "workspace", "commit": "schema:commit_v1"}},
		{"create_pr", `"createPR":{"workspaceInput":"workspace","branchInput":"branch","textInput":"text","base":"remote_default","draft":false}`, `"pull_request":{"type":"schema:pull_request_v1"}`, map[string]string{"workspace": "workspace", "branch": "schema:published_branch_v1", "text": "schema:delivery_text_v1"}},
	} {
		inputs := map[string]map[string]string{}
		bindings := map[string]map[string]string{}
		for name, kind := range example.inputs {
			inputs[name] = map[string]string{"type": kind}
			bindings[name] = map[string]string{"type": kind, "from": "run.input." + name}
		}
		encodedInputs, _ := json.Marshal(inputs)
		encodedBindings, _ := json.Marshal(bindings)
		raw := []byte(`{"apiVersion":"darkstar.local/v1alpha3","kind":"Workflow","metadata":{"name":"test/delivery","version":"1.0.0"},"spec":{"inputs":` + string(encodedInputs) + `,"routeDefaults":{"entry":"deliver","terminals":["deliver"]},"nodes":{"deliver":{"type":"` + example.kind + `","entry":true,"terminal":true,"inputs":` + string(encodedBindings) + `,"outputs":{` + example.output + `},` + example.configuration + `,"transitions":[]}}}}`)
		doc, err := Decode(raw)
		if err != nil {
			t.Fatal(err)
		}
		if issues := Validate(doc); len(issues) != 0 {
			t.Fatalf("%s: %v", example.kind, issues)
		}
		if issues := draftConnectionIssues(raw); len(issues) != 0 {
			t.Fatalf("%s draft: %v", example.kind, issues)
		}
	}
}
