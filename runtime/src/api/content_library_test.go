package api

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"darkstar/src/adapters/statestore/sqlite"
	"darkstar/src/core/contentlibrary"
)

func TestContentLibraryAPIProtectsPublicationAndRejectsStaleEdits(t *testing.T) {
	ctx := context.Background()
	database, err := sqlite.Open(ctx, filepath.Join(t.TempDir(), "api.db"), sqlite.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = database.Close()
	}()
	server, err := NewServer(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := server.SetContentLibrary(contentlibrary.New(database)); err != nil {
		t.Fatal(err)
	}
	if err := server.Start(ctx, 1234, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	defer closeTestServer(t, server)
	endpoint, _ := server.Endpoint()
	call := func(method, resource, body string, status int) []byte {
		t.Helper()
		response := workflowRequest(t, endpoint, method, "/api/v1/content-library"+resource, []byte(body))
		defer func() {
			_ = response.Body.Close()
		}()
		raw, err := io.ReadAll(response.Body)
		if err != nil || response.StatusCode != status {
			t.Fatalf("%s %s = %d: %s (%v)", method, resource, response.StatusCode, raw, err)
		}
		return raw
	}
	raw := call(http.MethodPost, "", `{"name":"Design","document":{"kind":"template","content":"# Behavior","requiredHeadings":["Behavior"]}}`, http.StatusOK)
	var item contentlibrary.Item
	if err := json.Unmarshal(raw, &item); err != nil {
		t.Fatal(err)
	}
	call(http.MethodPut, "/"+item.ID+"/draft", `{"expectedRevision":1,"document":{"kind":"prompt","instructions":"Cross kind"}}`, http.StatusBadRequest)
	call(http.MethodPost, "/"+item.ID+"/publish", `{"expectedRevision":1,"version":"latest"}`, http.StatusBadRequest)
	call(http.MethodPost, "/"+item.ID+"/publish", `{"expectedRevision":1,"version":"1.0.0"}`, http.StatusOK)
	call(http.MethodPut, "/"+item.ID+"/draft", `{"expectedRevision":1,"document":{"kind":"template","content":"Stale"}}`, http.StatusConflict)
	call(http.MethodPost, "/"+item.ID+"/archive", `{}`, http.StatusOK)
	call(http.MethodPost, "/"+item.ID+"/restore", `{}`, http.StatusOK)
	call(http.MethodPost, "/"+item.ID+"/duplicate", `{"name":"Alternative"}`, http.StatusOK)
	call(http.MethodGet, "", "", http.StatusOK)
	call(http.MethodGet, "/"+item.ID, "", http.StatusOK)
	call(http.MethodGet, "/missing", "", http.StatusNotFound)
	preview := call(http.MethodPost, "/preview", `{"document":{"kind":"prompt","instructions":"Design the behavior.","sections":[{"id":"open","when":{"kind":"input_linked","input":"open_items"},"instructions":"Reconcile linked items."}]},"linkedInputs":["open_items"],"revision":false}`, http.StatusOK)
	var result contentlibrary.PromptPreview
	if err := json.Unmarshal(preview, &result); err != nil || len(result.Sections) != 1 || !result.Sections[0].Included {
		t.Fatalf("conditional preview: %s %v", preview, err)
	}
	unauthorized, err := http.Post(endpoint.BaseURL()+"/api/v1/content-library/"+item.ID+"/publish", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = unauthorized.Body.Close()
	}()
	if unauthorized.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauthenticated publication status %d", unauthorized.StatusCode)
	}
}
