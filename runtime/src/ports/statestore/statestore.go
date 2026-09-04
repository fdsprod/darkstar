// Package statestore defines the durable event and current-state projection boundary.
package statestore

import (
	"context"
	"encoding/json"
	"errors"
	"time"
)

// ErrNotFound is the portable classification for missing durable state.
var ErrNotFound = errors.New("state not found")

// AggregateType identifies one domain event stream.
type AggregateType string

const (
	AggregateProject    AggregateType = "project"
	AggregateWork       AggregateType = "work"
	AggregateStory      AggregateType = "story"
	AggregatePoint      AggregateType = "point"
	AggregateRun        AggregateType = "run"
	AggregateVisit      AggregateType = "visit"
	AggregateAttempt    AggregateType = "attempt"
	AggregateArtifact   AggregateType = "artifact"
	AggregateApproval   AggregateType = "approval"
	AggregateOperation  AggregateType = "operation"
	AggregateAssessment AggregateType = "assessment"
)

// ActorType identifies the authority that caused an event.
type ActorType string

const (
	ActorUser     ActorType = "user"
	ActorSystem   ActorType = "system"
	ActorProvider ActorType = "provider"
	ActorExternal ActorType = "external"
)

// Actor names the authority that caused an event.
type Actor struct {
	Type ActorType `json:"type"`
	ID   string    `json:"id"`
}

// PendingEvent is an event before its database-assigned ordering fields exist.
// ExpectedRevision is the aggregate revision before this event is appended.
type PendingEvent struct {
	SchemaVersion    uint64
	ID               string
	AggregateType    AggregateType
	AggregateID      string
	ExpectedRevision uint64
	Kind             string
	OccurredAt       time.Time
	CorrelationID    string
	CausationID      *string
	CommandID        string
	Actor            Actor
	Data             json.RawMessage
	Metadata         json.RawMessage
}

// Event is one immutable committed fact. GlobalPosition orders the database;
// AggregateRevision orders its aggregate stream.
type Event struct {
	SchemaVersion     uint64          `json:"schemaVersion"`
	ID                string          `json:"id"`
	GlobalPosition    uint64          `json:"globalPosition"`
	AggregateType     AggregateType   `json:"aggregateType"`
	AggregateID       string          `json:"aggregateId"`
	AggregateRevision uint64          `json:"aggregateRevision"`
	Kind              string          `json:"kind"`
	OccurredAt        time.Time       `json:"occurredAt"`
	RecordedAt        time.Time       `json:"recordedAt"`
	CorrelationID     string          `json:"correlationId"`
	CausationID       *string         `json:"causationId"`
	CommandID         string          `json:"commandId"`
	Actor             Actor           `json:"actor"`
	Data              json.RawMessage `json:"data"`
	Metadata          json.RawMessage `json:"metadata"`
}

// EventBounds describes the retained inclusive global-position range. Zero
// values mean that the store contains no events; otherwise both positions are
// positive and Oldest is no greater than Latest.
type EventBounds struct {
	Oldest uint64
	Latest uint64
}

// JSONSnapshot stores immutable JSON bytes while remaining comparable in views
// and tests. Its JSON representation is the contained value, not a quoted string.
type JSONSnapshot string

func (snapshot JSONSnapshot) MarshalJSON() ([]byte, error) {
	if snapshot == "" {
		return []byte("null"), nil
	}
	encoded := []byte(snapshot)
	if !json.Valid(encoded) {
		return nil, errors.New("snapshot contains invalid JSON")
	}
	return encoded, nil
}

func (snapshot *JSONSnapshot) UnmarshalJSON(encoded []byte) error {
	if !json.Valid(encoded) {
		return errors.New("snapshot contains invalid JSON")
	}
	if string(encoded) == "null" {
		*snapshot = ""
		return nil
	}
	*snapshot = JSONSnapshot(string(encoded))
	return nil
}

// RunStatus is the closed set of persisted workflow-run states.
type RunStatus string

const (
	RunDraft             RunStatus = "draft"
	RunReady             RunStatus = "ready"
	RunQueued            RunStatus = "queued"
	RunRunning           RunStatus = "running"
	RunWaiting           RunStatus = "waiting"
	RunBlocked           RunStatus = "blocked"
	RunCompleted         RunStatus = "completed"
	RunFailed            RunStatus = "failed"
	RunCancelled         RunStatus = "cancelled"
	RunReconcileRequired RunStatus = "reconcile_required"

	// RunPending is retained as a source-compatible alias for pre-workflow
	// callers. Newly persisted runs use the precise draft state.
	RunPending = RunDraft
)

// Terminal reports whether a run can no longer transition normally. Failed is
// deliberately resumable by an explicit retry and is therefore non-terminal.
func (status RunStatus) Terminal() bool {
	return status == RunCompleted || status == RunCancelled || status == RunReconcileRequired
}

// RunProjection is the rebuildable query representation of a run.
type RunProjection struct {
	RunID              string       `json:"id"`
	WorkItemID         string       `json:"workItemId"`
	WorkflowID         string       `json:"workflowId"`
	WorkflowVersion    string       `json:"workflowVersion"`
	WorkflowDigest     string       `json:"workflowDigest,omitempty"`
	RouteDigest        string       `json:"routeDigest,omitempty"`
	RouteSnapshot      JSONSnapshot `json:"routeSnapshot,omitempty"`
	Priority           int          `json:"priority,omitempty"`
	Status             RunStatus    `json:"status"`
	ResourceVersion    uint64       `json:"resourceVersion"`
	LastGlobalPosition uint64       `json:"lastGlobalPosition"`
	CreatedAt          time.Time    `json:"createdAt"`
	UpdatedAt          time.Time    `json:"updatedAt"`
}

// NodeStatus is the closed lifecycle of one activated workflow node visit.
// A new visit is a new aggregate; retries create attempts beneath the visit.
type NodeStatus string

const (
	NodePending           NodeStatus = "pending"
	NodeReady             NodeStatus = "ready"
	NodeRunning           NodeStatus = "running"
	NodeValidating        NodeStatus = "validating"
	NodeWaitingCheckpoint NodeStatus = "waiting_checkpoint"
	NodeSucceeded         NodeStatus = "succeeded"
	NodeRejected          NodeStatus = "rejected"
	NodeFailed            NodeStatus = "failed"
	NodeCancelled         NodeStatus = "cancelled"
)

// Terminal reports whether this visit can produce no further attempts or
// successful output binding.
func (status NodeStatus) Terminal() bool {
	switch status {
	case NodeSucceeded, NodeRejected, NodeFailed, NodeCancelled:
		return true
	default:
		return false
	}
}

// NodeProjection is the rebuildable query representation of a node visit.
type NodeProjection struct {
	VisitID            string     `json:"id"`
	RunID              string     `json:"runId"`
	NodeID             string     `json:"nodeId"`
	Status             NodeStatus `json:"status"`
	ResourceVersion    uint64     `json:"resourceVersion"`
	LastGlobalPosition uint64     `json:"lastGlobalPosition"`
	CreatedAt          time.Time  `json:"createdAt"`
	UpdatedAt          time.Time  `json:"updatedAt"`
}

// AttemptStatus is the closed set of persisted provider-attempt states. It is
// intentionally separate from NodeStatus: provider completion is not node
// success until validation and any checkpoint have completed.
type AttemptStatus string

const (
	AttemptCreated           AttemptStatus = "created"
	AttemptStarting          AttemptStatus = "starting"
	AttemptRunning           AttemptStatus = "running"
	AttemptValidating        AttemptStatus = "validating"
	AttemptSucceeded         AttemptStatus = "succeeded"
	AttemptFailed            AttemptStatus = "failed"
	AttemptCancelled         AttemptStatus = "cancelled"
	AttemptInterrupted       AttemptStatus = "interrupted"
	AttemptReconcileRequired AttemptStatus = "reconcile_required"

	// AttemptCompleted is retained as a source-compatible alias. Persisted
	// state uses succeeded to match the workflow execution contract.
	AttemptCompleted = AttemptSucceeded
)

// Terminal reports whether no provider work remains for this attempt.
func (status AttemptStatus) Terminal() bool {
	switch status {
	case AttemptSucceeded, AttemptFailed, AttemptCancelled, AttemptInterrupted, AttemptReconcileRequired:
		return true
	default:
		return false
	}
}

// AttemptProjection is the rebuildable recovery and query representation of
// one provider attempt. Provider identity and LastSequence are the minimum
// durable cursor needed to resume without replaying already-recorded effects.
type AttemptProjection struct {
	AttemptID          string        `json:"id"`
	RunID              string        `json:"runId"`
	VisitID            string        `json:"visitId,omitempty"`
	NodeID             string        `json:"nodeId"`
	PointID            string        `json:"pointId,omitempty"`
	PointRevision      uint64        `json:"pointRevision,omitempty"`
	Priority           int           `json:"priority,omitempty"`
	Scenario           string        `json:"scenario"`
	Provider           string        `json:"provider"`
	Status             AttemptStatus `json:"status"`
	ProviderThreadID   string        `json:"providerThreadId,omitempty"`
	ProviderTurnID     string        `json:"providerTurnId,omitempty"`
	ProcessOwnerID     string        `json:"processOwnerId,omitempty"`
	LastSequence       uint64        `json:"lastSequence"`
	LogReference       string        `json:"logReference,omitempty"`
	ResourceVersion    uint64        `json:"resourceVersion"`
	LastGlobalPosition uint64        `json:"lastGlobalPosition"`
	CreatedAt          time.Time     `json:"createdAt"`
	UpdatedAt          time.Time     `json:"updatedAt"`
}

// CommandEvidence is the durable, replay-safe evidence for one command. The
// request itself is represented by its digest; response JSON is redacted again
// by the export boundary before it leaves the daemon.
type CommandEvidence struct {
	Scope              string          `json:"scope"`
	IdempotencyKey     string          `json:"idempotencyKey"`
	RequestDigest      string          `json:"requestDigest"`
	Status             string          `json:"status"`
	ResponseStatus     *int            `json:"responseStatus,omitempty"`
	Response           json.RawMessage `json:"response,omitempty"`
	FirstEventPosition *uint64         `json:"firstEventPosition,omitempty"`
	LastEventPosition  *uint64         `json:"lastEventPosition,omitempty"`
	CreatedAt          time.Time       `json:"createdAt"`
	CompletedAt        *time.Time      `json:"completedAt,omitempty"`
}

// BeginCommandRequest identifies one replay-safe public command.
type BeginCommandRequest struct {
	Scope          string
	IdempotencyKey string
	RequestDigest  string
	CreatedAt      time.Time
}

// CompleteCommandRequest closes one pending command with its stable response.
type CompleteCommandRequest struct {
	Scope              string
	IdempotencyKey     string
	ResponseStatus     int
	Response           json.RawMessage
	FirstEventPosition *uint64
	LastEventPosition  *uint64
	CompletedAt        time.Time
}

// RunEvidence is one transactionally consistent source snapshot for a run
// export. Events are authoritative; Run is a rebuildable convenience snapshot,
// and Commands records idempotency/response evidence associated with them.
type RunEvidence struct {
	Run      RunProjection
	Events   []Event
	Commands []CommandEvidence
}

// ApprovalClass identifies why a decision is required.
type ApprovalClass string

const (
	ApprovalWorkflowCheckpoint ApprovalClass = "workflow_checkpoint"
	ApprovalWorkflowControl    ApprovalClass = "workflow_control"
	ApprovalProviderPermission ApprovalClass = "provider_permission"
	ApprovalExternalDelivery   ApprovalClass = "external_delivery"
)

// ApprovalStatus is the closed set of persisted approval states.
type ApprovalStatus string

const (
	ApprovalPending          ApprovalStatus = "pending"
	ApprovalApproved         ApprovalStatus = "approved"
	ApprovalChangesRequested ApprovalStatus = "changes_requested"
	ApprovalRejected         ApprovalStatus = "rejected"
	ApprovalDenied           ApprovalStatus = "denied"
	ApprovalCancelled        ApprovalStatus = "cancelled"
	ApprovalExpired          ApprovalStatus = "expired"
)

// ApprovalDecisionProjection is present only after an approval resolves.
type ApprovalDecisionProjection struct {
	Action    string    `json:"action"`
	ActionKey string    `json:"actionKey"`
	Comment   string    `json:"comment,omitempty"`
	Actor     Actor     `json:"actor"`
	DecidedAt time.Time `json:"decidedAt"`
}

// ApprovalProjection is the rebuildable query representation of an approval.
type ApprovalProjection struct {
	ApprovalID               string                      `json:"id"`
	RunID                    string                      `json:"runId"`
	Class                    ApprovalClass               `json:"class"`
	Status                   ApprovalStatus              `json:"status"`
	CheckpointID             string                      `json:"checkpointId,omitempty"`
	VisitID                  string                      `json:"visitId,omitempty"`
	NodeID                   string                      `json:"nodeId,omitempty"`
	AttemptID                string                      `json:"attemptId,omitempty"`
	CheckpointRevision       uint64                      `json:"checkpointRevision,omitempty"`
	CandidateArtifactID      string                      `json:"candidateArtifactId,omitempty"`
	CandidateArtifactVersion uint64                      `json:"candidateArtifactVersion,omitempty"`
	CandidateDigest          string                      `json:"candidateDigest,omitempty"`
	CheckpointMode           string                      `json:"checkpointMode,omitempty"`
	MaxRevisions             *uint64                     `json:"maxRevisions,omitempty"`
	ScopeDigest              string                      `json:"scopeDigest"`
	PolicyDigest             string                      `json:"policyDigest"`
	Decision                 *ApprovalDecisionProjection `json:"decision,omitempty"`
	ResourceVersion          uint64                      `json:"resourceVersion"`
	LastGlobalPosition       uint64                      `json:"lastGlobalPosition"`
	CreatedAt                time.Time                   `json:"createdAt"`
	UpdatedAt                time.Time                   `json:"updatedAt"`
}

// ReadinessAssessmentStatus is the closed durable lifecycle of one validated
// readiness assessment.
type ReadinessAssessmentStatus string

const (
	ReadinessAssessmentPending ReadinessAssessmentStatus = "pending"
	ReadinessAssessmentDecided ReadinessAssessmentStatus = "decided"
)

// ReadinessEffectStatus records that a choice is durable without claiming its
// workflow effect has been performed by a downstream coordinator.
type ReadinessEffectStatus string

const ReadinessEffectPending ReadinessEffectStatus = "pending"

// ReadinessDecisionProjection exists only in the decided state.
type ReadinessDecisionProjection struct {
	DecisionID   string                `json:"decisionId"`
	Choice       string                `json:"choice"`
	RemedyCode   string                `json:"remedyCode,omitempty"`
	Reason       string                `json:"reason"`
	EffectStatus ReadinessEffectStatus `json:"effectStatus"`
	Actor        Actor                 `json:"actor"`
	DecidedAt    time.Time             `json:"decidedAt"`
}

// ReadinessAssessmentProjection is a rebuildable control boundary. Submission
// and RouteContext are trusted immutable inputs used to reproduce validation;
// presentation-only allowed actions are deliberately derived elsewhere.
type ReadinessAssessmentProjection struct {
	AssessmentID       string                       `json:"id"`
	RunID              string                       `json:"runId"`
	NodeID             string                       `json:"nodeId"`
	Disposition        string                       `json:"disposition"`
	AssessmentDigest   string                       `json:"assessmentDigest"`
	PolicyDigest       string                       `json:"policyDigest"`
	Submission         JSONSnapshot                 `json:"submission"`
	RouteContext       JSONSnapshot                 `json:"routeContext"`
	Status             ReadinessAssessmentStatus    `json:"status"`
	Decision           *ReadinessDecisionProjection `json:"decision,omitempty"`
	ResourceVersion    uint64                       `json:"resourceVersion"`
	LastGlobalPosition uint64                       `json:"lastGlobalPosition"`
	CreatedAt          time.Time                    `json:"createdAt"`
	UpdatedAt          time.Time                    `json:"updatedAt"`
}

// LeaseScopeKind identifies the resource whose mutations are fenced.
type LeaseScopeKind string

const (
	LeaseScopeAttempt    LeaseScopeKind = "attempt"
	LeaseScopeRepository LeaseScopeKind = "repository"
	LeaseScopeWorktree   LeaseScopeKind = "worktree"
)

// LeaseState is the explicit lifecycle of a durable lease.
type LeaseState string

const (
	LeaseHeld              LeaseState = "held"
	LeaseReleasing         LeaseState = "releasing"
	LeaseReleased          LeaseState = "released"
	LeaseReconcileRequired LeaseState = "reconcile_required"
)

const (
	// DefaultLeaseDuration is the MVP ownership interval.
	DefaultLeaseDuration = 30 * time.Second
	// MaximumHeartbeatInterval is the slowest normal MVP heartbeat cadence.
	MaximumHeartbeatInterval = 10 * time.Second
)

// LeaseGuard is the complete capability required for a fenced mutation.
// Matching only a lease ID or fencing token is intentionally insufficient.
type LeaseGuard struct {
	LeaseID          string
	HolderAttemptID  string
	DaemonInstanceID string
	FencingToken     uint64
}

// Lease is one durable ownership record. ReleasedAt and Evidence are populated
// only by terminal/reconciliation transitions, as constrained by SQLite.
type Lease struct {
	LeaseID          string
	ScopeKind        LeaseScopeKind
	ScopeID          string
	HolderAttemptID  string
	DaemonInstanceID string
	FencingToken     uint64
	AcquiredAt       time.Time
	HeartbeatAt      time.Time
	ExpiresAt        time.Time
	HostBootID       string
	ProcessIdentity  json.RawMessage
	State            LeaseState
	Evidence         json.RawMessage
	ReleasedAt       *time.Time
}

// AcquireLeaseRequest describes one compare-and-swap lease acquisition.
// ExpectedFencingToken must equal the last token issued for the scope.
type AcquireLeaseRequest struct {
	LeaseID              string
	ScopeKind            LeaseScopeKind
	ScopeID              string
	HolderAttemptID      string
	DaemonInstanceID     string
	HostBootID           string
	ExpectedFencingToken uint64
	Duration             time.Duration
	ProcessIdentity      json.RawMessage
}

// QueueKind identifies a deterministic scheduler queue.
type QueueKind string

const (
	QueueAttempt         QueueKind = "attempt"
	QueueRepositoryWrite QueueKind = "repository_write"
)

// QueueEntry is a queued item. Row presence is the sole source of queue
// membership; there is no duplicated queued state flag.
type QueueEntry struct {
	Kind        QueueKind
	ScopeID     string
	ItemID      string
	Priority    int
	AvailableAt time.Time
	EnqueuedAt  time.Time
	Payload     json.RawMessage
}

// EnqueueRequest describes one idempotent queue insertion.
type EnqueueRequest struct {
	Kind        QueueKind
	ScopeID     string
	ItemID      string
	Priority    int
	AvailableAt time.Time
	Payload     json.RawMessage
}

// Store atomically appends events and maintains rebuildable current state.
type Store interface {
	Append(context.Context, ...PendingEvent) ([]Event, error)
	EventsAfter(context.Context, uint64, int) ([]Event, error)
	EventBounds(context.Context) (EventBounds, error)
	Project(context.Context, string) (ProjectProjection, error)
	Projects(context.Context) ([]ProjectProjection, error)
	WorkItem(context.Context, string) (WorkItemProjection, error)
	WorkItems(context.Context) ([]WorkItemProjection, error)
	WorkItemsForProject(context.Context, string) ([]WorkItemProjection, error)
	Run(context.Context, string) (RunProjection, error)
	Runs(context.Context) ([]RunProjection, error)
	RunsForWorkItem(context.Context, string) ([]RunProjection, error)
	Story(context.Context, string) (StoryProjection, error)
	StoriesForWorkItem(context.Context, string) ([]StoryProjection, error)
	Point(context.Context, string) (PointProjection, error)
	PointsForStory(context.Context, string) ([]PointProjection, error)
	Node(context.Context, string) (NodeProjection, error)
	NodesForRun(context.Context, string) ([]NodeProjection, error)
	Attempt(context.Context, string) (AttemptProjection, error)
	AttemptsForRun(context.Context, string) ([]AttemptProjection, error)
	AttemptsForPoint(context.Context, string, uint64) ([]AttemptProjection, error)
	ActiveAttempts(context.Context) ([]AttemptProjection, error)
	RunEvidence(context.Context, string) (RunEvidence, error)
	BeginCommand(context.Context, BeginCommandRequest) (CommandEvidence, bool, error)
	CompleteCommand(context.Context, CompleteCommandRequest) (CommandEvidence, error)
	Approval(context.Context, string) (ApprovalProjection, error)
	ReadinessAssessment(context.Context, string) (ReadinessAssessmentProjection, error)
	LatestReadinessAssessmentForRun(context.Context, string) (ReadinessAssessmentProjection, error)
	RebuildProjections(context.Context) error
	AcquireLease(context.Context, AcquireLeaseRequest) (Lease, error)
	HeartbeatLease(context.Context, LeaseGuard, time.Duration) (Lease, error)
	BeginLeaseRelease(context.Context, LeaseGuard) (Lease, error)
	CompleteLeaseRelease(context.Context, LeaseGuard, json.RawMessage) (Lease, error)
	MarkLeaseReconcileRequired(context.Context, LeaseGuard, json.RawMessage) (Lease, error)
	ValidateLease(context.Context, LeaseScopeKind, string, LeaseGuard) (Lease, error)
	Enqueue(context.Context, EnqueueRequest) (QueueEntry, error)
	QueueHead(context.Context, QueueKind, string) (QueueEntry, error)
	RemoveQueueEntry(context.Context, QueueKind, string, string) error
	AcquireRepositoryLock(context.Context, string, AcquireLeaseRequest) (Lease, error)
}
