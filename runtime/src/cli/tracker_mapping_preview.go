package cli

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"time"

	"darkstar/src/api"
	"darkstar/src/core/backlog"
	"darkstar/src/core/ticketmanagement"
	"darkstar/src/core/trackerrules"
	"darkstar/src/ports"
	"darkstar/src/ports/statestore"
	"darkstar/src/ports/tracker"
)

func (s *daemonTrackerMapping) Preview(ctx context.Context, project string, input api.TrackerMappingRequest) (any, error) {
	result := map[string]any{"schemaVersion": 1, "valid": true, "issues": []any{}, "requiredFields": []string{}, "approval": "No action is authorized by a preview."}
	rules, discovery, observation, err := s.validated(ctx, project, input)
	if err != nil {
		result["valid"] = false
		result["issues"] = []any{map[string]string{"field": "rules", "code": "invalid_mapping", "message": err.Error()}}
		return result, nil
	}
	if input.ObservationID == "" || observation == nil {
		result["reason"] = "Choose a current ticket observation to preview rule matching and exact actions."
		return result, nil
	}
	intake, err := trackerrules.PreviewIntake(rules, discovery, *observation)
	if err != nil {
		result["valid"] = false
		result["issues"] = []any{map[string]string{"field": "observationId", "code": "unavailable_observation", "message": err.Error()}}
		result["reason"] = err.Error()
		return result, nil
	}
	result["intake"] = intake
	if action, ok := intake.Action.(trackerrules.Admit); ok {
		result["requestedAction"] = action
		result["approval"] = "Admission preserves the daemon's readiness and human preparation approval gates."
	}
	if input.Event == nil {
		return result, nil
	}
	result["outbound"] = map[string]any{"matched": false}
	for _, rule := range rules.Outbound {
		if rule.Milestone.ID != input.Event.MilestoneID || rule.Milestone.Workflow != input.Event.Workflow {
			continue
		}
		matched, err := trackerrules.MatchConditions(rule.When, *observation)
		if err != nil {
			result["valid"] = false
			result["reason"] = err.Error()
			return result, nil
		}
		if !matched {
			continue
		}
		result["outbound"] = map[string]any{"matched": true, "ruleId": rule.ID}
		result["requestedAction"] = rule.Action
		result["requiredEvidence"] = rule.Milestone.EvidenceTypes
		result["approval"] = "A retained validated milestone and daemon authorization are required; sample events cannot grant approval."
		result["reason"] = "This is a dry run. The daemon must validate the named output evidence before any effect is requested."
		if transition, ok := rule.Action.(trackerrules.Transition); ok {
			for _, candidate := range discovery.Transitions {
				if candidate.Identity.ID == transition.TransitionID {
					action := mappingBoardAction(candidate)
					result["requiredFields"] = action.RequiredFields
					if action.Reason != "" {
						result["reason"] = action.Reason
					}
				}
			}
		}
	}
	return result, nil
}

func (s *daemonTrackerMapping) Board(ctx context.Context, project string) (api.TrackerBoard, error) {
	result := api.TrackerBoard{SchemaVersion: 1, Columns: []api.TrackerBoardColumn{}, UnknownGroup: api.TrackerBoardColumn{ID: "unmapped", Name: "Unmapped statuses", StatusIDs: []string{}}, Actions: map[string][]api.TrackerBoardAction{}}
	view, err := s.backlog.View(ctx, project, backlog.ViewRequest{Limit: 100})
	if err != nil {
		return result, err
	}
	var rules *trackerrules.RuleSet
	record, err := s.database.ActiveTrackerMapping(ctx, project, view.Binding.Revision)
	if err == nil {
		parsed, err := trackerrules.Decode(record.RulesJSON)
		if err != nil {
			return result, err
		}
		rules = &parsed
		result.ActiveRevision = record.Revision
		result.UnknownGroup = api.TrackerBoardColumn{ID: parsed.Display.UnknownGroup.ID, Name: parsed.Display.UnknownGroup.Name, StatusIDs: []string{}}
		for _, group := range parsed.Display.Groups {
			result.Columns = append(result.Columns, api.TrackerBoardColumn{ID: group.ID, Name: group.Name, StatusIDs: group.StateIDs})
		}
	} else if !mappingMissing(err) {
		return result, err
	}
	if rules == nil {
		seen := map[string]bool{}
		for _, ticket := range view.Tickets {
			if known, ok := ticket.Ticket.BusinessState.(tracker.Known[tracker.NamedID]); ok && !seen[known.Value.ID] {
				result.Columns = append(result.Columns, api.TrackerBoardColumn{ID: "status-" + known.Value.ID, Name: known.Value.Name, StatusIDs: []string{known.Value.ID}})
				seen[known.Value.ID] = true
			}
		}
	}
	if backlog.Scope(view.Binding).Namespace.Provider != "built_in" {
		result.Reason = "The selected adapter exposes read-only tickets. Source transitions are unavailable; execution controls remain independent."
		return result, nil
	}
	for _, ticket := range view.Tickets {
		if !ticket.CurrentSource || ticket.Status == backlog.Missing || ticket.Status == backlog.Inaccessible || ticket.Status == backlog.OutOfScope || ticket.Status == backlog.Archived {
			continue
		}
		discovery, observation, err := s.discoveryFor(ctx, project, ticket.ObservationID)
		if err != nil {
			result.Reason = err.Error()
			continue
		}
		for _, transition := range discovery.Transitions {
			action := mappingBoardAction(transition)
			if rules != nil && observation != nil {
				if err := trackerrules.Validate(*rules, discovery); err != nil {
					action.Availability = "unavailable"
					action.Reason = "Active configuration drift: " + err.Error()
				} else {
					projected := *observation
					projected.Ticket.BusinessState = tracker.Known[tracker.NamedID]{Value: transition.ToState}
					preview, err := trackerrules.PreviewIntake(*rules, discovery, projected)
					if err != nil {
						action.Availability = "unavailable"
						action.Reason = err.Error()
					} else if admission, ok := preview.Action.(trackerrules.Admit); ok && preview.Matched {
						action.Automation = append(action.Automation, fmt.Sprintf("%s admission to %s@%s; readiness and human preparation approvals still apply", admission.Mode, admission.Workflow.ID, admission.Workflow.Version))
					}
				}
			}
			result.Actions[ticket.ObservationID] = append(result.Actions[ticket.ObservationID], action)
		}
	}
	return result, nil
}

func (s *daemonTrackerMapping) Transition(ctx context.Context, project string, input api.TrackerBoardTransition, key string) (any, error) {
	encoded, err := json.Marshal(input)
	if err != nil {
		return nil, err
	}
	scope := "tracker-board/transition/" + project
	digest := sha256.Sum256(encoded)
	command, _, err := s.database.BeginCommand(ctx, statestore.BeginCommandRequest{Scope: scope, IdempotencyKey: key, RequestDigest: fmt.Sprintf("%x", digest), CreatedAt: time.Now().UTC()})
	if err != nil {
		return nil, err
	}
	if command.Status == "completed" {
		return command.Response, nil
	}
	prepared, _, err := s.database.BeginCommand(ctx, statestore.BeginCommandRequest{Scope: scope + "/target", IdempotencyKey: key, RequestDigest: fmt.Sprintf("%x", digest), CreatedAt: time.Now().UTC()})
	if err != nil {
		return nil, err
	}
	if prepared.Status == "completed" {
		var target boardTransitionTarget
		if err := json.Unmarshal(prepared.Response, &target); err != nil {
			return nil, err
		}
		return s.applyBoardTransition(ctx, project, input, key, scope, target)
	}
	binding, err := s.database.BacklogBinding(ctx, project)
	if err != nil {
		return nil, err
	}
	if binding.Revision != input.ExpectedBindingRevision {
		return nil, mappingFailure(ports.FailureConflict, "Selected source changed; reload the board.")
	}
	if backlog.Scope(binding).Namespace.Provider != "built_in" {
		return nil, mappingFailure(ports.FailureUnsupported, "The selected adapter does not expose authorized source transitions.")
	}
	history, err := s.History(ctx, project)
	if err != nil {
		return nil, err
	}
	if history.ActiveRevision != input.ExpectedMappingRevision {
		return nil, mappingFailure(ports.FailureConflict, "Mapping changed; review the current automation before moving the ticket.")
	}
	board, err := s.Board(ctx, project)
	if err != nil {
		return nil, err
	}
	if board.ActiveRevision != input.ExpectedMappingRevision {
		return nil, mappingFailure(ports.FailureConflict, "Mapping changed while resolving allowed actions; reload the board.")
	}
	allowed := false
	for _, action := range board.Actions[input.ObservationID] {
		if action.ID == input.TransitionID && action.Availability == "available" {
			allowed = true
		}
	}
	if !allowed {
		return nil, mappingFailure(ports.FailureUnsupported, "Transition is not currently allowed for the exact ticket observation.")
	}
	_, observation, err := s.discoveryFor(ctx, project, input.ObservationID)
	if err != nil {
		return nil, err
	}
	target := boardTransitionTarget{Ref: observation.Ticket.Ref, Revision: observation.Ticket.Revision}
	frozen, err := json.Marshal(target)
	if err != nil {
		return nil, err
	}
	if _, err := s.database.CompleteCommand(ctx, statestore.CompleteCommandRequest{Scope: scope + "/target", IdempotencyKey: key, ResponseStatus: 200, Response: frozen, CompletedAt: time.Now().UTC()}); err != nil {
		return nil, err
	}
	return s.applyBoardTransition(ctx, project, input, key, scope, target)
}

type boardTransitionTarget struct {
	Ref      tracker.TicketRef `json:"ref"`
	Revision string            `json:"revision"`
}

func (s *daemonTrackerMapping) applyBoardTransition(ctx context.Context, project string, input api.TrackerBoardTransition, key, scope string, target boardTransitionTarget) (any, error) {
	result, err := s.native.Transition(ctx, project, target.Ref.ID, ticketmanagement.TransitionRequest{SchemaVersion: 1, Revision: target.Revision, TransitionID: input.TransitionID}, key)
	if err != nil {
		return nil, err
	}
	// Refresh records the source fact through the normal observation path. Any
	// configured intake is evaluated independently by the daemon's intake hook.
	_, _ = s.backlog.ReadRefresh(ctx, project, input.ExpectedBindingRevision, target.Ref)
	response, err := json.Marshal(result)
	if err != nil {
		return nil, err
	}
	if _, err := s.database.CompleteCommand(ctx, statestore.CompleteCommandRequest{Scope: scope, IdempotencyKey: key, ResponseStatus: 200, Response: response, CompletedAt: time.Now().UTC()}); err != nil {
		return nil, err
	}
	return result, nil
}
