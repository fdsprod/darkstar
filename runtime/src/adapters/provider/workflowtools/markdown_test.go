package workflowtools

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestMarkdownSnapshotsKeepVersionsAndIgnoreExistingFiles(t *testing.T) {
	ctx := t.Context()
	root := t.TempDir()
	if output, err := exec.Command("git", "-C", root, "init").CombinedOutput(); err != nil {
		t.Fatalf("%s: %v", output, err)
	}
	file := filepath.Join(root, "README.md")
	os.WriteFile(file, []byte("# Original"), 0600)
	s := &Session{Database: filepath.Join(t.TempDir(), "tools.db"), RunID: "run", AttemptID: "attempt", Workspace: root}
	if err := s.PrepareMarkdown(ctx); err != nil {
		t.Fatal(err)
	}
	if err := s.CaptureMarkdown(ctx, "first"); err != nil {
		t.Fatal(err)
	}
	rows, err := ReadRunArtifacts(ctx, s.Database, s.RunID, 0, 100)
	if err != nil || len(rows) != 0 {
		t.Fatalf("unchanged files reported: %v %v", rows, err)
	}
	for i, content := range []string{"# Updated", "# Final"} {
		os.WriteFile(file, []byte(content), 0600)
		if err := s.CaptureMarkdown(ctx, string(rune('a'+i))); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.PrepareMarkdown(ctx); err != nil {
		t.Fatal(err)
	}
	if err := s.CaptureMarkdown(ctx, "reconnected"); err != nil {
		t.Fatal(err)
	}
	rows, err = ReadRunArtifacts(ctx, s.Database, s.RunID, 0, 100)
	if err != nil || len(rows) != 2 || rows[0].Content != `"# Updated"` || rows[1].Content != `"# Final"` {
		t.Fatalf("snapshots: %#v %v", rows, err)
	}
	other, err := ReadRunArtifacts(ctx, s.Database, "other", 0, 100)
	if err != nil || len(other) != 0 {
		t.Fatal("cross-run artifact leak")
	}
}
