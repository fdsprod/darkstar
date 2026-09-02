package sqlite

import (
	"context"
	"strconv"
	"strings"
	"testing"

	"darkstar/src/ports/statestore"
)

func TestArtifactCheckpointProjectionPreservesRoundsAndDecisionEvidence(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	database := openEventTestDatabase(t)
	checkpointID := testID("checkpoint", 'C')
	runID := testID("run", 'C')
	visitID := testID("visit", 'C')
	attemptID := testID("attempt", 'C')
	artifactID := testID("artifact", 'C')
	firstID := testID("approval", 'C')
	secondID := testID("approval", 'D')
	policy := strings.Repeat("d", 64)

	first := pendingEvent(testID("event", 'C'), statestore.AggregateApproval, firstID, 0, "approval.requested",
		checkpointRequestJSON(runID, checkpointID, visitID, attemptID, artifactID, 1, 1, policy))
	decision := pendingEvent(testID("event", 'D'), statestore.AggregateApproval, firstID, 1, "approval.decided",
		`{"action":"request_changes","scopeDigest":"`+strings.Repeat("a", 64)+`","policyDigest":"`+policy+`","comment":"cover recovery"}`)
	decision.CommandID = "request-changes-key"
	decision.Actor = statestore.Actor{Type: statestore.ActorUser, ID: "reviewer"}
	second := pendingEvent(testID("event", 'E'), statestore.AggregateApproval, secondID, 0, "approval.requested",
		checkpointRequestJSON(runID, checkpointID, visitID, testID("attempt", 'D'), artifactID, 2, 2, policy))
	if _, err := database.Append(ctx, first, decision, second); err != nil {
		t.Fatal(err)
	}

	rounds, err := database.ApprovalsForCheckpoint(ctx, checkpointID)
	if err != nil || len(rounds) != 2 {
		t.Fatalf("ApprovalsForCheckpoint() = %#v, %v", rounds, err)
	}
	if rounds[0].ApprovalID != firstID || rounds[0].CheckpointRevision != 1 || rounds[0].Status != statestore.ApprovalChangesRequested {
		t.Fatalf("first round = %#v", rounds[0])
	}
	if rounds[0].Decision == nil || rounds[0].Decision.Action != "request_changes" || rounds[0].Decision.ActionKey != "request-changes-key" ||
		rounds[0].Decision.Comment != "cover recovery" || rounds[0].Decision.Actor.ID != "reviewer" || rounds[0].Decision.DecidedAt.IsZero() {
		t.Fatalf("decision evidence = %#v", rounds[0])
	}
	if rounds[1].ApprovalID != secondID || rounds[1].CandidateArtifactVersion != 2 || rounds[1].Status != statestore.ApprovalPending {
		t.Fatalf("second round = %#v", rounds[1])
	}
	committed, err := database.EventByCommand(ctx, firstID, "request-changes-key")
	if err != nil || committed.Kind != "approval.decided" || committed.Actor.ID != "reviewer" {
		t.Fatalf("EventByCommand() = %#v, %v", committed, err)
	}

	if err := database.RebuildProjections(ctx); err != nil {
		t.Fatal(err)
	}
	rebuilt, err := database.ApprovalsForCheckpoint(ctx, checkpointID)
	if err != nil || len(rebuilt) != 2 || rebuilt[0].Decision == nil || rebuilt[0].Decision.Comment != "cover recovery" || rebuilt[1].CandidateArtifactVersion != 2 {
		t.Fatalf("rebuilt rounds = %#v, %v", rebuilt, err)
	}
}

func TestArtifactCheckpointProjectionRejectsPartialSubjectsAndRepeatDecisions(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	database := openEventTestDatabase(t)
	runID := testID("run", 'F')
	approvalID := testID("approval", 'F')
	partial := pendingEvent(testID("event", 'F'), statestore.AggregateApproval, approvalID, 0, "approval.requested",
		`{"runId":"`+runID+`","class":"workflow_checkpoint","checkpointId":"checkpoint_partial","scopeDigest":"scope","policyDigest":"policy"}`)
	if _, err := database.Append(ctx, partial); err == nil {
		t.Fatal("partial checkpoint subject unexpectedly persisted")
	}

	checkpointID := testID("checkpoint", 'F')
	request := pendingEvent(testID("event", 'G'), statestore.AggregateApproval, approvalID, 0, "approval.requested",
		checkpointRequestJSON(runID, checkpointID, testID("visit", 'F'), testID("attempt", 'F'), testID("artifact", 'F'), 1, 1, strings.Repeat("e", 64)))
	approve := pendingEvent(testID("event", 'H'), statestore.AggregateApproval, approvalID, 1, "approval.decided",
		`{"action":"approve","scopeDigest":"`+strings.Repeat("a", 64)+`","policyDigest":"`+strings.Repeat("e", 64)+`"}`)
	if _, err := database.Append(ctx, request, approve); err != nil {
		t.Fatal(err)
	}
	reject := pendingEvent(testID("event", 'I'), statestore.AggregateApproval, approvalID, 2, "approval.decided",
		`{"action":"reject","scopeDigest":"`+strings.Repeat("a", 64)+`","policyDigest":"`+strings.Repeat("e", 64)+`"}`)
	if _, err := database.Append(ctx, reject); err == nil {
		t.Fatal("second decision unexpectedly persisted")
	}
}

func checkpointRequestJSON(runID, checkpointID, visitID, attemptID, artifactID string, artifactVersion, revision uint64, policy string) string {
	return `{"runId":"` + runID + `","class":"workflow_checkpoint","checkpointId":"` + checkpointID +
		`","visitId":"` + visitID + `","nodeId":"technical_design","attemptId":"` + attemptID +
		`","checkpointRevision":` + strconv.FormatUint(revision, 10) + `,"candidateArtifactId":"` + artifactID +
		`","candidateArtifactVersion":` + strconv.FormatUint(artifactVersion, 10) + `,"candidateDigest":"` + strings.Repeat("c", 64) +
		`","checkpointMode":"approve","maxRevisions":2,"scopeDigest":"` + strings.Repeat("a", 64) +
		`","policyDigest":"` + policy + `"}`
}
