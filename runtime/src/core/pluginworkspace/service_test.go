package pluginworkspace

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"darkstar/src/core/artifactingest"
	"darkstar/src/core/artifactops"
	"darkstar/src/ports/artifactbinding"
	"darkstar/src/ports/artifactregistry"
	"darkstar/src/ports/extension"
	"darkstar/src/ports/workspace"
)

type memoryWorkspace struct {
	grant workspace.Grant
	data  map[string][]byte
}

func (w *memoryWorkspace) Ensure(context.Context, string) error { return nil }
func (w *memoryWorkspace) Bind(_ context.Context, g workspace.Grant) (workspace.Handle, error) {
	w.grant = g
	return w, nil
}
func (w *memoryWorkspace) Close() error { return nil }
func (w *memoryWorkspace) ReplaceFile(_ context.Context, a workspace.Area, p, expected string, b []byte) error {
	k := string(a) + ":" + p
	old, ok := w.data[k]
	if !ok || digest(old) != expected {
		return errors.New("stale")
	}
	w.data[k] = append([]byte(nil), b...)
	return nil
}
func (w *memoryWorkspace) ReadFile(_ context.Context, a workspace.Area, p string) ([]byte, error) {
	b, ok := w.data[string(a)+":"+p]
	if !ok {
		return nil, errors.New("missing")
	}
	return append([]byte(nil), b...), nil
}
func (w *memoryWorkspace) WriteFile(_ context.Context, a workspace.Area, p string, b []byte) error {
	k := string(a) + ":" + p
	if _, ok := w.data[k]; ok {
		return errors.New("exists")
	}
	w.data[k] = append([]byte(nil), b...)
	return nil
}

type recordingArtifacts struct {
	inputs     []artifactops.IngestInput
	keys       []string
	bindings   []artifactops.AttachInput
	failAttach bool
}

func (a *recordingArtifacts) Ingest(_ context.Context, in artifactops.IngestInput, key string) (artifactingest.Result, error) {
	a.inputs = append(a.inputs, in)
	a.keys = append(a.keys, key)
	return artifactingest.Result{Artifact: artifactregistry.ArtifactVersion{ArtifactID: "artifact_test", Version: 1, BlobDigest: fmt.Sprintf("%x", sha256.Sum256(in.Content)), DetectedMediaType: in.MediaType}}, nil
}
func (a *recordingArtifacts) Attach(_ context.Context, in artifactops.AttachInput, _ string) (artifactbinding.Version, error) {
	a.bindings = append(a.bindings, in)
	if a.failAttach {
		a.failAttach = false
		return artifactbinding.Version{}, errors.New("interrupted")
	}
	return artifactbinding.Version{}, nil
}
func testService(t *testing.T) (*Service, *memoryWorkspace, *recordingArtifacts) {
	t.Helper()
	w := &memoryWorkspace{data: map[string][]byte{}}
	a := &recordingArtifacts{}
	s, err := New(w, a, Scope{WorkItemID: "work_one", RunID: "run_one", NodeID: "node_one", AttemptID: "attempt_one", Plugin: extension.Ref{ID: "test/files", Version: "1.0.0", Digest: strings.Repeat("a", 64)}})
	if err != nil {
		t.Fatal(err)
	}
	return s, w, a
}
func TestPublicationUsesBoundAttemptAndWorkItemAndCanReconcileAttachment(t *testing.T) {
	s, w, a := testService(t)
	ctx := context.Background()
	if _, err := s.Call(ctx, "workspace.write", json.RawMessage(`{"area":"staged","path":"notes.md","content":{"encoding":"utf8","text":"original"}}`)); err != nil {
		t.Fatal(err)
	}
	a.failAttach = true
	raw := json.RawMessage(`{"path":"notes.md","mediaType":"text/markdown","key":"publish-1"}`)
	if _, err := s.Call(ctx, "workspace.publish_artifact", raw); err == nil {
		t.Fatal("attachment failure hidden")
	}
	if _, err := s.Call(ctx, "workspace.publish_artifact", raw); err != nil {
		t.Fatal(err)
	}
	if a.keys[0] != a.keys[1] || a.inputs[0].GeneratedBy.AttemptID != "attempt_one" || a.bindings[1].Target.ID != "work_one" || a.bindings[1].Target.Kind != artifactbinding.TargetWork {
		t.Fatal("publication lost retry identity or ownership")
	}
	if w.grant != (workspace.Grant{WorkItemID: "work_one", PluginID: "test/files", AttemptID: "attempt_one"}) {
		t.Fatalf("grant=%+v", w.grant)
	}
	if !strings.Contains(a.inputs[0].Creator, "test/files@1.0.0#"+strings.Repeat("a", 64)) {
		t.Fatal("missing exact producer identity")
	}
	if _, err := s.Call(ctx, "workspace.write", json.RawMessage(`{"area":"staged","path":"notes.md","content":{"encoding":"utf8","text":"changed"}}`)); err == nil {
		t.Fatal("conflicting write accepted")
	}
	if string(a.inputs[0].Content) != "original" {
		t.Fatal("published content mutated")
	}
}
func TestWorkspaceCallsRejectOwnerInjectionAndInvalidContent(t *testing.T) {
	s, _, a := testService(t)
	for _, tc := range []struct{ method, raw string }{
		{"workspace.read", `{"area":"staged","path":"a","workItemId":"work_other"}`},
		{"workspace.write", `{"area":"staged","path":"a","content":{"encoding":"utf8","text":"a","data":"YQ=="}}`},
		{"workspace.write", `{"area":"staged","path":"a","content":{"encoding":"base64","data":"invalid"}}`},
		{"workspace.write", `{"area":"staged","path":"a","content":{"encoding":"utf8"}}`},
		{"workspace.publish_artifact", `{"path":"a","mediaType":"text/plain","key":""}`},
		{"approve", `{}`},
	} {
		if _, err := s.Call(context.Background(), tc.method, json.RawMessage(tc.raw)); err == nil {
			t.Fatalf("accepted %s %s", tc.method, tc.raw)
		}
	}
	if len(a.inputs) != 0 {
		t.Fatal("invalid calls published artifacts")
	}
}
func TestWorkspaceBinaryRoundTripAndIdenticalWriteRetry(t *testing.T) {
	s, _, _ := testService(t)
	raw := json.RawMessage(`{"area":"scratch","path":"binary","content":{"encoding":"base64","data":"AP8B"}}`)
	for i := 0; i < 2; i++ {
		if _, err := s.Call(context.Background(), "workspace.write", raw); err != nil {
			t.Fatal(err)
		}
	}
	value, err := s.Call(context.Background(), "workspace.read", json.RawMessage(`{"area":"scratch","path":"binary"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(value), `"data":"AP8B"`) {
		t.Fatalf("read=%s", value)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := s.Call(ctx, "workspace.read", json.RawMessage(`{}`)); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}
