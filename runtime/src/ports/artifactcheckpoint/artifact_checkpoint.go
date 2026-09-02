// Package artifactcheckpoint defines durable artifact review rounds.
package artifactcheckpoint

import (
	"context"
	"errors"
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
)

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
	ActionKey string           `json:"actionKey"`
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
	ResourceVersion   uint64                         `json:"resourceVersion"`
	CreatedAt         time.Time                      `json:"createdAt"`
	UpdatedAt         time.Time                      `json:"updatedAt"`
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
	EventByCommand(context.Context, string, string) (statestore.Event, error)
	Node(context.Context, string) (statestore.NodeProjection, error)
}
