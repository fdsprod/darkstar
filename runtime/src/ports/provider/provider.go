// Package provider defines the provider-neutral reasoning-provider boundary.
package provider

import (
	"context"
	"encoding/json"
	"time"

	"github.com/fdsprod/darkstar/runtime/src/ports"
)

// Provider owns the complete lifecycle of one provider-backed attempt. Methods
// use DARKSTAR identities and normalized values; provider protocol types remain
// inside the concrete adapter.
type Provider interface {
	ProbeHealth(context.Context) (Health, error)
	Capabilities(context.Context) (CapabilityManifest, error)
	StartAttempt(context.Context, AttemptRequest) (AttemptHandle, error)
	ResumeAttempt(context.Context, ResumeRequest) (AttemptHandle, error)
	StreamEvents(context.Context, EventRequest) (EventStream, error)
	Respond(context.Context, InteractionResponse) (InteractionReceipt, error)
	CancelAttempt(context.Context, CancelRequest) (CancelResult, error)
	GetResult(context.Context, ResultRequest) (AttemptResult, error)
}

type HealthState string

const (
	HealthAvailable       HealthState = "available"
	HealthUnavailable     HealthState = "unavailable"
	HealthUnauthenticated HealthState = "unauthenticated"
	HealthDegraded        HealthState = "degraded"
)

// Health contains only safe executable and connectivity observations.
type Health struct {
	State              HealthState
	Provider           string
	ProviderVersion    string
	ExecutableIdentity string
	Platform           string
	Authenticated      bool
	Diagnostics        []string
}

// CapabilityManifest is the immutable feature view used to prepare an attempt.
type CapabilityManifest struct {
	Provider    string
	Fingerprint string
	Features    map[string]Capability
	ObservedAt  time.Time
}

type Capability struct {
	Available bool
	Version   string
	Metadata  map[string]string
}

type AccessClass string

const (
	AccessReadOnly       AccessClass = "read_only"
	AccessWorkspaceWrite AccessClass = "workspace_write"
)

type NetworkPolicy string

const (
	NetworkDenied     NetworkPolicy = "denied"
	NetworkRestricted NetworkPolicy = "restricted"
	NetworkAllowed    NetworkPolicy = "allowed"
)

type InteractionPolicy string

const (
	InteractionDeny  InteractionPolicy = "deny"
	InteractionAsk   InteractionPolicy = "ask"
	InteractionAllow InteractionPolicy = "allow"
)

// AttemptRequest freezes everything an adapter may use for a new attempt.
type AttemptRequest struct {
	AttemptID             string
	RunID                 string
	NodeID                string
	IdempotencyKey        string
	Workspace             string
	AdditionalRoots       []string
	Access                AccessClass
	Network               NetworkPolicy
	CommandPolicy         InteractionPolicy
	FilePolicy            InteractionPolicy
	ToolPolicy            InteractionPolicy
	ModelHint             string
	ReasoningHint         string
	Prompt                string
	Inputs                []Input
	OutputSchema          json.RawMessage
	Timeout               time.Duration
	CancellationGrace     time.Duration
	UsageLimits           UsageLimits
	CapabilityFingerprint string
}

type InputKind string

const (
	InputText     InputKind = "text"
	InputImage    InputKind = "image"
	InputSkill    InputKind = "skill"
	InputArtifact InputKind = "artifact"
)

// Input references prepared context. Locator is an opaque DARKSTAR-owned
// reference, never an adapter-specific URL or protocol object.
type Input struct {
	Kind      InputKind
	Name      string
	MediaType string
	Locator   string
	Digest    string
	Text      string
	Metadata  map[string]string
}

type UsageLimits struct {
	InputTokens  int64
	OutputTokens int64
	CostUnits    int64
}

type AttemptHandle struct {
	AttemptID        string
	Provider         string
	ProviderThreadID string
	ProviderTurnID   string
	ProcessOwnerID   string
}

type ResumeRequest struct {
	AttemptID        string
	IdempotencyKey   string
	ProviderThreadID string
	ProviderTurnID   string
	LastSequence     uint64
	ContextDigest    string
	WorkspaceDigest  string
}

type EventRequest struct {
	Handle        AttemptHandle
	AfterSequence uint64
}

// EventStream produces ordered events until io.EOF. Closing a stream releases
// observation resources; it does not cancel the provider attempt.
type EventStream interface {
	Receive() (Event, error)
	Close() error
}

type Event struct {
	SchemaVersion    int
	AttemptID        string
	Sequence         uint64
	OccurredAt       time.Time
	Kind             string
	Provider         string
	ProviderVersion  string
	ProviderThreadID string
	ProviderTurnID   string
	ProviderItemID   string
	Payload          json.RawMessage
	RawEvidenceRef   string
}

type InteractionDecision string

const (
	InteractionAllowOnce       InteractionDecision = "allow_once"
	InteractionAllowForSession InteractionDecision = "allow_for_session"
	InteractionDenied          InteractionDecision = "deny"
	InteractionCancelled       InteractionDecision = "cancel"
	InteractionExpired         InteractionDecision = "expire"
	InteractionAnswered        InteractionDecision = "answer"
)

type InteractionResponse struct {
	AttemptID         string
	ProviderThreadID  string
	ProviderRequestID string
	IdempotencyKey    string
	Decision          InteractionDecision
	Answer            json.RawMessage
	ScopeDigest       string
}

type InteractionReceipt struct {
	ProviderRequestID string
	Recorded          bool
	RecordedAt        time.Time
}

type CancelRequest struct {
	Handle         AttemptHandle
	IdempotencyKey string
	GracePeriod    time.Duration
}

type CancelDisposition string

const (
	CancelGraceful    CancelDisposition = "graceful"
	CancelForced      CancelDisposition = "forced"
	CancelAlreadyDone CancelDisposition = "already_terminal"
	CancelUncertain   CancelDisposition = "uncertain"
)

type CancelResult struct {
	Disposition CancelDisposition
	EvidenceRef string
}

type ResultRequest struct {
	Handle AttemptHandle
}

type AttemptStatus string

const (
	AttemptSucceeded   AttemptStatus = "succeeded"
	AttemptFailed      AttemptStatus = "failed"
	AttemptInterrupted AttemptStatus = "interrupted"
	AttemptCancelled   AttemptStatus = "cancelled"
	AttemptUnknown     AttemptStatus = "unknown"
)

type AttemptResult struct {
	Status            AttemptStatus
	StructuredOutput  json.RawMessage
	ValidationFailure *ports.Failure
	Usage             Usage
	WorkspaceEvidence []Evidence
	Recovery          RecoveryMetadata
}

type Usage struct {
	InputTokens  int64
	CachedTokens int64
	OutputTokens int64
	CostUnits    int64
}

type Evidence struct {
	Kind   string
	Ref    string
	Digest string
}

type RecoveryMetadata struct {
	ProviderThreadID string
	ProviderTurnID   string
	LastSequence     uint64
	ProcessOwnerID   string
	Resumable        bool
	EvidenceRef      string
}
