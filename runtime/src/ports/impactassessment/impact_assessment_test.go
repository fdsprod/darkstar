package impactassessment

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestProposalVariantsMarshalClosedActionDiscriminators(t *testing.T) {
	t.Parallel()
	proposals := []Proposal{
		ContinueProposal{Reason: "future"}, RefreshProposal{AttemptID: "attempt_one", Reason: "missing"},
		ReviseProposal{Artifacts: []ArtifactEffect{}, Reason: "stale"},
		InsertProposal{RunID: "run_one", Roles: []string{}, Reason: "focus"},
		InvalidateProposal{Artifacts: []ArtifactEffect{}, Reason: "invalid"},
	}
	content, err := json.Marshal(proposals)
	if err != nil {
		t.Fatal(err)
	}
	for _, action := range []Action{ActionContinue, ActionRefresh, ActionRevise, ActionInsert, ActionInvalidate} {
		if !strings.Contains(string(content), `"action":"`+string(action)+`"`) {
			t.Fatalf("marshaled proposals omit %q discriminator: %s", action, content)
		}
	}
}

func TestAssessmentMarshalsVersionedBoundary(t *testing.T) {
	t.Parallel()
	content, err := json.Marshal(Assessment{Roles: []string{}, Coverage: []AttemptCoverage{}, Proposals: []Proposal{ContinueProposal{Reason: "future"}}})
	if err != nil || !strings.Contains(string(content), `"kind":"impact_assessment","schemaVersion":1`) {
		t.Fatalf("assessment boundary = %s, %v", content, err)
	}
}
