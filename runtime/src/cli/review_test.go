package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReadFeedbackSetDraftPreservesExactBindingsAndUTF8ByteOffsets(t *testing.T) {
	t.Parallel()
	name := filepath.Join(t.TempDir(), "feedback-set.json")
	content := `{"schemaVersion":1,"state":"draft","id":"feedbackset_00000000000000000000000000","approvalId":"approval_00000000000000000000000000","candidate":{"artifactId":"artifact_candidate","version":3},"candidateDigest":"` + strings.Repeat("a", 64) + `","scopeDigest":"` + strings.Repeat("b", 64) + `","policyDigest":"` + strings.Repeat("c", 64) + `","representation":{"representationId":"representation_safe","digest":"` + strings.Repeat("d", 64) + `","disclosure":"redacted"},"overallInstruction":"Revise the highlighted text.","annotations":[{"id":"annotation_one","anchor":{"startOffset":1,"endOffset":5,"quoteDigest":"` + strings.Repeat("e", 64) + `","quotedText":"éx"},"comment":"Be precise."}]}`
	if err := os.WriteFile(name, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	draft, err := readFeedbackSetDraft(name, "approval_00000000000000000000000000")
	if err != nil {
		t.Fatal(err)
	}
	if draft.Candidate.Version != 3 || draft.Representation.RepresentationID != "representation_safe" || draft.PolicyDigest != strings.Repeat("c", 64) || len(draft.Annotations) != 1 || draft.Annotations[0].Anchor.StartOffset != 1 || draft.Annotations[0].Anchor.EndOffset != 5 || draft.Annotations[0].Anchor.QuotedText != "éx" {
		t.Fatalf("draft = %#v", draft)
	}
}

func TestReadFeedbackSetDraftRejectsMissingInstructionAndApprovalMismatch(t *testing.T) {
	t.Parallel()
	name := filepath.Join(t.TempDir(), "feedback-set.json")
	content := `{"schemaVersion":1,"state":"draft","id":"feedbackset_00000000000000000000000000","approvalId":"approval_other","candidate":{"artifactId":"artifact_candidate","version":3},"candidateDigest":"` + strings.Repeat("a", 64) + `","scopeDigest":"` + strings.Repeat("b", 64) + `","policyDigest":"` + strings.Repeat("c", 64) + `","representation":{"representationId":"representation_safe","digest":"` + strings.Repeat("d", 64) + `","disclosure":"raw"},"overallInstruction":"","annotations":[]}`
	if err := os.WriteFile(name, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readFeedbackSetDraft(name, "approval_00000000000000000000000000"); err == nil {
		t.Fatal("invalid draft was accepted")
	}
}
