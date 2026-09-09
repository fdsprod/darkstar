package runexecution

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"

	"darkstar/src/core/workflow"
	"darkstar/src/ports/statestore"
)

func executionApprovalID(visitID string) string {
	return stableID("approval_", "execution-checkpoint:"+visitID)
}

func (s *Service) workflowCheckpointEvent(dispatch AttemptRequestContext, attempt statestore.AttemptProjection, visit statestore.NodeProjection) statestore.PendingEvent {
	output, _ := json.Marshal(dispatch.ExecutionContext.AcceptedOutputs[attempt.NodeID])
	// On initial completion accepted output is supplied by the caller's updated context.
	policy, _ := json.Marshal(dispatch.Node.Fields().Checkpoint)
	id := executionApprovalID(visit.VisitID)
	return pendingEvent("approval.requested", statestore.AggregateApproval, id, 0, attempt.RunID, "request:"+id, statestore.ActorSystem, "daemon", s.now(), map[string]any{
		"runId": attempt.RunID, "class": statestore.ApprovalWorkflowControl, "visitId": visit.VisitID, "nodeId": attempt.NodeID, "attemptId": attempt.AttemptID,
		"scopeDigest": fmt.Sprintf("%x", sha256.Sum256(append([]byte(attempt.AttemptID+"\x00"), output...))), "policyDigest": fmt.Sprintf("%x", sha256.Sum256(policy)),
	})
}

// Recover missing decisions from older scheduler versions, without deciding or launching work.
func (s *Service) repairWorkflowCheckpoints(ctx context.Context, run statestore.RunProjection) error {
	visits, err := s.store.NodesForRun(ctx, run.RunID)
	if err != nil {
		return err
	}
	attempts, err := s.store.AttemptsForRun(ctx, run.RunID)
	if err != nil {
		return err
	}
	for _, visit := range visits {
		if visit.Status != statestore.NodeWaitingCheckpoint {
			continue
		}
		if _, err := s.store.Approval(ctx, executionApprovalID(visit.VisitID)); err == nil {
			continue
		} else if !errors.Is(err, statestore.ErrNotFound) {
			return err
		}
		for i := len(attempts) - 1; i >= 0; i-- {
			attempt := attempts[i]
			if attempt.VisitID != visit.VisitID || attempt.Status != statestore.AttemptSucceeded {
				continue
			}
			dispatch, err := s.workflowAttemptContext(ctx, attempt, run)
			if err != nil {
				return err
			}
			if _, err = s.store.Append(ctx, s.workflowCheckpointEvent(dispatch, attempt, visit)); err != nil {
				return err
			}
			break
		}
	}
	return nil
}

// ApplyWorkflowCheckpointDecision commits the human decision and state transition
// in one event transaction. The global queue and inline controls share this path.
func (s *Service) ApplyWorkflowCheckpointDecision(ctx context.Context, approval statestore.ApprovalProjection, decision statestore.PendingEvent) ([]statestore.Event, bool, error) {
	if approval.VisitID == "" || approval.ApprovalID != executionApprovalID(approval.VisitID) {
		return nil, false, nil
	}
	if decision.Actor.Type != statestore.ActorUser {
		return nil, true, errors.New("workflow checkpoint decisions require a human")
	}
	s.checkpointMu.Lock()
	defer s.checkpointMu.Unlock()
	run, err := s.store.Run(ctx, approval.RunID)
	if err != nil {
		return nil, true, err
	}
	visit, err := s.store.Node(ctx, approval.VisitID)
	if err != nil {
		return nil, true, err
	}
	if run.Status != statestore.RunWaiting || visit.Status != statestore.NodeWaitingCheckpoint {
		return nil, true, errors.New("this workflow is no longer waiting for this decision")
	}
	attempt, err := s.store.Attempt(ctx, approval.AttemptID)
	if err != nil {
		return nil, true, err
	}
	if s.artifactReviews != nil {
		d, e := s.workflowAttemptContext(ctx, attempt, run)
		if e != nil {
			return nil, true, e
		}
		if len(reviewOutputs(d.Node)) > 0 {
			return nil, true, errors.New("review and approve each document in Artifacts")
		}
	}
	var body struct {
		Action string `json:"action"`
	}
	if err = json.Unmarshal(decision.Data, &body); err != nil {
		return nil, true, err
	}
	now := s.now()
	events := []statestore.PendingEvent{decision}
	if body.Action != "approve" {
		events = append(events, pendingEvent("visit.rejected", statestore.AggregateVisit, visit.VisitID, visit.ResourceVersion, run.RunID, "reject:"+decision.CommandID, statestore.ActorUser, decision.Actor.ID, now, map[string]any{}), pendingEvent("run.cancelled", statestore.AggregateRun, run.RunID, run.ResourceVersion, run.RunID, "cancel:"+decision.CommandID, statestore.ActorUser, decision.Actor.ID, now, map[string]any{}))
		_, err = s.store.Append(ctx, events...)
	} else {
		dispatch, readErr := s.workflowAttemptContext(ctx, attempt, run)
		if readErr != nil {
			return nil, true, readErr
		}
		outputs, present := dispatch.AcceptedOutputs[workflow.Identifier(attempt.NodeID)]
		if !present && len(dispatch.Node.Fields().Outputs) > 0 {
			return nil, true, errors.New("the checkpoint has no saved output to approve")
		}
		var binding struct {
			ScopeDigest  string `json:"scopeDigest"`
			PolicyDigest string `json:"policyDigest"`
		}
		_ = json.Unmarshal(s.workflowCheckpointEvent(dispatch, attempt, visit).Data, &binding)
		if binding.ScopeDigest != approval.ScopeDigest || binding.PolicyDigest != approval.PolicyDigest {
			return nil, true, errors.New("checkpoint output or policy changed; this decision is stale")
		}
		advance, advanceErr := prepareWorkflowAdvance(dispatch, outputs, s.valueSchemas)
		if advanceErr != nil {
			return nil, true, advanceErr
		}
		saved := dispatch.ExecutionContext
		if advance.persist {
			saved, err = s.store.SaveRunExecutionContext(ctx, advance.context, saved.Revision)
			if err != nil {
				return nil, true, err
			}
		}
		events = append(events, pendingEvent("run.resumed", statestore.AggregateRun, run.RunID, run.ResourceVersion, run.RunID, "resume:"+decision.CommandID, statestore.ActorUser, decision.Actor.ID, now, map[string]any{}), pendingEvent("run.visit_ready", statestore.AggregateRun, run.RunID, run.ResourceVersion+1, run.RunID, "ready:"+decision.CommandID, statestore.ActorSystem, "daemon", now, map[string]any{}))
		run.ResourceVersion += 2
		err = s.finishWorkflowAdvance(ctx, dispatch, attempt, run, visit, saved, advance, events, visit.ResourceVersion, map[string]any{"approvalId": approval.ApprovalID})
	}
	if err != nil {
		return nil, true, err
	}
	event, err := s.store.(interface {
		EventByCommand(context.Context, string, string) (statestore.Event, error)
	}).EventByCommand(ctx, decision.AggregateID, decision.CommandID)
	return []statestore.Event{event}, true, err
}
