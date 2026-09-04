// Package readinesscontrol coordinates validated readiness assessments and
// durable operator choices without performing their downstream workflow effects.
package readinesscontrol

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"darkstar/src/core/identity"
	"darkstar/src/core/routeassessment"
	"darkstar/src/core/workflow"
	"darkstar/src/ports/statestore"
)

var (
	ErrInvalidRequest      = errors.New("invalid readiness-control request")
	ErrAssessmentConflict  = errors.New("readiness assessment conflict")
	ErrAlreadyDecided      = errors.New("readiness assessment is already decided")
	ErrIdempotencyConflict = errors.New("readiness assessment idempotency conflict")
)

var (
	assessmentIDPattern = regexp.MustCompile(`^assessment_[0-9A-HJKMNP-TV-Z]{26}$`)
	digestPattern       = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

// Store is the exact event/projection subset required by readiness control.
type Store interface {
	Append(context.Context, ...statestore.PendingEvent) ([]statestore.Event, error)
	EventByCommand(context.Context, string, string) (statestore.Event, error)
	Run(context.Context, string) (statestore.RunProjection, error)
	ReadinessAssessment(context.Context, string) (statestore.ReadinessAssessmentProjection, error)
	LatestReadinessAssessmentForRun(context.Context, string) (statestore.ReadinessAssessmentProjection, error)
}

// WorkflowReader resolves the exact immutable workflow pinned by a run.
type WorkflowReader interface {
	Definition(context.Context, string, string) (workflow.Definition, error)
}

// ValidationContextResolver provides trusted facts that are not recoverable
// from the current RunProjection. Its output is frozen into the assessment event.
type ValidationContextResolver interface {
	ReadinessValidationContext(context.Context, statestore.RunProjection) (workflow.RouteContext, string, error)
}

// AuthorityResolver derives the authenticated event actor from request context.
type AuthorityResolver interface {
	Actor(context.Context) (statestore.Actor, error)
}

type SubmitRequest struct {
	Submission     routeassessment.Submission
	IdempotencyKey string
}

type DecisionRequest struct {
	AssessmentID            string
	ExpectedResourceVersion uint64
	ExpectedDigest          string
	DecisionID              string
	Choice                  routeassessment.Choice
	RemedyCode              string
	Reason                  string
	IdempotencyKey          string
}

// AllowedAction is derived from the validated assessment. Remedy is populated
// only for a supply_input action and is never client-authored authority.
type AllowedAction struct {
	Choice routeassessment.Choice    `json:"choice"`
	Remedy *workflow.ReadinessRemedy `json:"remedy,omitempty"`
}

// View joins the safe domain view to durable lifecycle metadata.
type View struct {
	Assessment      routeassessment.View                    `json:"assessment"`
	Status          statestore.ReadinessAssessmentStatus    `json:"status"`
	AllowedActions  []AllowedAction                         `json:"allowedActions"`
	Decision        *statestore.ReadinessDecisionProjection `json:"decision,omitempty"`
	ResourceVersion uint64                                  `json:"resourceVersion"`
	CreatedAt       time.Time                               `json:"createdAt"`
	UpdatedAt       time.Time                               `json:"updatedAt"`
}

type Service struct {
	store      Store
	workflows  WorkflowReader
	validation ValidationContextResolver
	authority  AuthorityResolver
	now        func() time.Time
}

func New(store Store, workflows WorkflowReader, validation ValidationContextResolver, authority AuthorityResolver) (*Service, error) {
	if store == nil || workflows == nil || validation == nil || authority == nil {
		return nil, errors.New("readiness control requires state, workflow, validation-context, and authority dependencies")
	}
	return &Service{store: store, workflows: workflows, validation: validation, authority: authority, now: time.Now}, nil
}

// Submit validates provider evidence against the run's exact installed
// workflow and frozen route, then records the immutable validation inputs.
func (service *Service) Submit(ctx context.Context, request SubmitRequest) (View, error) {
	if !assessmentIDPattern.MatchString(request.Submission.AssessmentID) || strings.TrimSpace(request.IdempotencyKey) == "" {
		return View{}, ErrInvalidRequest
	}
	actor, err := service.authority.Actor(ctx)
	if err != nil {
		return View{}, fmt.Errorf("resolve readiness submission authority: %w", err)
	}
	if actor.Type != statestore.ActorProvider && actor.Type != statestore.ActorSystem || !validActor(actor) {
		return View{}, fmt.Errorf("%w: assessments require provider or system authority", ErrInvalidRequest)
	}
	run, definition, routeState, err := service.runBoundary(ctx, request.Submission.RunID)
	if err != nil {
		return View{}, err
	}
	validationContext, policyDigest, err := service.validation.ReadinessValidationContext(ctx, run)
	if err != nil {
		return View{}, fmt.Errorf("resolve readiness validation context: %w", err)
	}
	if !digestPattern.MatchString(policyDigest) {
		return View{}, fmt.Errorf("%w: trusted policy digest is invalid", ErrInvalidRequest)
	}
	assessment, err := routeassessment.Assess(definition.Document, routeState, validationContext, policyDigest, request.Submission)
	if err != nil {
		return View{}, err
	}
	payload, err := assessmentPayload(assessment, request.Submission, validationContext, policyDigest)
	if err != nil {
		return View{}, err
	}
	if existing, readErr := service.store.ReadinessAssessment(ctx, request.Submission.AssessmentID); readErr == nil {
		if replayErr := service.validateReplay(ctx, existing.AssessmentID, request.IdempotencyKey, "readiness.assessment_recorded", payload, actor); replayErr != nil {
			return View{}, ErrIdempotencyConflict
		}
		return service.view(ctx, existing)
	} else if !errors.Is(readErr, statestore.ErrNotFound) {
		return View{}, readErr
	}
	_, err = service.store.Append(ctx, statestore.PendingEvent{
		SchemaVersion: 1, ID: identity.Random("event_"), AggregateType: statestore.AggregateAssessment,
		AggregateID: request.Submission.AssessmentID, ExpectedRevision: 0, Kind: "readiness.assessment_recorded",
		OccurredAt: service.now().UTC().Round(0), CorrelationID: request.Submission.RunID,
		CommandID: request.IdempotencyKey, Actor: actor, Data: payload, Metadata: json.RawMessage(`{}`),
	})
	if err != nil {
		return View{}, fmt.Errorf("record readiness assessment: %w", err)
	}
	return service.Get(ctx, request.Submission.AssessmentID)
}

// Decide records an exact choice using assessment version and digest CAS. It
// deliberately neither applies a route patch nor resumes/cancels the run.
func (service *Service) Decide(ctx context.Context, request DecisionRequest) (View, error) {
	if !assessmentIDPattern.MatchString(request.AssessmentID) || request.ExpectedResourceVersion == 0 ||
		!digestPattern.MatchString(request.ExpectedDigest) || strings.TrimSpace(request.IdempotencyKey) == "" {
		return View{}, ErrInvalidRequest
	}
	actor, err := service.authority.Actor(ctx)
	if err != nil {
		return View{}, fmt.Errorf("resolve readiness decision authority: %w", err)
	}
	if (actor.Type != statestore.ActorUser && actor.Type != statestore.ActorExternal) || !validActor(actor) {
		return View{}, fmt.Errorf("%w: decisions require user or external authority", ErrInvalidRequest)
	}
	projection, err := service.store.ReadinessAssessment(ctx, request.AssessmentID)
	if err != nil {
		return View{}, err
	}
	payload := decisionPayload(request)
	if projection.Status == statestore.ReadinessAssessmentDecided {
		if replayErr := service.validateReplay(ctx, request.AssessmentID, request.IdempotencyKey, "readiness.decision_recorded", payload, actor); replayErr != nil {
			if errors.Is(replayErr, statestore.ErrNotFound) {
				return View{}, ErrAlreadyDecided
			}
			return View{}, replayErr
		}
		return service.view(ctx, projection)
	}
	if projection.Status != statestore.ReadinessAssessmentPending || projection.ResourceVersion != request.ExpectedResourceVersion || projection.AssessmentDigest != request.ExpectedDigest {
		return View{}, ErrAssessmentConflict
	}
	assessment, err := service.restore(ctx, projection)
	if err != nil {
		return View{}, err
	}
	recorder := eventDecisionRecorder{service: service, projection: projection, request: request, actor: actor}
	_, _, err = assessment.Decide(ctx, recorder, routeassessment.DecisionRequest{
		DecisionID: request.DecisionID, Choice: request.Choice, RemedyCode: request.RemedyCode,
		Reason: request.Reason, Actor: actor, DecidedAt: service.now().UTC().Round(0),
	})
	if err != nil {
		return View{}, err
	}
	return service.Get(ctx, request.AssessmentID)
}

func (service *Service) Get(ctx context.Context, assessmentID string) (View, error) {
	projection, err := service.store.ReadinessAssessment(ctx, assessmentID)
	if err != nil {
		return View{}, err
	}
	return service.view(ctx, projection)
}

func (service *Service) LatestForRun(ctx context.Context, runID string) (View, error) {
	projection, err := service.store.LatestReadinessAssessmentForRun(ctx, runID)
	if err != nil {
		return View{}, err
	}
	return service.view(ctx, projection)
}

func (service *Service) view(ctx context.Context, projection statestore.ReadinessAssessmentProjection) (View, error) {
	assessment, err := service.restore(ctx, projection)
	if err != nil {
		return View{}, err
	}
	result := View{Assessment: assessment.View(), Status: projection.Status, Decision: projection.Decision,
		ResourceVersion: projection.ResourceVersion, CreatedAt: projection.CreatedAt, UpdatedAt: projection.UpdatedAt}
	if projection.Status == statestore.ReadinessAssessmentPending {
		result.AllowedActions = allowedActions(assessment.View())
	} else {
		result.AllowedActions = []AllowedAction{}
	}
	return result, nil
}

func (service *Service) restore(ctx context.Context, projection statestore.ReadinessAssessmentProjection) (routeassessment.Assessment, error) {
	_, definition, routeState, err := service.runBoundary(ctx, projection.RunID)
	if err != nil {
		return routeassessment.Assessment{}, err
	}
	var submission routeassessment.Submission
	var routeContext workflow.RouteContext
	if json.Unmarshal([]byte(projection.Submission), &submission) != nil || json.Unmarshal([]byte(projection.RouteContext), &routeContext) != nil {
		return routeassessment.Assessment{}, errors.New("persisted readiness assessment snapshots are invalid")
	}
	assessment, err := routeassessment.Assess(definition.Document, routeState, routeContext, projection.PolicyDigest, submission)
	if err != nil {
		return routeassessment.Assessment{}, fmt.Errorf("revalidate readiness assessment: %w", err)
	}
	if assessment.View().Digest != projection.AssessmentDigest || string(assessment.Disposition()) != projection.Disposition {
		return routeassessment.Assessment{}, ErrAssessmentConflict
	}
	return assessment, nil
}

func (service *Service) runBoundary(ctx context.Context, runID string) (statestore.RunProjection, workflow.Definition, workflow.RouteState, error) {
	run, err := service.store.Run(ctx, runID)
	if err != nil {
		return statestore.RunProjection{}, workflow.Definition{}, workflow.RouteState{}, err
	}
	if run.Status.Terminal() || run.Status == statestore.RunFailed || run.RouteSnapshot == "" || !digestPattern.MatchString(run.WorkflowDigest) {
		return statestore.RunProjection{}, workflow.Definition{}, workflow.RouteState{}, ErrAssessmentConflict
	}
	definition, err := service.workflows.Definition(ctx, run.WorkflowID, run.WorkflowVersion)
	if err != nil {
		return statestore.RunProjection{}, workflow.Definition{}, workflow.RouteState{}, fmt.Errorf("read frozen workflow: %w", err)
	}
	if definition.Version.Digest != run.WorkflowDigest {
		return statestore.RunProjection{}, workflow.Definition{}, workflow.RouteState{}, ErrAssessmentConflict
	}
	var route workflow.Route
	if err := json.Unmarshal([]byte(run.RouteSnapshot), &route); err != nil {
		return statestore.RunProjection{}, workflow.Definition{}, workflow.RouteState{}, fmt.Errorf("decode frozen route: %w", err)
	}
	state, err := workflow.NewRouteState(run.RunID, route)
	if err != nil {
		return statestore.RunProjection{}, workflow.Definition{}, workflow.RouteState{}, fmt.Errorf("restore frozen route: %w", err)
	}
	return run, definition, state, nil
}

type eventDecisionRecorder struct {
	service    *Service
	projection statestore.ReadinessAssessmentProjection
	request    DecisionRequest
	actor      statestore.Actor
}

func (recorder eventDecisionRecorder) RecordRouteAssessmentDecision(ctx context.Context, decision routeassessment.Decision) error {
	payload := decisionPayload(recorder.request)
	_, err := recorder.service.store.Append(ctx, statestore.PendingEvent{
		SchemaVersion: 1, ID: identity.Random("event_"), AggregateType: statestore.AggregateAssessment,
		AggregateID: recorder.projection.AssessmentID, ExpectedRevision: recorder.projection.ResourceVersion,
		Kind: "readiness.decision_recorded", OccurredAt: decision.DecidedAt,
		CorrelationID: recorder.projection.RunID, CommandID: recorder.request.IdempotencyKey,
		Actor: recorder.actor, Data: payload, Metadata: json.RawMessage(`{}`),
	})
	return err
}

func assessmentPayload(assessment routeassessment.Assessment, submission routeassessment.Submission, routeContext workflow.RouteContext, policyDigest string) (json.RawMessage, error) {
	data := struct {
		RunID            string                      `json:"runId"`
		NodeID           workflow.Identifier         `json:"nodeId"`
		Disposition      routeassessment.Disposition `json:"disposition"`
		AssessmentDigest string                      `json:"assessmentDigest"`
		PolicyDigest     string                      `json:"policyDigest"`
		Submission       routeassessment.Submission  `json:"submission"`
		RouteContext     workflow.RouteContext       `json:"routeContext"`
	}{submission.RunID, submission.NodeID, assessment.Disposition(), assessment.View().Digest, policyDigest, submission, routeContext}
	encoded, err := json.Marshal(data)
	if err != nil {
		return nil, fmt.Errorf("encode readiness assessment: %w", err)
	}
	return encoded, nil
}

func decisionPayload(request DecisionRequest) json.RawMessage {
	data := struct {
		DecisionID       string                           `json:"decisionId"`
		AssessmentDigest string                           `json:"assessmentDigest"`
		Choice           routeassessment.Choice           `json:"choice"`
		RemedyCode       string                           `json:"remedyCode,omitempty"`
		Reason           string                           `json:"reason"`
		EffectStatus     statestore.ReadinessEffectStatus `json:"effectStatus"`
	}{request.DecisionID, request.ExpectedDigest, request.Choice, request.RemedyCode, request.Reason, statestore.ReadinessEffectPending}
	encoded, _ := json.Marshal(data)
	return encoded
}

func (service *Service) validateReplay(ctx context.Context, aggregateID, commandID, kind string, payload json.RawMessage, actor statestore.Actor) error {
	event, err := service.store.EventByCommand(ctx, aggregateID, commandID)
	if err != nil {
		return err
	}
	if event.Kind != kind || string(event.Data) != string(payload) || event.Actor != actor {
		return ErrIdempotencyConflict
	}
	return nil
}

func allowedActions(view routeassessment.View) []AllowedAction {
	result := make([]AllowedAction, 0, len(view.Remedies)+3)
	if view.Disposition == routeassessment.DispositionReady || view.Disposition == routeassessment.DispositionChoiceRequired {
		result = append(result, AllowedAction{Choice: routeassessment.ChoiceContinue})
	}
	if view.RouteChange != nil {
		result = append(result, AllowedAction{Choice: routeassessment.ChoiceAcceptRouteChange})
	}
	for index := range view.Remedies {
		if view.Remedies[index].Action == workflow.ReadinessSupplyInput {
			remedy := view.Remedies[index]
			result = append(result, AllowedAction{Choice: routeassessment.ChoiceSupplyInput, Remedy: &remedy})
		}
	}
	return append(result, AllowedAction{Choice: routeassessment.ChoiceCancel})
}

func validActor(actor statestore.Actor) bool {
	return strings.TrimSpace(actor.ID) != "" && strings.TrimSpace(actor.ID) == actor.ID && len(actor.ID) <= 128
}
