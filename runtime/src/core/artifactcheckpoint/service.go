// Package artifactcheckpoint coordinates idempotent artifact review and revision.
package artifactcheckpoint

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"darkstar/src/core/identity"
	checkpointport "darkstar/src/ports/artifactcheckpoint"
	"darkstar/src/ports/artifactlineage"
	"darkstar/src/ports/artifactregistry"
	"darkstar/src/ports/statestore"
)

var (
	ErrInvalidRequest     = errors.New("invalid artifact checkpoint request")
	ErrCandidateConflict  = errors.New("artifact checkpoint candidate conflict")
	ErrCheckpointConflict = errors.New("artifact checkpoint configuration conflict")
)

var digestPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

// OpenRequest creates the first or next review request for an exact artifact
// draft. ApprovalID is the public request identity; CheckpointID groups rounds.
type OpenRequest struct {
	ApprovalID     string
	CheckpointID   string
	RunID          string
	VisitID        string
	NodeID         string
	AttemptID      string
	Candidate      artifactregistry.VersionRef
	Mode           checkpointport.Mode
	MaxRevisions   *uint64
	PolicyDigest   string
	IdempotencyKey string
	Actor          statestore.Actor
}

// DecisionRequest resolves one exact pending candidate.
type DecisionRequest struct {
	ApprovalID              string
	ExpectedResourceVersion uint64
	Action                  checkpointport.Action
	CandidateDigest         string
	ScopeDigest             string
	PolicyDigest            string
	Comment                 string
	IdempotencyKey          string
	Actor                   statestore.Actor
}

type FeedbackRequest struct {
	ApprovalID              string
	ExpectedResourceVersion uint64
	CandidateDigest         string
	ScopeDigest             string
	Message                 string
	IdempotencyKey          string
	Actor                   statestore.Actor
}

type ResumeRequest struct {
	ApprovalID              string
	ExpectedResourceVersion uint64
	CandidateDigest         string
	ScopeDigest             string
	AttemptID               string
	IdempotencyKey          string
	Actor                   statestore.Actor
}

type AgentResponseRequest struct {
	ApprovalID              string
	ExpectedResourceVersion uint64
	CandidateDigest         string
	ScopeDigest             string
	AttemptID               string
	Outcome                 checkpointport.AgentOutcome
	Message                 string
	Candidate               artifactregistry.VersionRef
	NextApprovalID          string
	IdempotencyKey          string
	Actor                   statestore.Actor
}

type ListRequest struct {
	RunID  string
	Status statestore.ApprovalStatus
}

// Service composes immutable artifact metadata, revision invalidations, and
// durable approval events without making workflow transitions itself.
type Service struct {
	store     checkpointport.Store
	artifacts ArtifactReader
	lineage   LineageReader
	now       func() time.Time
}

// ArtifactReader resolves immutable candidate metadata.
type ArtifactReader interface {
	ArtifactVersion(context.Context, artifactregistry.VersionRef) (artifactregistry.ArtifactVersion, error)
}

// LineageReader resolves already-persisted effects created by an artifact revision.
type LineageReader interface {
	AffectedBy(context.Context, artifactregistry.VersionRef) ([]artifactlineage.Invalidation, error)
}

func New(store checkpointport.Store, artifacts ArtifactReader, lineage LineageReader) (*Service, error) {
	if store == nil || artifacts == nil || lineage == nil {
		return nil, errors.New("artifact checkpoint requires state, artifact, and lineage stores")
	}
	return &Service{store: store, artifacts: artifacts, lineage: lineage, now: time.Now}, nil
}

// Open records one immutable draft review. A revision is accepted only after
// request_changes on the preceding round and must be the next artifact version.
func (service *Service) Open(ctx context.Context, request OpenRequest) (checkpointport.Round, error) {
	if err := validateOpen(request); err != nil {
		return checkpointport.Round{}, err
	}
	candidate, err := service.artifacts.ArtifactVersion(ctx, request.Candidate)
	if err != nil {
		return checkpointport.Round{}, fmt.Errorf("read checkpoint candidate: %w", err)
	}
	if existing, existingErr := service.store.Approval(ctx, request.ApprovalID); existingErr == nil {
		payload := openPayload(request, existing.CheckpointRevision, candidate.BlobDigest)
		committed, readErr := service.store.EventByCommand(ctx, request.ApprovalID, request.IdempotencyKey)
		if readErr == nil && committed.Kind == "approval.requested" && string(committed.Data) == string(payload) && committed.Actor == request.Actor {
			return service.roundFromProjection(ctx, existing)
		}
		if readErr == nil {
			return checkpointport.Round{}, checkpointport.ErrIdempotencyConflict
		}
		return checkpointport.Round{}, ErrCheckpointConflict
	} else if !errors.Is(existingErr, statestore.ErrNotFound) {
		return checkpointport.Round{}, existingErr
	}
	node, err := service.store.Node(ctx, request.VisitID)
	if err != nil {
		return checkpointport.Round{}, fmt.Errorf("read checkpoint visit: %w", err)
	}
	if node.RunID != request.RunID || node.NodeID != request.NodeID || node.Status != statestore.NodeWaitingCheckpoint {
		return checkpointport.Round{}, fmt.Errorf("%w: visit %s is not the waiting %s node in run %s", ErrCheckpointConflict, request.VisitID, request.NodeID, request.RunID)
	}

	history, err := service.store.ApprovalsForCheckpoint(ctx, request.CheckpointID)
	if err != nil {
		return checkpointport.Round{}, fmt.Errorf("read checkpoint history: %w", err)
	}
	revision := uint64(len(history) + 1)
	if len(history) == 0 {
		if request.Candidate.Version == 0 {
			return checkpointport.Round{}, fmt.Errorf("%w: initial candidate version is required", ErrInvalidRequest)
		}
	} else {
		prior := history[len(history)-1]
		if prior.Status != statestore.ApprovalChangesRequested {
			return checkpointport.Round{}, checkpointport.ErrRevisionRequired
		}
		if prior.RunID != request.RunID || prior.VisitID != request.VisitID || prior.NodeID != request.NodeID ||
			prior.CandidateArtifactID != request.Candidate.ArtifactID || prior.CandidateArtifactVersion+1 != request.Candidate.Version ||
			prior.CheckpointMode != string(request.Mode) || prior.PolicyDigest != request.PolicyDigest || !sameLimit(prior.MaxRevisions, request.MaxRevisions) {
			return checkpointport.Round{}, ErrCheckpointConflict
		}
		if request.MaxRevisions != nil && revision-1 > *request.MaxRevisions {
			return checkpointport.Round{}, checkpointport.ErrRevisionLimit
		}
	}

	encoded := openPayload(request, revision, candidate.BlobDigest)
	_, err = service.store.Append(ctx, statestore.PendingEvent{
		SchemaVersion: 1, ID: identity.Random("event_"), AggregateType: statestore.AggregateApproval,
		AggregateID: request.ApprovalID, ExpectedRevision: 0, Kind: "approval.requested",
		OccurredAt: service.now().UTC().Round(0), CorrelationID: request.RunID,
		CommandID: request.IdempotencyKey, Actor: request.Actor, Data: encoded, Metadata: json.RawMessage(`{}`),
	})
	if err != nil {
		return checkpointport.Round{}, fmt.Errorf("open artifact checkpoint: %w", err)
	}
	return service.round(ctx, request.ApprovalID)
}

func openPayload(request OpenRequest, revision uint64, candidateDigest string) json.RawMessage {
	scopeDigest := checkpointScopeDigest(request, revision, candidateDigest)
	data := struct {
		RunID                    string                   `json:"runId"`
		Class                    statestore.ApprovalClass `json:"class"`
		CheckpointID             string                   `json:"checkpointId"`
		VisitID                  string                   `json:"visitId"`
		NodeID                   string                   `json:"nodeId"`
		AttemptID                string                   `json:"attemptId"`
		CheckpointRevision       uint64                   `json:"checkpointRevision"`
		CandidateArtifactID      string                   `json:"candidateArtifactId"`
		CandidateArtifactVersion uint64                   `json:"candidateArtifactVersion"`
		CandidateDigest          string                   `json:"candidateDigest"`
		CheckpointMode           checkpointport.Mode      `json:"checkpointMode"`
		MaxRevisions             *uint64                  `json:"maxRevisions,omitempty"`
		ScopeDigest              string                   `json:"scopeDigest"`
		PolicyDigest             string                   `json:"policyDigest"`
	}{request.RunID, statestore.ApprovalWorkflowCheckpoint, request.CheckpointID, request.VisitID, request.NodeID,
		request.AttemptID, revision, request.Candidate.ArtifactID, request.Candidate.Version, candidateDigest,
		request.Mode, cloneLimit(request.MaxRevisions), scopeDigest, request.PolicyDigest}
	encoded, _ := json.Marshal(data)
	return encoded
}

// Decide commits exactly one terminal action for an exact request. Repeating
// its key and payload returns the original result; all other later decisions fail.
func (service *Service) Decide(ctx context.Context, request DecisionRequest) (checkpointport.Round, error) {
	if err := validateDecision(request); err != nil {
		return checkpointport.Round{}, err
	}
	approval, err := service.store.Approval(ctx, request.ApprovalID)
	if err != nil {
		return checkpointport.Round{}, err
	}
	if approval.Class != statestore.ApprovalWorkflowCheckpoint || approval.CheckpointID == "" {
		return checkpointport.Round{}, fmt.Errorf("%w: approval is not an artifact checkpoint", ErrInvalidRequest)
	}
	payload := decisionPayload(request)
	if approval.Status != statestore.ApprovalPending {
		committed, readErr := service.store.EventByCommand(ctx, request.ApprovalID, request.IdempotencyKey)
		if readErr == nil && committed.Kind == "approval.decided" && string(committed.Data) == string(payload) && committed.Actor == request.Actor {
			return service.round(ctx, request.ApprovalID)
		}
		if readErr == nil {
			return checkpointport.Round{}, checkpointport.ErrIdempotencyConflict
		}
		if state, _, stateErr := service.reviewState(ctx, approval); stateErr == nil && state == checkpointport.ReviewSuperseded {
			return checkpointport.Round{}, ErrCandidateConflict
		}
		return checkpointport.Round{}, checkpointport.ErrAlreadyResolved
	}
	state, _, stateErr := service.reviewState(ctx, approval)
	if stateErr != nil {
		return checkpointport.Round{}, stateErr
	}
	if state != checkpointport.ReviewAwaitingHuman {
		return checkpointport.Round{}, fmt.Errorf("%w: review is %s", checkpointport.ErrInvalidReviewState, state)
	}
	if request.ExpectedResourceVersion != approval.ResourceVersion {
		return checkpointport.Round{}, fmt.Errorf("%w: expected resource version %d, current %d", ErrCheckpointConflict, request.ExpectedResourceVersion, approval.ResourceVersion)
	}
	if request.ScopeDigest != approval.ScopeDigest || request.PolicyDigest != approval.PolicyDigest ||
		(request.CandidateDigest != "" && request.CandidateDigest != approval.CandidateDigest) {
		return checkpointport.Round{}, ErrCandidateConflict
	}
	_, err = service.store.Append(ctx, statestore.PendingEvent{
		SchemaVersion: 1, ID: identity.Random("event_"), AggregateType: statestore.AggregateApproval,
		AggregateID: request.ApprovalID, ExpectedRevision: approval.ResourceVersion, Kind: "approval.decided",
		OccurredAt: service.now().UTC().Round(0), CorrelationID: approval.RunID,
		CommandID: request.IdempotencyKey, Actor: request.Actor, Data: payload, Metadata: json.RawMessage(`{}`),
	})
	if err != nil {
		return checkpointport.Round{}, fmt.Errorf("decide artifact checkpoint: %w", err)
	}
	return service.round(ctx, request.ApprovalID)
}

// SubmitFeedback records exactly one human turn against the displayed
// candidate. The candidate remains current until an agent response commits a
// replacement, so failed or cancelled attempts can safely return to review.
func (service *Service) SubmitFeedback(ctx context.Context, request FeedbackRequest) (checkpointport.ReviewSession, error) {
	if err := validateReviewCommand(request.ApprovalID, request.ExpectedResourceVersion, request.CandidateDigest,
		request.ScopeDigest, request.IdempotencyKey, request.Actor); err != nil {
		return checkpointport.ReviewSession{}, err
	}
	if strings.TrimSpace(request.Message) == "" || strings.TrimSpace(request.Message) != request.Message || len(request.Message) > 16384 {
		return checkpointport.ReviewSession{}, fmt.Errorf("%w: feedback must contain at most 16384 bytes", ErrInvalidRequest)
	}
	approval, state, err := service.reviewForMutation(ctx, request.ApprovalID, request.ExpectedResourceVersion,
		request.CandidateDigest, request.ScopeDigest, checkpointport.ReviewAwaitingHuman)
	if err != nil {
		if replay, replayErr, found := service.replayReviewCommand(ctx, request.ApprovalID, request.IdempotencyKey, "approval.feedback_submitted", feedbackPayload(request)); found {
			return replay, replayErr
		}
		return checkpointport.ReviewSession{}, err
	}
	_ = state
	if approval.MaxRevisions != nil && approval.CheckpointRevision > *approval.MaxRevisions {
		return checkpointport.ReviewSession{}, checkpointport.ErrRevisionLimit
	}
	payload := feedbackPayload(request)
	if _, err = service.store.Append(ctx, statestore.PendingEvent{SchemaVersion: 1, ID: identity.Random("event_"),
		AggregateType: statestore.AggregateApproval, AggregateID: approval.ApprovalID, ExpectedRevision: approval.ResourceVersion,
		Kind: "approval.feedback_submitted", OccurredAt: service.now().UTC().Round(0), CorrelationID: approval.RunID,
		CommandID: request.IdempotencyKey, Actor: request.Actor, Data: payload, Metadata: json.RawMessage(`{}`)}); err != nil {
		return checkpointport.ReviewSession{}, fmt.Errorf("submit checkpoint feedback: %w", err)
	}
	return service.ReviewSession(ctx, request.ApprovalID)
}

// ResumeRevision records the agent attempt selected to handle the latest
// feedback. Provider permissions remain independent and are not granted here.
func (service *Service) ResumeRevision(ctx context.Context, request ResumeRequest) (checkpointport.ReviewSession, error) {
	if err := validateReviewCommand(request.ApprovalID, request.ExpectedResourceVersion, request.CandidateDigest,
		request.ScopeDigest, request.IdempotencyKey, request.Actor); err != nil || !strings.HasPrefix(request.AttemptID, "attempt_") {
		if err != nil {
			return checkpointport.ReviewSession{}, err
		}
		return checkpointport.ReviewSession{}, fmt.Errorf("%w: attempt ID is required", ErrInvalidRequest)
	}
	approval, _, err := service.reviewForMutation(ctx, request.ApprovalID, request.ExpectedResourceVersion,
		request.CandidateDigest, request.ScopeDigest, checkpointport.ReviewAwaitingAgent)
	if err != nil {
		if replay, replayErr, found := service.replayReviewCommand(ctx, request.ApprovalID, request.IdempotencyKey, "approval.revision_resumed", resumePayload(request)); found {
			return replay, replayErr
		}
		return checkpointport.ReviewSession{}, err
	}
	payload := resumePayload(request)
	if _, err = service.store.Append(ctx, statestore.PendingEvent{SchemaVersion: 1, ID: identity.Random("event_"), AggregateType: statestore.AggregateApproval,
		AggregateID: approval.ApprovalID, ExpectedRevision: approval.ResourceVersion, Kind: "approval.revision_resumed",
		OccurredAt: service.now().UTC().Round(0), CorrelationID: approval.RunID, CommandID: request.IdempotencyKey,
		Actor: request.Actor, Data: payload, Metadata: json.RawMessage(`{}`)}); err != nil {
		return checkpointport.ReviewSession{}, fmt.Errorf("resume checkpoint revision: %w", err)
	}
	return service.ReviewSession(ctx, request.ApprovalID)
}

// RecordAgentResponse closes an agent attempt. A revised response atomically
// supersedes the old candidate and opens the next exact candidate session;
// failures and cancellation return the unchanged candidate to the human.
func (service *Service) RecordAgentResponse(ctx context.Context, request AgentResponseRequest) (checkpointport.ReviewSession, error) {
	if err := validateReviewCommand(request.ApprovalID, request.ExpectedResourceVersion, request.CandidateDigest,
		request.ScopeDigest, request.IdempotencyKey, request.Actor); err != nil || !strings.HasPrefix(request.AttemptID, "attempt_") {
		if err != nil {
			return checkpointport.ReviewSession{}, err
		}
		return checkpointport.ReviewSession{}, fmt.Errorf("%w: attempt ID is required", ErrInvalidRequest)
	}
	if request.Outcome != checkpointport.AgentRevised && request.Outcome != checkpointport.AgentFailed && request.Outcome != checkpointport.AgentCancelled {
		return checkpointport.ReviewSession{}, fmt.Errorf("%w: invalid agent outcome", ErrInvalidRequest)
	}
	approval, _, err := service.reviewForMutation(ctx, request.ApprovalID, request.ExpectedResourceVersion,
		request.CandidateDigest, request.ScopeDigest, checkpointport.ReviewAwaitingAgent)
	if err != nil {
		replayDigest := ""
		if request.Outcome == checkpointport.AgentRevised {
			if candidate, readErr := service.artifacts.ArtifactVersion(ctx, request.Candidate); readErr == nil {
				replayDigest = candidate.BlobDigest
			}
		}
		if replay, replayErr, found := service.replayReviewCommand(ctx, request.ApprovalID, request.IdempotencyKey, "approval.agent_responded", agentResponsePayload(request, replayDigest)); found {
			if replayErr == nil && request.Outcome == checkpointport.AgentRevised {
				return service.ReviewSession(ctx, request.NextApprovalID)
			}
			return replay, replayErr
		}
		return checkpointport.ReviewSession{}, err
	}
	events, err := service.store.EventsForAggregate(ctx, approval.ApprovalID)
	if err != nil {
		return checkpointport.ReviewSession{}, err
	}
	activeAttempt := latestResumedAttempt(events)
	if activeAttempt == "" || activeAttempt != request.AttemptID {
		return checkpointport.ReviewSession{}, fmt.Errorf("%w: response attempt is not the resumed revision attempt", ErrCheckpointConflict)
	}

	resultDigest := ""
	if request.Outcome == checkpointport.AgentRevised {
		if !strings.HasPrefix(request.NextApprovalID, "approval_") || request.Candidate.ArtifactID != approval.CandidateArtifactID ||
			request.Candidate.Version != approval.CandidateArtifactVersion+1 {
			return checkpointport.ReviewSession{}, fmt.Errorf("%w: revised response requires the next exact artifact and approval ID", ErrCheckpointConflict)
		}
		if approval.MaxRevisions != nil && approval.CheckpointRevision > *approval.MaxRevisions {
			return checkpointport.ReviewSession{}, checkpointport.ErrRevisionLimit
		}
		candidate, readErr := service.artifacts.ArtifactVersion(ctx, request.Candidate)
		if readErr != nil {
			return checkpointport.ReviewSession{}, fmt.Errorf("read revised checkpoint candidate: %w", readErr)
		}
		resultDigest = candidate.BlobDigest
	} else if request.Candidate.ArtifactID != "" || request.Candidate.Version != 0 || request.NextApprovalID != "" {
		return checkpointport.ReviewSession{}, fmt.Errorf("%w: failed and cancelled responses cannot replace the candidate", ErrInvalidRequest)
	}

	responsePayload := agentResponsePayload(request, resultDigest)
	pending := []statestore.PendingEvent{{SchemaVersion: 1, ID: identity.Random("event_"), AggregateType: statestore.AggregateApproval,
		AggregateID: approval.ApprovalID, ExpectedRevision: approval.ResourceVersion, Kind: "approval.agent_responded",
		OccurredAt: service.now().UTC().Round(0), CorrelationID: approval.RunID, CommandID: request.IdempotencyKey,
		Actor: request.Actor, Data: responsePayload, Metadata: json.RawMessage(`{}`)}}
	if request.Outcome == checkpointport.AgentRevised {
		nextOpen := OpenRequest{ApprovalID: request.NextApprovalID, CheckpointID: approval.CheckpointID, RunID: approval.RunID,
			VisitID: approval.VisitID, NodeID: approval.NodeID, AttemptID: request.AttemptID, Candidate: request.Candidate,
			Mode: checkpointport.Mode(approval.CheckpointMode), MaxRevisions: cloneLimit(approval.MaxRevisions), PolicyDigest: approval.PolicyDigest,
			IdempotencyKey: request.IdempotencyKey + ":open", Actor: request.Actor}
		pending = append(pending, statestore.PendingEvent{SchemaVersion: 1, ID: identity.Random("event_"), AggregateType: statestore.AggregateApproval,
			AggregateID: request.NextApprovalID, ExpectedRevision: 0, Kind: "approval.requested", OccurredAt: service.now().UTC().Round(0),
			CorrelationID: approval.RunID, CommandID: nextOpen.IdempotencyKey, Actor: request.Actor,
			Data: openPayload(nextOpen, approval.CheckpointRevision+1, resultDigest), Metadata: json.RawMessage(`{}`)})
	}
	if _, err = service.store.Append(ctx, pending...); err != nil {
		return checkpointport.ReviewSession{}, fmt.Errorf("record checkpoint agent response: %w", err)
	}
	if request.Outcome == checkpointport.AgentRevised {
		return service.ReviewSession(ctx, request.NextApprovalID)
	}
	return service.ReviewSession(ctx, request.ApprovalID)
}

// History returns every retained draft, decision, feedback comment, and
// revision-driven descendant effect in checkpoint revision order.
func (service *Service) History(ctx context.Context, checkpointID string) (checkpointport.History, error) {
	if !strings.HasPrefix(checkpointID, "checkpoint_") {
		return checkpointport.History{}, fmt.Errorf("%w: checkpoint ID is required", ErrInvalidRequest)
	}
	values, err := service.store.ApprovalsForCheckpoint(ctx, checkpointID)
	if err != nil {
		return checkpointport.History{}, err
	}
	result := checkpointport.History{CheckpointID: checkpointID, Rounds: make([]checkpointport.Round, 0, len(values))}
	for _, value := range values {
		round, err := service.roundFromProjection(ctx, value)
		if err != nil {
			return checkpointport.History{}, err
		}
		result.Rounds = append(result.Rounds, round)
	}
	return result, nil
}

// Round returns one exact review round with its decision and lineage effects.
func (service *Service) Round(ctx context.Context, approvalID string) (checkpointport.Round, error) {
	if !strings.HasPrefix(approvalID, "approval_") {
		return checkpointport.Round{}, fmt.Errorf("%w: approval ID is required", ErrInvalidRequest)
	}
	return service.round(ctx, approvalID)
}

// List returns full actionable checkpoint rounds. An omitted status selects
// pending attention items; callers use an explicit terminal status for history
// slices and History for all rounds of one checkpoint.
func (service *Service) List(ctx context.Context, request ListRequest) (checkpointport.Queue, error) {
	status := request.Status
	if status == "" {
		status = statestore.ApprovalPending
	}
	if request.RunID != "" && !strings.HasPrefix(request.RunID, "run_") {
		return checkpointport.Queue{}, ErrInvalidRequest
	}
	if !validCheckpointStatus(status) {
		return checkpointport.Queue{}, ErrInvalidRequest
	}
	values, err := service.store.CheckpointApprovals(ctx, request.RunID, status)
	if err != nil {
		return checkpointport.Queue{}, err
	}
	items := make([]checkpointport.Round, 0, len(values))
	for _, value := range values {
		round, roundErr := service.roundFromProjection(ctx, value)
		if roundErr != nil {
			return checkpointport.Queue{}, roundErr
		}
		items = append(items, round)
	}
	return checkpointport.Queue{SchemaVersion: 1, Items: items}, nil
}

func (service *Service) ReviewSession(ctx context.Context, approvalID string) (checkpointport.ReviewSession, error) {
	approval, err := service.store.Approval(ctx, approvalID)
	if err != nil {
		return checkpointport.ReviewSession{}, err
	}
	if approval.Class != statestore.ApprovalWorkflowCheckpoint || approval.CheckpointID == "" {
		return checkpointport.ReviewSession{}, fmt.Errorf("%w: approval is not an artifact checkpoint", ErrInvalidRequest)
	}
	state, turns, activeIteration, err := service.reviewConversation(ctx, approval)
	if err != nil {
		return checkpointport.ReviewSession{}, err
	}
	reference := artifactregistry.VersionRef{ArtifactID: approval.CandidateArtifactID, Version: approval.CandidateArtifactVersion}
	affected, err := service.lineage.AffectedBy(ctx, reference)
	if err != nil {
		return checkpointport.ReviewSession{}, fmt.Errorf("read checkpoint revision effects: %w", err)
	}
	allowed := []checkpointport.Action{}
	revisionLimitReached := approval.MaxRevisions != nil && approval.CheckpointRevision > *approval.MaxRevisions
	if state == checkpointport.ReviewAwaitingHuman {
		allowed = []checkpointport.Action{checkpointport.ActionApprove}
		if !revisionLimitReached {
			allowed = append(allowed, checkpointport.ActionRequestChanges)
		}
		allowed = append(allowed, checkpointport.ActionReject)
	}
	var decision *checkpointport.Decision
	if approval.Decision != nil {
		action := checkpointport.Action(approval.Decision.Action)
		decision = &checkpointport.Decision{Action: action, Effect: effectFor(action), ActionKey: approval.Decision.ActionKey,
			Actor: approval.Decision.Actor, Comment: approval.Decision.Comment, DecidedAt: approval.Decision.DecidedAt}
	}
	return checkpointport.ReviewSession{SchemaVersion: 1, ID: approval.ApprovalID, CheckpointID: approval.CheckpointID,
		RunID: approval.RunID, VisitID: approval.VisitID, NodeID: approval.NodeID, Revision: approval.CheckpointRevision,
		Candidate:       reference,
		CandidateDigest: approval.CandidateDigest, ScopeDigest: approval.ScopeDigest, PolicyDigest: approval.PolicyDigest,
		Mode: checkpointport.Mode(approval.CheckpointMode), MaxRevisions: cloneLimit(approval.MaxRevisions), RevisionLimitReached: revisionLimitReached, State: state, Decision: decision,
		Turns: turns, AffectedArtifacts: affected, ActiveIteration: activeIteration, AllowedActions: allowed, ResourceVersion: approval.ResourceVersion,
		CreatedAt: approval.CreatedAt, UpdatedAt: approval.UpdatedAt}, nil
}

func (service *Service) ReviewHistory(ctx context.Context, checkpointID string) (checkpointport.ReviewHistory, error) {
	if !strings.HasPrefix(checkpointID, "checkpoint_") {
		return checkpointport.ReviewHistory{}, fmt.Errorf("%w: checkpoint ID is required", ErrInvalidRequest)
	}
	approvals, err := service.store.ApprovalsForCheckpoint(ctx, checkpointID)
	if err != nil {
		return checkpointport.ReviewHistory{}, err
	}
	result := checkpointport.ReviewHistory{SchemaVersion: 1, CheckpointID: checkpointID, Sessions: make([]checkpointport.ReviewSession, 0, len(approvals))}
	for _, approval := range approvals {
		session, sessionErr := service.ReviewSession(ctx, approval.ApprovalID)
		if sessionErr != nil {
			return checkpointport.ReviewHistory{}, sessionErr
		}
		result.Sessions = append(result.Sessions, session)
	}
	return result, nil
}

func (service *Service) reviewState(ctx context.Context, approval statestore.ApprovalProjection) (checkpointport.ReviewState, checkpointport.Turns, error) {
	state, turns, _, err := service.reviewConversation(ctx, approval)
	return state, turns, err
}

func (service *Service) reviewConversation(ctx context.Context, approval statestore.ApprovalProjection) (checkpointport.ReviewState, checkpointport.Turns, *checkpointport.ActiveAgentIteration, error) {
	events, err := service.store.EventsForAggregate(ctx, approval.ApprovalID)
	if err != nil {
		return "", nil, nil, err
	}
	state := checkpointport.ReviewAwaitingHuman
	turns := make(checkpointport.Turns, 0)
	var active *checkpointport.ActiveAgentIteration
	for _, event := range events {
		switch event.Kind {
		case "approval.feedback_submitted":
			var data struct{ CandidateDigest, Message string }
			if err := json.Unmarshal(event.Data, &data); err != nil {
				return "", nil, nil, err
			}
			turns = append(turns, checkpointport.HumanFeedbackTurn{Kind: "human_feedback", Sequence: uint64(len(turns) + 1),
				Actor: event.Actor, OccurredAt: event.RecordedAt, RunID: approval.RunID, AttemptID: approval.AttemptID,
				Candidate:       artifactregistry.VersionRef{ArtifactID: approval.CandidateArtifactID, Version: approval.CandidateArtifactVersion},
				CandidateDigest: data.CandidateDigest, Message: data.Message})
			state = checkpointport.ReviewAwaitingAgent
		case "approval.revision_resumed":
			var data struct {
				AttemptID string `json:"attemptId"`
			}
			if err := json.Unmarshal(event.Data, &data); err != nil {
				return "", nil, nil, err
			}
			active = &checkpointport.ActiveAgentIteration{AttemptID: data.AttemptID, RunID: approval.RunID, ResumedBy: event.Actor, ResumedAt: event.RecordedAt}
		case "approval.agent_responded":
			var data struct {
				Outcome                             checkpointport.AgentOutcome `json:"outcome"`
				Message, AttemptID, ResultingDigest string
				ResultingCandidate                  *artifactregistry.VersionRef `json:"resultingCandidate"`
			}
			if err := json.Unmarshal(event.Data, &data); err != nil {
				return "", nil, nil, err
			}
			turns = append(turns, checkpointport.AgentResponseTurn{Kind: "agent_response", Sequence: uint64(len(turns) + 1), Actor: event.Actor,
				OccurredAt: event.RecordedAt, RunID: approval.RunID, AttemptID: data.AttemptID, Outcome: data.Outcome,
				Message: data.Message, ResultingCandidate: data.ResultingCandidate, ResultingDigest: data.ResultingDigest})
			if data.Outcome == checkpointport.AgentRevised {
				state = checkpointport.ReviewSuperseded
			} else {
				state = checkpointport.ReviewAwaitingHuman
			}
			active = nil
		case "approval.decided":
			if approval.Status == statestore.ApprovalApproved {
				state = checkpointport.ReviewApproved
			} else if approval.Status == statestore.ApprovalRejected {
				state = checkpointport.ReviewRejected
			}
		case "approval.cancelled", "approval.expired":
			state = checkpointport.ReviewSuperseded
			active = nil
		}
	}
	return state, turns, active, nil
}

func (service *Service) reviewForMutation(ctx context.Context, id string, expected uint64, candidateDigest, scopeDigest string, allowed checkpointport.ReviewState) (statestore.ApprovalProjection, checkpointport.ReviewState, error) {
	approval, err := service.store.Approval(ctx, id)
	if err != nil {
		return statestore.ApprovalProjection{}, "", err
	}
	if approval.Class != statestore.ApprovalWorkflowCheckpoint || approval.CandidateDigest != candidateDigest || approval.ScopeDigest != scopeDigest {
		return statestore.ApprovalProjection{}, "", ErrCandidateConflict
	}
	if approval.ResourceVersion != expected {
		return statestore.ApprovalProjection{}, "", fmt.Errorf("%w: expected resource version %d, current %d", ErrCheckpointConflict, expected, approval.ResourceVersion)
	}
	state, _, err := service.reviewState(ctx, approval)
	if err != nil {
		return statestore.ApprovalProjection{}, "", err
	}
	if state != allowed {
		return statestore.ApprovalProjection{}, state, fmt.Errorf("%w: review is %s", checkpointport.ErrInvalidReviewState, state)
	}
	return approval, state, nil
}

func (service *Service) replayReviewCommand(ctx context.Context, id, key, kind string, payload json.RawMessage) (checkpointport.ReviewSession, error, bool) {
	event, err := service.store.EventByCommand(ctx, id, key)
	if err != nil {
		return checkpointport.ReviewSession{}, nil, false
	}
	if event.Kind != kind || string(event.Data) != string(payload) {
		return checkpointport.ReviewSession{}, checkpointport.ErrIdempotencyConflict, true
	}
	session, err := service.ReviewSession(ctx, id)
	return session, err, true
}

func validateReviewCommand(id string, expected uint64, candidateDigest, scopeDigest, key string, actor statestore.Actor) error {
	if !strings.HasPrefix(id, "approval_") || expected == 0 || !digestPattern.MatchString(candidateDigest) ||
		!digestPattern.MatchString(scopeDigest) || strings.TrimSpace(key) == "" || !validReviewActor(actor) {
		return ErrInvalidRequest
	}
	return nil
}

func validReviewActor(actor statestore.Actor) bool {
	if strings.TrimSpace(actor.ID) == "" || strings.TrimSpace(actor.ID) != actor.ID {
		return false
	}
	return actor.Type == statestore.ActorUser || actor.Type == statestore.ActorSystem || actor.Type == statestore.ActorProvider || actor.Type == statestore.ActorExternal
}

func feedbackPayload(request FeedbackRequest) json.RawMessage {
	value, _ := json.Marshal(struct {
		CandidateDigest string `json:"candidateDigest"`
		ScopeDigest     string `json:"scopeDigest"`
		Message         string `json:"message"`
	}{
		request.CandidateDigest, request.ScopeDigest, request.Message})
	return value
}

func resumePayload(request ResumeRequest) json.RawMessage {
	value, _ := json.Marshal(struct {
		CandidateDigest string `json:"candidateDigest"`
		ScopeDigest     string `json:"scopeDigest"`
		AttemptID       string `json:"attemptId"`
	}{
		request.CandidateDigest, request.ScopeDigest, request.AttemptID})
	return value
}

func agentResponsePayload(request AgentResponseRequest, digest string) json.RawMessage {
	var candidate *artifactregistry.VersionRef
	if request.Outcome == checkpointport.AgentRevised {
		value := request.Candidate
		candidate = &value
	}
	value, _ := json.Marshal(struct {
		CandidateDigest    string                       `json:"candidateDigest"`
		ScopeDigest        string                       `json:"scopeDigest"`
		AttemptID          string                       `json:"attemptId"`
		Outcome            checkpointport.AgentOutcome  `json:"outcome"`
		Message            string                       `json:"message,omitempty"`
		ResultingCandidate *artifactregistry.VersionRef `json:"resultingCandidate,omitempty"`
		ResultingDigest    string                       `json:"resultingDigest,omitempty"`
		NextApprovalID     string                       `json:"nextApprovalId,omitempty"`
	}{
		request.CandidateDigest, request.ScopeDigest, request.AttemptID, request.Outcome, request.Message, candidate, digest, request.NextApprovalID})
	return value
}

func latestResumedAttempt(events []statestore.Event) string {
	for index := len(events) - 1; index >= 0; index-- {
		if events[index].Kind == "approval.revision_resumed" {
			var data struct {
				AttemptID string `json:"attemptId"`
			}
			if json.Unmarshal(events[index].Data, &data) == nil {
				return data.AttemptID
			}
		}
	}
	return ""
}

func (service *Service) round(ctx context.Context, approvalID string) (checkpointport.Round, error) {
	value, err := service.store.Approval(ctx, approvalID)
	if err != nil {
		return checkpointport.Round{}, err
	}
	return service.roundFromProjection(ctx, value)
}

func (service *Service) roundFromProjection(ctx context.Context, value statestore.ApprovalProjection) (checkpointport.Round, error) {
	reference := artifactregistry.VersionRef{ArtifactID: value.CandidateArtifactID, Version: value.CandidateArtifactVersion}
	affected, err := service.lineage.AffectedBy(ctx, reference)
	if err != nil {
		return checkpointport.Round{}, fmt.Errorf("read checkpoint revision effects: %w", err)
	}
	round := checkpointport.Round{
		ApprovalID: value.ApprovalID, CheckpointID: value.CheckpointID, RunID: value.RunID, VisitID: value.VisitID,
		NodeID: value.NodeID, AttemptID: value.AttemptID, Revision: value.CheckpointRevision,
		Candidate: reference, CandidateDigest: value.CandidateDigest, ScopeDigest: value.ScopeDigest,
		PolicyDigest: value.PolicyDigest, Mode: checkpointport.Mode(value.CheckpointMode), MaxRevisions: cloneLimit(value.MaxRevisions),
		State: stateFromApproval(value.Status), AffectedArtifacts: affected, ResourceVersion: value.ResourceVersion,
		CreatedAt: value.CreatedAt, UpdatedAt: value.UpdatedAt,
	}
	if value.Decision != nil {
		round.Decision = &checkpointport.Decision{Action: checkpointport.Action(value.Decision.Action), Effect: effectFor(checkpointport.Action(value.Decision.Action)),
			ActionKey: value.Decision.ActionKey, Actor: value.Decision.Actor, Comment: value.Decision.Comment, DecidedAt: value.Decision.DecidedAt}
	}
	if round.State == checkpointport.StatePending {
		round.AllowedActions = []checkpointport.Action{checkpointport.ActionApprove, checkpointport.ActionRequestChanges, checkpointport.ActionReject}
	} else {
		round.AllowedActions = []checkpointport.Action{}
	}
	return round, nil
}

func validateOpen(request OpenRequest) error {
	for _, field := range []struct{ name, value, prefix string }{
		{"approval ID", request.ApprovalID, "approval_"}, {"checkpoint ID", request.CheckpointID, "checkpoint_"},
		{"run ID", request.RunID, "run_"}, {"visit ID", request.VisitID, "visit_"}, {"attempt ID", request.AttemptID, "attempt_"},
	} {
		if !strings.HasPrefix(field.value, field.prefix) || strings.TrimSpace(field.value) != field.value {
			return fmt.Errorf("%w: %s must start with %s", ErrInvalidRequest, field.name, field.prefix)
		}
	}
	if strings.TrimSpace(request.NodeID) == "" || strings.TrimSpace(request.NodeID) != request.NodeID || !strings.HasPrefix(request.Candidate.ArtifactID, "artifact_") || request.Candidate.Version == 0 {
		return fmt.Errorf("%w: node and exact candidate are required", ErrInvalidRequest)
	}
	if request.Mode != checkpointport.ModeApprove && request.Mode != checkpointport.ModeApproveOnChange {
		return fmt.Errorf("%w: unsupported checkpoint mode %q", ErrInvalidRequest, request.Mode)
	}
	if request.MaxRevisions != nil && *request.MaxRevisions == 0 {
		return fmt.Errorf("%w: max revisions must be positive", ErrInvalidRequest)
	}
	if !digestPattern.MatchString(request.PolicyDigest) || strings.TrimSpace(request.IdempotencyKey) == "" || !validActor(request.Actor) {
		return fmt.Errorf("%w: policy digest, idempotency key, and actor are required", ErrInvalidRequest)
	}
	return nil
}

func validateDecision(request DecisionRequest) error {
	if !strings.HasPrefix(request.ApprovalID, "approval_") || request.ExpectedResourceVersion == 0 ||
		(request.Action != checkpointport.ActionApprove && request.Action != checkpointport.ActionRequestChanges && request.Action != checkpointport.ActionReject) ||
		!digestPattern.MatchString(request.ScopeDigest) || !digestPattern.MatchString(request.PolicyDigest) ||
		(request.CandidateDigest != "" && !digestPattern.MatchString(request.CandidateDigest)) ||
		strings.TrimSpace(request.IdempotencyKey) == "" || !validActor(request.Actor) || strings.TrimSpace(request.Comment) != request.Comment {
		return ErrInvalidRequest
	}
	if len(request.Comment) > 4096 {
		return fmt.Errorf("%w: comment exceeds 4096 bytes", ErrInvalidRequest)
	}
	if (request.Action == checkpointport.ActionRequestChanges || request.Action == checkpointport.ActionReject) && strings.TrimSpace(request.Comment) == "" {
		return fmt.Errorf("%w: request_changes and reject require an explanatory comment", ErrInvalidRequest)
	}
	return nil
}

func validCheckpointStatus(status statestore.ApprovalStatus) bool {
	switch status {
	case statestore.ApprovalPending, statestore.ApprovalApproved, statestore.ApprovalChangesRequested, statestore.ApprovalRejected:
		return true
	default:
		return false
	}
}

func validActor(actor statestore.Actor) bool {
	if strings.TrimSpace(actor.ID) == "" || strings.TrimSpace(actor.ID) != actor.ID {
		return false
	}
	switch actor.Type {
	case statestore.ActorUser, statestore.ActorSystem, statestore.ActorExternal:
		return true
	default:
		return false
	}
}

func checkpointScopeDigest(request OpenRequest, revision uint64, candidateDigest string) string {
	value, _ := json.Marshal(struct {
		CheckpointID string                      `json:"checkpointId"`
		RunID        string                      `json:"runId"`
		VisitID      string                      `json:"visitId"`
		NodeID       string                      `json:"nodeId"`
		AttemptID    string                      `json:"attemptId"`
		Revision     uint64                      `json:"revision"`
		Candidate    artifactregistry.VersionRef `json:"candidate"`
		Digest       string                      `json:"digest"`
		Mode         checkpointport.Mode         `json:"mode"`
		PolicyDigest string                      `json:"policyDigest"`
	}{request.CheckpointID, request.RunID, request.VisitID, request.NodeID, request.AttemptID, revision, request.Candidate, candidateDigest, request.Mode, request.PolicyDigest})
	sum := sha256.Sum256(value)
	return hex.EncodeToString(sum[:])
}

func decisionPayload(request DecisionRequest) json.RawMessage {
	value, _ := json.Marshal(struct {
		Action       checkpointport.Action `json:"action"`
		ScopeDigest  string                `json:"scopeDigest"`
		PolicyDigest string                `json:"policyDigest"`
		Comment      string                `json:"comment,omitempty"`
	}{request.Action, request.ScopeDigest, request.PolicyDigest, request.Comment})
	return value
}

func stateFromApproval(status statestore.ApprovalStatus) checkpointport.State {
	switch status {
	case statestore.ApprovalApproved:
		return checkpointport.StateApproved
	case statestore.ApprovalChangesRequested:
		return checkpointport.StateChangesRequested
	case statestore.ApprovalRejected:
		return checkpointport.StateRejected
	default:
		return checkpointport.StatePending
	}
}

func effectFor(action checkpointport.Action) checkpointport.Effect {
	switch action {
	case checkpointport.ActionApprove:
		return checkpointport.EffectAcceptCandidate
	case checkpointport.ActionRequestChanges:
		return checkpointport.EffectStartRevision
	default:
		return checkpointport.EffectRejectVisit
	}
}

func sameLimit(left, right *uint64) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

func cloneLimit(value *uint64) *uint64 {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}
