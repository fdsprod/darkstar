package api

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"darkstar/src/adapters/statestore/sqlite"
	"darkstar/src/core/workflow"
	"darkstar/src/core/workflowchat"
)

type scriptedWorkflowChat struct{}

func (scriptedWorkflowChat) Run(ctx context.Context, s *workflowchat.Session, _ []workflowchat.Message, emit workflowchat.Emit) error {
	if _, err := s.Call(ctx, "create", "create_workflow", json.RawMessage(`{"name":"api-workflow","document":`+apiWorkflowDocument()+`}`)); err != nil {
		return err
	}
	return emit("text", map[string]string{"text": "Created a draft. Publishing remains your choice."})
}

func TestWorkflowChatAuthenticatedStreamPersistsDraftOnly(t *testing.T) {
	ctx := context.Background()
	db, err := sqlite.Open(ctx, filepath.Join(t.TempDir(), "chat.db"), sqlite.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	catalog, err := workflow.NewCatalog(emptyWorkflowSource{}, db)
	if err != nil {
		t.Fatal(err)
	}
	server, err := NewServer(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err = server.SetWorkflows(catalog); err != nil {
		t.Fatal(err)
	}
	if err = server.SetWorkflowChat(scriptedWorkflowChat{}); err != nil {
		t.Fatal(err)
	}
	if err = server.Start(ctx, 1234, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	defer closeTestServer(t, server)
	endpoint, _ := server.Endpoint()
	response := workflowRequest(t, endpoint, http.MethodPost, "/api/v1/workflows/chat", []byte(`{"target":{"kind":"new"},"messages":[{"role":"user","text":"Create a workflow"}]}`))
	defer response.Body.Close()
	if response.StatusCode != 200 || response.Header.Get("Content-Type") != "application/x-ndjson" {
		t.Fatalf("response: %s %s", response.Status, response.Header.Get("Content-Type"))
	}
	scanner := bufio.NewScanner(response.Body)
	kinds := []string{}
	for scanner.Scan() {
		var event struct {
			Kind string `json:"kind"`
		}
		if err = json.Unmarshal(scanner.Bytes(), &event); err != nil {
			t.Fatal(err)
		}
		kinds = append(kinds, event.Kind)
	}
	if err = scanner.Err(); err != nil {
		t.Fatal(err)
	}
	if len(kinds) != 5 || kinds[0] != "status" || kinds[1] != "draft" || kinds[4] != "done" {
		t.Fatalf("events: %v", kinds)
	}
	library, err := catalog.Library(ctx)
	if err != nil || len(library.Drafts) != 1 || len(library.Versions) != 0 {
		t.Fatalf("library: %#v %v", library, err)
	}
	invalid := workflowRequest(t, endpoint, http.MethodPost, "/api/v1/workflows/chat", []byte(`{"target":{"kind":"publish"},"messages":[{"role":"user","text":"Publish"}]}`))
	defer invalid.Body.Close()
	if invalid.StatusCode != http.StatusBadRequest {
		t.Fatalf("invalid target status: %d", invalid.StatusCode)
	}
}
