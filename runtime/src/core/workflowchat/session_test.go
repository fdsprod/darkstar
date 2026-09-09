package workflowchat_test

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	"darkstar/src/adapters/statestore/sqlite"
	"darkstar/src/core/workflow"
	"darkstar/src/core/workflowchat"
	"darkstar/src/ports/workflowstore"
)

const document = `{"apiVersion":"darkstar.local/v1alpha2","kind":"Workflow","metadata":{"name":"chat-test","version":"1.0.0"},"spec":{"routeDefaults":{"entry":"finish","terminals":["finish"]},"nodes":{"finish":{"type":"command","entry":true,"terminal":true,"inputs":{},"outputs":{},"command":{"argv":["echo","done"]},"transitions":[]}}}}`

type source struct{}

func (source) Load(context.Context) ([]workflowstore.Candidate, error) { return nil, nil }

func setup(t *testing.T, target workflowchat.Target) (*workflowchat.Session, *workflow.Catalog, *[]string) {
	t.Helper()
	ctx := context.Background()
	db, err := sqlite.Open(ctx, filepath.Join(t.TempDir(), "chat.db"), sqlite.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	catalog, err := workflow.NewCatalog(source{}, db)
	if err != nil {
		t.Fatal(err)
	}
	events := []string{}
	session := &workflowchat.Session{Store: catalog, Target: target, Key: "chat-test-operation", Emit: func(kind string, _ any) error { events = append(events, kind); return nil }}
	return session, catalog, &events
}

func call(t *testing.T, s *workflowchat.Session, name string, args string) json.RawMessage {
	t.Helper()
	result, err := s.Call(context.Background(), "call", name, json.RawMessage(args))
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func TestNewWorkflowEditsValidateButCannotPublish(t *testing.T) {
	s, store, events := setup(t, workflowchat.Target{Kind: "new"})
	call(t, s, "inspect_workflow", `{}`)
	call(t, s, "create_workflow", `{"name":"chat-test","document":`+document+`}`)
	id := s.Target.ID
	call(t, s, "edit_workflow", `{"document":`+document+`}`)
	call(t, s, "validate_workflow", `{}`)
	if s.Target.ID != id || s.Target.Revision != 2 {
		t.Fatalf("target = %#v", s.Target)
	}
	if len(*events) != 5 || (*events)[0] != "draft" || (*events)[4] != "validation" {
		t.Fatalf("events = %v", *events)
	}
	for _, name := range []string{"publish_workflow", "publish", "install", "archive", "execute"} {
		if _, err := s.Call(context.Background(), "denied", name, json.RawMessage(`{}`)); err == nil {
			t.Fatalf("allowed %s", name)
		}
	}
	library, err := store.Library(context.Background())
	if err != nil || len(library.Versions) != 0 {
		t.Fatalf("published versions: %#v %v", library, err)
	}
}

func TestCreateIntentCanSelectNewDraftWithoutChangingCurrentDraft(t *testing.T) {
	s, store, _ := setup(t, workflowchat.Target{Kind: "new"})
	call(t, s, "create_workflow", `{"name":"chat-test","document":`+document+`}`)
	previous := s.Target
	s = &workflowchat.Session{Store: store, Target: previous, Key: "another-workflow", Emit: func(string, any) error { return nil }}
	call(t, s, "create_workflow", `{"name":"chat-test","document":`+document+`}`)
	if s.Target.ID == previous.ID {
		t.Fatal("create edited the selected draft")
	}
	old, err := store.Draft(context.Background(), previous.ID)
	if err != nil || old.Revision != previous.Revision {
		t.Fatalf("selected draft changed: %#v %v", old, err)
	}
}

func TestSingleStepExampleIsValidWithoutAgentRegistry(t *testing.T) {
	s, _, _ := setup(t, workflowchat.Target{Kind: "new"})
	var context struct {
		SingleStepExample json.RawMessage   `json:"singleStepExample"`
		ExecutionContext  map[string]string `json:"executionContext"`
	}
	if err := json.Unmarshal(call(t, s, "inspect_workflow", `{}`), &context); err != nil {
		t.Fatal(err)
	}
	if context.ExecutionContext["reasoningAgent"] == "" {
		t.Fatal("missing runtime role semantics")
	}
	call(t, s, "create_workflow", `{"name":"workflow/single-shot","document":`+string(context.SingleStepExample)+`}`)
	var report workflow.DraftValidationReport
	if err := json.Unmarshal(call(t, s, "validate_workflow", `{}`), &report); err != nil {
		t.Fatal(err)
	}
	if len(report.Findings) != 0 {
		t.Fatalf("single-step example findings: %#v", report.Findings)
	}
}

func TestImmutableVersionForkedOnFirstEdit(t *testing.T) {
	s, store, events := setup(t, workflowchat.Target{Kind: "version", Name: "chat-test", Version: "1.0.0"})
	ctx := context.Background()
	_, err := store.Install(ctx, workflowstore.Candidate{Content: json.RawMessage(document), Scope: workflowstore.ScopeUser, Reference: "test"})
	if err != nil {
		t.Fatal(err)
	}
	original, err := store.Definition(ctx, "chat-test", "1.0.0")
	if err != nil {
		t.Fatal(err)
	}
	call(t, s, "edit_workflow", `{"document":`+document+`}`)
	draft, err := store.Draft(ctx, s.Target.ID)
	if err != nil || draft.BaseVersion != "1.0.0" {
		t.Fatalf("draft %#v %v", draft, err)
	}
	after, err := store.Definition(ctx, "chat-test", "1.0.0")
	if err != nil || original.Version.Digest != after.Version.Digest {
		t.Fatal("immutable version changed")
	}
	if len(*events) != 3 {
		t.Fatalf("fork and save events: %v", *events)
	}
}

func TestConflictDoesNotOverwriteAndBlocksFurtherWrites(t *testing.T) {
	s, store, events := setup(t, workflowchat.Target{Kind: "new"})
	call(t, s, "create_workflow", `{"name":"chat-test","document":`+document+`}`)
	_, err := store.UpdateDraft(context.Background(), workflow.DraftUpdateRequest{ID: s.Target.ID, ExpectedRevision: 1, Layout: json.RawMessage(`{"human":true}`)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Call(context.Background(), "stale", "edit_workflow", json.RawMessage(`{"document":`+document+`}`)); err == nil {
		t.Fatal("stale edit accepted")
	}
	if (*events)[len(*events)-1] != "conflict" {
		t.Fatal("conflict not surfaced")
	}
	if _, err = s.Call(context.Background(), "retry", "edit_workflow", json.RawMessage(`{"document":`+document+`}`)); err == nil {
		t.Fatal("agent bypassed conflict")
	}
	remote, err := store.Draft(context.Background(), s.Target.ID)
	if err != nil || remote.Revision != 2 || string(remote.Layout) != `{"human":true}` {
		t.Fatalf("remote changed: %#v %v", remote, err)
	}
}

func TestQuestionsPauseMutationsAndTargetsAreClosed(t *testing.T) {
	s, _, _ := setup(t, workflowchat.Target{Kind: "new"})
	call(t, s, "ask_question", `{"question":"Automatic deployment conflicts with mandatory approval. Which do you want?","options":["Keep approval","Remove deployment"]}`)
	if _, err := s.Call(context.Background(), "create", "create_workflow", json.RawMessage(`{"name":"chat-test","document":`+document+`}`)); err == nil {
		t.Fatal("agent edited while awaiting answer")
	}
	for _, target := range []workflowchat.Target{{Kind: "new", ID: "x"}, {Kind: "draft", ID: "x"}, {Kind: "version", Name: "x"}, {Kind: "publish"}} {
		if target.Validate() == nil {
			t.Fatalf("invalid target accepted: %#v", target)
		}
	}
}
