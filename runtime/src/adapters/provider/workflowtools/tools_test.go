package workflowtools

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"darkstar/src/core/workflow"
	"darkstar/src/ports/plugin"
	"darkstar/src/ports/provider"
)

type resourceRuntimeFunc func(context.Context, plugin.Invocation, plugin.HostServices) (json.RawMessage, error)

func (f resourceRuntimeFunc) Describe(context.Context) (plugin.Descriptor, error) {
	return plugin.Descriptor{}, nil
}
func (f resourceRuntimeFunc) Invoke(ctx context.Context, in plugin.Invocation, host plugin.HostServices) (json.RawMessage, error) {
	return f(ctx, in, host)
}

func TestResourceObjectsUseScopedPluginHostAndCustomCreationHistory(t *testing.T) {
	resource := builtinResources()[0]
	resource.Kind = "questions"
	resource.Tool.ID = "example/questions"
	resource.CreateOperation = "ask"
	resource.UpdateOperations = []string{"answer"}
	resource.Tool.InputSchema = json.RawMessage(`{"type":"object","additionalProperties":false,"properties":{"operation":{"enum":["read","ask","answer"]},"entryId":{"type":"string"},"text":{"type":"string"},"key":{"type":"string"},"expectedRevision":{"type":"integer","minimum":0}},"required":["operation"]}`)
	calls := 0
	runtime := resourceRuntimeFunc(func(ctx context.Context, in plugin.Invocation, host plugin.HostServices) (json.RawMessage, error) {
		calls++
		if in.Contribution != "example/questions" {
			t.Fatal(in.Contribution)
		}
		if strings.Contains(string(in.Arguments), "tools.db") || strings.Contains(string(in.Arguments), "secret-resource") {
			t.Fatal("host identities disclosed")
		}
		var args struct {
			Operation string `json:"operation"`
		}
		_ = json.Unmarshal(in.Arguments, &args)
		if args.Operation == "read" {
			return host.Call(ctx, "journal.read", json.RawMessage(`{}`))
		}
		return host.Call(ctx, "journal.mutate", in.Arguments)
	})
	s := &Session{Database: filepath.Join(t.TempDir(), "tools.db"), RunID: "run", AttemptID: "attempt", Node: workflow.ReasoningNode{}, Inputs: map[workflow.Identifier]json.RawMessage{"questions": json.RawMessage(`{"kind":"questions","resourceId":"secret-resource"}`)}, ResourcePlugin: &ResourcePlugin{Runtime: runtime, Descriptor: plugin.Descriptor{Resources: []plugin.Resource{resource}}}}
	created, err := s.Call(t.Context(), "first", "journal_questions", json.RawMessage(`{"operation":"ask","text":"Which?","key":"first","expectedRevision":0}`))
	if err != nil {
		t.Fatal(err)
	}
	var item struct {
		EntryID string `json:"entryId"`
	}
	_ = json.Unmarshal(created, &item)
	answer, _ := json.Marshal(map[string]any{"operation": "answer", "entryId": item.EntryID, "text": "This.", "key": "answer", "expectedRevision": 1})
	if _, err = s.Call(t.Context(), "second", "journal_questions", answer); err != nil {
		t.Fatal(err)
	}
	read, err := s.Call(t.Context(), "read", "journal_questions", json.RawMessage(`{"operation":"read"}`))
	if err != nil || !strings.Contains(string(read), "Which?") || !strings.Contains(string(read), "This.") {
		t.Fatalf("%s %v", read, err)
	}
	if calls != 3 {
		t.Fatal(calls)
	}
}

func TestPluginCannotBypassJournalOwnershipOrEntryChecks(t *testing.T) {
	resource := builtinResources()[0]
	for _, body := range []string{
		`{"operation":"resolve","entryId":"missing","text":"done","key":"x"}`,
		`{"operation":"add","entryId":"","text":"new","key":"x","resourceId":"other"}`,
		`{"operation":"resolve","entryId":"missing","text":"done","key":"x","requiresEntry":false}`,
	} {
		t.Run(body, func(t *testing.T) {
			runtime := resourceRuntimeFunc(func(ctx context.Context, _ plugin.Invocation, host plugin.HostServices) (json.RawMessage, error) {
				return host.Call(ctx, "journal.mutate", json.RawMessage(body))
			})
			s := &Session{Database: filepath.Join(t.TempDir(), "tools.db"), RunID: "run", AttemptID: "attempt", Node: workflow.ReasoningNode{}, Inputs: map[workflow.Identifier]json.RawMessage{"open": json.RawMessage(`{"kind":"open_items","resourceId":"open"}`)}, ResourcePlugin: &ResourcePlugin{Runtime: runtime, Descriptor: plugin.Descriptor{Resources: []plugin.Resource{resource}}}}
			if _, err := s.Call(t.Context(), "x", "journal_open", json.RawMessage(`{"operation":"read"}`)); err == nil {
				t.Fatal("unsafe callback accepted")
			}
		})
	}
}

func TestToolCatalogRejectsDuplicateAndInvalidArgumentsOrResults(t *testing.T) {
	s := &Session{Node: workflow.ReasoningNode{}, Inputs: map[workflow.Identifier]json.RawMessage{"connected": json.RawMessage(`"value"`)}}
	if _, err := s.Call(t.Context(), "x", "read_input", json.RawMessage(`{"id":"connected","owner":"other"}`)); err == nil {
		t.Fatal("extra argument accepted")
	}
	if _, err := s.Call(t.Context(), "x", "not_registered", json.RawMessage(`{}`)); err == nil {
		t.Fatal("unknown tool accepted")
	}
	invoke := func(context.Context, string, json.RawMessage) (json.RawMessage, error) {
		return json.RawMessage(`42`), nil
	}
	s.AdditionalTools = []Tool{{Definition: provider.ToolDefinition{Name: "read_input"}, Invoke: invoke}}
	if s.Validate() == nil {
		t.Fatal("duplicate accepted")
	}
	s.AdditionalTools = []Tool{{Definition: provider.ToolDefinition{Name: "custom", InputSchema: json.RawMessage(`{"type":"object"}`)}, ResultSchema: json.RawMessage(`{"type":"string"}`), Invoke: invoke}}
	if _, err := s.Call(t.Context(), "x", "custom", json.RawMessage(`{}`)); err == nil {
		t.Fatal("invalid result accepted")
	}
}

func TestJournalRevisionConflictAndReplayReceipt(t *testing.T) {
	resource := builtinResources()[0]
	runtime := resourceRuntimeFunc(func(ctx context.Context, in plugin.Invocation, host plugin.HostServices) (json.RawMessage, error) {
		return host.Call(ctx, "journal.mutate", in.Arguments)
	})
	s := &Session{Database: filepath.Join(t.TempDir(), "tools.db"), RunID: "run", AttemptID: "attempt", Node: workflow.ReasoningNode{}, Inputs: map[workflow.Identifier]json.RawMessage{"open": json.RawMessage(`{"kind":"open_items","resourceId":"open"}`)}, ResourcePlugin: &ResourcePlugin{Runtime: runtime, Descriptor: plugin.Descriptor{Resources: []plugin.Resource{resource}}}}
	if _, err := s.Call(t.Context(), "missing", "journal_open", json.RawMessage(`{"operation":"add","text":"Missing revision","key":"missing"}`)); err == nil {
		t.Fatal("plugin mutation without revision accepted")
	}
	first := json.RawMessage(`{"operation":"add","text":"First","key":"first","expectedRevision":0}`)
	receipt, err := s.Call(t.Context(), "first", "journal_open", first)
	if err != nil {
		t.Fatal(err)
	}
	var entry struct {
		EntryID string `json:"entryId"`
	}
	_ = json.Unmarshal(receipt, &entry)
	mutate := func(key string, revision uint64) error {
		body, _ := json.Marshal(map[string]any{"operation": "resolve", "entryId": entry.EntryID, "text": "Done", "key": key, "expectedRevision": revision})
		_, err := s.Call(t.Context(), key, "journal_open", body)
		return err
	}
	if err := mutate("stale", 0); err == nil || !strings.Contains(err.Error(), "RESOURCE_REVISION_CONFLICT") {
		t.Fatalf("stale update: %v", err)
	}
	if err := mutate("current", 1); err != nil {
		t.Fatal(err)
	}
	replay, err := s.Call(t.Context(), "replay", "journal_open", first)
	if err != nil {
		t.Fatal(err)
	}
	var result struct {
		Revision uint64 `json:"revision"`
	}
	_ = json.Unmarshal(replay, &result)
	if result.Revision != 1 {
		t.Fatalf("replay changed original revision: %s", replay)
	}
	snapshot, err := s.read(t.Context(), "open")
	if err != nil {
		t.Fatal(err)
	}
	_ = json.Unmarshal(snapshot, &result)
	if result.Revision != 2 {
		t.Fatalf("invalid snapshot revision %s", snapshot)
	}
}

func TestJournalCannotAliasOutputAuthority(t *testing.T) {
	s := &Session{Database: filepath.Join(t.TempDir(), "tools.db"), RunID: "run", AttemptID: "attempt", Node: workflow.ReasoningNode{Common: workflow.NodeFields{Outputs: map[workflow.Identifier]workflow.OutputDeclaration{"out": {Type: workflow.ValueString}}}}, Inputs: map[workflow.Identifier]json.RawMessage{"open": json.RawMessage(`{"kind":"open_items","resourceId":"output:attempt:out"}`)}}
	if err := s.Validate(); err == nil {
		t.Fatal("reserved resource exposed")
	}
	s.Inputs = nil
	if _, err := s.append(t.Context(), "output:attempt:out", "add", "item", "malicious", `"unvalidated"`, false); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ResolveSubmittedOutputs(t.Context()); err == nil {
		t.Fatal("non-submission event became an output")
	}
}
