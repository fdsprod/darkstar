// Package artifactstore defines immutable binary storage owned by DARKSTAR.
package artifactstore

import (
	"context"
	"io"
	"time"
)

// Store persists and retrieves immutable content without exposing backend paths.
// Metadata registration, provenance, and workflow binding remain core concerns.
type Store interface {
	Put(context.Context, PutRequest) (Blob, error)
	Open(context.Context, OpenRequest) (io.ReadCloser, error)
	Stat(context.Context, StatRequest) (Blob, error)
	List(context.Context, ListRequest) (Page, error)
}

// PutRequest supports single-pass hashing and atomic adoption. ExpectedDigest and
// ExpectedSize are optional integrity assertions; IdempotencyKey identifies the
// logical storage operation rather than the content itself.
type PutRequest struct {
	IdempotencyKey string
	Content        io.Reader
	ExpectedDigest string
	ExpectedSize   *int64
	MediaType      string
}

// Locator is an opaque store-owned value. Core code may persist and compare it,
// but must not parse it into a filesystem path or backend key.
type Locator string

type Blob struct {
	Locator   Locator
	Digest    string
	Size      int64
	MediaType string
	StoredAt  time.Time
}

type OpenRequest struct {
	Locator        Locator
	ExpectedDigest string
}

type StatRequest struct {
	Locator Locator
}

type ListRequest struct {
	After string
	Limit int
}

type Page struct {
	Blobs     []Blob
	NextAfter string
}
