// Package delivery defines provider-neutral publication and change-request
// operations. GitHub, GitLab, and other provider concepts stay in adapters.
package delivery

import (
	"context"
	"time"
)

// Connector owns remote delivery observations and mutations. Every mutation is
// identified before dispatch and can be reconciled through a read operation.
type Connector interface {
	ProbeHealth(context.Context, HealthRequest) (Health, error)
	ObserveBranch(context.Context, ObserveBranchRequest) (BranchObservation, error)
	PublishBranch(context.Context, PublishBranchRequest) (BranchPublication, error)
	FindChangeRequests(context.Context, FindChangeRequestsRequest) ([]ChangeRequest, error)
	CreateChangeRequest(context.Context, CreateChangeRequestRequest) (ChangeRequest, error)
	UpdateChangeRequest(context.Context, UpdateChangeRequestRequest) (ChangeRequest, error)
	ObserveChangeRequest(context.Context, ObserveChangeRequestRequest) (ChangeRequest, error)
}

type Repository struct {
	Provider string
	Owner    string
	Name     string
}

type HealthRequest struct {
	Repository Repository
	Account    string
}

type HealthState string

const (
	HealthAvailable       HealthState = "available"
	HealthUnavailable     HealthState = "unavailable"
	HealthUnauthenticated HealthState = "unauthenticated"
	HealthDegraded        HealthState = "degraded"
)

type Health struct {
	Provider     string
	Account      string
	ReadState    HealthState
	PublishState HealthState
	Diagnostics  []string
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
	Outcome     BranchOutcome
	ObservedAt  time.Time
	EvidenceRef string
}

type PublishBranchRequest struct {
	OperationID       string
	IdempotencyKey    string
	LocalRepository   string
	SourceCommitSHA   string
	Destination       BranchRef
	ExpectedRemoteSHA string
}

type BranchPublication struct {
	Branch      BranchRef
	CommitSHA   string
	ObservedAt  time.Time
	EvidenceRef string
}

type ChangeRequestState string

const (
	ChangeRequestDraft  ChangeRequestState = "draft"
	ChangeRequestOpen   ChangeRequestState = "open"
	ChangeRequestMerged ChangeRequestState = "merged"
	ChangeRequestClosed ChangeRequestState = "closed"
)

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

type ChangeRequest struct {
	Ref             ChangeRequestRef
	Coordinates     ChangeRequestCoordinates
	URL             string
	Title           string
	Body            string
	State           ChangeRequestState
	OwnershipMarker string
	OwnedRevision   string
	ObservedAt      time.Time
	EvidenceRef     string
}

type CreateChangeRequestRequest struct {
	OperationID     string
	IdempotencyKey  string
	Coordinates     ChangeRequestCoordinates
	Title           string
	Body            string
	Draft           bool
	OwnershipMarker string
	ExpectedHeadSHA string
}

// UpdateChangeRequestRequest replaces only the connector-owned body section.
// Human-authored content outside the ownership markers must be preserved.
type UpdateChangeRequestRequest struct {
	OperationID     string
	IdempotencyKey  string
	Ref             ChangeRequestRef
	Title           *string
	OwnedBody       string
	OwnershipMarker string
	OwnedRevision   string
	MarkReady       bool
}

type ObserveChangeRequestRequest struct {
	Ref ChangeRequestRef
}
