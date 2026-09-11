package workflowtools

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"darkstar/src/core/contentlibrary"
	"darkstar/src/core/workflow"
)

func TestLinkedPromptJournalIsReadOnlyAtSchemaAndHostBoundary(t *testing.T) {
	ref := contentlibrary.BuiltinReference("questions", "prompt")
	session := &Session{Database: filepath.Join(t.TempDir(), "tools.db"), RunID: "run", AttemptID: "attempt", Node: workflow.ReasoningNode{Common: workflow.NodeFields{Prompt: &ref}}, Inputs: map[workflow.Identifier]json.RawMessage{"open_items": json.RawMessage(`{"kind":"open_items","resourceId":"items"}`)}}
	if _, err := session.Call(t.Context(), "read", "journal_open_items", json.RawMessage(`{"operation":"read"}`)); err != nil {
		t.Fatal(err)
	}
	if _, err := session.Call(t.Context(), "write", "journal_open_items", json.RawMessage(`{"operation":"add","key":"new","text":"Proposed"}`)); err == nil {
		t.Fatal("linked task was granted journal mutation")
	}
	if _, err := session.resourceCall(t.Context(), builtinResources()[0], "items", "journal.mutate", json.RawMessage(`{"operation":"add","key":"new","text":"Proposed"}`)); err == nil {
		t.Fatal("plugin host bypass granted journal mutation")
	}
}

func TestPromptJournalSnapshotIsScopedFilteredAndStableForVisit(t *testing.T) {
	ref := contentlibrary.BuiltinReference("questions", "prompt")
	session := &Session{Database: filepath.Join(t.TempDir(), "tools.db"), RunID: "run", AttemptID: "attempt-secret", Node: workflow.ReasoningNode{Common: workflow.NodeFields{Prompt: &ref}}, Inputs: map[workflow.Identifier]json.RawMessage{
		"open_items":    json.RawMessage(`{"kind":"open_items","resourceId":"items"}`),
		"deferred_work": json.RawMessage(`{"kind":"open_items","resourceId":"items"}`),
	}}
	if _, err := session.append(t.Context(), "items", "add", "first", "add-first", "Open question", false); err != nil {
		t.Fatal(err)
	}
	if _, err := session.append(t.Context(), "items", "add", "second", "add-second", "Future work", false); err != nil {
		t.Fatal(err)
	}
	if _, err := session.append(t.Context(), "items", "defer", "second", "defer-second", "Outside current scope", true); err != nil {
		t.Fatal(err)
	}
	first, err := session.SnapshotPromptInputs(t.Context(), "visit")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(first["open_items"]), "Open question") || strings.Contains(string(first["open_items"]), "Future work") || !strings.Contains(string(first["deferred_work"]), "Future work") || strings.Contains(string(first["deferred_work"]), "attempt-secret") {
		t.Fatalf("incorrect scoped projections: %s %s", first["open_items"], first["deferred_work"])
	}
	if _, err := session.append(t.Context(), "items", "resolve", "first", "resolve-first", "Answered", true); err != nil {
		t.Fatal(err)
	}
	session.AttemptID = "retry"
	retry, err := session.SnapshotPromptInputs(t.Context(), "visit")
	if err != nil || string(first["open_items"]) != string(retry["open_items"]) {
		t.Fatalf("retry changed immutable evidence: %v", err)
	}
	next, err := session.SnapshotPromptInputs(t.Context(), "next-visit")
	if err != nil || strings.Contains(string(next["open_items"]), "Open question") {
		t.Fatalf("new visit failed to select current evidence: %v", err)
	}
}
