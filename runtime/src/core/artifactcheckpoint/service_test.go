package artifactcheckpoint_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"testing"
	"time"

	checkpoint "darkstar/src/core/artifactcheckpoint"
	"darkstar/src/core/projection"
	checkpointport "darkstar/src/ports/artifactcheckpoint"
	"darkstar/src/ports/artifactlineage"
	"darkstar/src/ports/artifactregistry"
	"darkstar/src/ports/artifactstore"
	"darkstar/src/ports/contentprocessor"
	"darkstar/src/ports/representationregistry"
	"darkstar/src/ports/statestore"
)

func TestRevisionLoopPreservesDraftsFeedbackAndScopedEffects(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := newMemoryStore()
	artifacts := memoryArtifacts{values: map[artifactregistry.VersionRef]artifactregistry.ArtifactVersion{}}
	lineage := memoryLineage{values: map[artifactregistry.VersionRef][]artifactlineage.Invalidation{}}
	artifactID := "artifact_design"
	firstRef := artifactregistry.VersionRef{ArtifactID: artifactID, Version: 1}
	secondRef := artifactregistry.VersionRef{ArtifactID: artifactID, Version: 2}
	artifacts.values[firstRef] = artifactregistry.ArtifactVersion{ArtifactID: artifactID, Version: 1, BlobDigest: strings.Repeat("a", 64)}
	artifacts.values[secondRef] = artifactregistry.ArtifactVersion{ArtifactID: artifactID, Version: 2, BlobDigest: strings.Repeat("b", 64)}
	lineage.values[secondRef] = []artifactlineage.Invalidation{{
		Trigger: secondRef, Descendant: artifactregistry.VersionRef{ArtifactID: "artifact_story", Version: 1},
		Freshness: artifactlineage.FreshnessInvalidated, CreatedAt: time.Date(2026, 9, 2, 20, 0, 0, 0, time.UTC),
	}}
	service, err := checkpoint.New(store, artifacts, artifacts, artifacts, lineage)
	if err != nil {
		t.Fatal(err)
	}
	limit := uint64(2)
	firstRequest := openRequest("approval_first", "attempt_first", firstRef, &limit)
	first, err := service.Open(ctx, firstRequest)
	if err != nil {
		t.Fatal(err)
	}
	if first.Revision != 1 || first.State != checkpointport.StatePending || first.ScopeDigest == "" {
		t.Fatalf("first round = %#v", first)
	}
	replayed, err := service.Open(ctx, firstRequest)
	if err != nil || replayed.ApprovalID != first.ApprovalID || len(store.events) != 1 {
		t.Fatalf("Open(replay) = %#v, %v; events = %d", replayed, err, len(store.events))
	}

	revisionDecision := checkpoint.DecisionRequest{
		ApprovalID: first.ApprovalID, ExpectedResourceVersion: first.ResourceVersion,
		Action: checkpointport.ActionRequestChanges, ScopeDigest: first.ScopeDigest, PolicyDigest: first.PolicyDigest,
		Comment: "Keep the draft, but cover the crash-recovery path.", IdempotencyKey: "decision-revise", Actor: userActor(),
	}
	requested, err := service.Decide(ctx, revisionDecision)
	if err != nil {
		t.Fatal(err)
	}
	if requested.State != checkpointport.StateChangesRequested || requested.Decision == nil || requested.Decision.Effect != checkpointport.EffectStartRevision {
		t.Fatalf("request-changes round = %#v", requested)
	}

	secondRequest := openRequest("approval_second", "attempt_second", secondRef, &limit)
	secondRequest.IdempotencyKey = "open-second"
	second, err := service.Open(ctx, secondRequest)
	if err != nil {
		t.Fatal(err)
	}
	if second.Revision != 2 || second.State != checkpointport.StatePending || len(second.AffectedArtifacts) != 1 {
		t.Fatalf("second round = %#v", second)
	}
	approved, err := service.Decide(ctx, checkpoint.DecisionRequest{
		ApprovalID: second.ApprovalID, ExpectedResourceVersion: second.ResourceVersion,
		Action: checkpointport.ActionApprove, ScopeDigest: second.ScopeDigest, PolicyDigest: second.PolicyDigest,
		Comment: "Approved with recovery covered.", IdempotencyKey: "decision-approve", Actor: userActor(),
	})
	if err != nil || approved.State != checkpointport.StateApproved || approved.Decision.Effect != checkpointport.EffectAcceptCandidate {
		t.Fatalf("approve = %#v, %v", approved, err)
	}

	history, err := service.History(ctx, "checkpoint_design")
	if err != nil {
		t.Fatal(err)
	}
	if len(history.Rounds) != 2 || history.Rounds[0].Candidate != firstRef || history.Rounds[1].Candidate != secondRef {
		t.Fatalf("history = %#v", history)
	}
	if history.Rounds[0].Decision == nil || history.Rounds[0].Decision.Comment != revisionDecision.Comment {
		t.Fatalf("first-round feedback was not preserved: %#v", history.Rounds[0])
	}
	if len(history.Rounds[1].AffectedArtifacts) != 1 || history.Rounds[1].AffectedArtifacts[0].Descendant.ArtifactID != "artifact_story" {
		t.Fatalf("revision effects = %#v", history.Rounds[1].AffectedArtifacts)
	}
}

func TestDecisionIdempotencyIsBoundToExactPayload(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, artifacts, lineage := checkpointFixture()
	service, _ := checkpoint.New(store, artifacts, artifacts, artifacts, lineage)
	round, err := service.Open(ctx, openRequest("approval_one", "attempt_one", artifactregistry.VersionRef{ArtifactID: "artifact_design", Version: 1}, nil))
	if err != nil {
		t.Fatal(err)
	}
	request := checkpoint.DecisionRequest{ApprovalID: round.ApprovalID, ExpectedResourceVersion: round.ResourceVersion,
		Action: checkpointport.ActionApprove, ScopeDigest: round.ScopeDigest, PolicyDigest: round.PolicyDigest,
		IdempotencyKey: "same-action", Actor: userActor()}
	first, err := service.Decide(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	repeated, err := service.Decide(ctx, request)
	if err != nil || repeated.ResourceVersion != first.ResourceVersion || len(store.events) != 2 {
		t.Fatalf("Decide(replay) = %#v, %v; events = %d", repeated, err, len(store.events))
	}
	conflict := request
	conflict.Action = checkpointport.ActionReject
	conflict.Comment = "candidate is unsafe"
	if _, err := service.Decide(ctx, conflict); !errors.Is(err, checkpointport.ErrIdempotencyConflict) {
		t.Fatalf("conflicting replay error = %v", err)
	}
	newKey := request
	newKey.IdempotencyKey = "another-action"
	if _, err := service.Decide(ctx, newKey); !errors.Is(err, checkpointport.ErrAlreadyResolved) {
		t.Fatalf("new decision after resolution error = %v", err)
	}
	wrongScope := request
	wrongScope.ApprovalID = "approval_two"
	wrongScope.IdempotencyKey = "wrong-scope"
	secondOpen := openRequest("approval_two", "attempt_two", artifactregistry.VersionRef{ArtifactID: "artifact_design", Version: 1}, nil)
	secondOpen.CheckpointID = "checkpoint_other"
	second, err := service.Open(ctx, secondOpen)
	if err != nil {
		t.Fatal(err)
	}
	wrongScope.ExpectedResourceVersion = second.ResourceVersion
	wrongScope.ScopeDigest = strings.Repeat("f", 64)
	if _, err := service.Decide(ctx, wrongScope); !errors.Is(err, checkpoint.ErrCandidateConflict) {
		t.Fatalf("wrong candidate scope error = %v", err)
	}
}

func TestRevisionRequiresRequestChangesNextVersionAndBudget(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, artifacts, lineage := checkpointFixture()
	secondRef := artifactregistry.VersionRef{ArtifactID: "artifact_design", Version: 2}
	thirdRef := artifactregistry.VersionRef{ArtifactID: "artifact_design", Version: 3}
	artifacts.values[secondRef] = artifactregistry.ArtifactVersion{ArtifactID: secondRef.ArtifactID, Version: 2, BlobDigest: strings.Repeat("b", 64)}
	artifacts.values[thirdRef] = artifactregistry.ArtifactVersion{ArtifactID: thirdRef.ArtifactID, Version: 3, BlobDigest: strings.Repeat("c", 64)}
	service, _ := checkpoint.New(store, artifacts, artifacts, artifacts, lineage)
	limit := uint64(1)
	first, err := service.Open(ctx, openRequest("approval_one", "attempt_one", artifactregistry.VersionRef{ArtifactID: "artifact_design", Version: 1}, &limit))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Open(ctx, openRequest("approval_early", "attempt_early", secondRef, &limit)); !errors.Is(err, checkpointport.ErrRevisionRequired) {
		t.Fatalf("early revision error = %v", err)
	}
	requestChanges(t, ctx, service, first, "revise-one")
	wrong := openRequest("approval_wrong", "attempt_wrong", thirdRef, &limit)
	if _, err := service.Open(ctx, wrong); !errors.Is(err, checkpoint.ErrCheckpointConflict) {
		t.Fatalf("non-consecutive revision error = %v", err)
	}
	second, err := service.Open(ctx, openRequest("approval_two", "attempt_two", secondRef, &limit))
	if err != nil {
		t.Fatal(err)
	}
	requestChanges(t, ctx, service, second, "revise-two")
	if _, err := service.Open(ctx, openRequest("approval_three", "attempt_three", thirdRef, &limit)); !errors.Is(err, checkpointport.ErrRevisionLimit) {
		t.Fatalf("revision budget error = %v", err)
	}
}

func TestReviewSessionPreservesTurnsRejectsStaleCandidateAndReconstructsHistory(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, artifacts, lineage := checkpointFixture()
	secondRef := artifactregistry.VersionRef{ArtifactID: "artifact_design", Version: 2}
	artifacts.values[secondRef] = artifactregistry.ArtifactVersion{ArtifactID: secondRef.ArtifactID, Version: 2, BlobDigest: strings.Repeat("b", 64)}
	service, _ := checkpoint.New(store, artifacts, artifacts, artifacts, lineage)
	first, err := service.Open(ctx, openRequest("approval_one", "attempt_one", artifactregistry.VersionRef{ArtifactID: "artifact_design", Version: 1}, nil))
	if err != nil {
		t.Fatal(err)
	}

	feedbackRequest := roundFeedbackRequest(first, "Add a restart-safe recovery section.", "feedback-one")
	draft := checkpoint.FeedbackDraftRequest{ApprovalID: first.ApprovalID, ExpectedResourceVersion: first.ResourceVersion, Candidate: first.Candidate,
		CandidateDigest: first.CandidateDigest, ScopeDigest: first.ScopeDigest, PolicyDigest: first.PolicyDigest, Representation: feedbackRequest.Representation}
	if err := service.ValidateFeedbackSetDraft(ctx, draft); err != nil {
		t.Fatalf("valid draft binding = %v", err)
	}
	draft.Representation.Digest = strings.Repeat("f", 64)
	if err := service.ValidateFeedbackSetDraft(ctx, draft); !errors.Is(err, checkpoint.ErrCandidateConflict) {
		t.Fatalf("changed representation draft error = %v", err)
	}
	feedbackRequest.Annotations = []checkpointport.FeedbackAnnotation{{ID: "annotation_recovery", Anchor: checkpointport.TextRangeAnchor{
		StartOffset: 6, EndOffset: 11, QuotedText: "café", QuoteDigest: sha256Text("café")}, Comment: "Explain the multibyte case."}}
	wrongAnchor := feedbackRequest
	wrongAnchor.IdempotencyKey = "feedback-wrong-anchor"
	wrongAnchor.Annotations = append([]checkpointport.FeedbackAnnotation(nil), feedbackRequest.Annotations...)
	wrongAnchor.Annotations[0].Anchor.StartOffset = 7
	wrongAnchor.Annotations[0].Anchor.EndOffset = 12
	if _, err := service.SubmitFeedback(ctx, wrongAnchor); !errors.Is(err, checkpoint.ErrCandidateConflict) {
		t.Fatalf("wrong offset with self-consistent quote digest error = %v", err)
	}
	afterFeedback, err := service.SubmitFeedback(ctx, feedbackRequest)
	if err != nil || afterFeedback.State != checkpointport.ReviewAwaitingAgent || len(afterFeedback.Turns) != 1 {
		t.Fatalf("feedback = %#v, %v", afterFeedback, err)
	}
	human := afterFeedback.Turns[0].(checkpointport.HumanFeedbackTurn)
	if human.FeedbackSet == nil || len(human.FeedbackSet.Digest) != 64 || human.FeedbackSet.ScopeDigest != first.ScopeDigest || human.FeedbackSet.PolicyDigest != first.PolicyDigest ||
		human.FeedbackSet.Receipt.EventID == "" || human.FeedbackSet.Receipt.AggregateRevision != 2 || human.FeedbackSet.Author.ID != "reviewer" ||
		len(human.FeedbackSet.Annotations) != 1 || human.FeedbackSet.Annotations[0].Anchor.QuotedText != "café" {
		t.Fatalf("submitted feedback set = %#v", human.FeedbackSet)
	}
	replayed, err := service.SubmitFeedback(ctx, feedbackRequest)
	if err != nil || replayed.ResourceVersion != afterFeedback.ResourceVersion || len(store.events) != 2 {
		t.Fatalf("feedback replay = %#v, %v; events=%d", replayed, err, len(store.events))
	}
	conflictingFeedback := feedbackRequest
	conflictingFeedback.OverallInstruction = "Different feedback under the same key."
	if _, err := service.SubmitFeedback(ctx, conflictingFeedback); !errors.Is(err, checkpointport.ErrIdempotencyConflict) {
		t.Fatalf("conflicting feedback replay error = %v", err)
	}

	resumed, err := service.ResumeRevision(ctx, checkpoint.ResumeRequest{ApprovalID: first.ApprovalID, ExpectedResourceVersion: afterFeedback.ResourceVersion,
		CandidateDigest: first.CandidateDigest, ScopeDigest: first.ScopeDigest, AttemptID: "attempt_revision_one", IdempotencyKey: "resume-one",
		Actor: statestore.Actor{Type: statestore.ActorSystem, ID: "coordinator"}})
	if err != nil {
		t.Fatal(err)
	}
	if resumed.ActiveIteration == nil || resumed.ActiveIteration.AttemptID != "attempt_revision_one" || resumed.ActiveIteration.RunID != "run_one" {
		t.Fatalf("active iteration = %#v", resumed.ActiveIteration)
	}
	if resumed.ActiveIteration.FeedbackSetID != human.FeedbackSet.ID || resumed.ActiveIteration.FeedbackSetDigest != human.FeedbackSet.Digest {
		t.Fatalf("revision attempt feedback binding = %#v", resumed.ActiveIteration)
	}
	next, err := service.RecordAgentResponse(ctx, checkpoint.AgentResponseRequest{ApprovalID: first.ApprovalID, ExpectedResourceVersion: resumed.ResourceVersion,
		CandidateDigest: first.CandidateDigest, ScopeDigest: first.ScopeDigest, AttemptID: "attempt_revision_one", Outcome: checkpointport.AgentRevised,
		Message: "Added recovery details.", Candidate: secondRef, NextApprovalID: "approval_two", IdempotencyKey: "response-one",
		Actor: statestore.Actor{Type: statestore.ActorProvider, ID: "codex"}})
	if err != nil || next.State != checkpointport.ReviewAwaitingHuman || next.Candidate != secondRef {
		t.Fatalf("agent response = %#v, %v", next, err)
	}
	if next.ActiveIteration != nil {
		t.Fatalf("completed iteration remained active: %#v", next.ActiveIteration)
	}
	if _, err := service.Decide(ctx, checkpoint.DecisionRequest{ApprovalID: first.ApprovalID, ExpectedResourceVersion: resumed.ResourceVersion,
		Action: checkpointport.ActionApprove, CandidateDigest: first.CandidateDigest, ScopeDigest: first.ScopeDigest,
		PolicyDigest: first.PolicyDigest, IdempotencyKey: "stale-tab", Actor: userActor()}); !errors.Is(err, checkpoint.ErrCandidateConflict) {
		t.Fatalf("stale decision error = %v", err)
	}
	history, err := service.ReviewHistory(ctx, "checkpoint_design")
	if err != nil || len(history.Sessions) != 2 || history.Sessions[0].State != checkpointport.ReviewSuperseded || len(history.Sessions[0].Turns) != 2 {
		t.Fatalf("review history = %#v, %v", history, err)
	}
}

func TestLegacyFeedbackMessageRemainsReplayable(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, artifacts, lineage := checkpointFixture()
	service, _ := checkpoint.New(store, artifacts, artifacts, artifacts, lineage)
	first, err := service.Open(ctx, openRequest("approval_legacy", "attempt_legacy", artifactregistry.VersionRef{ArtifactID: "artifact_design", Version: 1}, nil))
	if err != nil {
		t.Fatal(err)
	}
	request := checkpoint.FeedbackRequest{ApprovalID: first.ApprovalID, ExpectedResourceVersion: first.ResourceVersion,
		CandidateDigest: first.CandidateDigest, ScopeDigest: first.ScopeDigest, Message: "Preserve the original endpoint.", IdempotencyKey: "legacy-feedback", Actor: userActor()}
	result, err := service.SubmitFeedback(ctx, request)
	if err != nil || len(result.Turns) != 1 {
		t.Fatalf("legacy feedback = %#v, %v", result, err)
	}
	turn := result.Turns[0].(checkpointport.HumanFeedbackTurn)
	if turn.Message != request.Message || turn.FeedbackSet != nil {
		t.Fatalf("legacy turn = %#v", turn)
	}
	replayed, err := service.SubmitFeedback(ctx, request)
	if err != nil || replayed.ResourceVersion != result.ResourceVersion || len(store.events) != 2 {
		t.Fatalf("legacy replay = %#v, %v", replayed, err)
	}
}

func TestReviewSessionAgentFailureAndCancellationReturnCandidateToHuman(t *testing.T) {
	t.Parallel()
	for _, outcome := range []checkpointport.AgentOutcome{checkpointport.AgentFailed, checkpointport.AgentCancelled} {
		outcome := outcome
		t.Run(string(outcome), func(t *testing.T) {
			ctx := context.Background()
			store, artifacts, lineage := checkpointFixture()
			service, _ := checkpoint.New(store, artifacts, artifacts, artifacts, lineage)
			first, _ := service.Open(ctx, openRequest("approval_one", "attempt_one", artifactregistry.VersionRef{ArtifactID: "artifact_design", Version: 1}, nil))
			feedback, _ := service.SubmitFeedback(ctx, roundFeedbackRequest(first, "Revise.", "feedback"))
			resumed, _ := service.ResumeRevision(ctx, checkpoint.ResumeRequest{ApprovalID: first.ApprovalID, ExpectedResourceVersion: feedback.ResourceVersion,
				CandidateDigest: first.CandidateDigest, ScopeDigest: first.ScopeDigest, AttemptID: "attempt_retry", IdempotencyKey: "resume", Actor: userActor()})
			result, err := service.RecordAgentResponse(ctx, checkpoint.AgentResponseRequest{ApprovalID: first.ApprovalID, ExpectedResourceVersion: resumed.ResourceVersion,
				CandidateDigest: first.CandidateDigest, ScopeDigest: first.ScopeDigest, AttemptID: "attempt_retry", Outcome: outcome, Message: "provider stopped",
				IdempotencyKey: "response", Actor: statestore.Actor{Type: statestore.ActorProvider, ID: "codex"}})
			if err != nil || result.State != checkpointport.ReviewAwaitingHuman || result.CandidateDigest != first.CandidateDigest || len(result.Turns) != 2 {
				t.Fatalf("result = %#v, %v", result, err)
			}
		})
	}
}

func TestReviewSessionSurfacesRevisionLimitBeforeReplacingCandidate(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, artifacts, lineage := checkpointFixture()
	secondRef := artifactregistry.VersionRef{ArtifactID: "artifact_design", Version: 2}
	artifacts.values[secondRef] = artifactregistry.ArtifactVersion{ArtifactID: secondRef.ArtifactID, Version: 2, BlobDigest: strings.Repeat("b", 64)}
	service, _ := checkpoint.New(store, artifacts, artifacts, artifacts, lineage)
	limit := uint64(1)
	first, _ := service.Open(ctx, openRequest("approval_one", "attempt_one", artifactregistry.VersionRef{ArtifactID: "artifact_design", Version: 1}, &limit))
	feedback, _ := service.SubmitFeedback(ctx, roundFeedbackRequest(first, "one", "f1"))
	resumed, _ := service.ResumeRevision(ctx, checkpoint.ResumeRequest{ApprovalID: first.ApprovalID, ExpectedResourceVersion: feedback.ResourceVersion, CandidateDigest: first.CandidateDigest, ScopeDigest: first.ScopeDigest, AttemptID: "attempt_two", IdempotencyKey: "r1", Actor: userActor()})
	second, err := service.RecordAgentResponse(ctx, checkpoint.AgentResponseRequest{ApprovalID: first.ApprovalID, ExpectedResourceVersion: resumed.ResourceVersion, CandidateDigest: first.CandidateDigest, ScopeDigest: first.ScopeDigest, AttemptID: "attempt_two", Outcome: checkpointport.AgentRevised, Candidate: secondRef, NextApprovalID: "approval_two", IdempotencyKey: "a1", Actor: userActor()})
	if err != nil {
		t.Fatal(err)
	}
	if !second.RevisionLimitReached {
		t.Fatal("second candidate did not surface the exhausted revision limit")
	}
	for _, action := range second.AllowedActions {
		if action == checkpointport.ActionRequestChanges {
			t.Fatalf("exhausted session allows request_changes: %#v", second.AllowedActions)
		}
	}
	_, err = service.SubmitFeedback(ctx, feedbackRequest(second, "two", "f2"))
	if !errors.Is(err, checkpointport.ErrRevisionLimit) {
		t.Fatalf("revision limit error = %v", err)
	}
}

func checkpointFixture() (*memoryStore, memoryArtifacts, memoryLineage) {
	store := newMemoryStore()
	ref := artifactregistry.VersionRef{ArtifactID: "artifact_design", Version: 1}
	return store,
		memoryArtifacts{values: map[artifactregistry.VersionRef]artifactregistry.ArtifactVersion{ref: {ArtifactID: ref.ArtifactID, Version: 1, BlobDigest: strings.Repeat("a", 64)}}},
		memoryLineage{values: map[artifactregistry.VersionRef][]artifactlineage.Invalidation{}}
}

func openRequest(approvalID, attemptID string, candidate artifactregistry.VersionRef, limit *uint64) checkpoint.OpenRequest {
	return checkpoint.OpenRequest{
		ApprovalID: approvalID, CheckpointID: "checkpoint_design", RunID: "run_one", VisitID: "visit_one",
		NodeID: "technical_design", AttemptID: attemptID, Candidate: candidate,
		Mode: checkpointport.ModeApprove, MaxRevisions: limit, PolicyDigest: strings.Repeat("d", 64),
		IdempotencyKey: "open-first", Actor: statestore.Actor{Type: statestore.ActorSystem, ID: "daemon"},
	}
}

func requestChanges(t *testing.T, ctx context.Context, service *checkpoint.Service, round checkpointport.Round, key string) {
	t.Helper()
	_, err := service.Decide(ctx, checkpoint.DecisionRequest{
		ApprovalID: round.ApprovalID, ExpectedResourceVersion: round.ResourceVersion,
		Action: checkpointport.ActionRequestChanges, ScopeDigest: round.ScopeDigest, PolicyDigest: round.PolicyDigest,
		Comment: "revise", IdempotencyKey: key, Actor: userActor(),
	})
	if err != nil {
		t.Fatal(err)
	}
}

func userActor() statestore.Actor {
	return statestore.Actor{Type: statestore.ActorUser, ID: "reviewer"}
}

func sha256Text(value string) string {
	digest := sha256.Sum256([]byte(value))
	return hex.EncodeToString(digest[:])
}

func roundFeedbackRequest(session checkpointport.Round, instruction, key string) checkpoint.FeedbackRequest {
	return checkpoint.FeedbackRequest{ApprovalID: session.ApprovalID, ExpectedResourceVersion: session.ResourceVersion,
		FeedbackSetID: "feedbackset_" + key, Candidate: session.Candidate, CandidateDigest: session.CandidateDigest, ScopeDigest: session.ScopeDigest, PolicyDigest: session.PolicyDigest,
		Representation:     checkpointport.TextRepresentationBinding{RepresentationID: fmt.Sprintf("representation_text_%d", session.Candidate.Version), Digest: strings.Repeat("e", 64), Disclosure: representationregistry.DisclosureRaw},
		OverallInstruction: instruction, Annotations: []checkpointport.FeedbackAnnotation{}, IdempotencyKey: key, Actor: userActor()}
}

func feedbackRequest(session checkpointport.ReviewSession, instruction, key string) checkpoint.FeedbackRequest {
	return roundFeedbackRequest(checkpointport.Round{ApprovalID: session.ID, Candidate: session.Candidate, CandidateDigest: session.CandidateDigest,
		ScopeDigest: session.ScopeDigest, PolicyDigest: session.PolicyDigest, ResourceVersion: session.ResourceVersion}, instruction, key)
}

type memoryArtifacts struct {
	values map[artifactregistry.VersionRef]artifactregistry.ArtifactVersion
}

func (store memoryArtifacts) ArtifactVersion(_ context.Context, ref artifactregistry.VersionRef) (artifactregistry.ArtifactVersion, error) {
	value, ok := store.values[ref]
	if !ok {
		return artifactregistry.ArtifactVersion{}, artifactregistry.ErrNotFound
	}
	if value.Sensitivity == "" {
		value.Sensitivity = artifactregistry.SensitivityPublic
	}
	return value, nil
}

func (store memoryArtifacts) Representation(_ context.Context, id string) (representationregistry.Representation, error) {
	var version uint64
	if _, err := fmt.Sscanf(id, "representation_text_%d", &version); err != nil {
		return representationregistry.Representation{}, representationregistry.ErrNotFound
	}
	ref := artifactregistry.VersionRef{ArtifactID: "artifact_design", Version: version}
	if _, ok := store.values[ref]; !ok {
		return representationregistry.Representation{}, representationregistry.ErrNotFound
	}
	return representationregistry.Representation{RepresentationID: id, Artifact: ref, Kind: contentprocessor.RepresentationText,
		MediaType: "text/plain", Locator: artifactstore.Locator("memory:text"), Digest: strings.Repeat("e", 64), Disclosure: representationregistry.DisclosureRaw}, nil
}

func (store memoryArtifacts) Open(_ context.Context, _ artifactstore.OpenRequest) (io.ReadCloser, error) {
	return io.NopCloser(strings.NewReader("alpha café omega")), nil
}

type memoryLineage struct {
	values map[artifactregistry.VersionRef][]artifactlineage.Invalidation
}

func (lineage memoryLineage) AffectedBy(_ context.Context, ref artifactregistry.VersionRef) ([]artifactlineage.Invalidation, error) {
	return append([]artifactlineage.Invalidation(nil), lineage.values[ref]...), nil
}

type memoryStore struct {
	approvals map[string]statestore.ApprovalProjection
	events    map[string]statestore.Event
	node      statestore.NodeProjection
	position  uint64
}

func newMemoryStore() *memoryStore {
	return &memoryStore{
		approvals: map[string]statestore.ApprovalProjection{}, events: map[string]statestore.Event{},
		node: statestore.NodeProjection{VisitID: "visit_one", RunID: "run_one", NodeID: "technical_design", Status: statestore.NodeWaitingCheckpoint},
	}
}

func (store *memoryStore) Append(_ context.Context, pending ...statestore.PendingEvent) ([]statestore.Event, error) {
	result := make([]statestore.Event, 0, len(pending))
	for _, item := range pending {
		key := item.AggregateID + "\x00" + item.CommandID
		if existing, ok := store.events[key]; ok {
			if existing.Kind != item.Kind || existing.Actor != item.Actor || string(existing.Data) != string(item.Data) {
				return nil, checkpointport.ErrIdempotencyConflict
			}
			result = append(result, existing)
			continue
		}
		current, exists := store.approvals[item.AggregateID]
		if uint64(0) != item.ExpectedRevision && (!exists || current.ResourceVersion != item.ExpectedRevision) {
			return nil, errors.New("revision conflict")
		}
		store.position++
		recordedAt := time.Date(2026, 9, 2, 19, 0, int(store.position), 0, time.UTC)
		event := statestore.Event{SchemaVersion: item.SchemaVersion, ID: item.ID, GlobalPosition: store.position,
			AggregateType: item.AggregateType, AggregateID: item.AggregateID, AggregateRevision: item.ExpectedRevision + 1,
			Kind: item.Kind, OccurredAt: item.OccurredAt, RecordedAt: recordedAt, CorrelationID: item.CorrelationID,
			CommandID: item.CommandID, Actor: item.Actor, Data: append(json.RawMessage(nil), item.Data...), Metadata: json.RawMessage(`{}`)}
		var currentPointer *statestore.ApprovalProjection
		if exists {
			currentCopy := current
			currentPointer = &currentCopy
		}
		next, applies, err := projection.ReduceApproval(currentPointer, event)
		if err != nil || !applies {
			return nil, err
		}
		store.approvals[item.AggregateID] = next
		store.events[key] = event
		result = append(result, event)
	}
	return result, nil
}

func (store *memoryStore) Approval(_ context.Context, id string) (statestore.ApprovalProjection, error) {
	value, ok := store.approvals[id]
	if !ok {
		return statestore.ApprovalProjection{}, statestore.ErrNotFound
	}
	return value, nil
}

func (store *memoryStore) ApprovalsForCheckpoint(_ context.Context, id string) ([]statestore.ApprovalProjection, error) {
	values := make([]statestore.ApprovalProjection, 0)
	for _, value := range store.approvals {
		if value.CheckpointID == id {
			values = append(values, value)
		}
	}
	sort.Slice(values, func(left, right int) bool { return values[left].CheckpointRevision < values[right].CheckpointRevision })
	return values, nil
}

func (store *memoryStore) CheckpointApprovals(_ context.Context, runID string, status statestore.ApprovalStatus) ([]statestore.ApprovalProjection, error) {
	values := make([]statestore.ApprovalProjection, 0)
	for _, value := range store.approvals {
		if value.Class == statestore.ApprovalWorkflowCheckpoint && value.Status == status && (runID == "" || value.RunID == runID) {
			values = append(values, value)
		}
	}
	return values, nil
}

func (store *memoryStore) EventByCommand(_ context.Context, aggregateID, commandID string) (statestore.Event, error) {
	value, ok := store.events[aggregateID+"\x00"+commandID]
	if !ok {
		return statestore.Event{}, statestore.ErrNotFound
	}
	return value, nil
}

func (store *memoryStore) EventsForAggregate(_ context.Context, aggregateID string) ([]statestore.Event, error) {
	values := make([]statestore.Event, 0)
	for _, event := range store.events {
		if event.AggregateID == aggregateID {
			values = append(values, event)
		}
	}
	if len(values) == 0 {
		return nil, statestore.ErrNotFound
	}
	sort.Slice(values, func(left, right int) bool { return values[left].AggregateRevision < values[right].AggregateRevision })
	return values, nil
}

func (store *memoryStore) Node(_ context.Context, id string) (statestore.NodeProjection, error) {
	if id != store.node.VisitID {
		return statestore.NodeProjection{}, statestore.ErrNotFound
	}
	return store.node, nil
}
