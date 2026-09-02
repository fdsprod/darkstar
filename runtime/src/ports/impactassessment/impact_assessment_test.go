package impactassessment

import (
	"encoding/json"
	"strings"
	"testing"

	"darkstar/src/ports/artifactbinding"
	"darkstar/src/ports/artifactregistry"
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

func TestAssessmentRoundTripsClosedProposalVariants(t *testing.T) {
	t.Parallel()
	original := Assessment{
		Evidence: artifactregistry.VersionRef{ArtifactID: "artifact_one", Version: 2},
		Target:   artifactbinding.Target{Kind: artifactbinding.TargetRun, ID: "run_one"},
		Roles:    []string{"evidence"},
		Coverage: []AttemptCoverage{},
		Proposals: []Proposal{
			ContinueProposal{Reason: "future"},
			RefreshProposal{AttemptID: "attempt_one", Reason: "missing"},
			ReviseProposal{Artifacts: []ArtifactEffect{}, Reason: "stale"},
			InsertProposal{RunID: "run_one", Roles: []string{"evidence"}, Reason: "focus"},
			InvalidateProposal{Artifacts: []ArtifactEffect{}, Reason: "invalid"},
		},
	}
	content, err := json.Marshal(original)
	if err != nil {
		t.Fatal(err)
	}
	var decoded Assessment
	if err := json.Unmarshal(content, &decoded); err != nil {
		t.Fatal(err)
	}
	want := []Action{ActionContinue, ActionRefresh, ActionRevise, ActionInsert, ActionInvalidate}
	if len(decoded.Proposals) != len(want) {
		t.Fatalf("decoded proposals = %d, want %d", len(decoded.Proposals), len(want))
	}
	for index, action := range want {
		if decoded.Proposals[index].Action() != action {
			t.Fatalf("decoded proposal %d action = %q, want %q", index, decoded.Proposals[index].Action(), action)
		}
	}
}
