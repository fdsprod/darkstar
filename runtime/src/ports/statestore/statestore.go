// Package statestore defines the durable event and current-state projection boundary.
package statestore

import (
	"context"
	"encoding/json"
	"time"
)

// AggregateType identifies one domain event stream.
type AggregateType string

const (
	AggregateProject   AggregateType = "project"
	AggregateWork      AggregateType = "work"
	AggregateRun       AggregateType = "run"
	AggregateVisit     AggregateType = "visit"
	AggregateAttempt   AggregateType = "attempt"
	AggregateArtifact  AggregateType = "artifact"
	AggregateApproval  AggregateType = "approval"
	AggregateOperation AggregateType = "operation"
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
	SchemaVersion     uint64
	ID                string
	GlobalPosition    uint64
	AggregateType     AggregateType
	AggregateID       string
	AggregateRevision uint64
	Kind              string
	OccurredAt        time.Time
	RecordedAt        time.Time
	CorrelationID     string
	CausationID       *string
	CommandID         string
	Actor             Actor
	Data              json.RawMessage
	Metadata          json.RawMessage
}

// RunStatus is the closed set of persisted run states.
type RunStatus string

const (
	RunPending           RunStatus = "pending"
	RunRunning           RunStatus = "running"
	RunWaiting           RunStatus = "waiting"
	RunCompleted         RunStatus = "completed"
	RunFailed            RunStatus = "failed"
	RunCancelled         RunStatus = "cancelled"
	RunReconcileRequired RunStatus = "reconcile_required"
)

// RunProjection is the rebuildable query representation of a run.
type RunProjection struct {
	RunID              string
	WorkItemID         string
	WorkflowID         string
	WorkflowVersion    string
	Status             RunStatus
	ResourceVersion    uint64
	LastGlobalPosition uint64
	CreatedAt          time.Time
	UpdatedAt          time.Time
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

// ApprovalProjection is the rebuildable query representation of an approval.
type ApprovalProjection struct {
	ApprovalID         string
	RunID              string
	Class              ApprovalClass
	Status             ApprovalStatus
	ScopeDigest        string
	PolicyDigest       string
	ResourceVersion    uint64
	LastGlobalPosition uint64
	CreatedAt          time.Time
	UpdatedAt          time.Time
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
	Run(context.Context, string) (RunProjection, error)
	Approval(context.Context, string) (ApprovalProjection, error)
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
