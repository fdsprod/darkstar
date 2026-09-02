package pointexecution

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"testing"
	"time"

	"darkstar/src/core/attemptexecution"
	"darkstar/src/ports/statestore"
)

type capturingRunner struct {
	request attemptexecution.Request
	err     error
}

func (runner *capturingRunner) Run(_ context.Context, request attemptexecution.Request) (attemptexecution.Commit, error) {
	runner.request = request
	if runner.err != nil {
		return attemptexecution.Commit{}, runner.err
	}
	return attemptexecution.Commit{}, nil
}

type capturingFactory struct {
	builder attemptexecution.ContextBuilder
	runner  AttemptRunner
	err     error
}

func (factory *capturingFactory) New(builder attemptexecution.ContextBuilder) (AttemptRunner, error) {
	factory.builder = builder
	if factory.err != nil {
		return nil, factory.err
	}
	return factory.runner, nil
}

func validContract() CompletionContract {
	return CompletionContract{
		Outcome:           "ok",
		Evidence:          []string{"unit-test"},
		ValidationProfile: "default",
	}
}

func basePoint(id string, rev uint64, position int, story string) Point {
	return Point{
		ID:            id,
		StoryID:       story,
		Revision:      rev,
		Position:      position,
		Dependencies:  nil,
		Contract:      validContract(),
		Context:       ScopedContext{Data: json.RawMessage(`{"scope":"value"}`)},
		AffectedAreas: []string{"backend"},
		RiskTags:      nil,
	}
}

func baseApproval(mode ApprovalMode, riskTags []string) ApprovalPolicy {
	return ApprovalPolicy{
		Mode:     mode,
		RiskTags: riskTags,
	}
}

func basePlan(mode ApprovalMode, riskTags []string, points ...Point) Plan {
	return Plan{
		ID:             "plan-id",
		RunID:          "run-id",
		VisitID:        "visit-id",
		ExecutorKind:   "executor-kind",
		RepositoryID:   "repo-id",
		WorktreeID:     "worktree-id",
		AttemptTimeout: time.Second,
		Approval:       baseApproval(mode, riskTags),
		Points:         points,
	}
}

func requestFor(plan Plan, attemptID string, statuses map[string]statestore.PointStatus) ExecutionRequest {
	return ExecutionRequest{
		Plan:      plan,
		AttemptID: attemptID,
		Statuses:  statuses,
	}
}

func TestPlanValidateRejectsEmptyPlan(t *testing.T) {
	t.Parallel()

	plan := basePlan(ApprovalModeNone, nil)
	err := plan.Validate()
	if err == nil {
		t.Fatalf("expected validation error for empty plan")
	}
	if !strings.Contains(err.Error(), "plan must contain points") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestPlanValidateRejectsMissingAndSelfDependencies(t *testing.T) {
	t.Parallel()

	missing := basePlan(
		ApprovalModeNone,
		nil,
		basePoint("a", 1, 1, "story-id"),
	)
	missing.Points[0].Dependencies = []string{"does-not-exist"}
	if err := missing.Validate(); err == nil {
		t.Fatalf("expected missing dependency validation failure")
	}

	self := basePlan(
		ApprovalModeNone,
		nil,
		basePoint("a", 1, 1, "story-id"),
	)
	self.Points[0].Dependencies = []string{"a"}
	if err := self.Validate(); err == nil {
		t.Fatalf("expected self dependency validation failure")
	}
}

func TestPlanValidateRejectsCrossStoryDependenciesAndCycles(t *testing.T) {
	t.Parallel()

	pointA := basePoint("a", 1, 1, "story-a")
	pointB := basePoint("b", 1, 2, "story-b")
	pointB.Dependencies = []string{"a"}
	crossStory := basePlan(ApprovalModeNone, nil, pointA, pointB)
	if err := crossStory.Validate(); err == nil {
		t.Fatalf("expected cross-story dependency validation failure")
	}

	pointA.StoryID = "story-a"
	pointA.Dependencies = []string{"b"}
	pointB.StoryID = "story-a"
	cycle := basePlan(ApprovalModeNone, nil, pointA, pointB)
	if err := cycle.Validate(); err == nil {
		t.Fatalf("expected dependency cycle validation failure")
	}
}

func TestPlanValidateRejectsDuplicateIDsAndMissingRevision(t *testing.T) {
	t.Parallel()

	dup := basePlan(
		ApprovalModeNone,
		nil,
		basePoint("a", 1, 1, "story-id"),
		basePoint("a", 1, 2, "story-id"),
	)
	if err := dup.Validate(); err == nil {
		t.Fatalf("expected duplicate ID validation failure")
	}

	rev := basePlan(ApprovalModeNone, nil, basePoint("a", 0, 1, "story-id"))
	if err := rev.Validate(); err == nil {
		t.Fatalf("expected revision validation failure")
	}
}

func TestPlanValidateRejectsMissingAffectedAreas(t *testing.T) {
	t.Parallel()

	point := basePoint("a", 1, 1, "story-id")
	point.AffectedAreas = nil
	plan := basePlan(ApprovalModeNone, nil, point)
	if err := plan.Validate(); err == nil {
		t.Fatalf("expected affected areas validation failure")
	}
}

func TestPlanValidateRejectsNegativePositionAndUntrimmedValues(t *testing.T) {
	t.Parallel()

	point := basePoint("a", 1, -1, "story-id")
	plan := basePlan(ApprovalModeNone, nil, point)
	if err := plan.Validate(); err == nil {
		t.Fatalf("expected negative position validation failure")
	}

	point = basePoint("a", 1, 0, "story-id")
	point.Contract.Outcome = " outcome "
	plan = basePlan(ApprovalModeNone, nil, point)
	if err := plan.Validate(); err == nil {
		t.Fatalf("expected untrimmed completion outcome validation failure")
	}

	point = basePoint("a", 1, 0, "story-id")
	point.AffectedAreas = []string{" backend "}
	plan = basePlan(ApprovalModeNone, nil, point)
	if err := plan.Validate(); err == nil {
		t.Fatalf("expected untrimmed affected area validation failure")
	}
}

func TestPlanValidateAllowsOptionalRiskTags(t *testing.T) {
	t.Parallel()

	point := basePoint("a", 1, 1, "story-id")
	point.RiskTags = nil
	plan := basePlan(ApprovalModeNone, nil, point)
	if err := plan.Validate(); err != nil {
		t.Fatalf("unexpected validation error for nil risk tags: %v", err)
	}
}

func TestPlanValidateRejectsRiskModeWithoutSelectorTags(t *testing.T) {
	t.Parallel()

	plan := basePlan(
		ApprovalModeRisk,
		nil,
		basePoint("a", 1, 1, "story-id"),
	)
	if err := plan.Validate(); err == nil {
		t.Fatalf("expected risk mode selector validation error")
	}
}

func TestPlanValidateRejectsSelectorTagsOutsideRiskMode(t *testing.T) {
	t.Parallel()

	for _, mode := range []ApprovalMode{
		ApprovalModeNone,
		ApprovalModeEvery,
		ApprovalModeCombined,
	} {
		plan := basePlan(
			mode,
			[]string{"security"},
			basePoint("a", 1, 1, "story-id"),
		)
		if err := plan.Validate(); err == nil {
			t.Fatalf("expected selector tag validation failure for mode %q", mode)
		}
	}
}

func TestPlanReadyRequiresStatusAndDependencyStatuses(t *testing.T) {
	t.Parallel()

	plan := basePlan(
		ApprovalModeNone,
		nil,
		basePoint("a", 1, 2, "story-id"),
		basePoint("b", 1, 1, "story-id"),
		func() Point {
			p := basePoint("c", 1, 3, "story-id")
			p.Dependencies = []string{"a"}
			return p
		}(),
	)

	ready, err := plan.Ready(map[string]statestore.PointStatus{
		"a": statestore.PointAccepted,
		"b": statestore.PointReady,
		"c": statestore.PointReady,
	})
	if err != nil {
		t.Fatalf("unexpected validation error: %v", err)
	}
	if len(ready) != 2 {
		t.Fatalf("expected 2 ready points, got %d", len(ready))
	}
	if got, want := ready[0].ID, "b"; got != want {
		t.Fatalf("first ready point %q, want %q", got, want)
	}
	if got, want := ready[1].ID, "c"; got != want {
		t.Fatalf("second ready point %q, want %q", got, want)
	}

	ready, err = plan.Ready(map[string]statestore.PointStatus{
		"b": statestore.PointReady,
		"c": statestore.PointReady,
	})
	if err != nil {
		t.Fatalf("unexpected validation error: %v", err)
	}
	if len(ready) != 1 {
		t.Fatalf("expected only explicit ready point (b), got %d", len(ready))
	}

	ready, err = plan.Ready(map[string]statestore.PointStatus{
		"a": statestore.PointRunning,
		"b": statestore.PointReady,
		"c": statestore.PointReady,
	})
	if err != nil {
		t.Fatalf("unexpected validation error: %v", err)
	}
	if len(ready) != 1 {
		t.Fatalf("expected dependency-c gate to block c, got %d", len(ready))
	}

	ready, err = plan.Ready(map[string]statestore.PointStatus{
		"a": statestore.PointPublished,
		"b": statestore.PointReady,
		"c": statestore.PointReady,
	})
	if err != nil {
		t.Fatalf("unexpected validation error: %v", err)
	}
	if len(ready) != 2 {
		t.Fatalf("expected ready points when dependency is published, got %d", len(ready))
	}

	ready, err = plan.Ready(nil)
	if err != nil {
		t.Fatalf("unexpected validation error: %v", err)
	}
	if len(ready) != 0 {
		t.Fatalf("expected no points ready without statuses, got %d", len(ready))
	}
}

func TestServiceExecuteRequiresAttemptID(t *testing.T) {
	t.Parallel()

	point := basePoint("a", 1, 1, "story-id")
	plan := basePlan(ApprovalModeNone, nil, point)
	request := requestFor(plan, " attempt-id ", map[string]statestore.PointStatus{
		"a": statestore.PointReady,
	})

	svc, err := New(&capturingFactory{})
	if err != nil {
		t.Fatalf("unexpected service error %v", err)
	}
	if _, err := svc.Execute(context.Background(), request); err == nil {
		t.Fatalf("expected missing attempt ID validation error")
	}
}

func TestServiceExecuteUsesCallerAttemptIDAndPolicyActions(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		mode      ApprovalMode
		riskTags  []string
		want      NextAction
		revision  uint64
		attemptID string
	}{
		{name: "none", mode: ApprovalModeNone, riskTags: nil, want: NextActionContinue, revision: 2, attemptID: "attempt-none"},
		{name: "every", mode: ApprovalModeEvery, riskTags: nil, want: NextActionPauseForApproval, revision: 9, attemptID: "attempt-every"},
		{name: "risk-match", mode: ApprovalModeRisk, riskTags: []string{"security"}, want: NextActionPauseForApproval, revision: 3, attemptID: "attempt-risk-match"},
		{name: "risk-no-match", mode: ApprovalModeRisk, riskTags: []string{"finance"}, want: NextActionContinue, revision: 4, attemptID: "attempt-risk-nomatch"},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			runner := &capturingRunner{}
			factory := &capturingFactory{
				runner: runner,
			}
			svc, err := New(factory)
			if err != nil {
				t.Fatalf("unexpected service error %v", err)
			}
			point := basePoint("a", tc.revision, 1, "story-id")
			if tc.mode == ApprovalModeRisk && tc.name == "risk-match" {
				point.RiskTags = []string{"security", "privacy"}
			}
			if tc.mode == ApprovalModeRisk && tc.name == "risk-no-match" {
				point.RiskTags = []string{"privacy"}
			}

			result, err := svc.Execute(context.Background(), requestFor(basePlan(tc.mode, tc.riskTags, point), tc.attemptID, map[string]statestore.PointStatus{
				"a": statestore.PointReady,
			}))
			if err != nil {
				t.Fatalf("unexpected execute error: %v", err)
			}
			if result.Action != tc.want {
				t.Fatalf("action %q, want %q", result.Action, tc.want)
			}
			if result.Request.AttemptID != tc.attemptID {
				t.Fatalf("attempt id %q, want %q", result.Request.AttemptID, tc.attemptID)
			}
		})
	}
}

func TestServiceExecuteCombinedConsidersOnlyAcceptedCommittedPublishedAsComplete(t *testing.T) {
	t.Parallel()

	base := basePoint("current", 1, 1, "story-id")
	base2 := basePoint("other", 1, 2, "story-id")
	plan := basePlan(ApprovalModeCombined, nil, base, base2)

	svc, err := New(&capturingFactory{runner: &capturingRunner{}})
	if err != nil {
		t.Fatalf("unexpected service error %v", err)
	}
	first, err := svc.Execute(context.Background(), requestFor(plan, "attempt-1", map[string]statestore.PointStatus{
		"current": statestore.PointReady,
		"other":   statestore.PointFailed,
	}))
	if err != nil {
		t.Fatalf("unexpected execute error: %v", err)
	}
	if first.Action != NextActionContinue {
		t.Fatalf("combined action with failed other point = %q, want %q", first.Action, NextActionContinue)
	}

	plan.Points[1].Revision = 2
	svc, err = New(&capturingFactory{runner: &capturingRunner{}})
	if err != nil {
		t.Fatalf("unexpected service error %v", err)
	}
	final, err := svc.Execute(context.Background(), requestFor(plan, "attempt-2", map[string]statestore.PointStatus{
		"current": statestore.PointReady,
		"other":   statestore.PointAccepted,
	}))
	if err != nil {
		t.Fatalf("unexpected execute error: %v", err)
	}
	if final.Action != NextActionAwaitCombined {
		t.Fatalf("combined action with other accepted = %q, want %q", final.Action, NextActionAwaitCombined)
	}
}

func TestServiceExecuteTypedFrozenInputsIncludePointContractContext(t *testing.T) {
	t.Parallel()

	contextValue := json.RawMessage(`{"scope":{"owner":"unit-test"}}`)
	point := basePoint("a", 42, 1, "story-id")
	point.Context.Data = contextValue
	point.Contract = CompletionContract{
		Outcome:           "merged",
		Evidence:          []string{"ci-green", "security-scan"},
		ValidationProfile: "strict",
	}
	point.AffectedAreas = []string{"backend", "api"}
	point.RiskTags = []string{"security", "privacy"}

	runner := &capturingRunner{}
	factory := &capturingFactory{runner: runner}
	svc, err := New(factory)
	if err != nil {
		t.Fatalf("unexpected service error %v", err)
	}
	plan := basePlan(ApprovalModeRisk, []string{"security"}, point)
	_, err = svc.Execute(context.Background(), requestFor(plan, "attempt-typed", map[string]statestore.PointStatus{
		"a": statestore.PointReady,
	}))
	if err != nil {
		t.Fatalf("unexpected execute error: %v", err)
	}
	if factory.builder == nil {
		t.Fatalf("runner factory did not receive context builder")
	}

	snapshot, err := factory.builder.Build(context.Background(), runner.request, attemptexecution.Allocation{
		ID:              "allocation-id",
		Workspace:       "workspace-id",
		WorkspaceDigest: "digest",
	})
	if err != nil {
		t.Fatalf("builder build failed: %v", err)
	}
	type inputs struct {
		PointID       string             `json:"pointId"`
		StoryID       string             `json:"storyId"`
		Revision      uint64             `json:"revision"`
		Position      int                `json:"position"`
		Dependencies  []string           `json:"dependencies"`
		Completion    CompletionContract `json:"completionContract"`
		Context       ScopedContext      `json:"scopedContext"`
		AffectedAreas []string           `json:"affectedAreas"`
		RiskTags      []string           `json:"riskTags"`
	}
	var frozen inputs
	if err := json.Unmarshal(snapshot.Inputs, &frozen); err != nil {
		t.Fatalf("decode frozen inputs: %v", err)
	}
	if frozen.PointID != "a" || frozen.StoryID != "story-id" || frozen.Revision != 42 || frozen.Position != 1 {
		t.Fatalf("expected point metadata in frozen inputs")
	}
	if frozen.Completion.Outcome != "merged" {
		t.Fatalf("completion outcome %q, want %q", frozen.Completion.Outcome, "merged")
	}
	if !json.Valid(frozen.Context.Data) || string(frozen.Context.Data) != string(contextValue) {
		t.Fatalf("scoped context %q, want %q", frozen.Context.Data, contextValue)
	}
	if len(frozen.AffectedAreas) != 2 {
		t.Fatalf("expected affected areas in frozen inputs")
	}
	if len(frozen.RiskTags) != 2 {
		t.Fatalf("expected risk tags in frozen inputs")
	}

	originalInputs := append(json.RawMessage(nil), snapshot.Inputs...)
	snapshot.Inputs[0] = 'x'
	reloaded, err := factory.builder.Build(context.Background(), runner.request, attemptexecution.Allocation{
		ID:              "allocation-id",
		Workspace:       "workspace-id",
		WorkspaceDigest: "digest",
	})
	if err != nil {
		t.Fatalf("builder build failed: %v", err)
	}
	if string(reloaded.Inputs) != string(originalInputs) {
		t.Fatalf("context snapshot should not be mutated")
	}

	sortedRiskTags := append([]string(nil), plan.Approval.RiskTags...)
	sort.Strings(sortedRiskTags)
	policyBytes, err := json.Marshal(struct {
		Mode     ApprovalMode       `json:"mode"`
		RiskTags []string           `json:"riskTags"`
		Contract CompletionContract `json:"contract"`
	}{
		Mode:     plan.Approval.Mode,
		RiskTags: sortedRiskTags,
		Contract: point.Contract,
	})
	if err != nil {
		t.Fatalf("marshal policy: %v", err)
	}
	wantDigest := sha256.Sum256(policyBytes)
	if snapshot.PolicyDigest != hex.EncodeToString(wantDigest[:]) {
		t.Fatalf("policy digest mismatch")
	}
}

func TestServiceExecuteFactoryRunnerErrorPaths(t *testing.T) {
	t.Parallel()

	point := basePoint("a", 1, 1, "story-id")
	plan := basePlan(ApprovalModeNone, nil, point)

	if _, err := New(nil); err == nil {
		t.Fatalf("expected New(nil) error")
	}

	factoryErr := errors.New("factory failed")
	svc, err := New(&capturingFactory{
		err:    factoryErr,
		runner: &capturingRunner{},
	})
	if err != nil {
		t.Fatalf("unexpected service error %v", err)
	}
	if _, err := svc.Execute(context.Background(), requestFor(plan, "attempt", map[string]statestore.PointStatus{
		"a": statestore.PointReady,
	})); err == nil || !errors.Is(err, factoryErr) {
		t.Fatalf("expected factory error, got %v", err)
	}

	svc, err = New(&capturingFactory{runner: nil})
	if err != nil {
		t.Fatalf("unexpected service error %v", err)
	}
	if _, err := svc.Execute(context.Background(), requestFor(plan, "attempt", map[string]statestore.PointStatus{
		"a": statestore.PointReady,
	})); err == nil || !errors.Is(err, ErrNoRunner) {
		t.Fatalf("expected runner nil error, got %v", err)
	}
}
