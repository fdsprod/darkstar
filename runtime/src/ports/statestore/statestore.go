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

// Store atomically appends events and maintains rebuildable current state.
type Store interface {
	Append(context.Context, ...PendingEvent) ([]Event, error)
	EventsAfter(context.Context, uint64, int) ([]Event, error)
	Run(context.Context, string) (RunProjection, error)
	Approval(context.Context, string) (ApprovalProjection, error)
	RebuildProjections(context.Context) error
}
