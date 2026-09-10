package pluginworkspace_test

import (
	"context"
	"encoding/json"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"darkstar/src/adapters/artifactstore/folder"
	"darkstar/src/adapters/contentprocessor/common"
	"darkstar/src/adapters/statestore/sqlite"
	workspacefolder "darkstar/src/adapters/workspace/folder"
	"darkstar/src/core/artifactderive"
	"darkstar/src/core/artifactingest"
	"darkstar/src/core/artifactops"
	"darkstar/src/core/lateevidence"
	"darkstar/src/core/pluginworkspace"
	"darkstar/src/ports/artifactbinding"
	"darkstar/src/ports/artifactregistry"
	"darkstar/src/ports/extension"
)

type fixture struct {
	manager       *workspacefolder.Folder
	artifacts     *artifactops.Service
	database      *sqlite.Database
	workspaceRoot string
}

func newFixture(t *testing.T) fixture {
	t.Helper()
	ctx := context.Background()
	store, err := folder.New(filepath.Join(t.TempDir(), "artifacts"))
	if err != nil {
		t.Fatal(err)
	}
	db, err := sqlite.Open(ctx, filepath.Join(t.TempDir(), "state.db"), sqlite.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		db.Close()
	})
	derive, err := artifactderive.New(store, db, db, common.New())
	if err != nil {
		t.Fatal(err)
	}
	ingest, err := artifactingest.New(store, db, derive)
	if err != nil {
		t.Fatal(err)
	}
	impact, err := lateevidence.New(db, db, db, db, db)
	if err != nil {
		t.Fatal(err)
	}
	artifacts, err := artifactops.New(store, db, db, db, db, db, ingest, derive, impact)
	if err != nil {
		t.Fatal(err)
	}
	directory := t.TempDir()
	manager, err := workspacefolder.New(directory, workspacefolder.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		manager.Close()
	})
	return fixture{manager: manager, artifacts: artifacts, database: db, workspaceRoot: directory}
}

func (f fixture) service(t *testing.T, work, attempt string) *pluginworkspace.Service {
	t.Helper()
	s, err := pluginworkspace.New(f.manager, f.artifacts, pluginworkspace.Scope{
		WorkItemID: work, RunID: "run_" + work, NodeID: "produce", AttemptID: attempt,
		Plugin: extension.Ref{ID: "builtin/research", Version: "1.0.0", Digest: strings.Repeat("a", 64)},
	})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func call(t *testing.T, service *pluginworkspace.Service, method string, args any) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(args)
	if err != nil {
		t.Fatal(err)
	}
	result, err := service.Call(context.Background(), method, raw)
	if err != nil {
		t.Fatalf("%s: %v", method, err)
	}
	return result
}

func stage(t *testing.T, service *pluginworkspace.Service, name, text string) {
	t.Helper()
	call(t, service, "workspace.write", map[string]any{"area": "staged", "path": name, "content": map[string]string{"encoding": "utf8", "text": text}})
}

type publication struct {
	Artifact artifactregistry.VersionRef `json:"artifact"`
	Digest   string                      `json:"digest"`
}

func publish(t *testing.T, service *pluginworkspace.Service, name, key string) publication {
	t.Helper()
	raw := call(t, service, "workspace.publish_artifact", map[string]string{"path": name, "mediaType": "text/markdown", "key": key})
	var result publication
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatal(err)
	}
	return result
}

func original(t *testing.T, f fixture, ref artifactregistry.VersionRef) string {
	t.Helper()
	value, err := f.artifacts.OriginalContent(context.Background(), ref)
	if err != nil {
		t.Fatal(err)
	}
	defer value.Reader.Close()
	data, err := io.ReadAll(value.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestPublicationIsImmutableBoundToWorkAndRetrySafe(t *testing.T) {
	f := newFixture(t)
	s := f.service(t, "work_one", "attempt_one")
	stage(t, s, "note.md", "# Original\n")
	stage(t, s, "note.md", "# Original\n") // repeated write must reconcile
	first := publish(t, s, "note.md", "publication-one")
	repeated := publish(t, f.service(t, "work_one", "attempt_one"), "note.md", "publication-one")
	if first != repeated || first.Artifact.Version != 1 {
		t.Fatalf("publication retry changed reference: %#v / %#v", first, repeated)
	}
	target := artifactbinding.Target{Kind: artifactbinding.TargetWork, ID: "work_one"}
	listed, err := f.artifacts.List(context.Background(), artifactops.ListInput{Target: &target})
	if err != nil || len(listed) != 1 || listed[0].Artifact.ArtifactID != first.Artifact.ArtifactID || listed[0].Artifact.Version != first.Artifact.Version {
		t.Fatalf("work binding = %#v: %v", listed, err)
	}
	provenance, ok := listed[0].Artifact.Provenance.(artifactregistry.AttemptProvenance)
	if !ok || provenance.RunID != "run_work_one" || provenance.NodeID != "produce" || provenance.AttemptID != "attempt_one" {
		t.Fatalf("producer provenance = %#v", listed[0].Artifact.Provenance)
	}
	if !strings.Contains(listed[0].Artifact.Creator, "builtin/research@1.0.0#") {
		t.Fatalf("missing pinned producer: %s", listed[0].Artifact.Creator)
	}
	// Simulate a host-local edit after publication, independently of the plugin's
	// exclusive-write API. Artifact storage must retain the original snapshot.
	var candidate string
	if err := filepath.WalkDir(f.workspaceRoot, func(name string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !entry.IsDir() && entry.Name() == "note.md" {
			candidate = name
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if candidate == "" {
		t.Fatal("staged file missing")
	}
	if err := os.WriteFile(candidate, []byte("# Changed\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if content := original(t, f, first.Artifact); content != "# Original\n" {
		t.Fatalf("published snapshot changed: %q", content)
	}
	_, err = s.Call(context.Background(), "workspace.publish_artifact", json.RawMessage(`{"path":"note.md","mediaType":"text/markdown","key":"publication-one"}`))
	if err == nil {
		t.Fatal("same publication key accepted different bytes")
	}
	if content := original(t, f, first.Artifact); content != "# Original\n" {
		t.Fatalf("conflicting retry modified durable bytes: %q", content)
	}
	second := publish(t, s, "note.md", "publication-two")
	if second.Artifact == first.Artifact || second.Digest == first.Digest {
		t.Fatal("new publication reused stale snapshot")
	}
	if content := original(t, f, second.Artifact); content != "# Changed\n" {
		t.Fatalf("new snapshot = %q", content)
	}
}

func TestHostBindsWorkspaceAndPublicationOwners(t *testing.T) {
	f := newFixture(t)
	s := f.service(t, "work_one", "attempt_one")
	stage(t, s, "private.md", "private")
	first := publish(t, s, "private.md", "same-key")
	for _, tc := range []struct{ work, attempt string }{{"work_two", "attempt_one"}, {"work_one", "attempt_two"}} {
		other := f.service(t, tc.work, tc.attempt)
		if _, err := other.Call(context.Background(), "workspace.read", json.RawMessage(`{"area":"staged","path":"private.md"}`)); err == nil {
			t.Fatalf("%s/%s read another attempt's staged file", tc.work, tc.attempt)
		}
		if _, err := other.Call(context.Background(), "workspace.publish_artifact", json.RawMessage(`{"path":"private.md","mediaType":"text/markdown","key":"same-key"}`)); err == nil {
			t.Fatal("published unbound staged file")
		}
		stage(t, other, "private.md", "own content")
		own := publish(t, other, "private.md", "same-key")
		if own.Artifact == first.Artifact {
			t.Fatal("operation identity crossed scope")
		}
		if tc.work != "work_one" {
			target := artifactbinding.Target{Kind: artifactbinding.TargetWork, ID: tc.work}
			items, err := f.artifacts.List(context.Background(), artifactops.ListInput{Target: &target})
			if err != nil || len(items) != 1 || items[0].Artifact.ArtifactID != own.Artifact.ArtifactID {
				t.Fatalf("other work received incorrect bindings: %#v: %v", items, err)
			}
		}
	}
	if _, err := s.Call(context.Background(), "workspace.read", json.RawMessage(`{"area":"staged","path":"private.md","workItemId":"work_two"}`)); err == nil {
		t.Fatal("plugin selected another owner")
	}
	if _, err := s.Call(context.Background(), "workspace.publish_artifact", json.RawMessage(`{"path":"private.md","mediaType":"text/markdown","key":"another-key","artifactId":"selected-by-plugin"}`)); err == nil {
		t.Fatal("plugin selected artifact identity")
	}
}

func TestStagedCorrectionThroughWorkspaceAPIPreservesPublishedArtifact(t *testing.T) {
	f := newFixture(t)
	s := f.service(t, "work_one", "attempt_one")
	stage(t, s, "draft.md", "# First\n")
	first := publish(t, s, "draft.md", "first-revision")
	read := call(t, s, "workspace.read", map[string]string{"area": "staged", "path": "draft.md"})
	var snapshot struct {
		Digest string `json:"digest"`
	}
	if err := json.Unmarshal(read, &snapshot); err != nil || snapshot.Digest == "" {
		t.Fatalf("workspace snapshot omitted revision: %s: %v", read, err)
	}
	call(t, s, "workspace.write", map[string]any{
		"area": "staged", "path": "draft.md", "expectedDigest": snapshot.Digest,
		"content": map[string]string{"encoding": "utf8", "text": "# Corrected\n"},
	})
	second := publish(t, s, "draft.md", "corrected-revision")
	if first.Artifact == second.Artifact || first.Digest == second.Digest {
		t.Fatal("corrected publication reused the original revision")
	}
	if content := original(t, f, first.Artifact); content != "# First\n" {
		t.Fatalf("correction changed published original: %q", content)
	}
	if content := original(t, f, second.Artifact); content != "# Corrected\n" {
		t.Fatalf("corrected artifact contains stale bytes: %q", content)
	}
}
