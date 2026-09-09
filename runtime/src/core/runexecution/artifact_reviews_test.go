package runexecution

import (
	"context"
	"darkstar/src/adapters/artifactstore/folder"
	"darkstar/src/adapters/contentprocessor/common"
	"darkstar/src/adapters/contentprocessor/commonimage"
	"darkstar/src/core/artifactcheckpoint"
	"darkstar/src/core/artifactderive"
	"darkstar/src/core/artifactingest"
	"darkstar/src/core/artifactops"
	"darkstar/src/core/lateevidence"
	cp "darkstar/src/ports/artifactcheckpoint"
	"darkstar/src/ports/provider"
	"darkstar/src/ports/statestore"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
)

func TestArtifactReviewLoopsUntilExactHumanApproval(t *testing.T) {
	s, db, runID, visitID, attemptID := checkpointFixture(t)
	ctx := t.Context()
	s.schedulingAllowed = true
	blobs, err := folder.New(filepath.Join(t.TempDir(), "artifacts"))
	if err != nil {
		t.Fatal(err)
	}
	derive, err := artifactderive.New(blobs, db, db, common.New(), commonimage.New())
	if err != nil {
		t.Fatal(err)
	}
	ingest, err := artifactingest.New(blobs, db, derive)
	if err != nil {
		t.Fatal(err)
	}
	impact, err := lateevidence.New(db, db, db, db, db)
	if err != nil {
		t.Fatal(err)
	}
	artifacts, err := artifactops.New(blobs, db, db, db, db, db, ingest, derive, impact)
	if err != nil {
		t.Fatal(err)
	}
	reviewStore := &failReviewResponseStore{Store: db}
	reviews, err := artifactcheckpoint.New(reviewStore, db, db, blobs, db)
	if err != nil {
		t.Fatal(err)
	}
	run, _ := db.Run(ctx, runID)
	visit, _ := db.Node(ctx, visitID)
	attempt, _ := db.Attempt(ctx, attemptID)
	if err = s.completeWorkflowSucceeded(ctx, attempt, run, visit, provider.SucceededResult{StructuredOutput: json.RawMessage(`{"plan":"# Plan\nOriginal document"}`)}); err != nil {
		t.Fatal(err)
	}
	s.SetArtifactReviews(artifacts, reviews)
	for n := 0; n < 2; n++ {
		if err = s.ReconcileArtifactReviews(ctx); err != nil {
			t.Fatal(err)
		}
	}
	rounds, err := s.reviewRounds(ctx, visitID, "plan")
	if err != nil || len(rounds) != 1 {
		t.Fatalf("initial review: %v %v", rounds, err)
	}
	if rounds[0].MaxRevisions != nil {
		t.Fatal("review unexpectedly capped")
	}
	for n := 0; n < 3; n++ {
		a := rounds[len(rounds)-1]
		session, err := reviews.SubmitFeedback(ctx, artifactcheckpoint.FeedbackRequest{ApprovalID: a.ApprovalID, ExpectedResourceVersion: a.ResourceVersion, CandidateDigest: a.CandidateDigest, ScopeDigest: a.ScopeDigest, Message: fmt.Sprintf("Improve revision %d", n), IdempotencyKey: fmt.Sprintf("feedback-%d", n), Actor: statestore.Actor{Type: statestore.ActorUser, ID: "reviewer"}})
		if err != nil {
			t.Fatal(err)
		}
		id := stableID("attempt_", fmt.Sprintf("review:%s:%d", session.ID, session.ResourceVersion))
		s.workers[id] = &worker{} // Synthetic provider; never starts a real model.
		if err = s.ReconcileArtifactReviews(ctx); err != nil {
			t.Fatal(err)
		}
		attempt, err = db.Attempt(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		run, _ = db.Run(ctx, runID)
		request, err := s.workflowAttemptContext(ctx, attempt, run)
		if err != nil {
			t.Fatal(err)
		}
		if len(request.NodeInputs) != 2 || !strings.Contains(string(request.NodeInputs["feedback"]), "Improve revision") {
			t.Fatal("revision task not scoped to candidate and feedback")
		}
		for _, key := range []string{"runId", "attemptId", "candidateDigest", "actor"} {
			if strings.Contains(string(request.NodeInputs["feedback"]), "\""+key+"\"") {
				t.Fatalf("review bookkeeping leaked: %s", key)
			}
		}
		if len(request.Node.Fields().Inputs) != 2 {
			t.Fatal("revision lost declared inputs")
		}
		_, err = db.Append(ctx, pendingEvent("attempt.started", statestore.AggregateAttempt, id, attempt.ResourceVersion, runID, "start:"+id, statestore.ActorSystem, "test", s.now(), map[string]any{"providerThreadId": "revision-thread", "providerTurnId": "revision-turn", "processOwnerId": "test"}))
		if err != nil {
			t.Fatal(err)
		}
		attempt, _ = db.Attempt(ctx, id)
		visit, _ = db.Node(ctx, visitID)
		output, _ := json.Marshal(map[string]string{"plan": fmt.Sprintf("# Plan\nRevised document %d", n)})
		if n == 1 {
			reviewStore.failNext = true
		}
		err = s.completeWorkflowSucceeded(ctx, attempt, run, visit, provider.SucceededResult{StructuredOutput: output})
		if n == 1 {
			if err == nil {
				t.Fatal("crash boundary was not injected")
			}
			reviews, err = artifactcheckpoint.New(db, db, db, blobs, db)
			if err != nil {
				t.Fatal(err)
			}
			s.SetArtifactReviews(artifacts, reviews)
		} else if err != nil {
			t.Fatal(err)
		}
		if err = s.ReconcileArtifactReviews(ctx); err != nil {
			t.Fatal(err)
		}
		run, _ = db.Run(ctx, runID)
		if run.Status != statestore.RunWaiting {
			t.Fatalf("revision auto-advanced: %s", run.Status)
		}
		rounds, err = s.reviewRounds(ctx, visitID, "plan")
		if err != nil || len(rounds) != n+2 {
			t.Fatalf("revision missing: %d %v", len(rounds), err)
		}
	}
	old := rounds[0]
	if _, err = reviews.Decide(ctx, artifactcheckpoint.DecisionRequest{ApprovalID: old.ApprovalID, ExpectedResourceVersion: old.ResourceVersion, Action: cp.ActionApprove, CandidateDigest: old.CandidateDigest, ScopeDigest: old.ScopeDigest, PolicyDigest: old.PolicyDigest, IdempotencyKey: "stale", Actor: statestore.Actor{Type: statestore.ActorUser, ID: "reviewer"}}); err == nil {
		t.Fatal("stale approval accepted")
	}
	a := rounds[len(rounds)-1]
	_, err = reviews.Decide(ctx, artifactcheckpoint.DecisionRequest{ApprovalID: a.ApprovalID, ExpectedResourceVersion: a.ResourceVersion, Action: cp.ActionApprove, CandidateDigest: a.CandidateDigest, ScopeDigest: a.ScopeDigest, PolicyDigest: a.PolicyDigest, IdempotencyKey: "approve-final", Actor: statestore.Actor{Type: statestore.ActorUser, ID: "reviewer"}})
	if err != nil {
		t.Fatal(err)
	}
	if err = s.ReconcileArtifactReviews(ctx); err != nil {
		t.Fatal(err)
	}
	run, _ = db.Run(ctx, runID)
	if run.Status != statestore.RunCompleted {
		t.Fatalf("approved run not complete: %s", run.Status)
	}
	saved, _ := db.RunExecutionContext(ctx, runID)
	if !strings.Contains(string(saved.AcceptedOutputs["plan"]["plan"]), "Revised document 2") {
		t.Fatal("downstream output not approved revision")
	}
}

type failReviewResponseStore struct {
	cp.Store
	failNext bool
}

func (s *failReviewResponseStore) Append(ctx context.Context, events ...statestore.PendingEvent) ([]statestore.Event, error) {
	for _, e := range events {
		if e.Kind == "approval.agent_responded" && s.failNext {
			s.failNext = false
			return nil, errors.New("simulated stop before review response commit")
		}
	}
	return s.Store.Append(ctx, events...)
}
