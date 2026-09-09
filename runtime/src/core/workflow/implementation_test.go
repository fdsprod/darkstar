package workflow_test

import (
	"darkstar/src/core/workflow"
	"encoding/json"
	"strings"
	"testing"
)

const implementationDocument = `{"apiVersion":"darkstar.local/v1alpha3","kind":"Workflow","metadata":{"name":"update-readme","version":"1.0.0"},"spec":{"inputs":{"task":{"type":"task","resource":{"kind":"task"}}},"routeDefaults":{"entry":"implement","terminals":["implement"]},"nodes":{"implement":{"type":"implementation","entry":true,"terminal":true,"inputs":{"task":{"type":"task","from":"run.input.task"}},"outputs":{"changeset":{"type":"object"}},"implementation":{"taskInput":"task","instructions":"Update README.md based on the work item."},"permissions":["process.run","workspace.write"],"transitions":[]}}}}`

func TestImplementationDecodesWithoutPlanAndRejectsContradictoryExecutors(t *testing.T) {
	doc, err := workflow.Decode([]byte(implementationDocument))
	if err != nil {
		t.Fatal(err)
	}
	node, ok := doc.Spec.Nodes["implement"].(workflow.ImplementationNode)
	if !ok || node.Executor.TaskInput != "task" {
		t.Fatalf("node: %#v", doc.Spec.Nodes["implement"])
	}
	encoded, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = workflow.Decode(encoded); err != nil {
		t.Fatal(err)
	}
	for _, invalid := range []string{strings.ReplaceAll(implementationDocument, "v1alpha3", "v1alpha2"), strings.Replace(implementationDocument, `"implementation":{"taskInput"`, `"reasoning":{"agent":"a"},"implementation":{"taskInput"`, 1)} {
		if _, err = workflow.Decode([]byte(invalid)); err == nil {
			t.Fatal("invalid executor accepted")
		}
	}
}

func TestImplementationRequiresTaskChangesetAndPermissions(t *testing.T) {
	for _, tc := range []struct{ name, old, replacement string }{
		{"valid", "", ""},
		{"missing task", `"taskInput":"task"`, `"taskInput":"missing"`},
		{"wrong output", `"changeset":{"type":"object"}`, `"changeset":{"type":"string"}`},
		{"missing permissions", `"permissions":["process.run","workspace.write"]`, `"permissions":[]`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw := implementationDocument
			if tc.old != "" {
				raw = strings.Replace(raw, tc.old, tc.replacement, 1)
			}
			doc, err := workflow.Decode([]byte(raw))
			if err != nil {
				t.Fatal(err)
			}
			findings := workflow.Validate(doc)
			if (len(findings) == 0) != (tc.name == "valid") {
				t.Fatalf("unexpected validation: %v", findings)
			}
		})
	}
}
