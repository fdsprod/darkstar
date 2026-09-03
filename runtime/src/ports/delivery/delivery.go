// Package delivery defines provider-neutral publication and change-request
// operations. GitHub, GitLab, and other provider concepts stay in adapters.
package delivery

import (
	"context"
	"time"
)

// Connector is the complete remote-delivery capability. The embedded narrow
// interfaces let composition depend on only the operation family it needs.
type Connector interface {
	HealthProber
	BranchObserver
	BranchPublisher
	ChangeRequestFinder
	ChangeRequestCreator
	ChangeRequestUpdater
	ChangeRequestObserver
}

type HealthProber interface {
	ProbeHealth(context.Context, HealthRequest) (HealthObservation, error)
}

type BranchObserver interface {
	ObserveBranch(context.Context, ObserveBranchRequest) (BranchObservation, error)
}

type BranchPublisher interface {
	PublishBranch(context.Context, PublishBranchRequest) (BranchPublication, error)
}

type ChangeRequestFinder interface {
	FindChangeRequests(context.Context, FindChangeRequestsRequest) (ChangeRequestSearch, error)
}

type ChangeRequestCreator interface {
	CreateChangeRequest(context.Context, CreateChangeRequestRequest) (ChangeRequestCreation, error)
}

type ChangeRequestUpdater interface {
	UpdateChangeRequest(context.Context, UpdateChangeRequestRequest) (ChangeRequestUpdate, error)
}

type ChangeRequestObserver interface {
	ObserveChangeRequest(context.Context, ObserveChangeRequestRequest) (ChangeRequestObservation, error)
}

// Repository is one provider-owned repository. Provider and Host are explicit
// because enterprise hosts and cross-repository change requests are valid.
type Repository struct {
	Provider string
	Host     string
	Owner    string
	Name     string
}

type HealthRequest struct {
	Repository Repository
	Account    string
}

// HealthOutcome is the authoritative, mutually exclusive access observation.
// Read-only access is distinct from degraded access so callers never infer
// publish permission from independent booleans.
type HealthOutcome interface{ isHealthOutcome() }

type HealthReady struct{}

func (HealthReady) isHealthOutcome() {}

type HealthReadOnly struct{ Reason string }

func (HealthReadOnly) isHealthOutcome() {}

type HealthUnauthenticated struct{ Reason string }

func (HealthUnauthenticated) isHealthOutcome() {}

type HealthUnavailable struct{ Reason string }

func (HealthUnavailable) isHealthOutcome() {}

type HealthDegraded struct{ Reason string }

func (HealthDegraded) isHealthOutcome() {}

type HealthObservation struct {
	Repository  Repository
	Account     string
	Outcome     HealthOutcome
	Diagnostics []string
	ObservedAt  time.Time
	EvidenceRef string
}

type BranchRef struct {
	Repository Repository
	Name       string
}

type ObserveBranchRequest struct {
	Branch BranchRef
}

type BranchOutcome interface{ isBranchOutcome() }

type BranchFound struct{ CommitSHA string }

func (BranchFound) isBranchOutcome() {}

type BranchMissing struct{}

func (BranchMissing) isBranchOutcome() {}

type BranchObservation struct {
	Branch      BranchRef
	Outcome     BranchOutcome
	ObservedAt  time.Time
	EvidenceRef string
}

// RemoteBranchExpectation is the compare-and-swap precondition for branch
// publication. A missing branch cannot accidentally be represented by an empty
// expected commit SHA.
type RemoteBranchExpectation interface{ isRemoteBranchExpectation() }

type RemoteBranchMissing struct{}

func (RemoteBranchMissing) isRemoteBranchExpectation() {}

type RemoteBranchAt struct{ CommitSHA string }

func (RemoteBranchAt) isRemoteBranchExpectation() {}

type PublishBranchRequest struct {
	OperationID     string
	LocalRepository string
	SourceCommitSHA string
	Destination     BranchRef
	ExpectedRemote  RemoteBranchExpectation
}

// BranchPublicationOutcome reports whether this invocation performed the
// mutation or reconciled the effect of an earlier invocation.
type BranchPublicationOutcome interface{ isBranchPublicationOutcome() }

type BranchPublished struct{}

func (BranchPublished) isBranchPublicationOutcome() {}

type BranchAlreadyPublished struct{}

func (BranchAlreadyPublished) isBranchPublicationOutcome() {}

type BranchPublication struct {
	Branch      BranchRef
	CommitSHA   string
	Outcome     BranchPublicationOutcome
	ObservedAt  time.Time
	EvidenceRef string
}

// ChangeRequestState is a closed remote lifecycle observation.
type ChangeRequestState interface{ isChangeRequestState() }

type DraftState struct{}

func (DraftState) isChangeRequestState() {}

type OpenState struct{}

func (OpenState) isChangeRequestState() {}

type MergedState struct{}

func (MergedState) isChangeRequestState() {}

type ClosedState struct{}

func (ClosedState) isChangeRequestState() {}

type ChangeRequestCoordinates struct {
	Base BranchRef
	Head BranchRef
}

type FindChangeRequestsRequest struct {
	Coordinates     ChangeRequestCoordinates
	OwnershipMarker string
}

type ChangeRequestRef struct {
	Repository Repository
	ID         string
}

// ChangeRequestOwnership keeps marker and revision presence atomic. Unowned
// requests remain observable because they are required collision evidence.
type ChangeRequestOwnership interface{ isChangeRequestOwnership() }

type UnownedChangeRequest struct{}

func (UnownedChangeRequest) isChangeRequestOwnership() {}

type OwnedChangeRequest struct {
	Marker   string
	Revision string
}

func (OwnedChangeRequest) isChangeRequestOwnership() {}

type ChangeRequest struct {
	Ref         ChangeRequestRef
	Coordinates ChangeRequestCoordinates
	URL         string
	Title       string
	Body        string
	State       ChangeRequestState
	Ownership   ChangeRequestOwnership
}

type ChangeRequestSearch struct {
	Matches     []ChangeRequest
	ObservedAt  time.Time
	EvidenceRef string
}

// ChangeRequestMode is the required creation mode; its zero value cannot be
// mistaken for a final change request.
type ChangeRequestMode interface{ isChangeRequestMode() }

type CreateDraft struct{}

func (CreateDraft) isChangeRequestMode() {}

type CreateReady struct{}

func (CreateReady) isChangeRequestMode() {}

// OwnedSection is the only part of a change-request body the connector may
// replace after creation.
type OwnedSection struct {
	Marker   string
	Revision string
	Body     string
}

type CreateChangeRequestRequest struct {
	OperationID     string
	Coordinates     ChangeRequestCoordinates
	Title           string
	OwnedSection    OwnedSection
	Mode            ChangeRequestMode
	ExpectedHeadSHA string
}

type ChangeRequestCreationOutcome interface{ isChangeRequestCreationOutcome() }

type ChangeRequestCreated struct{ ChangeRequest ChangeRequest }

func (ChangeRequestCreated) isChangeRequestCreationOutcome() {}

type ChangeRequestReconciled struct{ ChangeRequest ChangeRequest }

func (ChangeRequestReconciled) isChangeRequestCreationOutcome() {}

type ChangeRequestCreation struct {
	Outcome     ChangeRequestCreationOutcome
	ObservedAt  time.Time
	EvidenceRef string
}

type TitleEdit interface{ isTitleEdit() }

type KeepTitle struct{}

func (KeepTitle) isTitleEdit() {}

type ReplaceTitle struct{ Title string }

func (ReplaceTitle) isTitleEdit() {}

type ReadinessEdit interface{ isReadinessEdit() }

type KeepReadiness struct{}

func (KeepReadiness) isReadinessEdit() {}

type MarkReady struct{}

func (MarkReady) isReadinessEdit() {}

// UpdateChangeRequestRequest replaces only the connector-owned body section.
// Human-authored content outside the ownership markers must be preserved.
type UpdateChangeRequestRequest struct {
	OperationID  string
	Ref          ChangeRequestRef
	Title        TitleEdit
	OwnedSection OwnedSection
	Readiness    ReadinessEdit
}

type ChangeRequestUpdateOutcome interface{ isChangeRequestUpdateOutcome() }

type ChangeRequestUpdated struct{ ChangeRequest ChangeRequest }

func (ChangeRequestUpdated) isChangeRequestUpdateOutcome() {}

type ChangeRequestUnchanged struct{ ChangeRequest ChangeRequest }

func (ChangeRequestUnchanged) isChangeRequestUpdateOutcome() {}

type ChangeRequestUpdate struct {
	Outcome     ChangeRequestUpdateOutcome
	ObservedAt  time.Time
	EvidenceRef string
}

type ObserveChangeRequestRequest struct {
	Ref ChangeRequestRef
}

type ChangeRequestObservationOutcome interface{ isChangeRequestObservationOutcome() }

type ChangeRequestFound struct{ ChangeRequest ChangeRequest }

func (ChangeRequestFound) isChangeRequestObservationOutcome() {}

type ChangeRequestMissing struct{}

func (ChangeRequestMissing) isChangeRequestObservationOutcome() {}

type ChangeRequestObservation struct {
	Outcome     ChangeRequestObservationOutcome
	ObservedAt  time.Time
	EvidenceRef string
}
