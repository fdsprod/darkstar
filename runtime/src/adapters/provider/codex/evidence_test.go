package codex

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDirectoryEvidenceRecorderContainsAttemptPathsAndUsesDigest(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	recorder, err := NewDirectoryEvidenceRecorder(root)
	if err != nil {
		t.Fatalf("NewDirectoryEvidenceRecorder() error = %v", err)
	}
	evidence, err := recorder.Record(context.Background(), EvidenceRecord{
		AttemptID: "../../attempt:1",
		Sequence:  1,
		Kind:      "provider/event",
		MediaType: "application/json",
		Data:      []byte(`{"ok":true}`),
	})
	if err != nil {
		t.Fatalf("Record() error = %v", err)
	}
	relative, err := filepath.Rel(root, evidence.Ref)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) || filepath.IsAbs(relative) {
		t.Fatalf("evidence escaped root: ref=%s relative=%s error=%v", evidence.Ref, relative, err)
	}
	payload, err := os.ReadFile(evidence.Ref)
	if err != nil || string(payload) != `{"ok":true}` || len(evidence.Digest) != 64 {
		t.Fatalf("evidence = %#v payload=%s error=%v", evidence, payload, err)
	}
}
