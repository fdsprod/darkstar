package workflowtools

import (
	"encoding/json"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"darkstar/src/adapters/plugin/process"
	"darkstar/src/core/workflow"
)

func TestTypeScriptResourcePluginPreservesDurableJournalAcrossReconnect(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("Node.js is required for plugin integration")
	}
	entrypoint, err := pluginprocess.MaterializeBuiltin(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := pluginprocess.New(pluginprocess.Config{Executable: node, Entrypoint: entrypoint, Ref: pluginprocess.BuiltinRef(), GrantedCapabilities: []string{"journal.read", "journal.mutate", "workspace.read", "workspace.write", "workspace.publish_artifact"}})
	if err != nil {
		t.Fatal(err)
	}
	descriptor, err := runtime.Describe(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	s := &Session{Database: filepath.Join(t.TempDir(), "tools.db"), RunID: "run", AttemptID: "first", Node: workflow.ReasoningNode{}, Inputs: map[workflow.Identifier]json.RawMessage{"decisions": json.RawMessage(`{"kind":"decision_log","resourceId":"decisions"}`)}, ResourcePlugin: &ResourcePlugin{Runtime: runtime, Descriptor: descriptor}}
	call := json.RawMessage(`{"operation":"record","text":"Use TypeScript","key":"language","expectedRevision":0}`)
	first, err := s.Call(t.Context(), "first", "journal_decisions", call)
	if err != nil {
		t.Fatal(err)
	}
	s.AttemptID = "reconnected"
	repeated, err := s.Call(t.Context(), "second", "journal_decisions", call)
	if err != nil || !strings.Contains(string(repeated), "already_recorded") {
		t.Fatalf("%s %v", repeated, err)
	}
	var entry struct {
		EntryID string `json:"entryId"`
	}
	if err = json.Unmarshal(first, &entry); err != nil {
		t.Fatal(err)
	}
	update, _ := json.Marshal(map[string]any{"operation": "supersede", "entryId": entry.EntryID, "text": "Keep Go host authority", "key": "boundary", "expectedRevision": 1})
	if _, err = s.Call(t.Context(), "third", "journal_decisions", update); err != nil {
		t.Fatal(err)
	}
	read, err := s.Call(t.Context(), "read", "journal_decisions", json.RawMessage(`{"operation":"read"}`))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(read), "Use TypeScript") != 1 || !strings.Contains(string(read), "Keep Go host authority") {
		t.Fatal(string(read))
	}
	s.RunID = "other"
	read, err = s.Call(t.Context(), "other", "journal_decisions", json.RawMessage(`{"operation":"read"}`))
	if err != nil || strings.Contains(string(read), "Use TypeScript") {
		t.Fatalf("%s %v", read, err)
	}
}
