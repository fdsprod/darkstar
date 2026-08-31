// Package contentprocessor defines bounded derivation of inspectable artifact
// representations. Artifact bytes remain untrusted data at this boundary.
package contentprocessor

import (
	"context"
	"io"
	"time"
)

// Processor derives zero or more immutable representations from one source.
// Implementations must isolate failures and honor the supplied resource limits.
type Processor interface {
	Descriptor() Descriptor
	Supports(context.Context, SourceDescriptor) (Support, error)
	Process(context.Context, ProcessRequest, Sink) (ProcessResult, error)
}

type Descriptor struct {
	Name       string
	Version    string
	MediaTypes []string
}

type SourceDescriptor struct {
	ArtifactID        string
	DeclaredMediaType string
	DetectedMediaType string
	Digest            string
	Size              int64
}

type SupportState string

const (
	SupportSupported   SupportState = "supported"
	SupportUnsupported SupportState = "unsupported"
	SupportQuarantined SupportState = "quarantined"
)

type Support struct {
	State       SupportState
	MediaType   string
	Diagnostics []string
}

type Limits struct {
	SourceBytes     int64
	OutputBytes     int64
	Representations int
	Pages           int
	Pixels          int64
	WallTime        time.Duration
	MemoryBytes     int64
}

type ProcessRequest struct {
	OperationID    string
	IdempotencyKey string
	Source         SourceDescriptor
	Content        io.Reader
	Limits         Limits
	PolicyVersion  string
}

type RepresentationKind string

const (
	RepresentationText       RepresentationKind = "text"
	RepresentationStructured RepresentationKind = "structured"
	RepresentationTable      RepresentationKind = "table"
	RepresentationImage      RepresentationKind = "image"
	RepresentationPreview    RepresentationKind = "preview"
	RepresentationDescriptor RepresentationKind = "descriptor"
)

// Representation is streamed to Sink so processors do not need to retain
// bounded-but-potentially-large derived content in memory.
type Representation struct {
	Kind          RepresentationKind
	MediaType     string
	Content       io.Reader
	Digest        string
	Size          int64
	TokenEstimate int64
	Truncated     bool
	Diagnostics   []string
	Metadata      map[string]string
}

// Sink accepts a representation into application-owned durable storage. It must
// either return a durable receipt or an error; partial output is not success.
type Sink interface {
	Store(context.Context, Representation) (Receipt, error)
}

type Receipt struct {
	RepresentationID string
	Locator          string
	Digest           string
	Size             int64
}

type ProcessResult struct {
	Representations []Receipt
	Diagnostics     []string
	Limited         bool
}
