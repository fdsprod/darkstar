// Package tracker owns the v1alpha1 ticket-source and writer vocabulary.
// Provider configuration, credentials, scheduling, and authorization are not ports.
package tracker

import "time"

const Version = "darkstar.tracker/v1alpha1"

// Pin freezes implementation and non-secret configuration independently of identity.
type AdapterConfigPin struct {
	ContractVersion, AdapterID, AdapterVersion string
	InstallationID, AccountID                  string
	BindingRevision, ConfigRevision            string
	ConfigDigest                               string
}

// Discovery takes exact implementation/configuration; its response adds the
// capability fingerprint. Runs and operations freeze the completed Pin.
type Pin struct {
	AdapterConfigPin
	CapabilitiesDigest string
}

// Namespace uses immutable provider coordinates, never display keys or URLs.
type Namespace struct {
	Provider, Host, TenantID, ScopeID string
}

// Scope is a binding selection: e.g. a Linear team in a workspace namespace.
// Moving an issue between teams does not change its workspace-scoped TicketRef.
type Scope struct {
	Namespace   Namespace
	ContainerID string
}

type TicketRef struct {
	Namespace Namespace
	ID        string
}

type NamedID struct {
	ID, Name string
}

// FieldCatalog is a complete observed set of supported values for one source
// field. Omitted fields are unavailable; labels never substitute for identity.
type FieldCatalog struct {
	Identity NamedID
	Values   []NamedID
}

// Knowledge distinguishes absent values from incomplete reads and unsupported data.
// Known[[]T] with an empty slice means observed none; unknown never means none.
type Knowledge[T any] interface{ isKnowledge(T) }

type Known[T any] struct{ Value T }

func (Known[T]) isKnowledge(T) {}

type Unknown[T any] struct{ Reason string }

func (Unknown[T]) isKnowledge(T) {}

type Unsupported[T any] struct{ Reason string }

func (Unsupported[T]) isKnowledge(T) {}

type Capability string

const (
	Fetch          Capability = "fetch"
	List           Capability = "list"
	Search         Capability = "search"
	Filter         Capability = "filter"
	Page           Capability = "page"
	Refresh        Capability = "refresh"
	Create         Capability = "create"
	Edit           Capability = "edit"
	Progress       Capability = "progress"
	Transitions    Capability = "transitions"
	Hierarchy      Capability = "hierarchy"
	Dependencies   Capability = "dependencies"
	ManagedLinks   Capability = "managed_links"
	Reconciliation Capability = "reconciliation"
)

type Manifest struct {
	Pin          Pin
	Scope        Scope
	Capabilities map[Capability]Knowledge[bool]
	Filters      map[string][]FilterOperator
	MaxPageSize  int
	ObservedAt   time.Time
	EvidenceRef  string
}

type FilterOperator string

const (
	Equals FilterOperator = "equals"
	In     FilterOperator = "in"
	After  FilterOperator = "after"
)

type Predicate struct {
	FieldID  string
	Operator FilterOperator
	Values   []string
}

// Query filters are conjunctive; unsupported operators must not be dropped.
type Query struct {
	Text       string
	Predicates []Predicate
	PageSize   int
	Cursor     string
}

type Freshness interface{ isFreshness() }

type Fresh struct {
	ObservedAt time.Time
	Revision   string
}

func (Fresh) isFreshness() {}

type Stale struct {
	LastObservedAt time.Time
	Reason         string
}

func (Stale) isFreshness() {}

type NeverObserved struct{ Reason string }

func (NeverObserved) isFreshness() {}

type RelationKind string

const (
	ChildOf   RelationKind = "child_of"
	DependsOn RelationKind = "depends_on"
)

type Relation struct {
	Kind   RelationKind
	Target TicketRef
}

// Ticket is business data only; execution and sync state have separate owners.
type Ticket struct {
	Ref                 TicketRef
	Revision, Key, URL  string
	Title, Description  string
	BusinessState       Knowledge[NamedID]
	Archived            Knowledge[bool]
	UpdatedAt           Knowledge[time.Time]
	Placement           Knowledge[Scope]
	BusinessStateReason Knowledge[NamedID]
	IssueType           Knowledge[NamedID]
	Sprint              Knowledge[[]NamedID]
	Assignees, Labels   Knowledge[[]NamedID]
	Priority            Knowledge[NamedID]
	Relationships       Knowledge[[]Relation]
	Freshness           Freshness
	EvidenceRef         string
}

type ReadResult interface{ isReadResult() }

type Found struct{ Ticket Ticket }

func (Found) isReadResult() {}

type Unchanged struct {
	Ref   TicketRef
	Fresh Fresh
}

func (Unchanged) isReadResult() {}

type Missing struct {
	Ref         TicketRef
	ObservedAt  time.Time
	EvidenceRef string
}

func (Missing) isReadResult() {}

type Continuation interface{ isContinuation() }

type End struct{}

func (End) isContinuation() {}

// More's cursor is opaque and bound to the pin and complete query by the adapter.
type More struct{ Cursor string }

func (More) isContinuation() {}

type TicketPage struct {
	Tickets   []Ticket
	Next      Continuation
	Freshness Freshness
}

type FieldValue interface{ isFieldValue() }

type TextValue string

func (TextValue) isFieldValue() {}

type IDsValue []string

func (IDsValue) isFieldValue() {}

type NumberValue float64

func (NumberValue) isFieldValue() {}

type Field struct {
	Identity   NamedID
	Required   bool
	Constraint FieldConstraint
}

type FieldConstraint interface{ isFieldConstraint() }

type TextConstraint struct{}

func (TextConstraint) isFieldConstraint() {}

type NumberConstraint struct{}

func (NumberConstraint) isFieldConstraint() {}

// Only ID-valued fields can carry an ID choice constraint.
type IDsConstraint struct{ Choices IDConstraint }

func (IDsConstraint) isFieldConstraint() {}

type IDConstraint interface{ isIDConstraint() }

type AnyID struct{}

func (AnyID) isIDConstraint() {}

// AllowedIDs with zero values means no choices, never unrestricted choices.
type AllowedIDs struct{ Values []string }

func (AllowedIDs) isIDConstraint() {}

type UnknownIDs struct{ Reason string }

func (UnknownIDs) isIDConstraint() {}

// Guard outcomes describe observed provider conditions, not executable expressions.
type Guard struct {
	Identity    NamedID
	Satisfied   Knowledge[bool]
	EvidenceRef string
}

type Transition struct {
	Identity NamedID
	ToState  NamedID
	Fields   []Field
	Guards   []Guard
}

// WriteOptions is scoped to one exact ticket/type/workflow/status/sprint snapshot.
// Creation options use CreationScope; existing-ticket options use TicketScope.
type OptionScope interface{ isOptionScope() }

type CreationScope struct{ IssueTypeID string }

func (CreationScope) isOptionScope() {}

type TicketScope struct {
	Ref                                              TicketRef
	Revision, WorkflowRevision, IssueTypeID, StateID string
	Sprint                                           Knowledge[[]NamedID]
}

func (TicketScope) isOptionScope() {}

type WriteOptions struct {
	Pin                    Pin
	Scope                  OptionScope
	Fields                 Knowledge[[]Field]
	Transitions            Knowledge[[]Transition]
	ObservedAt, ValidUntil time.Time
	EvidenceRef            string
}

type ArtifactRef struct {
	ArtifactID string
	Version    uint64
	SHA256     string
}

type StoryRef struct {
	ProjectID, FeatureKey, StoryKey string
}

// Intent is one daemon-authorized desired effect, never a batch of destinations.
// AuthorizationRef names a durable decision/rule evaluation; its existence alone
// does not grant authority. The daemon resolves and verifies it before dispatch.
type Intent struct {
	OperationID, DesiredDigest, AuthorizationRef string
	Pin                                          Pin
	Destination                                  Scope
	Effect                                       Effect
}

type Effect interface{ isEffect() }

type CreateTicket struct {
	Origin      CreationOrigin
	IssueTypeID string
	Fields      map[string]FieldValue
}

func (CreateTicket) isEffect() {}

type CreationOrigin interface{ isCreationOrigin() }

type PlannedCreation struct {
	Story   StoryRef
	Backlog ArtifactRef
}

func (PlannedCreation) isCreationOrigin() {}

type DirectCreation struct{ Request ArtifactRef }

func (DirectCreation) isCreationOrigin() {}

type EditTicket struct {
	Target TicketScope
	Fields map[string]FieldValue
}

func (EditTicket) isEffect() {}

type ReportProgress struct {
	Target   TicketScope
	Evidence ProgressEvidence
	Body     string
}

func (ReportProgress) isEffect() {}

type ProgressEvidence interface{ isProgressEvidence() }

type MilestoneProgress struct{ Handoff ArtifactRef }

func (MilestoneProgress) isProgressEvidence() {}

// GeneralProgress covers retained clarification, blocker and progress reports.
type GeneralProgress struct{ Artifacts []ArtifactRef }

func (GeneralProgress) isProgressEvidence() {}

type TakeTransition struct {
	Target       TicketScope
	TransitionID string
	Fields       map[string]FieldValue
}

func (TakeTransition) isEffect() {}

type Representation interface{ isRepresentation() }

type NativeRelation struct{}

func (NativeRelation) isRepresentation() {}

// ManagedRelation retains direction, story keys and ticket refs in a visible
// managed body section. It never claims native provider dependency enforcement.
type ManagedRelation struct{ FallbackRevision, SectionID string }

func (ManagedRelation) isRepresentation() {}

type LinkStories struct {
	Target              TicketScope
	Story, RelatedStory StoryRef
	Backlog             ArtifactRef
	Relation            Relation
	Representation      Representation
}

func (LinkStories) isEffect() {}

type EffectResult interface{ isEffectResult() }

// Applied is emitted only after read-back verifies the complete desired effect.
type Applied struct{ Receipt Receipt }

func (Applied) isEffectResult() {}

// NotApplied requires positive evidence of absence, not an empty search result.
type NotApplied struct {
	OperationID, DesiredDigest string
	Pin                        Pin
	Destination                Scope
	EvidenceRef                string
}

func (NotApplied) isEffectResult() {}

type Uncertain struct{ Reason, RecoveryRef string }

func (Uncertain) isEffectResult() {}

type Receipt struct {
	OperationID, DesiredDigest string
	Pin                        Pin
	Destination                Scope
	Target                     TicketRef
	ObservedRevision           string
	ObservedAt                 time.Time
	EvidenceRef                string
}

// SyncState is independent of Ticket.BusinessState and existing run/attempt state.
// No function maps execution success to a provider status or a sync outcome.
type SyncState interface{ isSyncState() }

type Pending struct{ Intent Intent }

func (Pending) isSyncState() {}

type Reconciling struct {
	Intent      Intent
	Uncertainty Uncertain
}

func (Reconciling) isSyncState() {}

type Synchronized struct{ Receipt Receipt }

func (Synchronized) isSyncState() {}

type Blocked struct{ Code, Reason string }

func (Blocked) isSyncState() {}
