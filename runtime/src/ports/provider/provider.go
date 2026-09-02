// Package provider defines the provider-neutral reasoning-provider boundary.
package provider

import (
	"context"
	"encoding/json"
	"time"

	"darkstar/src/ports"
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
	Diagnostics        []string
}

// CapabilityManifest is the immutable feature view used to prepare an attempt.
type CapabilityManifest struct {
	Provider    string
	Fingerprint string
	Features    map[string]Capability
	ObservedAt  time.Time
}

type Capability interface{ isCapability() }

type AvailableCapability struct {
	Version  string
	Metadata map[string]string
}

func (AvailableCapability) isCapability() {}

type UnavailableCapability struct {
	Reason string
}

func (UnavailableCapability) isCapability() {}

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

// InteractionKind is the closed checkpoint vocabulary exposed by provider
// adapters. It keeps permission-bearing actions distinct from user questions
// and client-executed tool calls without leaking provider method names.
type InteractionKind string

const (
	InteractionCommand    InteractionKind = "command"
	InteractionFile       InteractionKind = "file"
	InteractionNetwork    InteractionKind = "network"
	InteractionPermission InteractionKind = "permission"
	InteractionTool       InteractionKind = "tool"
	InteractionUser       InteractionKind = "user"
)

type InteractionCheckpoint struct {
	Kind              InteractionKind `json:"kind"`
	ProviderRequestID string          `json:"providerRequestId"`
	ScopeDigest       string          `json:"scopeDigest"`
}

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
	Kind             EventKind
	Provider         string
	ProviderVersion  string
	ProviderThreadID string
	ProviderTurnID   string
	ProviderItemID   string
	Payload          json.RawMessage
	RawEvidenceRef   string
}

type PermissionDecision string

const (
	PermissionAllowOnce       PermissionDecision = "allow_once"
	PermissionAllowForSession PermissionDecision = "allow_for_session"
	PermissionDenied          PermissionDecision = "deny"
	PermissionCancelled       PermissionDecision = "cancel"
	PermissionExpired         PermissionDecision = "expire"
)

type InteractionContext struct {
	AttemptID         string
	ProviderThreadID  string
	ProviderRequestID string
	IdempotencyKey    string
	ScopeDigest       string
}

type InteractionResponse interface{ isInteractionResponse() }

type PermissionResponse struct {
	InteractionContext
	Decision PermissionDecision
}

func (PermissionResponse) isInteractionResponse() {}

type AnswerResponse struct {
	InteractionContext
	Answer json.RawMessage
}

func (AnswerResponse) isInteractionResponse() {}

type InteractionReceipt struct {
	ProviderRequestID string
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

type AttemptResultMetadata struct {
	Usage             Usage
	WorkspaceEvidence []Evidence
	Recovery          RecoveryMetadata
}

type AttemptResult interface{ isAttemptResult() }

type SucceededResult struct {
	AttemptResultMetadata
	StructuredOutput json.RawMessage
}

func (SucceededResult) isAttemptResult() {}

type FailedResult struct {
	AttemptResultMetadata
	Failure ports.Failure
}

func (FailedResult) isAttemptResult() {}

type InterruptedResult struct {
	AttemptResultMetadata
	Failure ports.Failure
}

func (InterruptedResult) isAttemptResult() {}

type CancelledResult struct{ AttemptResultMetadata }

func (CancelledResult) isAttemptResult() {}

type UnknownResult struct {
	AttemptResultMetadata
	Failure ports.Failure
}

func (UnknownResult) isAttemptResult() {}

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
