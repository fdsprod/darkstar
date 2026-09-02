// Package artifactregistry defines immutable artifact metadata and provenance.
package artifactregistry

import (
	"context"
	"errors"
	"time"

	"darkstar/src/ports/artifactstore"
)

var (
	// ErrNotFound classifies a missing artifact version.
	ErrNotFound = errors.New("artifact version not found")
	// ErrVersionConflict means an idempotency key already owns different metadata.
	ErrVersionConflict = errors.New("artifact version conflict")
)

// SourceKind identifies how original bytes entered DARKSTAR.
type SourceKind string

const (
	SourceFile      SourceKind = "file"
	SourcePaste     SourceKind = "paste"
	SourceStdin     SourceKind = "stdin"
	SourceGenerated SourceKind = "generated"
	SourceExternal  SourceKind = "external"
)

// Sensitivity is the explicit disclosure classification for an exact version.
type Sensitivity string

const (
	SensitivityUnknown   Sensitivity = "unknown"
	SensitivityPublic    Sensitivity = "public"
	SensitivityInternal  Sensitivity = "internal"
	SensitivitySensitive Sensitivity = "sensitive"
	SensitivitySecret    Sensitivity = "secret"
)

// Status records whether immutable bytes may be inspected.
type Status string

const (
	StatusStored              Status = "stored"
	StatusStoredUninspectable Status = "stored_uninspectable"
	StatusQuarantined         Status = "quarantined"
)

// VersionRef identifies one exact artifact version. It never means "latest".
type VersionRef struct {
	ArtifactID string `json:"artifactId"`
	Version    uint64 `json:"version"`
}

// Provenance is a closed origin variant. Implementations require either the
// complete attempt identity or operation-only identity.
type Provenance interface {
	isProvenance()
}

// OperationProvenance describes supplied or externally produced content.
type OperationProvenance struct {
	OperationID string      `json:"operationId"`
	Source      *VersionRef `json:"source,omitempty"`
}

func (OperationProvenance) isProvenance() {}

// AttemptProvenance describes content produced by one exact provider attempt.
type AttemptProvenance struct {
	RunID       string      `json:"runId"`
	NodeID      string      `json:"nodeId"`
	AttemptID   string      `json:"attemptId"`
	OperationID string      `json:"operationId"`
	Source      *VersionRef `json:"source,omitempty"`
}

func (AttemptProvenance) isProvenance() {}

// Producer fingerprints the component that supplied or generated the version.
type Producer struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// RegisterRequest contains all immutable facts for a new artifact version.
// IdempotencyKey names this logical registration and is scoped to ArtifactID.
type RegisterRequest struct {
	ArtifactID        string
	IdempotencyKey    string
	SourceKind        SourceKind
	SourceName        string
	BlobDigest        string
	Size              int64
	DeclaredMediaType string
	DetectedMediaType string
	Locator           artifactstore.Locator
	Sensitivity       Sensitivity
	Creator           string
	Status            Status
	Producer          Producer
	Roles             []string
	Tags              []string
	Metadata          map[string]string
	Provenance        Provenance
	CreatedAt         time.Time
}

// ArtifactVersion is one immutable metadata snapshot tied to exact bytes.
type ArtifactVersion struct {
	ArtifactID        string                `json:"artifactId"`
	Version           uint64                `json:"version"`
	SourceKind        SourceKind            `json:"sourceKind"`
	SourceName        string                `json:"sourceName"`
	BlobDigest        string                `json:"blobDigest"`
	Size              int64                 `json:"size"`
	DeclaredMediaType string                `json:"declaredMediaType"`
	DetectedMediaType string                `json:"detectedMediaType"`
	Locator           artifactstore.Locator `json:"locator"`
	Sensitivity       Sensitivity           `json:"sensitivity"`
	Trust             string                `json:"trust"`
	Creator           string                `json:"creator"`
	Status            Status                `json:"status"`
	Producer          Producer              `json:"producer"`
	Roles             []string              `json:"roles"`
	Tags              []string              `json:"tags"`
	Metadata          map[string]string     `json:"metadata"`
	Provenance        Provenance            `json:"provenance"`
	CreatedAt         time.Time             `json:"createdAt"`
}

// Registry allocates and reads immutable versions. Repeating an identical
// registration is idempotent; reusing its key with different facts conflicts.
type Registry interface {
	Register(context.Context, RegisterRequest) (ArtifactVersion, bool, error)
	ArtifactVersion(context.Context, VersionRef) (ArtifactVersion, error)
	LatestVersion(context.Context, string) (ArtifactVersion, error)
	Versions(context.Context, string) ([]ArtifactVersion, error)
	VersionsByDigest(context.Context, string) ([]ArtifactVersion, error)
	Artifacts(context.Context) ([]ArtifactVersion, error)
}
