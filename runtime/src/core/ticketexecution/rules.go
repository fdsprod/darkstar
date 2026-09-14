package ticketexecution

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"

	"darkstar/src/core/trackercontract"
	"darkstar/src/core/trackerrules"
	"darkstar/src/ports"
	"darkstar/src/ports/statestore"
	"darkstar/src/ports/tracker"
)

// RuleResolver returns activated daemon-owned configuration and current provider
// discovery. Missing configuration preserves explicit legacy manual admission.
type RuleResolver interface {
	ResolveRules(context.Context, string, string) (trackerrules.RuleSet, trackerrules.Discovery, error)
}

// RuleDiscoveryResolver observes current provider/workflow facts independently
// of the selected mapping, so existing work can validate its immutable rules.
type RuleDiscoveryResolver interface {
	DiscoverRules(context.Context, string, string) (trackerrules.Discovery, error)
}

type RuleIntakeResult struct {
	State     string
	Admission *Result
}

func (s *Service) resolveIntakeRules(ctx context.Context, project, observationID string) (trackerrules.RuleSet, trackerrules.Discovery, error) {
	store, ok := s.store.(statestore.SourceAdmissionReceipts)
	if !ok {
		return s.options.Rules.ResolveRules(ctx, project, observationID)
	}
	observation, err := s.store.BacklogObservation(ctx, observationID)
	if err != nil {
		return trackerrules.RuleSet{}, trackerrules.Discovery{}, err
	}
	views, err := s.TicketView(ctx, project, observationID)
	if err != nil {
		return trackerrules.RuleSet{}, trackerrules.Discovery{}, err
	}
	for _, view := range views {
		if view.Lineage == nil || view.Lineage.Ref != observation.Ref || view.Approval == nil || view.Approval.LineageRevision != view.Lineage.Revision {
			continue
		}
		pin, err := store.TicketAdmissionRulePin(ctx, view.Approval.ID)
		if err != nil {
			return trackerrules.RuleSet{}, trackerrules.Discovery{}, err
		}
		if pin == nil {
			continue
		}
		rules, err := trackerrules.Decode(pin.Rules)
		if err != nil {
			return trackerrules.RuleSet{}, trackerrules.Discovery{}, err
		}
		var discovery trackerrules.Discovery
		if resolver, ok := s.options.Rules.(RuleDiscoveryResolver); ok {
			discovery, err = resolver.DiscoverRules(ctx, project, observationID)
		} else {
			_, discovery, err = s.options.Rules.ResolveRules(ctx, project, observationID)
		}
		if err != nil {
			return rules, discovery, err
		}
		if err := trackerrules.Validate(rules, discovery); err != nil {
			return rules, discovery, err
		}
		return rules, discovery, nil
	}
	return s.options.Rules.ResolveRules(ctx, project, observationID)
}

func (s *Service) replayAdmission(ctx context.Context, key, digest string) (Result, bool, error) {
	store, ok := s.store.(statestore.SourceAdmissionReceipts)
	if !ok {
		return Result{}, false, nil
	}
	admission, err := store.ReplayTicketAdmission(ctx, key, digest)
	if missing(err) {
		return Result{}, false, nil
	}
	if err != nil {
		return Result{}, true, err
	}
	result, err := s.result(ctx, admission)
	return result, true, err
}

func (s *Service) ruleForApproval(ctx context.Context, lineage statestore.SourceLineage, observationID string) (*statestore.SourceRulePin, *statestore.SourceIntakeCursorMutation, error) {
	if s.options.Rules == nil {
		return nil, nil, nil
	}
	store, ok := s.store.(statestore.SourceAdmissionReceipts)
	if !ok {
		return nil, nil, fail(ports.FailureUnavailable, "immutable admission rule evidence is unavailable")
	}
	previous, err := s.store.LatestTicketAdmission(ctx, lineage.WorkID)
	if err == nil {
		pin, err := store.TicketAdmissionRulePin(ctx, previous.ID)
		if err != nil {
			return nil, nil, err
		}
		if pin != nil {
			// Later human approvals retain existing work's original workflow.
			// They do not reinterpret it through a new project mapping.
			return pin, nil, nil
		}
	} else if !missing(err) {
		return nil, nil, err
	}
	retained, err := s.store.BacklogObservation(ctx, observationID)
	if err != nil {
		return nil, nil, err
	}
	ticket, err := trackercontract.DecodeTicket(retained.Ticket)
	if err != nil {
		return nil, nil, err
	}
	return s.resolveAdmissionRule(ctx, AdmissionRequest{ProjectID: lineage.ProjectID, BindingRevision: lineage.BindingRevision, ObservationID: observationID}, ticket)
}

// EvaluateIntake consumes a retained source observation. It creates only local
// work; execution preparation, human checkpoints and scheduling keep their own
// authority. The cursor and admission commit in one writer transaction.
func (s *Service) EvaluateIntake(ctx context.Context, project, observationID string) (RuleIntakeResult, error) {
	if s.options.Rules == nil {
		return RuleIntakeResult{State: "unconfigured"}, nil
	}
	rules, discovery, err := s.resolveIntakeRules(ctx, project, observationID)
	if missing(err) {
		return RuleIntakeResult{State: "unconfigured"}, nil
	}
	if err != nil {
		return RuleIntakeResult{}, err
	}
	store, ok := s.store.(statestore.SourceIntakeCursorStore)
	if !ok {
		return RuleIntakeResult{}, fail(ports.FailureUnavailable, "durable intake cursor storage is unavailable")
	}
	retained, err := s.store.BacklogObservation(ctx, observationID)
	if err != nil {
		return RuleIntakeResult{}, err
	}
	ticket, err := trackercontract.DecodeTicket(retained.Ticket)
	if err != nil {
		return RuleIntakeResult{}, err
	}
	identity := trackerrules.AdmissionIdentity(project, ticket.Ref)
	cursor, expected, settled, err := s.intakeState(ctx, store, identity, project, observationID)
	if err != nil {
		return RuleIntakeResult{}, err
	}
	event := trackerrules.Event{ID: observationID, Observation: trackerrules.Observation{ProjectID: project, BindingRevision: rules.Scope.BindingRevision, Pin: discovery.Manifest.Pin, Ticket: ticket, WorkflowID: tracker.Unsupported[tracker.NamedID]{Reason: "workflow identity is unavailable"}}}
	decision, err := trackerrules.EvaluateAdmission(rules, discovery, event, cursor, settled, nil)
	if err != nil {
		return RuleIntakeResult{}, err
	}
	result := RuleIntakeResult{State: decision.State}
	if decision.State == "manual" || decision.State == "duplicate" || decision.State == "out_of_order" || decision.State == "previous_execution_active" {
		return result, nil
	}
	next, err := json.Marshal(decision.Cursor)
	if err != nil {
		return result, err
	}
	change := statestore.SourceIntakeCursorMutation{Identity: identity, ExpectedDigest: expected, Cursor: next}
	if decision.State != "automatic" {
		return result, store.SaveSourceIntakeCursor(ctx, change)
	}
	action, ok := decision.Preview.Action.(trackerrules.Admit)
	if !ok {
		return result, fail(ports.FailureInvalidRequest, "automatic admission requires a typed intake action")
	}
	pin, err := admissionRulePin(rules, decision.Preview.RuleID, action)
	if err != nil {
		return result, err
	}
	admission, err := s.admit(ctx, AdmissionRequest{ProjectID: project, BindingRevision: rules.Scope.BindingRevision, ObservationID: observationID}, decision.AdmissionID, "tracker-intake", pin, &change)
	if err != nil {
		return result, err
	}
	result.Admission = &admission
	return result, nil
}

func (s *Service) resolveAdmissionRule(ctx context.Context, request AdmissionRequest, ticket tracker.Ticket) (*statestore.SourceRulePin, *statestore.SourceIntakeCursorMutation, error) {
	if s.options.Rules == nil {
		return nil, nil, nil
	}
	rules, discovery, err := s.resolveIntakeRules(ctx, request.ProjectID, request.ObservationID)
	if missing(err) {
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, err
	}
	observation := trackerrules.Observation{ProjectID: request.ProjectID, BindingRevision: request.BindingRevision, Pin: discovery.Manifest.Pin, Ticket: ticket, WorkflowID: tracker.Unsupported[tracker.NamedID]{Reason: "source workflow identity was not discovered"}}
	preview, err := trackerrules.PreviewIntake(rules, discovery, observation)
	if err != nil {
		return nil, nil, err
	}
	action, ok := preview.Action.(trackerrules.Admit)
	if !preview.Matched || !ok {
		return nil, nil, fail(ports.FailureConflict, "ticket does not match an admitting intake rule; inspect the mapping preview")
	}
	store, ok := s.store.(statestore.SourceIntakeCursorStore)
	if !ok {
		return nil, nil, fail(ports.FailureUnavailable, "durable intake cursor storage is unavailable")
	}
	identity := trackerrules.AdmissionIdentity(request.ProjectID, ticket.Ref)
	cursor, expected, settled, err := s.intakeState(ctx, store, identity, request.ProjectID, request.ObservationID)
	if err != nil {
		return nil, nil, err
	}
	event := trackerrules.Event{ID: request.ObservationID, Observation: observation}
	var decision trackerrules.AdmissionDecision
	if action.Mode == trackerrules.Manual {
		decision, err = trackerrules.ConfirmManualAdmission(rules, discovery, event, cursor, settled, nil)
	} else {
		decision, err = trackerrules.EvaluateAdmission(rules, discovery, event, cursor, settled, nil)
	}
	if err != nil {
		return nil, nil, err
	}
	if decision.State != "manual_confirmed" && decision.State != "automatic" && decision.State != "duplicate" {
		return nil, nil, fail(ports.FailureConflict, "intake cannot admit this observation: "+decision.State)
	}
	pin, err := admissionRulePin(rules, preview.RuleID, action)
	if err != nil || decision.State == "duplicate" {
		return pin, nil, err
	}
	encoded, err := json.Marshal(decision.Cursor)
	return pin, &statestore.SourceIntakeCursorMutation{Identity: identity, ExpectedDigest: expected, Cursor: encoded}, err
}

func (s *Service) intakeState(ctx context.Context, store statestore.SourceIntakeCursorStore, identity, project, observation string) (trackerrules.Cursor, string, bool, error) {
	var cursor trackerrules.Cursor
	encoded, err := store.SourceIntakeCursor(ctx, identity)
	if err != nil && !missing(err) {
		return cursor, "", false, err
	}
	expected := ""
	if len(encoded) != 0 {
		if err := json.Unmarshal(encoded, &cursor); err != nil {
			return cursor, "", false, err
		}
		expected = fmt.Sprintf("%x", sha256.Sum256(encoded))
	}
	views, err := s.TicketView(ctx, project, observation)
	if err != nil {
		return cursor, "", false, err
	}
	settled := true
	for _, view := range views {
		for _, run := range view.Runs {
			if run.Status != statestore.RunCompleted && run.Status != statestore.RunCancelled {
				settled = false
			}
		}
	}
	return cursor, expected, settled, nil
}

func admissionRulePin(rules trackerrules.RuleSet, ruleID string, action trackerrules.Admit) (*statestore.SourceRulePin, error) {
	encoded, err := trackerrules.Encode(rules)
	if err != nil {
		return nil, err
	}
	return &statestore.SourceRulePin{RuleSetID: rules.ID, Revision: rules.Revision, RuleID: ruleID, Rules: encoded, WorkflowID: action.Workflow.ID, WorkflowVersion: action.Workflow.Version, WorkflowDigest: action.Workflow.Digest, ReadinessPolicy: action.ReadinessPolicy, AdmissionMode: string(action.Mode)}, nil
}
