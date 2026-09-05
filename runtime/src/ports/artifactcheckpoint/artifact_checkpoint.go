// Package artifactcheckpoint defines durable artifact review rounds.
package artifactcheckpoint

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"darkstar/src/ports/artifactlineage"
	"darkstar/src/ports/artifactregistry"
	"darkstar/src/ports/statestore"
)

var (
	ErrAlreadyResolved     = errors.New("artifact checkpoint round is already resolved")
	ErrIdempotencyConflict = errors.New("artifact checkpoint idempotency conflict")
	ErrRevisionLimit       = errors.New("artifact checkpoint revision limit reached")
	ErrRevisionRequired    = errors.New("artifact checkpoint does not accept a revision")
	ErrInvalidReviewState  = errors.New("artifact checkpoint review state does not allow this action")
)

// ReviewState is the closed lifecycle of one exact checkpoint candidate.
// It is derived from immutable events rather than persisted as a second source
// of truth beside the approval projection.
type ReviewState string

const (
	ReviewAwaitingHuman ReviewState = "awaiting_human"
	ReviewAwaitingAgent ReviewState = "awaiting_agent"
	ReviewApproved      ReviewState = "approved"
	ReviewRejected      ReviewState = "rejected"
	ReviewSuperseded    ReviewState = "superseded"
)

type AgentOutcome string

const (
	AgentRevised   AgentOutcome = "revised"
	AgentFailed    AgentOutcome = "failed"
	AgentCancelled AgentOutcome = "cancelled"
)

// Turn is an immutable tagged member of a review conversation.
type Turn interface {
	reviewTurn()
}

type Turns []Turn

func (turns *Turns) UnmarshalJSON(encoded []byte) error {
	var raw []json.RawMessage
	if err := json.Unmarshal(encoded, &raw); err != nil {
		return err
	}
	result := make(Turns, 0, len(raw))
	for _, item := range raw {
		var discriminator struct {
			Kind string `json:"kind"`
		}
		if err := json.Unmarshal(item, &discriminator); err != nil {
			return err
		}
		switch discriminator.Kind {
		case "human_feedback":
			var value HumanFeedbackTurn
			if err := json.Unmarshal(item, &value); err != nil {
				return err
			}
			result = append(result, value)
		case "agent_response":
			var value AgentResponseTurn
			if err := json.Unmarshal(item, &value); err != nil {
				return err
			}
			result = append(result, value)
		default:
			return fmt.Errorf("unknown review turn kind %q", discriminator.Kind)
		}
	}
	*turns = result
	return nil
}

type HumanFeedbackTurn struct {
	Kind            string                      `json:"kind"`
	Sequence        uint64                      `json:"sequence"`
	Actor           statestore.Actor            `json:"actor"`
	OccurredAt      time.Time                   `json:"occurredAt"`
	RunID           string                      `json:"runId"`
	AttemptID       string                      `json:"attemptId"`
	Candidate       artifactregistry.VersionRef `json:"candidate"`
	CandidateDigest string                      `json:"candidateDigest"`
	Message         string                      `json:"message"`
}

func (HumanFeedbackTurn) reviewTurn() {}

type AgentResponseTurn struct {
	Kind               string                       `json:"kind"`
	Sequence           uint64                       `json:"sequence"`
	Actor              statestore.Actor             `json:"actor"`
	OccurredAt         time.Time                    `json:"occurredAt"`
	RunID              string                       `json:"runId"`
	AttemptID          string                       `json:"attemptId"`
	Outcome            AgentOutcome                 `json:"outcome"`
	Message            string                       `json:"message,omitempty"`
	ResultingCandidate *artifactregistry.VersionRef `json:"resultingCandidate,omitempty"`
	ResultingDigest    string                       `json:"resultingDigest,omitempty"`
}

func (AgentResponseTurn) reviewTurn() {}

// ActiveAgentIteration links review state to the authoritative attempt
// lifecycle without duplicating its queued/running/validating phase.
type ActiveAgentIteration struct {
	AttemptID string           `json:"attemptId"`
	RunID     string           `json:"runId"`
	ResumedBy statestore.Actor `json:"resumedBy"`
	ResumedAt time.Time        `json:"resumedAt"`
}

// ReviewSession is one immutable candidate and its ordered conversation. A
// revised artifact creates a new session and leaves this one superseded.
type ReviewSession struct {
	SchemaVersion        int                            `json:"schemaVersion"`
	ID                   string                         `json:"id"`
	CheckpointID         string                         `json:"checkpointId"`
	RunID                string                         `json:"runId"`
	VisitID              string                         `json:"visitId"`
	NodeID               string                         `json:"nodeId"`
	Revision             uint64                         `json:"revision"`
	Candidate            artifactregistry.VersionRef    `json:"candidate"`
	CandidateDigest      string                         `json:"candidateDigest"`
	ScopeDigest          string                         `json:"scopeDigest"`
	PolicyDigest         string                         `json:"policyDigest"`
	Mode                 Mode                           `json:"mode"`
	MaxRevisions         *uint64                        `json:"maxRevisions,omitempty"`
	RevisionLimitReached bool                           `json:"revisionLimitReached"`
	State                ReviewState                    `json:"state"`
	Decision             *Decision                      `json:"decision,omitempty"`
	Turns                Turns                          `json:"turns"`
	AffectedArtifacts    []artifactlineage.Invalidation `json:"affectedArtifacts"`
	ActiveIteration      *ActiveAgentIteration          `json:"activeIteration,omitempty"`
	AllowedActions       []Action                       `json:"allowedActions"`
	ResourceVersion      uint64                         `json:"resourceVersion"`
	CreatedAt            time.Time                      `json:"createdAt"`
	UpdatedAt            time.Time                      `json:"updatedAt"`
}

type ReviewHistory struct {
	SchemaVersion int             `json:"schemaVersion"`
	CheckpointID  string          `json:"checkpointId"`
	Sessions      []ReviewSession `json:"sessions"`
}

// Mode is the closed set of artifact checkpoint policies supported by the
// iterative review loop.
type Mode string

const (
	ModeApprove         Mode = "approve"
	ModeApproveOnChange Mode = "approve_on_change"
)

// Action is a terminal decision for exactly one immutable candidate.
type Action string

const (
	ActionApprove        Action = "approve"
	ActionRequestChanges Action = "request_changes"
	ActionReject         Action = "reject"
)

// Effect tells the workflow coordinator what the committed decision permits.
// It is not evidence that the downstream effect has run.
type Effect string

const (
	EffectAcceptCandidate Effect = "accept_candidate"
	EffectStartRevision   Effect = "start_revision"
	EffectRejectVisit     Effect = "reject_visit"
)

// State is derived from the presence and action of a round decision.
type State string

const (
	StatePending          State = "pending"
	StateApproved         State = "approved"
	StateChangesRequested State = "changes_requested"
	StateRejected         State = "rejected"
)

// Decision is the immutable review outcome for one round.
type Decision struct {
	Action    Action           `json:"action"`
	Effect    Effect           `json:"effect"`
	ActionKey string           `json:"-"`
	Actor     statestore.Actor `json:"actor"`
	Comment   string           `json:"comment,omitempty"`
	DecidedAt time.Time        `json:"decidedAt"`
}

// Round is one immutable candidate plus its optional terminal decision.
type Round struct {
	ApprovalID        string                         `json:"approvalId"`
	CheckpointID      string                         `json:"checkpointId"`
	RunID             string                         `json:"runId"`
	VisitID           string                         `json:"visitId"`
	NodeID            string                         `json:"nodeId"`
	AttemptID         string                         `json:"attemptId"`
	Revision          uint64                         `json:"revision"`
	Candidate         artifactregistry.VersionRef    `json:"candidate"`
	CandidateDigest   string                         `json:"candidateDigest"`
	ScopeDigest       string                         `json:"scopeDigest"`
	PolicyDigest      string                         `json:"policyDigest"`
	Mode              Mode                           `json:"mode"`
	MaxRevisions      *uint64                        `json:"maxRevisions,omitempty"`
	State             State                          `json:"state"`
	Decision          *Decision                      `json:"decision,omitempty"`
	AffectedArtifacts []artifactlineage.Invalidation `json:"affectedArtifacts"`
	AllowedActions    []Action                       `json:"allowedActions"`
	ResourceVersion   uint64                         `json:"resourceVersion"`
	CreatedAt         time.Time                      `json:"createdAt"`
	UpdatedAt         time.Time                      `json:"updatedAt"`
}

type Queue struct {
	SchemaVersion int     `json:"schemaVersion"`
	Items         []Round `json:"items"`
}

// History is the complete ordered review record for one checkpoint.
type History struct {
	CheckpointID string  `json:"checkpointId"`
	Rounds       []Round `json:"rounds"`
}

// Store is the durable approval/event subset required by the checkpoint loop.
type Store interface {
	Append(context.Context, ...statestore.PendingEvent) ([]statestore.Event, error)
	Approval(context.Context, string) (statestore.ApprovalProjection, error)
	ApprovalsForCheckpoint(context.Context, string) ([]statestore.ApprovalProjection, error)
	CheckpointApprovals(context.Context, string, statestore.ApprovalStatus) ([]statestore.ApprovalProjection, error)
	EventByCommand(context.Context, string, string) (statestore.Event, error)
	EventsForAggregate(context.Context, string) ([]statestore.Event, error)
	Node(context.Context, string) (statestore.NodeProjection, error)
}
