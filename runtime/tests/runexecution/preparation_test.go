package runexecution_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"darkstar/src/core/attention"
	"darkstar/src/core/preparation"
	. "darkstar/src/core/runexecution"
	"darkstar/src/core/workflow"
	"darkstar/src/ports/routeadvisor"
	"darkstar/src/ports/statestore"
)

func TestPreparationFailureReplayIsDurableAndRetryUsesNewKey(t *testing.T) {
	s, db, _ := newControlTestService(t, false)
	_, work := seedWorkflowWork(t, db)
	planner := workflowDispatchPlannerFor(workflow.NoCheckpoint{}, true)
	_ = s.SetWorkflowPlanner(planner)
	calls := 0
	_ = s.SetRouteAdvisor(routeadvisor.AdvisorFunc(func(context.Context, routeadvisor.Request) (routeadvisor.Advice, error) {
		calls++
		return routeadvisor.Advice{}, errors.New("provider unavailable")
	}))
	request := CreateRequest{WorkItemID: work, WorkflowID: planner.preview.Workflow.Name}
	if _, err := s.Prepare(context.Background(), request, "assessment-failure"); err == nil {
		t.Fatal("expected provider failure")
	}
	if _, err := s.Prepare(context.Background(), request, "assessment-failure"); err == nil || errors.Is(err, ErrCommandInProgress) {
		t.Fatalf("failure replay=%v", err)
	}
	if calls != 1 {
		t.Fatal("replay invoked provider")
	}
	_, _ = s.Prepare(context.Background(), request, "assessment-retry")
	if calls != 2 {
		t.Fatal("new key did not retry provider")
	}
}

func TestPreparationQuestionsBlockResumeAndAnswersReachAdvisor(t *testing.T) {
	s, db, _ := newControlTestService(t, false)
	_, work := seedWorkflowWork(t, db)
	planner := workflowDispatchPlannerFor(workflow.NoCheckpoint{}, true)
	_ = s.SetWorkflowPlanner(planner)
	_ = s.SetRouteAdvisor(routeadvisor.AdvisorFunc(func(_ context.Context, input routeadvisor.Request) (routeadvisor.Advice, error) {
		item := routeadvisor.CandidateAdvice{Entry: "design", Terminals: []string{"design"}, Disposition: "input_required", Rationale: "Acceptance criteria are missing", Questions: []routeadvisor.Question{{ID: "acceptance", Prompt: "What must be verified?"}}}
		if input.Answers["acceptance"] != "" {
			item.Disposition = "suitable"
			item.Questions = nil
		}
		return routeadvisor.Advice{Confidence: "high", Candidates: []routeadvisor.CandidateAdvice{item}}, nil
	}))
	request := CreateRequest{WorkItemID: work, WorkflowID: planner.preview.Workflow.Name}
	waiting, err := s.Prepare(context.Background(), request, "assessment-questions")
	if err != nil {
		t.Fatal(err)
	}
	if waiting.Status != statestore.RunWaiting {
		t.Fatal(waiting.Status)
	}
	queue, queueErr := attention.New(db)
	if queueErr != nil {
		t.Fatal(queueErr)
	}
	page, queueErr := queue.List(context.Background(), attention.ListRequest{WorkItemID: work, IncludePreparation: true})
	if queueErr != nil || len(page.Items) != 1 {
		t.Fatalf("missing preparation attention: %v %v", page, queueErr)
	}
	if _, err := s.Resume(context.Background(), ControlRequest{RunID: waiting.RunID, ExpectedResourceVersion: waiting.ResourceVersion, IdempotencyKey: "resume-unanswered", Actor: statestore.Actor{Type: statestore.ActorUser, ID: "test"}}); err == nil {
		t.Fatal("resumed missing semantic input")
	}
	request.Preparation = &PreparationInput{Answers: map[string]string{"acceptance": "Verify output schema"}}
	ready, err := s.Prepare(context.Background(), request, "assessment-answered")
	if err != nil || ready.Status != statestore.RunReady {
		t.Fatalf("answered=%s %v", ready.Status, err)
	}
	previous, err := db.Run(context.Background(), waiting.RunID)
	if err != nil || previous.Status != statestore.RunCancelled {
		t.Fatalf("old preparation not superseded: %s %v", previous.Status, err)
	}
	page, queueErr = queue.List(context.Background(), attention.ListRequest{WorkItemID: work, IncludePreparation: true})
	if queueErr != nil || len(page.Items) != 0 {
		t.Fatalf("stale input attention: %v %v", page, queueErr)
	}
}

func TestPreparationResolvesSubmittedEvidenceReferencesIntoFrozenAssessment(t *testing.T) {
	s, db, _ := newControlTestService(t, false)
	_, work := seedWorkflowWork(t, db)
	planner := workflowDispatchPlannerFor(workflow.NoCheckpoint{}, true)
	_ = s.SetWorkflowPlanner(planner)
	_ = s.SetRouteEvidenceResolver(testEvidenceResolver(func(_ context.Context, reference string) (routeadvisor.Evidence, error) {
		if reference != "docs/plan.md" {
			return routeadvisor.Evidence{}, routeadvisor.ErrEvidenceUnavailable
		}
		return routeadvisor.Evidence{Reference: reference, Digest: strings.Repeat("d", 64), Content: "Accepted plan"}, nil
	}))
	_ = s.SetRouteAdvisor(routeadvisor.AdvisorFunc(func(_ context.Context, input routeadvisor.Request) (routeadvisor.Advice, error) {
		if len(input.Evidence) != 1 || input.Evidence[0].Reference != "docs/plan.md" || input.Evidence[0].Content != "Accepted plan" {
			t.Fatalf("advisor evidence = %#v", input.Evidence)
		}
		return routeadvisor.Advice{Confidence: "high", EvidenceUsed: []string{"docs/plan.md"}, Candidates: []routeadvisor.CandidateAdvice{{Entry: "design", Terminals: []string{"design"}, Disposition: "suitable", Rationale: "Evidence establishes the focused route."}}}, nil
	}))
	run, err := s.Prepare(context.Background(), CreateRequest{WorkItemID: work, WorkflowID: planner.preview.Workflow.Name, Preparation: &PreparationInput{Evidence: []string{" docs/plan.md ", "docs/plan.md"}}}, "assessment-evidence")
	if err != nil {
		t.Fatal(err)
	}
	view, err := s.Get(context.Background(), run.RunID)
	if err != nil || view.Assessment == nil || len(view.Assessment.Input.Evidence) != 1 || view.Assessment.Input.Evidence[0].Reference != "docs/plan.md" {
		t.Fatalf("frozen evidence = %#v, err=%v", view.Assessment, err)
	}
}

func TestPreparationAcceptsExplicitRouteOverrideForAutomaticWork(t *testing.T) {
	s, db, _ := newControlTestService(t, false)
	_, work := seedWorkflowWork(t, db)
	planner := workflowDispatchPlannerFor(workflow.NoCheckpoint{}, true)
	_ = s.SetWorkflowPlanner(planner)
	run, err := s.Prepare(context.Background(), CreateRequest{WorkItemID: work, WorkflowID: planner.preview.Workflow.Name, Preparation: &PreparationInput{RouteOverride: &workflow.RouteRequest{From: "design", Until: []workflow.Identifier{"design"}}}}, "assessment-route-override")
	if err != nil {
		t.Fatal(err)
	}
	view, err := s.Get(context.Background(), run.RunID)
	if err != nil || view.Assessment == nil || view.Assessment.Input.Override == nil || view.Assessment.Input.Override.From != "design" {
		t.Fatalf("frozen route override = %#v, err=%v", view.Assessment, err)
	}
}

type testEvidenceResolver func(context.Context, string) (routeadvisor.Evidence, error)

func (resolve testEvidenceResolver) Resolve(ctx context.Context, reference string) (routeadvisor.Evidence, error) {
	return resolve(ctx, reference)
}

func TestLaunchRejectsPolicyChangedAfterAssessment(t *testing.T) {
	s, db, _ := newControlTestService(t, false)
	_, work := seedWorkflowWork(t, db)
	planner := workflowDispatchPlannerFor(workflow.NoCheckpoint{}, true)
	_ = s.SetWorkflowPlanner(planner)
	policy := preparation.Policy{Version: "policy-1", RequiredNodes: []workflow.Identifier{}, ConsequentialNodes: []workflow.Identifier{}, AllowedAssumptions: []string{}}
	_ = s.SetPreparationPolicyResolver(func(context.Context, statestore.ProjectProjection) (preparation.Policy, error) {
		return policy, nil
	})
	run, err := s.Prepare(context.Background(), CreateRequest{WorkItemID: work, WorkflowID: planner.preview.Workflow.Name}, "assessment-policy")
	if err != nil {
		t.Fatal(err)
	}
	policy.Version = "policy-2"
	_, err = s.Launch(context.Background(), ControlRequest{RunID: run.RunID, ExpectedResourceVersion: run.ResourceVersion, IdempotencyKey: "launch-tightened-policy", Actor: statestore.Actor{Type: statestore.ActorUser, ID: "test"}})
	if err == nil || !strings.Contains(err.Error(), "policy changed") {
		t.Fatalf("changed policy launched: %v", err)
	}
}

func TestConfirmationRequiresExactDigestAndAuditsActor(t *testing.T) {
	s, db, _ := newControlTestService(t, false)
	if err := s.EnableQueue(func() (int, error) {
		return 3, nil
	}); err != nil {
		t.Fatal(err)
	}
	_, work := seedWorkflowWork(t, db)
	planner := workflowDispatchPlannerFor(workflow.NoCheckpoint{}, true)
	_ = s.SetWorkflowPlanner(planner)
	_ = s.SetRouteAdvisor(routeadvisor.AdvisorFunc(func(context.Context, routeadvisor.Request) (routeadvisor.Advice, error) {
		return routeadvisor.Advice{Confidence: "medium", Candidates: []routeadvisor.CandidateAdvice{{Entry: "design", Terminals: []string{"design"}, Disposition: "suitable", Rationale: "Outcome likely covered"}}}, nil
	}))
	run, err := s.Create(context.Background(), CreateRequest{WorkItemID: work, WorkflowID: planner.preview.Workflow.Name}, "assessment-confirm")
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != statestore.RunReady {
		t.Fatal("consequential create started automatically")
	}
	view, err := s.Get(context.Background(), run.RunID)
	if err != nil {
		t.Fatal(err)
	}
	control := ControlRequest{RunID: run.RunID, ExpectedResourceVersion: run.ResourceVersion, IdempotencyKey: "launch-no-confirm", Actor: statestore.Actor{Type: statestore.ActorUser, ID: "reviewer"}}
	if err := s.DispatchQueue(context.Background()); err != nil {
		t.Fatal(err)
	}
	attempts, _ := db.AttemptsForRun(context.Background(), run.RunID)
	if len(attempts) != 0 {
		t.Fatal("queue bypassed route confirmation")
	}
	if _, err := s.Launch(context.Background(), control); err == nil {
		t.Fatal("launched without digest")
	}
	control.IdempotencyKey = "launch-with-confirm"
	control.ConfirmationDigest = view.Assessment.Digest
	if _, err := s.Launch(context.Background(), control); err != nil {
		t.Fatal(err)
	}
	evidence, _ := db.RunEvidence(context.Background(), run.RunID)
	found := false
	for _, event := range evidence.Events {
		if event.Kind == "run.started" && event.Actor.ID == "reviewer" && strings.Contains(string(event.Data), view.Assessment.Digest) {
			found = true
		}
	}
	if !found {
		t.Fatal("confirmation not audited")
	}
}

func TestLaunchRejectsArchivedProjectButReplayPreservesAssessment(t *testing.T) {
	s, db, _ := newControlTestService(t, false)
	project, work := seedWorkflowWork(t, db)
	planner := workflowDispatchPlannerFor(workflow.NoCheckpoint{}, true)
	_ = s.SetWorkflowPlanner(planner)
	request := CreateRequest{WorkItemID: work, WorkflowID: planner.preview.Workflow.Name}
	run, err := s.Prepare(context.Background(), request, "assessment-freshness")
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Append(context.Background(), pendingEvent("project.archived", statestore.AggregateProject, project, 1, project, "archive-assessed", statestore.ActorUser, "test", time.Now(), map[string]any{}))
	if err != nil {
		t.Fatal(err)
	}
	replay, err := s.Prepare(context.Background(), request, "assessment-freshness")
	if err != nil || replay.RouteDigest != run.RouteDigest {
		t.Fatalf("frozen replay=%v", err)
	}
	_, err = s.Launch(context.Background(), ControlRequest{RunID: run.RunID, ExpectedResourceVersion: run.ResourceVersion, IdempotencyKey: "launch-stale-project", Actor: statestore.Actor{Type: statestore.ActorUser, ID: "test"}})
	if err == nil {
		t.Fatal("archived project launched")
	}
}
