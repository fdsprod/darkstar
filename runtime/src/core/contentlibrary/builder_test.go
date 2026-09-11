package contentlibrary

import (
	"reflect"
	"strings"
	"testing"
)

func TestPromptBuilderUsesConnectionsAndRevisionWithoutInputInterpolation(t *testing.T) {
	document := Document{Kind: "prompt", Instructions: "Scoped task", Sections: trackingSections()}
	first, err := BuildPrompt(document, []string{"open_items"}, false)
	if err != nil {
		t.Fatal(err)
	}
	second, err := BuildPrompt(document, []string{"open_items", "open_items"}, false)
	if err != nil || !reflect.DeepEqual(first, second) {
		t.Fatal("same connections must assemble deterministically")
	}
	if !strings.Contains(first.Instructions, "Open items is connected") || strings.Contains(first.Instructions, "Open items is not connected") || !strings.Contains(first.Instructions, "Deferred work is not connected") {
		t.Fatalf("wrong conditional sections: %s", first.Instructions)
	}
	if first.EstimatedTokens == 0 || first.TokenEstimateMethod != "characters_divided_by_four" {
		t.Fatal("token estimate must identify its estimation method")
	}
	revision, err := BuildPrompt(document, nil, true)
	if err != nil || !strings.Contains(revision.Instructions, "This is a revision") || !strings.Contains(revision.Instructions, "absence of a link is not evidence") {
		t.Fatalf("revision/absent guidance missing: %v %s", err, revision.Instructions)
	}
}

func TestPromptBuilderRejectsUnknownConditionsAndTemplateDocuments(t *testing.T) {
	for _, document := range []Document{
		{Kind: "template", Content: "# Template"},
		{Kind: "prompt", Instructions: "Task", Sections: []Section{{ID: "invalid", When: Condition{Kind: "evaluate_expression"}, Instructions: "Not permitted"}}},
	} {
		if _, err := BuildPrompt(document, nil, false); err == nil {
			t.Fatal("invalid prompt definition accepted")
		}
	}
}

func TestBuiltinContentIsValidPinnedAndScoped(t *testing.T) {
	items := BuiltinItems()
	if len(items) != 20 {
		t.Fatalf("expected prompt and template for ten stages, got %d", len(items))
	}
	for _, item := range items {
		if err := item.Draft.Document.Validate(); err != nil {
			t.Fatalf("%s: %v", item.ID, err)
		}
		version := item.Versions[0]
		if version.Reference.Digest != Digest(version.Document) || version.Reference.Validate() != nil {
			t.Fatalf("%s has invalid pin", item.ID)
		}
		if item.Kind == "prompt" {
			preview, err := BuildPrompt(version.Document, nil, false)
			if err != nil || !strings.Contains(preview.Instructions, "Required in-scope work remains a gap") || strings.Contains(preview.Instructions, "forge flow") || strings.Contains(preview.Instructions, "{plugin-root}") {
				t.Fatalf("%s does not retain scoped adaptation: %v", item.ID, err)
			}
		}
	}
}
