package statestore

import (
	"context"
	"encoding/json"
	"time"

	"darkstar/src/ports/artifactregistry"
	"darkstar/src/ports/extension"
	"darkstar/src/ports/provider"
)

type InvestigationArtifactReference struct {
	ArtifactID string `json:"artifactId"`
	Version    uint64 `json:"version"`
	SHA256     string `json:"sha256"`
}

type InvestigationTaskInput struct {
	Kind         string                          `json:"kind"`
	Text         string                          `json:"text,omitempty"`
	FeatureBrief *InvestigationArtifactReference `json:"featureBrief,omitempty"`
}

type InvestigationFrozenTask struct {
	Kind    string                          `json:"kind"`
	Content json.RawMessage                 `json:"content"`
	Digest  string                          `json:"digest"`
	Source  *InvestigationArtifactReference `json:"source,omitempty"`
}

type InvestigationProviderSelection struct {
	Provider              string         `json:"provider"`
	Extension             *extension.Ref `json:"extension,omitempty"`
	CapabilityFingerprint string         `json:"capabilityFingerprint"`
}

type InvestigationCollection struct {
	SchemaVersion int                            `json:"schemaVersion"`
	CollectionID  string                         `json:"id"`
	ProjectID     string                         `json:"projectId"`
	ScopeID       string                         `json:"scopeId"`
	ScopeDigest   string                         `json:"scopeDigest"`
	Task          InvestigationFrozenTask        `json:"task"`
	Provider      InvestigationProviderSelection `json:"provider"`
	RepositoryIDs []string                       `json:"repositoryIds"`
	Concurrency   int                            `json:"concurrency"`
	RequestDigest string                         `json:"requestDigest"`
	CreatedAt     time.Time                      `json:"createdAt"`
	Status        string                         `json:"status"`
	Revision      uint64                         `json:"revision"`
	UpdatedAt     time.Time                      `json:"updatedAt"`
}

type InvestigationUnitResult struct {
	Kind         string                      `json:"kind"`
	RepositoryID string                      `json:"repositoryId,omitempty"`
	Quality      string                      `json:"quality"`
	Findings     json.RawMessage             `json:"findings"`
	Artifact     artifactregistry.VersionRef `json:"artifact"`
	Digest       string                      `json:"digest"`
}

type InvestigationUnit struct {
	UnitID           string                   `json:"id"`
	CollectionID     string                   `json:"collectionId"`
	Kind             string                   `json:"kind"`
	RepositoryID     string                   `json:"repositoryId,omitempty"`
	Status           string                   `json:"status"`
	CurrentAttemptID string                   `json:"currentAttemptId,omitempty"`
	Result           *InvestigationUnitResult `json:"result,omitempty"`
	Reason           string                   `json:"reason,omitempty"`
}

type InvestigationAttempt struct {
	AttemptID             string                   `json:"id"`
	CollectionID          string                   `json:"collectionId"`
	UnitID                string                   `json:"unitId"`
	Number                uint64                   `json:"number"`
	State                 string                   `json:"state"`
	OwnerID               string                   `json:"ownerId"`
	LeaseExpiresAt        time.Time                `json:"leaseExpiresAt"`
	CapabilityFingerprint string                   `json:"capabilityFingerprint,omitempty"`
	ContextDigest         string                   `json:"contextDigest,omitempty"`
	Request               json.RawMessage          `json:"request,omitempty"`
	Handle                *provider.AttemptHandle  `json:"handle,omitempty"`
	LastSequence          uint64                   `json:"lastSequence"`
	Submission            json.RawMessage          `json:"submission,omitempty"`
	Result                *InvestigationUnitResult `json:"result,omitempty"`
	Reason                string                   `json:"reason,omitempty"`
	CreatedAt             time.Time                `json:"createdAt"`
	UpdatedAt             time.Time                `json:"updatedAt"`
}

// Attempt observations are durable before acknowledging provider handles,
// submitted outputs or retained artifacts. Their kind selects exactly one value.
type InvestigationObservation struct {
	Kind                  string
	CapabilityFingerprint string
	ContextDigest         string
	Request               json.RawMessage
	Handle                *provider.AttemptHandle
	Event                 *provider.Event
	Submission            json.RawMessage
	Result                *InvestigationUnitResult
}

type InvestigationClaim struct {
	Attempt   InvestigationAttempt
	Reconcile bool
}

type InvestigationStore interface {
	CreateInvestigation(context.Context, InvestigationCollection) (InvestigationCollection, error)
	Investigation(context.Context, string) (InvestigationCollection, error)
	Investigations(context.Context) ([]InvestigationCollection, error)
	InvestigationUnits(context.Context, string) ([]InvestigationUnit, error)
	InvestigationAttempts(context.Context, string) ([]InvestigationAttempt, error)
	TransitionInvestigation(context.Context, string, uint64, string, string) (InvestigationCollection, error)
	ClaimInvestigation(context.Context, string, int, time.Duration) (InvestigationClaim, bool, error)
	RenewInvestigationAttempt(context.Context, string, string, time.Duration) error
	ObserveInvestigationAttempt(context.Context, string, string, InvestigationObservation) error
	CompleteInvestigationAttempt(context.Context, string, string, string, string) (InvestigationAttempt, error)
}
