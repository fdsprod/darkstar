package pointexecution

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"darkstar/src/core/attemptexecution"
	"darkstar/src/ports/statestore"
)

var (
	ErrInvalidPlan  = errors.New("invalid point execution plan")
	ErrNoReadyPoint = errors.New("no point is ready")
	ErrNoFactory    = errors.New("runner factory is required")
	ErrNoBuilder    = errors.New("context builder is required")
	ErrNoRunner     = errors.New("runner is required")
	ErrNoAttemptID  = errors.New("attempt ID is required")
)

type ApprovalMode string

const (
	ApprovalModeNone     ApprovalMode = "none"
	ApprovalModeEvery    ApprovalMode = "every"
	ApprovalModeRisk     ApprovalMode = "risk"
	ApprovalModeCombined ApprovalMode = "combined"
)

type ApprovalPolicy struct {
	Mode     ApprovalMode `json:"mode"`
	RiskTags []string     `json:"riskTags"`
}

type CompletionContract struct {
	Outcome           string   `json:"outcome"`
	Evidence          []string `json:"evidence"`
	ValidationProfile string   `json:"validationProfile"`
}

type ScopedContext struct {
	Data json.RawMessage `json:"data"`
}

type Point struct {
	ID            string             `json:"id"`
	StoryID       string             `json:"storyId"`
	Revision      uint64             `json:"revision"`
	Position      int                `json:"position"`
	Dependencies  []string           `json:"dependencies"`
	Contract      CompletionContract `json:"contract"`
	Context       ScopedContext      `json:"context"`
	AffectedAreas []string           `json:"affectedAreas"`
	RiskTags      []string           `json:"riskTags"`
}

type Plan struct {
	ID             string         `json:"id"`
	RunID          string         `json:"runId"`
	VisitID        string         `json:"visitId"`
	ExecutorKind   string         `json:"executorKind"`
	RepositoryID   string         `json:"repositoryId"`
	WorktreeID     string         `json:"worktreeId"`
	AttemptTimeout time.Duration  `json:"attemptTimeout"`
	Approval       ApprovalPolicy `json:"approval"`
	Points         []Point        `json:"points"`
}

type ExecutionRequest struct {
	Plan      Plan
	Statuses  map[string]statestore.PointStatus
	AttemptID string
}

type NextAction string

const (
	NextActionContinue         NextAction = "continue"
	NextActionPauseForApproval NextAction = "pause_for_approval"
	NextActionAwaitCombined    NextAction = "await_combined_approval"
)

type ExecutionResult struct {
	Point     Point
	Action    NextAction
	AttemptID string
	Request   attemptexecution.Request
	Commit    attemptexecution.Commit
}

type AttemptRunner interface {
	Run(context.Context, attemptexecution.Request) (attemptexecution.Commit, error)
}

type RunnerFactory interface {
	New(attemptexecution.ContextBuilder) (AttemptRunner, error)
}

type RunnerFactoryFunc func(attemptexecution.ContextBuilder) (AttemptRunner, error)

func (creator RunnerFactoryFunc) New(builder attemptexecution.ContextBuilder) (AttemptRunner, error) {
	return creator(builder)
}

type Service struct {
	runnerFactory RunnerFactory
}

func New(factory RunnerFactory) (*Service, error) {
	if factory == nil {
		return nil, ErrNoFactory
	}
	return &Service{runnerFactory: factory}, nil
}

func (plan Plan) Validate() error {
	for _, field := range []struct {
		name, value string
	}{
		{"plan ID", plan.ID},
		{"run ID", plan.RunID},
		{"visit ID", plan.VisitID},
		{"executor kind", plan.ExecutorKind},
		{"repository ID", plan.RepositoryID},
		{"worktree ID", plan.WorktreeID},
	} {
		if strings.TrimSpace(field.value) == "" || strings.TrimSpace(field.value) != field.value {
			return fmt.Errorf("%w: %s is required", ErrInvalidPlan, field.name)
		}
	}

	if plan.AttemptTimeout <= 0 {
		return fmt.Errorf("%w: attempt timeout must be positive", ErrInvalidPlan)
	}
	if len(plan.Points) == 0 {
		return fmt.Errorf("%w: plan must contain points", ErrInvalidPlan)
	}

	if err := validateApprovalPolicy(plan.Approval); err != nil {
		return fmt.Errorf("%w: %s", ErrInvalidPlan, err)
	}

	pointByID := map[string]Point{}
	for _, point := range plan.Points {
		pointByID[point.ID] = point
	}
	seen := map[string]struct{}{}
	for _, point := range plan.Points {
		if strings.TrimSpace(point.ID) == "" || strings.TrimSpace(point.ID) != point.ID {
			return fmt.Errorf("%w: point ID is required and must be trimmed", ErrInvalidPlan)
		}
		if _, exists := seen[point.ID]; exists {
			return fmt.Errorf("%w: duplicate point ID %q", ErrInvalidPlan, point.ID)
		}
		seen[point.ID] = struct{}{}
		if point.Revision == 0 {
			return fmt.Errorf("%w: point %q revision must be greater than 0", ErrInvalidPlan, point.ID)
		}
		if point.Position < 0 {
			return fmt.Errorf("%w: point %q position must be non-negative", ErrInvalidPlan, point.ID)
		}
		if strings.TrimSpace(point.StoryID) == "" || strings.TrimSpace(point.StoryID) != point.StoryID {
			return fmt.Errorf("%w: point %q story ID is required and must be trimmed", ErrInvalidPlan, point.ID)
		}
		if err := point.Contract.validate(); err != nil {
			return fmt.Errorf("%w: point %q %w", ErrInvalidPlan, point.ID, err)
		}
		if !json.Valid(point.ContextData()) {
			return fmt.Errorf("%w: point %q scoped context must be valid JSON", ErrInvalidPlan, point.ID)
		}
		if len(point.AffectedAreas) == 0 {
			return fmt.Errorf("%w: point %q affected areas are required", ErrInvalidPlan, point.ID)
		}
		if err := validateDistinctTrimmedList(point.AffectedAreas, "affected area", point.ID); err != nil {
			return err
		}
		if len(point.RiskTags) > 0 {
			if err := validateDistinctTrimmedList(point.RiskTags, "risk tag", point.ID); err != nil {
				return err
			}
		}

		dependencies := map[string]struct{}{}
		for _, dependency := range point.Dependencies {
			if strings.TrimSpace(dependency) == "" || strings.TrimSpace(dependency) != dependency {
				return fmt.Errorf("%w: point %q has invalid dependency %q", ErrInvalidPlan, point.ID, dependency)
			}
			if dependency == point.ID {
				return fmt.Errorf("%w: point %q has self dependency", ErrInvalidPlan, point.ID)
			}
			if _, exists := dependencies[dependency]; exists {
				return fmt.Errorf("%w: point %q has duplicate dependency %q", ErrInvalidPlan, point.ID, dependency)
			}
			dependencies[dependency] = struct{}{}
			dependencyPoint, ok := pointByID[dependency]
			if !ok {
				return fmt.Errorf("%w: point %q depends on missing point %q", ErrInvalidPlan, point.ID, dependency)
			}
			if dependencyPoint.StoryID != point.StoryID {
				return fmt.Errorf("%w: point %q depends on point %q in another story", ErrInvalidPlan, point.ID, dependency)
			}
		}
	}

	dependencyState := map[string]int{}
	var walk func(string) error
	walk = func(id string) error {
		switch dependencyState[id] {
		case 1:
			return fmt.Errorf("%w: dependency cycle contains %q", ErrInvalidPlan, id)
		case 2:
			return nil
		}
		dependencyState[id] = 1
		current := pointByID[id]
		for _, dependency := range current.Dependencies {
			if err := walk(dependency); err != nil {
				return err
			}
		}
		dependencyState[id] = 2
		return nil
	}
	for _, point := range plan.Points {
		if err := walk(point.ID); err != nil {
			return err
		}
	}
	return nil
}

func (plan Plan) Ready(statuses map[string]statestore.PointStatus) ([]Point, error) {
	if err := plan.Validate(); err != nil {
		return nil, err
	}

	ready := make([]Point, 0, len(plan.Points))
	for _, point := range plan.Points {
		if !point.isReady(statuses) {
			continue
		}
		ready = append(ready, point)
	}

	sort.Slice(ready, func(a, b int) bool {
		if ready[a].Position != ready[b].Position {
			return ready[a].Position < ready[b].Position
		}
		return ready[a].ID < ready[b].ID
	})
	return ready, nil
}

func (svc *Service) Execute(ctx context.Context, exec ExecutionRequest) (ExecutionResult, error) {
	if svc == nil || svc.runnerFactory == nil {
		return ExecutionResult{}, ErrNoFactory
	}
	if strings.TrimSpace(exec.AttemptID) == "" || strings.TrimSpace(exec.AttemptID) != exec.AttemptID {
		return ExecutionResult{}, fmt.Errorf("%w and must be trimmed", ErrNoAttemptID)
	}
	plan := exec.Plan
	statuses := exec.Statuses

	points, err := plan.Ready(statuses)
	if err != nil {
		return ExecutionResult{}, err
	}
	if len(points) == 0 {
		return ExecutionResult{}, ErrNoReadyPoint
	}

	point := points[0]
	builder, err := newPointContextBuilder(plan, point)
	if err != nil {
		return ExecutionResult{}, err
	}
	if builder == nil {
		return ExecutionResult{}, ErrNoBuilder
	}

	attemptRunner, err := svc.runnerFactory.New(builder)
	if err != nil {
		return ExecutionResult{}, err
	}
	if attemptRunner == nil {
		return ExecutionResult{}, ErrNoRunner
	}

	request := attemptexecution.Request{
		AttemptID:    exec.AttemptID,
		RunID:        plan.RunID,
		VisitID:      plan.VisitID,
		NodeID:       point.ID,
		ExecutorKind: plan.ExecutorKind,
		Resources: attemptexecution.RepositoryWriteResources{
			RepositoryID: plan.RepositoryID,
			WorktreeID:   plan.WorktreeID,
		},
		Invocation: attemptexecution.Start{Timeout: plan.AttemptTimeout},
	}

	commit, err := attemptRunner.Run(ctx, request)
	if err != nil {
		return ExecutionResult{}, err
	}

	return ExecutionResult{
		Point:     point,
		Action:    point.nextAction(plan.Approval, plan.Points, statuses),
		AttemptID: request.AttemptID,
		Request:   request,
		Commit:    commit,
	}, nil
}

func (point Point) isReady(statuses map[string]statestore.PointStatus) bool {
	if statuses == nil {
		return false
	}
	if statuses[point.ID] != statestore.PointReady {
		return false
	}
	for _, dependency := range point.Dependencies {
		if statuses[dependency] != statestore.PointAccepted &&
			statuses[dependency] != statestore.PointCommitted &&
			statuses[dependency] != statestore.PointPublished {
			return false
		}
	}
	return true
}

func (point Point) nextAction(policy ApprovalPolicy, allPoints []Point, statuses map[string]statestore.PointStatus) NextAction {
	switch policy.Mode {
	case ApprovalModeEvery:
		return NextActionPauseForApproval
	case ApprovalModeRisk:
		if riskMatches(point.RiskTags, policy.RiskTags) {
			return NextActionPauseForApproval
		}
		return NextActionContinue
	case ApprovalModeCombined:
		if hasUnfinishedPoints(point.ID, allPoints, statuses) {
			return NextActionContinue
		}
		return NextActionAwaitCombined
	default:
		return NextActionContinue
	}
}

func (point Point) ContextData() []byte {
	if len(strings.TrimSpace(string(point.Context.Data))) == 0 {
		return []byte("{}")
	}
	return point.Context.Data
}

func (contract CompletionContract) validate() error {
	if strings.TrimSpace(contract.Outcome) == "" || strings.TrimSpace(contract.Outcome) != contract.Outcome {
		return fmt.Errorf("completion contract outcome is required and must be trimmed")
	}
	if strings.TrimSpace(contract.ValidationProfile) == "" || strings.TrimSpace(contract.ValidationProfile) != contract.ValidationProfile {
		return fmt.Errorf("completion contract validation profile is required and must be trimmed")
	}
	if len(contract.Evidence) == 0 {
		return fmt.Errorf("completion contract evidence is required")
	}
	return validateDistinctTrimmedList(contract.Evidence, "evidence", "")
}

func validateApprovalPolicy(policy ApprovalPolicy) error {
	switch policy.Mode {
	case ApprovalModeNone, ApprovalModeEvery, ApprovalModeCombined:
		if len(policy.RiskTags) > 0 {
			return fmt.Errorf("selector risk tags are not allowed for mode %q", policy.Mode)
		}
		return nil
	case ApprovalModeRisk:
		if len(policy.RiskTags) == 0 {
			return fmt.Errorf("risk mode requires selector risk tags")
		}
		return validateDistinctTrimmedList(policy.RiskTags, "risk selector", "")
	default:
		return fmt.Errorf("invalid approval mode %q", policy.Mode)
	}
}

func validateDistinctTrimmedList(values []string, label, pointID string) error {
	seen := map[string]struct{}{}
	for _, value := range values {
		trimmed := strings.TrimSpace(value)
		if trimmed == "" || trimmed != value {
			if pointID == "" {
				return fmt.Errorf("%w: %s is required", ErrInvalidPlan, label)
			}
			return fmt.Errorf("%w: point %q has an invalid %s", ErrInvalidPlan, pointID, label)
		}
		if _, exists := seen[trimmed]; exists {
			if pointID == "" {
				return fmt.Errorf("%w: duplicate %s %q", ErrInvalidPlan, label, trimmed)
			}
			return fmt.Errorf("%w: point %q has duplicate %s %q", ErrInvalidPlan, pointID, label, trimmed)
		}
		seen[trimmed] = struct{}{}
	}
	return nil
}

func riskMatches(pointRiskTags, policyRiskTags []string) bool {
	policyTagSet := map[string]struct{}{}
	for _, tag := range policyRiskTags {
		policyTagSet[strings.TrimSpace(tag)] = struct{}{}
	}
	for _, risk := range pointRiskTags {
		if _, ok := policyTagSet[strings.TrimSpace(risk)]; ok {
			return true
		}
	}
	return false
}

func hasUnfinishedPoints(currentPointID string, allPoints []Point, statuses map[string]statestore.PointStatus) bool {
	for _, point := range allPoints {
		if point.ID == currentPointID {
			continue
		}
		if statuses == nil {
			return true
		}
		if statuses[point.ID] != statestore.PointAccepted &&
			statuses[point.ID] != statestore.PointCommitted &&
			statuses[point.ID] != statestore.PointPublished {
			return true
		}
	}
	return false
}

type frozenExecutionPolicy struct {
	Mode     ApprovalMode       `json:"mode"`
	RiskTags []string           `json:"riskTags"`
	Contract CompletionContract `json:"contract"`
}

type pointContextBuilder struct {
	snapshot attemptexecution.ContextSnapshot
}

func (builder pointContextBuilder) Build(_ context.Context, _ attemptexecution.Request, _ attemptexecution.Allocation) (attemptexecution.ContextSnapshot, error) {
	return cloneSnapshot(builder.snapshot), nil
}

func newPointContextBuilder(plan Plan, point Point) (attemptexecution.ContextBuilder, error) {
	contextData := append(json.RawMessage(nil), point.ContextData()...)
	if !json.Valid(contextData) {
		return nil, fmt.Errorf("%w: point %q has invalid scoped context", ErrInvalidPlan, point.ID)
	}
	sortedRiskTags := append([]string(nil), plan.Approval.RiskTags...)
	sort.Strings(sortedRiskTags)
	policy := frozenExecutionPolicy{
		Mode:     plan.Approval.Mode,
		RiskTags: sortedRiskTags,
		Contract: point.Contract,
	}
	policyDigestBytes, err := json.Marshal(policy)
	if err != nil {
		return nil, err
	}
	policyDigest := digestJSON(policyDigestBytes)
	inputs, err := json.Marshal(struct {
		PointID       string             `json:"pointId"`
		StoryID       string             `json:"storyId"`
		Revision      uint64             `json:"revision"`
		Position      int                `json:"position"`
		Dependencies  []string           `json:"dependencies"`
		Completion    CompletionContract `json:"completionContract"`
		Context       ScopedContext      `json:"scopedContext"`
		AffectedAreas []string           `json:"affectedAreas"`
		RiskTags      []string           `json:"riskTags"`
	}{
		PointID:      point.ID,
		StoryID:      point.StoryID,
		Revision:     point.Revision,
		Position:     point.Position,
		Dependencies: append([]string(nil), point.Dependencies...),
		Completion:   point.Contract,
		Context: ScopedContext{
			Data: contextData,
		},
		AffectedAreas: point.AffectedAreas,
		RiskTags:      point.RiskTags,
	})
	if err != nil {
		return nil, err
	}
	return pointContextBuilder{
		snapshot: attemptexecution.ContextSnapshot{
			Digest:       digestJSON(inputs),
			Inputs:       clonePayload(inputs),
			PolicyDigest: policyDigest,
		},
	}, nil
}

func digestJSON(value []byte) string {
	sum := sha256.Sum256(value)
	return hex.EncodeToString(sum[:])
}

func clonePayload(values json.RawMessage) json.RawMessage {
	return append(json.RawMessage(nil), values...)
}

func cloneSnapshot(snapshot attemptexecution.ContextSnapshot) attemptexecution.ContextSnapshot {
	snapshot.Inputs = clonePayload(snapshot.Inputs)
	return snapshot
}
