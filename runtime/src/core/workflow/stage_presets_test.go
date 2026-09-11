package workflow

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAdaptivePlanningExampleRemainsPublishableThroughPR(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "..", "examples", "workflows", "v1alpha3", "adaptive-planning.json"))
	if err != nil {
		t.Fatal(err)
	}
	document, err := Decode(raw)
	if err != nil {
		t.Fatal(err)
	}
	if issues := Validate(document); len(issues) != 0 {
		t.Fatal(issues)
	}
	if issues := draftConnectionIssues(raw); len(issues) != 0 {
		t.Fatal(issues)
	}
	if len(document.Spec.RouteDefaults.Terminals) != 1 || document.Spec.RouteDefaults.Terminals[0] != "create_pr" {
		t.Fatal("default route must finish by creating the pull request")
	}
	for _, id := range []Identifier{"product_design", "technical_design", "plan"} {
		if document.Spec.Nodes[id].Fields().Checkpoint.Mode() != CheckpointApprove {
			t.Fatalf("%s must retain its human review", id)
		}
	}
	for _, node := range document.Spec.Nodes {
		if node.Type() == NodeWorkspaceValidate {
			t.Fatal("this example intentionally skips the validation stage")
		}
	}
}

func TestStagePresetsRoundTripAndRetainRequiredHumanReviews(t *testing.T) {
	for _, preset := range StagePresets() {
		t.Run(preset.ID, func(t *testing.T) {
			encoded, err := json.Marshal(preset)
			if err != nil {
				t.Fatal(err)
			}
			var decoded StagePreset
			if err := json.Unmarshal(encoded, &decoded); err != nil {
				t.Fatal(err)
			}
			fields := decoded.Node.Fields()
			if fields.Prompt == nil || fields.Prompt.Validate() != nil {
				t.Fatal("preset lost its pinned prompt")
			}
			for _, name := range []Identifier{"open_items", "deferred_work"} {
				if _, exists := fields.Inputs[name]; exists || decoded.OptionalInputs[name].Type != ValueOpenItems {
					t.Fatal("tracking input must be offered without a phantom link")
				}
			}
			review := preset.ID == "product_design" || preset.ID == "technical_design" || preset.ID == "plan"
			if (fields.Checkpoint.Mode() == CheckpointApprove) != review {
				t.Fatal("human review policy changed")
			}
			document := Document{APIVersion: APIVersionV1Alpha3, Kind: KindWorkflow, Metadata: Metadata{Name: "preset-test", Version: "1.0.0"}, Spec: Spec{Inputs: decoded.Inputs, Nodes: map[Identifier]Node{"stage": decoded.Node}, RouteDefaults: RouteDefaults{Entry: "stage", Terminals: []Identifier{"stage"}}}}
			if _, err := Encode(document); err != nil {
				t.Fatal(err)
			}
			if preset.ID != "implementation" {
				raw, err := json.Marshal(document)
				if err != nil {
					t.Fatal(err)
				}
				if issues := draftConnectionIssues(raw); len(issues) != 0 {
					t.Fatalf("stage preset has unpublishable ports: %v", issues)
				}
			}
		})
	}
}

func TestTemplateReferenceRejectsEmbeddedOverrides(t *testing.T) {
	ref := StagePresets()[0].Node.Fields().Prompt
	raw := `{"kind":"template_reference","reference":{"id":"` + ref.ID + `","version":"` + ref.Version + `","digest":"` + ref.Digest + `"},"content":"override"}`
	var resource Resource
	if err := json.Unmarshal([]byte(raw), &resource); err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatal("reference must not carry a second editable body")
	}
}
