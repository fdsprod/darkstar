// Package representationregistry defines immutable, derived artifact views.
package representationregistry

import (
	"context"
	"errors"
	"time"

	"darkstar/src/ports/artifactregistry"
	"darkstar/src/ports/artifactstore"
	"darkstar/src/ports/contentprocessor"
)

var (
	ErrNotFound = errors.New("artifact representation not found")
	ErrConflict = errors.New("artifact representation conflict")
)

type Disclosure string

const (
	DisclosureRaw      Disclosure = "raw"
	DisclosureRedacted Disclosure = "redacted"
	DisclosureWithheld Disclosure = "withheld"
)

type RegisterRequest struct {
	RepresentationID string
	IdempotencyKey   string
	Artifact         artifactregistry.VersionRef
	Kind             contentprocessor.RepresentationKind
	Processor        contentprocessor.Descriptor
	MediaType        string
	Locator          artifactstore.Locator
	Digest           string
	Size             int64
	TokenEstimate    int64
	Truncated        bool
	Disclosure       Disclosure
	Diagnostics      []string
	Metadata         map[string]string
	CreatedAt        time.Time
}

type Representation struct {
	RepresentationID string                              `json:"representationId"`
	Artifact         artifactregistry.VersionRef         `json:"artifact"`
	Kind             contentprocessor.RepresentationKind `json:"representationKind"`
	Processor        contentprocessor.Descriptor         `json:"processor"`
	MediaType        string                              `json:"mediaType"`
	Locator          artifactstore.Locator               `json:"locator"`
	Digest           string                              `json:"digest"`
	Size             int64                               `json:"size"`
	TokenEstimate    int64                               `json:"tokenEstimate"`
	Truncated        bool                                `json:"truncated"`
	Disclosure       Disclosure                          `json:"disclosure"`
	Diagnostics      []string                            `json:"diagnostics"`
	Metadata         map[string]string                   `json:"metadata"`
	CreatedAt        time.Time                           `json:"createdAt"`
}

type Registry interface {
	RegisterRepresentation(context.Context, RegisterRequest) (Representation, bool, error)
	Representation(context.Context, string) (Representation, error)
	ForArtifact(context.Context, artifactregistry.VersionRef) ([]Representation, error)
}
