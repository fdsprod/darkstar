package workflow

import (
	"encoding/json"

	"darkstar/src/core/contentlibrary"
)

// StagePreset supplies an editable node plus the run-input declarations it needs.
// Tracking ports are offered separately: a preset never creates phantom links.
type StagePreset struct {
	ID             string                          `json:"id"`
	Name           string                          `json:"name"`
	Description    string                          `json:"description"`
	Node           Node                            `json:"node"`
	Inputs         map[Identifier]ValueDeclaration `json:"inputs"`
	OptionalInputs map[Identifier]ValueDeclaration `json:"optionalInputs"`
}

func (preset *StagePreset) UnmarshalJSON(data []byte) error {
	var wire struct {
		ID             string                          `json:"id"`
		Name           string                          `json:"name"`
		Description    string                          `json:"description"`
		Node           json.RawMessage                 `json:"node"`
		Inputs         map[Identifier]ValueDeclaration `json:"inputs"`
		OptionalInputs map[Identifier]ValueDeclaration `json:"optionalInputs"`
	}
	if err := strictDecode(data, &wire); err != nil {
		return err
	}
	node, err := decodeNode(wire.Node, APIVersionV1Alpha3)
	if err != nil {
		return err
	}
	*preset = StagePreset{ID: wire.ID, Name: wire.Name, Description: wire.Description, Node: node, Inputs: wire.Inputs, OptionalInputs: wire.OptionalInputs}
	return nil
}

func StagePresets() []StagePreset {
	result := []StagePreset{}
	for _, stage := range contentlibrary.BuiltinStages() {
		prompt := contentlibrary.BuiltinReference(stage.ID, "prompt")
		template := contentlibrary.BuiltinReference(stage.ID, "template")
		templateName := Identifier(stage.ID + "_template")
		fields := NodeFields{
			DisplayName: stage.Name, Prompt: &prompt, Entry: true, Terminal: true,
			Inputs: map[Identifier]Binding{
				"task":     RequiredBinding{From: "run.input.task", Type: ValueTask},
				"template": RequiredBinding{From: "run.input." + string(templateName), Type: ValueTemplate},
			},
			Outputs: map[Identifier]OutputDeclaration{
				"document": {Type: ValueMarkdown, Artifact: &ArtifactContract{Filename: stage.ID + ".md", TemplateInput: "template"}},
				"findings": {Type: "schema:planning_findings_v1", SchemaDefinition: trackingFindingsSchema()},
			},
			Checkpoint: NoCheckpoint{}, TransitionMode: TransitionExclusive,
		}
		if stage.ReviewRequired {
			fields.Checkpoint = ApproveCheckpoint{}
		}
		if stage.ID == "assessment" {
			fields.Outputs["readiness"] = OutputDeclaration{Type: "schema:planning_readiness_v1", SchemaDefinition: assessmentReadinessSchema()}
		}
		var node Node = ReasoningNode{Common: fields, Executor: ReasoningExecutor{Agent: stage.ID}}
		if stage.ID == "implementation" {
			fields.Permissions = []string{"process.run", "workspace.write"}
			fields.Outputs["changeset"] = OutputDeclaration{Type: "schema:changeset_v1"}
			node = ImplementationNode{Common: fields, Executor: ImplementationExecutor{TaskInput: "task"}}
		}
		result = append(result, StagePreset{
			ID: stage.ID, Name: stage.Name, Description: "Versioned instructions and artifact template for " + stage.Name + ".",
			Node: node,
			Inputs: map[Identifier]ValueDeclaration{
				"task":       {Type: ValueTask, Resource: &Resource{Source: TaskResource{}}},
				templateName: {Type: ValueTemplate, Resource: &Resource{Source: TemplateReferenceResource{Reference: template}}},
			},
			OptionalInputs: map[Identifier]ValueDeclaration{
				"open_items":    {Type: ValueOpenItems, Description: "Existing unresolved items; absence is distinct from an empty collection."},
				"deferred_work": {Type: ValueOpenItems, Description: "Existing deferred work, supplied as an explicit filtered collection."},
				"context":       {Type: ValueMarkdown, Description: "Relevant supplied evidence and approved decisions."},
			},
		})
	}
	return result
}

func assessmentReadinessSchema() json.RawMessage {
	return json.RawMessage(`{"type":"object","additionalProperties":false,"properties":{"questions_ready":{"type":"boolean"},"research_ready":{"type":"boolean"},"design_ready":{"type":"boolean"},"technical_design_ready":{"type":"boolean"},"evidence":{"type":"object","additionalProperties":false,"properties":{"questions_ready":{"type":"string"},"research_ready":{"type":"string"},"design_ready":{"type":"string"},"technical_design_ready":{"type":"string"}},"required":["questions_ready","research_ready","design_ready","technical_design_ready"]}},"required":["questions_ready","research_ready","design_ready","technical_design_ready","evidence"]}`)
}

func trackingFindingsSchema() json.RawMessage {
	return json.RawMessage(`{"type":"object","additionalProperties":false,"properties":{"blockers":{"type":"array","items":{"type":"string"}},"proposed_changes":{"type":"array","items":{"type":"object","additionalProperties":false,"properties":{"kind":{"type":"string","enum":["open_item","deferred_work"]},"action":{"type":"string","enum":["add","resolve","update"]},"item_id":{"type":"string"},"summary":{"type":"string"},"evidence":{"type":"string"}},"required":["kind","action","item_id","summary","evidence"]}}},"required":["blockers","proposed_changes"]}`)
}
