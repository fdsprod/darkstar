package runexecution

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"

	"darkstar/src/core/preparation"
	"darkstar/src/core/workflow"
	"darkstar/src/ports/routeadvisor"
	"darkstar/src/ports/statestore"
)

func (s *Service) SetRouteAdvisor(advisor routeadvisor.Advisor) error {
	if advisor == nil {
		return errors.New("route advisor is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.workers) > 0 {
		return errors.New("route advisor cannot change during execution")
	}
	s.advisor = advisor
	return nil
}

func (s *Service) SetRouteEvidenceResolver(resolver routeadvisor.EvidenceResolver) error {
	if resolver == nil {
		return errors.New("evidence resolver is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.workers) > 0 {
		return errors.New("evidence resolver cannot change during execution")
	}
	s.evidenceResolver = resolver
	return nil
}

func (s *Service) SetPreparationPolicyResolver(resolver func(context.Context, statestore.ProjectProjection) (preparation.Policy, error)) error {
	if resolver == nil {
		return errors.New("preparation policy resolver is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.preparationPolicyResolver = resolver
	return nil
}

func (s *Service) assessPreparation(ctx context.Context, request CreateRequest, work statestore.WorkItemProjection, project statestore.ProjectProjection, routeContext workflow.RouteContext, preview workflow.RoutePreview) (workflow.RoutePreview, error) {
	reader, ok := s.planner.(WorkflowDefinitionReader)
	if !ok {
		return preview, ErrWorkflowUnavailable
	}
	definition, err := reader.Definition(ctx, preview.Workflow.Name, preview.Workflow.Version)
	if err != nil {
		return preview, err
	}
	if definition.Version.Digest != preview.Workflow.Digest {
		return preview, errors.New("workflow changed during route assessment")
	}
	policy, err := s.resolvePreparationPolicy(ctx, project, definition.Document)
	if err != nil {
		return preview, err
	}
	input := preparation.Input{Work: work, Project: project, Workflow: definition.Document, WorkflowDigest: definition.Version.Digest, Policy: policy, Context: routeContext, Evidence: []routeadvisor.Evidence{}}
	return s.assessPreparationInput(ctx, request, work, input, preview)
}

func (s *Service) resolvePreparationPolicy(ctx context.Context, project statestore.ProjectProjection, document workflow.Document) (preparation.Policy, error) {
	policy := preparation.Policy{Version: "smallest-safe-v1", RequiredNodes: []workflow.Identifier{}, ConsequentialNodes: []workflow.Identifier{}, AllowedAssumptions: []string{}}
	s.mu.Lock()
	policyResolver := s.preparationPolicyResolver
	s.mu.Unlock()
	if policyResolver != nil {
		var err error
		policy, err = policyResolver(ctx, project)
		if err != nil {
			return policy, err
		}
	}
	// Local readiness gates stay local to retained nodes. They are not global
	// project requirements and cannot force irrelevant branches into the route.
	for id, node := range document.Spec.Nodes {
		fields := node.Fields()
		if node.Type() == workflow.NodeCommand || node.Type() == workflow.NodeApproval || node.Type() == workflow.NodePointExecution || len(fields.Permissions) > 0 {
			policy.ConsequentialNodes = append(policy.ConsequentialNodes, id)
		}
	}
	sortIdentifiers(policy.RequiredNodes)
	sortIdentifiers(policy.ConsequentialNodes)
	return policy, nil
}

func (s *Service) assessPreparationInput(ctx context.Context, request CreateRequest, work statestore.WorkItemProjection, input preparation.Input, preview workflow.RoutePreview) (workflow.RoutePreview, error) {
	var err error
	if request.Preparation != nil {
		input.Answers = request.Preparation.Answers
	}
	// A submitted reference is not resolved evidence. It remains visible as
	// unavailable; the assessor may not cite it as inspected content.
	s.mu.Lock()
	resolver := s.evidenceResolver
	s.mu.Unlock()
	for _, reference := range work.Evidence {
		evidence := routeadvisor.Evidence{Reference: reference}
		if resolver != nil {
			resolved, resolveErr := resolver.Resolve(ctx, reference)
			if resolveErr != nil && !errors.Is(resolveErr, routeadvisor.ErrEvidenceUnavailable) {
				return preview, resolveErr
			}
			if resolveErr == nil {
				evidence = resolved
			}
		}
		input.Evidence = append(input.Evidence, evidence)
	}
	if work.RoutingIntent.Mode == statestore.WorkRoutingOverride {
		selected, selectionErr := workRouteRequest(work, request)
		if selectionErr != nil {
			return preview, selectionErr
		}
		input.Override = &selected
	} else if request.Profile != "" {
		selected := workflow.RouteRequest{From: preview.Route.Entry, Until: preview.Route.Terminals}
		input.Override = &selected
	}
	// Semantic advice is sampled once per exact immutable input identity. A
	// new command with unchanged inputs reuses that validated assessment.
	priorRuns, err := s.store.RunsForWorkItem(ctx, work.WorkItemID)
	if err != nil {
		return preview, err
	}
	for _, run := range priorRuns {
		var route workflow.Route
		if json.Unmarshal([]byte(run.RouteSnapshot), &route) != nil {
			continue
		}
		prior, readErr := readPreparation(route)
		if readErr != nil {
			return preview, readErr
		}
		if prior != nil && prior.InputDigest == preparation.Digest(input) {
			preview.Route = route
			return preview, nil
		}
	}
	advice := routeadvisor.Advice{Confidence: "high", Candidates: []routeadvisor.CandidateAdvice{}, EvidenceUsed: []string{}}
	if input.Override == nil {
		assessmentRequest, _ := preparation.Candidates(input)
		s.mu.Lock()
		advisor := s.advisor
		s.mu.Unlock()
		if advisor != nil && len(assessmentRequest.Candidates) > 0 {
			advice, err = advisor.Assess(ctx, assessmentRequest)
			if err != nil {
				return preview, fmt.Errorf("route assessment failed: %w", err)
			}
		} else {
			// Without a semantic provider there is no authority to guess that a
			// later node satisfies free-text work. Surface an actionable question.
			for _, candidate := range assessmentRequest.Candidates {
				advice.Candidates = append(advice.Candidates, routeadvisor.CandidateAdvice{Entry: candidate.Entry, Terminals: candidate.Terminals, Disposition: "input_required", Rationale: "Semantic outcome assessment is unavailable.", Questions: []routeadvisor.Question{{ID: "routing", Prompt: "Configure the reasoning provider, or supply an explicit workflow routing override."}}, Assumptions: []string{}})
			}
		}
	}
	assessment, err := preparation.Assess(input, advice)
	if err != nil {
		return preview, fmt.Errorf("%w: %v", ErrInvalidRequest, err)
	}
	preview.Route = assessment.Route
	preview.Route.Assessment, err = json.Marshal(assessment)
	return preview, err
}

func sortIdentifiers(ids []workflow.Identifier) {
	for i := 1; i < len(ids); i++ {
		for j := i; j > 0 && ids[j] < ids[j-1]; j-- {
			ids[j], ids[j-1] = ids[j-1], ids[j]
		}
	}
}

func readPreparation(route workflow.Route) (*preparation.Assessment, error) {
	if len(route.Assessment) == 0 {
		return nil, nil
	}
	var a preparation.Assessment
	if err := json.Unmarshal(route.Assessment, &a); err != nil {
		return nil, err
	}
	if err := preparation.Verify(a); err != nil {
		return nil, err
	}
	without := route
	without.Assessment = nil
	if preparation.Digest(without) != preparation.Digest(a.Route) {
		return nil, errors.New("route differs from immutable assessment")
	}
	return &a, nil
}

func preparationQuestions(a *preparation.Assessment) string {
	questions := []string{}
	for _, q := range a.Questions {
		questions = append(questions, q.ID+": "+q.Prompt)
	}
	return strings.Join(questions, " ") + " Prepare again with preparation.answers/runInputs and a new idempotency key; the new assessment supersedes this preparation wait."
}

func preparationInputType(raw json.RawMessage, kind workflow.ValueType) bool {
	var value any
	if json.Unmarshal(raw, &value) != nil {
		return false
	}
	switch kind {
	case workflow.ValueNull:
		return value == nil
	case workflow.ValueBoolean:
		_, ok := value.(bool)
		return ok
	case workflow.ValueString:
		_, ok := value.(string)
		return ok
	case workflow.ValueObject:
		_, ok := value.(map[string]any)
		return ok
	case workflow.ValueArray:
		_, ok := value.([]any)
		return ok
	case workflow.ValueNumber:
		_, ok := value.(float64)
		return ok
	case workflow.ValueInteger:
		n, ok := value.(float64)
		return ok && math.Trunc(n) == n
	default:
		return false
	}
}
