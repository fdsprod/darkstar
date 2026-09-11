package runexecution

import (
	"context"
	"crypto/sha256"
	"darkstar/src/core/extensions"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"sync"

	"darkstar/src/core/artifactcheckpoint"
	"darkstar/src/core/artifactops"
	"darkstar/src/core/workflow"
	"darkstar/src/ports/artifactbinding"
	cp "darkstar/src/ports/artifactcheckpoint"
	"darkstar/src/ports/artifactregistry"
	"darkstar/src/ports/provider"
	"darkstar/src/ports/statestore"
)

type artifactReviewBridge struct {
	artifacts *artifactops.Service
	reviews   *artifactcheckpoint.Service
	mu        sync.Mutex
}

func (s *Service) SetArtifactReviews(a *artifactops.Service, r *artifactcheckpoint.Service) {
	s.artifactReviews = &artifactReviewBridge{artifacts: a, reviews: r}
}
func reviewID(visit, output string) string {
	return stableID("checkpoint_", "output-review:"+visit+":"+output)
}
func (s *Service) reviewRounds(ctx context.Context, visit, output string) ([]statestore.ApprovalProjection, error) {
	return s.store.(cp.Store).ApprovalsForCheckpoint(ctx, reviewID(visit, output))
}
func reviewOutputs(node workflow.Node) []string {
	checkpoint := node.Fields().Checkpoint
	if checkpoint == nil || (checkpoint.Mode() != workflow.CheckpointApprove && checkpoint.Mode() != workflow.CheckpointApproveOnChange) {
		return nil
	}
	var ids []string
	for id, d := range node.Fields().Outputs {
		if d.Type == workflow.ValueMarkdown || (d.Artifact != nil && d.Type.StorageType() == workflow.ValueString) {
			ids = append(ids, string(id))
		}
	}
	sort.Strings(ids)
	return ids
}
func (s *Service) reviewText(ctx context.Context, ref artifactregistry.VersionRef) (string, error) {
	content, err := s.artifactReviews.artifacts.OriginalContent(ctx, ref)
	if err != nil {
		return "", err
	}
	defer content.Reader.Close()
	b, err := io.ReadAll(content.Reader)
	return string(b), err
}

// Reconcile derives work from durable reviews. A restart never approves a candidate.
func (s *Service) ReconcileArtifactReviews(ctx context.Context) error {
	b := s.artifactReviews
	if b == nil {
		return nil
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	s.checkpointMu.Lock()
	defer s.checkpointMu.Unlock()
	runs, err := s.store.Runs(ctx)
	if err != nil {
		return err
	}
	for _, run := range runs {
		if run.Status != statestore.RunWaiting {
			continue
		}
		visits, err := s.store.NodesForRun(ctx, run.RunID)
		if err != nil {
			return err
		}
		for _, visit := range visits {
			if visit.Status != statestore.NodeWaitingCheckpoint {
				continue
			}
			attempts, err := s.store.AttemptsForRun(ctx, run.RunID)
			if err != nil {
				return err
			}
			var original statestore.AttemptProjection
			for _, a := range attempts {
				if a.VisitID == visit.VisitID && a.Status == statestore.AttemptSucceeded && (original.AttemptID == "" || a.CreatedAt.Before(original.CreatedAt)) {
					original = a
				}
			}
			if original.AttemptID == "" {
				continue
			}
			dispatch, err := s.workflowAttemptContext(ctx, original, run)
			if err != nil {
				return err
			}
			ids := reviewOutputs(dispatch.Node)
			if len(ids) == 0 {
				continue
			}
			allApproved := true
			for _, id := range ids {
				rounds, err := s.reviewRounds(ctx, visit.VisitID, id)
				if err != nil {
					return err
				}
				if len(rounds) == 0 {
					var text string
					if err = json.Unmarshal(dispatch.AcceptedOutputs[workflow.Identifier(visit.NodeID)][workflow.Identifier(id)], &text); err != nil {
						return fmt.Errorf("review output %s must be Markdown: %w", id, err)
					}
					key := reviewID(visit.VisitID, id)
					ingested, err := b.artifacts.Ingest(ctx, artifactops.IngestInput{GeneratedBy: &artifactregistry.AttemptProvenance{RunID: run.RunID, NodeID: visit.NodeID, AttemptID: original.AttemptID}, SourceKind: artifactregistry.SourceGenerated, SourceName: id + ".md", MediaType: "text/markdown", Content: []byte(text), Creator: "daemon", Roles: []string{"workflow-output"}}, key)
					if err != nil {
						return err
					}
					ref := artifactregistry.VersionRef{ArtifactID: ingested.Artifact.ArtifactID, Version: ingested.Artifact.Version}
					if _, err = b.artifacts.Extract(ctx, ref, key+":extract"); err != nil {
						return err
					}
					if _, err = b.artifacts.Attach(ctx, artifactops.AttachInput{Artifact: ref, Target: artifactbinding.Target{Kind: artifactbinding.TargetRun, ID: run.RunID}}, key+":attach"); err != nil {
						return err
					}
					_, err = b.reviews.Open(ctx, artifactcheckpoint.OpenRequest{ApprovalID: stableID("approval_", key+":1"), CheckpointID: key, RunID: run.RunID, VisitID: visit.VisitID, NodeID: visit.NodeID, AttemptID: original.AttemptID, Candidate: ref, Mode: cp.ModeApprove, PolicyDigest: fmt.Sprintf("%x", shaReviewPolicy()), IdempotencyKey: key + ":open", Actor: statestore.Actor{Type: statestore.ActorSystem, ID: "daemon"}})
					if err != nil {
						return err
					}
					rounds, err = s.reviewRounds(ctx, visit.VisitID, id)
					if err != nil {
						return err
					}
				}
				current := rounds[len(rounds)-1]
				if current.Status == statestore.ApprovalApproved {
					continue
				}
				allApproved = false
				session, err := b.reviews.ReviewSession(ctx, current.ApprovalID)
				if err != nil {
					return err
				}
				if session.State == cp.ReviewAwaitingAgent && s.schedulingAdmitted() {
					if err = s.startReviewRevision(ctx, run, visit, original, current, session); err != nil {
						return err
					}
					break
				}
			}
			// Retire the coarse control only after all exact artifact review requests exist.
			control, err := s.store.Approval(ctx, executionApprovalID(visit.VisitID))
			if err == nil && control.Status == statestore.ApprovalPending {
				_, err = s.store.Append(ctx, pendingEvent("approval.cancelled", statestore.AggregateApproval, control.ApprovalID, control.ResourceVersion, run.RunID, "artifact-review:"+control.ApprovalID, statestore.ActorSystem, "daemon", s.now(), map[string]any{}))
				if err != nil {
					return err
				}
			}
			if allApproved && s.schedulingAdmitted() {
				if err = s.advanceReviewedArtifacts(ctx, dispatch, original, run, visit, ids); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func shaReviewPolicy() [32]byte {
	return sha256.Sum256([]byte("artifact-review:human-approval-required:unlimited:v1"))
}
func (s *Service) startReviewRevision(ctx context.Context, run statestore.RunProjection, visit statestore.NodeProjection, original statestore.AttemptProjection, approval statestore.ApprovalProjection, session cp.ReviewSession) error {
	id := stableID("attempt_", fmt.Sprintf("review:%s:%d", session.ID, session.ResourceVersion))
	if session.ActiveIteration != nil {
		id = session.ActiveIteration.AttemptID
	} else {
		var err error
		session, err = s.artifactReviews.reviews.ResumeRevision(ctx, artifactcheckpoint.ResumeRequest{ApprovalID: session.ID, ExpectedResourceVersion: session.ResourceVersion, CandidateDigest: session.CandidateDigest, ScopeDigest: session.ScopeDigest, AttemptID: id, IdempotencyKey: "resume:" + id, Actor: statestore.Actor{Type: statestore.ActorSystem, ID: "daemon"}})
		if err != nil {
			return err
		}
	}
	if a, err := s.store.Attempt(ctx, id); err == nil {
		if a.Status.Terminal() {
			return s.recoverReviewResponse(ctx, session, a)
		}
		return s.launch(a)
	} else if !errors.Is(err, statestore.ErrNotFound) {
		return err
	}
	now := s.now()
	_, err := s.store.Append(ctx,
		pendingEvent("run.resumed", statestore.AggregateRun, run.RunID, run.ResourceVersion, run.RunID, "revision-resume:"+id, statestore.ActorSystem, "daemon", now, map[string]any{}),
		pendingEvent("run.visit_ready", statestore.AggregateRun, run.RunID, run.ResourceVersion+1, run.RunID, "revision-ready:"+id, statestore.ActorSystem, "daemon", now, map[string]any{}),
		pendingEvent("visit.changes_requested", statestore.AggregateVisit, visit.VisitID, visit.ResourceVersion, run.RunID, "revision-visit:"+id, statestore.ActorSystem, "daemon", now, map[string]any{}),
		pendingEvent("attempt.created", statestore.AggregateAttempt, id, 0, run.RunID, "attempt-create:"+id, statestore.ActorSystem, "daemon", now, map[string]any{"runId": run.RunID, "visitId": visit.VisitID, "nodeId": visit.NodeID, "scenario": ScenarioWorkflow, "provider": original.Provider, "logReference": id + ".log", "priority": run.Priority}))
	if err != nil {
		return err
	}
	a, err := s.store.Attempt(ctx, id)
	if err != nil {
		return err
	}
	return s.launch(a)
}
func (s *Service) activeReview(ctx context.Context, attempt statestore.AttemptProjection) (*cp.ReviewSession, error) {
	if s.artifactReviews == nil {
		return nil, nil
	}
	values, err := s.store.(cp.Store).CheckpointApprovals(ctx, attempt.RunID, statestore.ApprovalPending)
	if err != nil {
		return nil, err
	}
	for _, a := range values {
		if a.VisitID != attempt.VisitID {
			continue
		}
		session, err := s.artifactReviews.reviews.ReviewSession(ctx, a.ApprovalID)
		if err != nil {
			return nil, err
		}
		if session.ActiveIteration != nil && session.ActiveIteration.AttemptID == attempt.AttemptID {
			return &session, nil
		}
	}
	return nil, nil
}
func (s *Service) reviewAttemptContext(ctx context.Context, request AttemptRequestContext) (AttemptRequestContext, error) {
	session, err := s.activeReview(ctx, request.Attempt)
	if err != nil || session == nil {
		return request, err
	}
	var output string
	for _, id := range reviewOutputs(request.Node) {
		if reviewID(request.Attempt.VisitID, id) == session.CheckpointID {
			output = id
			break
		}
	}
	if output == "" {
		return request, errors.New("review output binding missing")
	}
	text, err := s.reviewText(ctx, session.Candidate)
	if err != nil {
		return request, err
	}
	var feedback cp.HumanFeedbackTurn
	for _, turn := range session.Turns {
		if v, ok := turn.(cp.HumanFeedbackTurn); ok {
			feedback = v
		}
	}
	candidate, _ := json.Marshal(text)
	feedbackContent := map[string]any{"instruction": feedback.Message}
	if feedback.FeedbackSet != nil {
		feedbackContent["instruction"] = feedback.FeedbackSet.OverallInstruction
		feedbackContent["annotations"] = feedback.FeedbackSet.Annotations
	}
	instructions, _ := json.Marshal(feedbackContent)
	return buildReviewTask(request, output, candidate, instructions), nil
}

func buildReviewTask(request AttemptRequestContext, output string, candidate, instructions json.RawMessage) AttemptRequestContext {
	originalNode := request.Node
	original := request.Node.Fields()
	originalInputs := request.NodeInputs
	request.Revision = true
	request.NodeInputs = map[workflow.Identifier]json.RawMessage{"candidate": candidate, "feedback": instructions}
	declaration := request.Node.Fields().Outputs[workflow.Identifier(output)]
	declaration.Type = workflow.ValueMarkdown
	request.Node = workflow.ReasoningNode{Common: workflow.NodeFields{Inputs: map[workflow.Identifier]workflow.Binding{"candidate": workflow.RequiredBinding{Type: workflow.ValueMarkdown}, "feedback": workflow.RequiredBinding{Type: workflow.ValueObject}}, Outputs: map[workflow.Identifier]workflow.OutputDeclaration{workflow.Identifier(output): declaration}}, Executor: workflow.ReasoningExecutor{Instructions: "Revise the supplied Markdown candidate using the human feedback and annotations. Return the complete revised document under output " + output + ". Preserve content outside the requested changes. Do not approve the document, edit repository files, or perform the work described in the document."}}
	revisionNode := request.Node.(workflow.ReasoningNode)
	if source, ok := originalNode.(workflow.ReasoningNode); ok {
		revisionNode.Executor.Skills = append([]string(nil), source.Executor.Skills...)
	}
	if original.Prompt != nil {
		revisionNode.Common.Prompt = original.Prompt
		linkedNames := map[workflow.Identifier]bool{"open_items": true, "deferred_work": true}
		snapshot := request.ExecutionContext.PromptSnapshots[request.Attempt.NodeID]
		for _, section := range snapshot.Document.Sections {
			if section.When.Kind == "input_linked" || section.When.Kind == "input_absent" {
				linkedNames[workflow.Identifier(section.When.Input)] = true
			}
		}
		for name := range linkedNames {
			if binding, exists := original.Inputs[name]; exists {
				revisionNode.Common.Inputs[name] = binding
				if value, available := originalInputs[name]; available {
					request.NodeInputs[name] = value
				}
			}
		}
	}
	if declaration.Artifact != nil && declaration.Artifact.TemplateInput != "" {
		name := declaration.Artifact.TemplateInput
		revisionNode.Common.Inputs[name] = original.Inputs[name]
		if value, exists := originalInputs[name]; exists {
			request.NodeInputs[name] = value
		}
	}
	request.Node = revisionNode
	return request
}
func (s *Service) completeReviewAttempt(ctx context.Context, dispatch AttemptRequestContext, attempt statestore.AttemptProjection, run statestore.RunProjection, visit statestore.NodeProjection, result provider.SucceededResult, validationEvidence []extensions.ValidationEvidence) (bool, error) {
	session, err := s.activeReview(ctx, attempt)
	if err != nil || session == nil {
		return session != nil, err
	}
	outputs, err := decodeNodeOutputs(dispatch.Node, result.StructuredOutput)
	if err != nil {
		return true, err
	}
	var document string
	for id, value := range outputs {
		if err = workflow.ValidateDeliverable(dispatch.Node, id, value, dispatch.NodeInputs, s.valueSchemas); err != nil {
			return true, err
		}
		if err = json.Unmarshal(value, &document); err != nil {
			return true, err
		}
	}
	ref := session.Candidate
	key := "review-result:" + attempt.AttemptID
	ingested, err := s.artifactReviews.artifacts.Revise(ctx, ref.ArtifactID, ref.Version, artifactops.IngestInput{GeneratedBy: &artifactregistry.AttemptProvenance{RunID: run.RunID, NodeID: visit.NodeID, AttemptID: attempt.AttemptID, Source: &session.Candidate}, SourceKind: artifactregistry.SourceGenerated, SourceName: stringOnlyOutput(dispatch.Node) + ".md", MediaType: "text/markdown", Content: []byte(document), Creator: "daemon"}, key)
	if err != nil {
		return true, err
	}
	ref.Version = ingested.Artifact.Version
	if _, err = s.artifactReviews.artifacts.Extract(ctx, ref, key+":extract"); err != nil {
		return true, err
	}
	_, err = s.artifactReviews.artifacts.Attach(ctx, artifactops.AttachInput{Artifact: ref, Target: artifactbinding.Target{Kind: artifactbinding.TargetRun, ID: run.RunID}}, key+":attach")
	if err != nil {
		return true, err
	}
	// Completion is recorded before publishing the next review; recovery can finish
	// this sequence from the immutable result if the daemon stops between commits.
	now := s.now()
	data := map[string]any{"lastSequence": attempt.LastSequence, "output": json.RawMessage(result.StructuredOutput)}
	if len(validationEvidence) > 0 {
		data["validationEvidence"] = validationEvidence
	}
	_, err = s.store.Append(ctx, pendingEvent("attempt.result_received", statestore.AggregateAttempt, attempt.AttemptID, attempt.ResourceVersion, run.RunID, "result:"+attempt.AttemptID, statestore.ActorProvider, attempt.Provider, now, data), pendingEvent("attempt.succeeded", statestore.AggregateAttempt, attempt.AttemptID, attempt.ResourceVersion+1, run.RunID, "terminal:"+attempt.AttemptID, statestore.ActorSystem, "daemon", now, data), pendingEvent("visit.result_received", statestore.AggregateVisit, visit.VisitID, visit.ResourceVersion, run.RunID, "result:"+attempt.AttemptID, statestore.ActorSystem, "daemon", now, data), pendingEvent("visit.waiting_checkpoint", statestore.AggregateVisit, visit.VisitID, visit.ResourceVersion+1, run.RunID, "checkpoint:"+attempt.AttemptID, statestore.ActorSystem, "daemon", now, data), pendingEvent("run.waiting", statestore.AggregateRun, run.RunID, run.ResourceVersion, run.RunID, "checkpoint:"+attempt.AttemptID, statestore.ActorSystem, "daemon", now, data))
	if err != nil {
		return true, err
	}
	_, err = s.artifactReviews.reviews.RecordAgentResponse(ctx, artifactcheckpoint.AgentResponseRequest{ApprovalID: session.ID, ExpectedResourceVersion: session.ResourceVersion, CandidateDigest: session.CandidateDigest, ScopeDigest: session.ScopeDigest, AttemptID: attempt.AttemptID, Outcome: cp.AgentRevised, Candidate: ref, NextApprovalID: stableID("approval_", key), IdempotencyKey: key, Actor: statestore.Actor{Type: statestore.ActorSystem, ID: "daemon"}})
	return true, err
}
func stringOnlyOutput(node workflow.Node) string {
	for id := range node.Fields().Outputs {
		return string(id)
	}
	return "document"
}
func (s *Service) advanceReviewedArtifacts(ctx context.Context, dispatch AttemptRequestContext, attempt statestore.AttemptProjection, run statestore.RunProjection, visit statestore.NodeProjection, ids []string) error {
	outputs := cloneRawMap(dispatch.AcceptedOutputs[workflow.Identifier(visit.NodeID)])
	for _, id := range ids {
		rounds, err := s.reviewRounds(ctx, visit.VisitID, id)
		if err != nil {
			return err
		}
		if len(rounds) == 0 {
			return errors.New("artifact approval missing")
		}
		a := rounds[len(rounds)-1]
		if a.Status != statestore.ApprovalApproved || a.Decision == nil || a.Decision.Actor.Type != statestore.ActorUser {
			return errors.New("artifact requires explicit human approval")
		}
		text, err := s.reviewText(ctx, artifactregistry.VersionRef{ArtifactID: a.CandidateArtifactID, Version: a.CandidateArtifactVersion})
		if err != nil {
			return err
		}
		outputs[workflow.Identifier(id)], _ = json.Marshal(text)
	}
	validationEvidence, err := s.validateExtensionOutputs(ctx, dispatch, outputs)
	if err != nil {
		return err
	}
	advance, err := prepareWorkflowAdvance(dispatch, outputs, s.valueSchemas)
	if err != nil {
		return err
	}
	saved := dispatch.ExecutionContext
	if advance.persist {
		saved, err = s.store.SaveRunExecutionContext(ctx, advance.context, saved.Revision)
		if err != nil {
			return err
		}
	}
	events := []statestore.PendingEvent{pendingEvent("run.resumed", statestore.AggregateRun, run.RunID, run.ResourceVersion, run.RunID, "review-accepted:"+visit.VisitID, statestore.ActorSystem, "daemon", s.now(), map[string]any{}), pendingEvent("run.visit_ready", statestore.AggregateRun, run.RunID, run.ResourceVersion+1, run.RunID, "review-ready:"+visit.VisitID, statestore.ActorSystem, "daemon", s.now(), map[string]any{})}
	run.ResourceVersion += 2
	return s.finishWorkflowAdvance(ctx, dispatch, attempt, run, visit, saved, advance, events, visit.ResourceVersion, map[string]any{"reviewed": true, "validationEvidence": validationEvidence})
}
func (s *Service) recoverReviewResponse(ctx context.Context, session cp.ReviewSession, attempt statestore.AttemptProjection) error {
	outcome := cp.AgentFailed
	ref := artifactregistry.VersionRef{}
	next := ""
	message := "Revision did not complete. Your document and feedback are preserved; you can request another revision."
	if attempt.Status == statestore.AttemptSucceeded {
		outcome = cp.AgentRevised
		ref = session.Candidate
		ref.Version++
		next = stableID("approval_", "review-result:"+attempt.AttemptID)
		message = ""
	}
	_, err := s.artifactReviews.reviews.RecordAgentResponse(ctx, artifactcheckpoint.AgentResponseRequest{ApprovalID: session.ID, ExpectedResourceVersion: session.ResourceVersion, CandidateDigest: session.CandidateDigest, ScopeDigest: session.ScopeDigest, AttemptID: attempt.AttemptID, Outcome: outcome, Candidate: ref, NextApprovalID: next, Message: message, IdempotencyKey: "review-result:" + attempt.AttemptID, Actor: statestore.Actor{Type: statestore.ActorSystem, ID: "daemon"}})
	return err
}
func (s *Service) failReviewAttempt(ctx context.Context, attempt statestore.AttemptProjection, run statestore.RunProjection, visit statestore.NodeProjection, message string) (bool, error) {
	session, err := s.activeReview(ctx, attempt)
	if err != nil || session == nil {
		return session != nil, err
	}
	if run.Status != statestore.RunRunning || visit.Status != statestore.NodeRunning {
		return false, nil
	}
	data := map[string]any{"message": message}
	now := s.now()
	_, err = s.store.Append(ctx, pendingEvent("attempt.failed", statestore.AggregateAttempt, attempt.AttemptID, attempt.ResourceVersion, run.RunID, "failure:"+attempt.AttemptID, statestore.ActorSystem, "daemon", now, data), pendingEvent("visit.result_received", statestore.AggregateVisit, visit.VisitID, visit.ResourceVersion, run.RunID, "revision-failed:"+attempt.AttemptID, statestore.ActorSystem, "daemon", now, data), pendingEvent("visit.waiting_checkpoint", statestore.AggregateVisit, visit.VisitID, visit.ResourceVersion+1, run.RunID, "revision-wait:"+attempt.AttemptID, statestore.ActorSystem, "daemon", now, data), pendingEvent("run.waiting", statestore.AggregateRun, run.RunID, run.ResourceVersion, run.RunID, "revision-wait:"+attempt.AttemptID, statestore.ActorSystem, "daemon", now, data))
	if err != nil {
		return true, err
	}
	attempt.Status = statestore.AttemptFailed
	return true, s.recoverReviewResponse(ctx, *session, attempt)
}
