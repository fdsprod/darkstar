package workflowchat_test

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"darkstar/src/adapters/provider/codex"
	"darkstar/src/core/workflow"
	"darkstar/src/core/workflowchat"
	"darkstar/src/ports/workflowstore"
)

// Opt-in because this uses the locally authenticated provider and model usage.
func TestLiveWorkspaceIntent(t *testing.T) {
	executable := os.Getenv("DARKSTAR_CHAT_LIVE_CODEX")
	if executable == "" {
		t.Skip("opt-in provider test")
	}
	s, store, events := setup(t, workflowchat.Target{Kind: "new"})
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	err := (codex.WorkflowChat{Executable: executable}).Run(ctx, s, []workflowchat.Message{{Role: "user", Text: "Create a workflow that creates an isolated worktree from HEAD on branch darkstar/{runId}, implements the connected work item there, and runs git diff --check in that same workspace before Done. Do not publish."}}, s.Emit)
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range *events {
		if event == "question" {
			t.Fatal("unnecessary question for explicit workspace request")
		}
	}
	draft, err := store.Draft(ctx, s.Target.ID)
	if err != nil {
		t.Fatal(err)
	}
	doc, err := workflow.Decode(draft.Document)
	if err != nil {
		t.Fatal(err)
	}
	found := map[workflow.NodeType]bool{}
	for _, node := range doc.Spec.Nodes {
		found[node.Type()] = true
		if prepare, ok := node.(workflow.WorkspacePrepareNode); ok {
			plan, yes := prepare.Executor.Checkout.(workflow.NewWorktree)
			if !yes || plan.BaseRef != "HEAD" || plan.Branch != "darkstar/{runId}" {
				t.Fatalf("wrong preparation: %#v", prepare.Executor)
			}
		}
	}
	for _, kind := range []workflow.NodeType{workflow.NodeWorkspacePrepare, workflow.NodeImplementation, workflow.NodeWorkspaceValidate} {
		if !found[kind] {
			t.Fatalf("missing %s", kind)
		}
	}
	report, err := store.ValidateDraft(ctx, draft.ID, draft.Revision)
	if err != nil || len(report.Findings) > 0 {
		t.Fatalf("invalid workflow: %#v %v", report, err)
	}
	library, err := store.Library(ctx)
	if err != nil || len(library.Versions) > 0 {
		t.Fatal("must not publish")
	}
}

func TestLiveWorkflowChat(t *testing.T) {
	executable := os.Getenv("DARKSTAR_CHAT_LIVE_CODEX")
	if executable == "" {
		t.Skip("set DARKSTAR_CHAT_LIVE_CODEX to run the real provider smoke test")
	}
	s, store, events := setup(t, workflowchat.Target{Kind: "new"})
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	err := (codex.WorkflowChat{Executable: executable}).Run(ctx, s, []workflowchat.Message{{Role: "user", Text: "Create a workflow named chat-test with one command node that runs echo done. It is both entry and terminal, with no other steps. Validate it. Do not publish."}}, s.Emit)
	if err != nil {
		t.Fatal(err)
	}
	library, err := store.Library(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(library.Drafts) != 1 || len(library.Versions) != 0 {
		t.Fatalf("expected one unpublished draft, got %#v; events %v", library, events)
	}
	report, err := store.ValidateDraft(ctx, library.Drafts[0].ID, library.Drafts[0].Revision)
	if err != nil || len(report.Findings) != 0 {
		t.Fatalf("invalid draft: %#v %v", report, err)
	}
}

func TestLiveSingleShotIntent(t *testing.T) {
	executable := os.Getenv("DARKSTAR_CHAT_LIVE_CODEX")
	if executable == "" {
		t.Skip("set DARKSTAR_CHAT_LIVE_CODEX for the authenticated provider smoke test")
	}
	s, store, events := setup(t, workflowchat.Target{Kind: "new"})
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	initial, err := store.CreateDraft(ctx, workflow.DraftCreateRequest{Name: "single-shot", Scope: workflowstore.DraftScopeUser, ScopeReference: "local-user", IdempotencyKey: "initial-selected-draft", Document: json.RawMessage(strings.ReplaceAll(document, "chat-test", "single-shot"))})
	if err != nil {
		t.Fatal(err)
	}
	s.Target = workflowchat.Target{Kind: "draft", ID: initial.ID, Revision: initial.Revision}
	runner := codex.WorkflowChat{Executable: executable}
	models, err := runner.Models(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(models) == 0 {
		t.Fatal("empty model catalog")
	}
	chosen := models[0]
	for _, model := range models {
		if model.IsDefault {
			chosen = model
			break
		}
	}
	s.Generation = &workflowchat.Generation{Model: chosen.ID, Effort: chosen.DefaultEffort}
	t.Logf("Testing model %s, effort %s", chosen.ID, chosen.DefaultEffort)
	err = runner.Run(ctx, s, []workflowchat.Message{{Role: "user", Text: "Create a single shot workflow that allows the agent to take in the work item, and just produce the result the work item is asking to do."}}, s.Emit)
	if err != nil {
		t.Fatal(err)
	}
	for _, kind := range *events {
		if kind == "question" {
			t.Fatal("clear single-shot request unnecessarily asked a question")
		}
	}
	if s.Target.Kind != "draft" {
		t.Fatal("no draft selected")
	}
	draft, err := store.Draft(ctx, s.Target.ID)
	if err != nil {
		t.Fatal(err)
	}
	var authored workflow.Document
	if err = json.Unmarshal(draft.Document, &authored); err != nil {
		t.Fatal(err)
	}
	implementations := 0
	for _, node := range authored.Spec.Nodes {
		if implementation, ok := node.(workflow.ImplementationNode); ok {
			implementations++
			if implementation.Executor.WorkspaceInput == "" {
				t.Fatal("Implementation missing explicit workspace")
			}
		}
	}
	if implementations != 1 {
		t.Fatalf("expected one implementation step, got %d", implementations)
	}
	report, err := store.ValidateDraft(ctx, draft.ID, draft.Revision)
	if err != nil || len(report.Findings) != 0 {
		t.Fatalf("invalid workflow: %#v %v", report, err)
	}
	library, err := store.Library(ctx)
	if err != nil || len(library.Versions) != 0 {
		t.Fatal("test must not publish")
	}
}
