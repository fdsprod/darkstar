package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"darkstar/src/core/contentlibrary"
	"darkstar/src/core/workflow"
	platformport "darkstar/src/ports/platform"
	"darkstar/src/ports/workflowstore"
)

func TestContentCLIControlsDurableLibraryAndPreviewsPrompts(t *testing.T) {
	root := t.TempDir()
	paths := platformport.Paths{Config: filepath.Join(root, "config"), Data: filepath.Join(root, "data"), Cache: filepath.Join(root, "cache"), Logs: filepath.Join(root, "logs"), Runtime: filepath.Join(root, "runtime")}
	for _, directory := range []string{paths.Config, paths.Data, paths.Cache, paths.Logs, paths.Runtime} {
		if err := os.MkdirAll(directory, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	original := resolveApplicationPaths
	resolveApplicationPaths = func(context.Context) (platformport.Paths, error) {
		return paths, nil
	}
	t.Cleanup(func() {
		resolveApplicationPaths = original
	})
	service := startAcceptanceService(t, paths, "93939393939393939393939393939393")
	t.Cleanup(func() {
		_ = service.Close()
	})
	requestPath := filepath.Join(root, "request.json")
	writeRequest := func(value string) {
		t.Helper()
		if err := os.WriteFile(requestPath, []byte(value), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	runItem := func(args ...string) contentlibrary.Item {
		t.Helper()
		var item contentlibrary.Item
		runCLIJSON(t, append(append([]string{"content"}, args...), "--json"), &struct {
			Result *contentlibrary.Item `json:"result"`
		}{Result: &item})
		return item
	}
	writeRequest(`{"name":"Custom research","document":{"kind":"prompt","instructions":"Research only the supplied work item."}}`)
	item := runItem("create", requestPath)
	published := runItem("publish", item.ID, "1.0.0", "--revision", "1")
	if len(published.Versions) != 1 || published.Draft.Revision != 2 {
		t.Fatalf("publish: %#v", published)
	}
	writeRequest(`{"expectedRevision":2,"document":{"kind":"prompt","instructions":"Research the linked questions."}}`)
	updated := runItem("update", item.ID, requestPath)
	if updated.Draft.Revision != 3 || updated.Versions[0].Reference != published.Versions[0].Reference {
		t.Fatal("update changed the pinned version")
	}
	archived := runItem("archive", item.ID)
	if archived.ArchivedAt == nil {
		t.Fatal("archive did not persist")
	}
	restored := runItem("restore", item.ID)
	if restored.ArchivedAt != nil {
		t.Fatal("restore did not persist")
	}
	duplicate := runItem("duplicate", item.ID, "Alternate research")
	if duplicate.ID == item.ID || len(duplicate.Versions) != 0 {
		t.Fatal("duplicate reused identity or publications")
	}
	shown := runItem("show", item.ID)
	if shown.Versions[0].Reference != published.Versions[0].Reference {
		t.Fatal("show lost immutable publication")
	}
	var list struct {
		Result struct {
			Items []contentlibrary.Item `json:"items"`
		} `json:"result"`
	}
	runCLIJSON(t, []string{"content", "list", "--json"}, &list)
	if len(list.Result.Items) != len(contentlibrary.BuiltinItems())+2 {
		t.Fatalf("seeded library: %d items", len(list.Result.Items))
	}
	writeRequest(`{"document":{"kind":"prompt","instructions":"Research.","sections":[{"id":"unlinked","when":{"kind":"input_absent","input":"open_items"},"instructions":"Report new unknowns."}]},"linkedInputs":[],"revision":false}`)
	var preview struct {
		Result contentlibrary.PromptPreview `json:"result"`
	}
	runCLIJSON(t, []string{"content", "preview", requestPath, "--json"}, &preview)
	if len(preview.Result.Sections) != 1 || !preview.Result.Sections[0].Included {
		raw, _ := json.Marshal(preview)
		t.Fatalf("preview: %s", raw)
	}
	// Usage discovery reads the same authoritative draft references as the UI.
	linkedReference, _ := json.Marshal(published.Versions[0].Reference)
	linkedWorkflow := strings.Replace(cliWorkflowDocument(), "darkstar.local/v1alpha1", "darkstar.local/v1alpha3", 1)
	linkedWorkflow = strings.Replace(linkedWorkflow, `"reasoning":{"agent":"fake"}`, `"prompt":`+string(linkedReference)+`,"reasoning":{"agent":"fake"}`, 1)
	writeRequest(linkedWorkflow)
	var draft workflowstore.Draft
	runCLIJSON(t, []string{"workflow", "draft-create", requestPath, "--scope", "project", "--scope-reference", "project-test", "--idempotency-key", "content-usage-draft", "--json"}, &struct {
		Result *workflowstore.Draft `json:"result"`
	}{Result: &draft})
	var usages struct {
		Result struct {
			Usages []workflow.ContentUsage `json:"usages"`
		} `json:"result"`
	}
	runCLIJSON(t, []string{"workflow", "content-usage", item.ID, "--json"}, &usages)
	if len(usages.Result.Usages) != 1 || usages.Result.Usages[0].DraftID != draft.ID || usages.Result.Usages[0].NodeID != "finish" || usages.Result.Usages[0].Reference != published.Versions[0].Reference {
		t.Fatalf("usage lookup did not identify the pinned draft node: %#v", usages)
	}
	runCLIJSON(t, []string{"workflow", "content-usage", duplicate.ID, "--json"}, &usages)
	if len(usages.Result.Usages) != 0 {
		t.Fatalf("usage lookup leaked unrelated references: %#v", usages)
	}
}

func TestContentUsageCLIRejectsMissingOrExtraArgumentsBeforeConnection(t *testing.T) {
	for _, args := range [][]string{
		{"workflow", "content-usage", "--json"},
		{"workflow", "content-usage", " ", "--json"},
		{"workflow", "content-usage", "content", "extra", "--json"},
	} {
		var stdout, stderr bytes.Buffer
		if code := Run(args, &stdout, &stderr); code != int(ExitInvalidInput) {
			t.Fatalf("Run(%q)=%d: %s %s", args, code, stdout.String(), stderr.String())
		}
		var result machineErrorOutput
		if err := json.Unmarshal(stdout.Bytes(), &result); err != nil || result.Error.Code != "ARGUMENT_INVALID" || !strings.Contains(result.Error.Message, "content-usage <content-id>") {
			t.Fatalf("argument failure envelope: %s (%v)", stdout.String(), err)
		}
	}
}
