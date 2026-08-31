// Package executor defines the node-execution boundary used by the scheduler.
package executor

import (
	"context"
	"encoding/json"
	"time"
)

// Executor starts or resumes one bounded node attempt. Provider, command, gate,
// approval, and sub-workflow implementations adapt their specialized contracts
// to this application-facing lifecycle.
type Executor interface {
	Kind() string
	Start(context.Context, Request) (Execution, error)
	Resume(context.Context, ResumeRequest) (Execution, error)
}

type Request struct {
	AttemptID      string
	RunID          string
	VisitID        string
	NodeID         string
	IdempotencyKey string
	InputDigest    string
	Inputs         json.RawMessage
	Workspace      string
	Timeout        time.Duration
	PolicyDigest   string
}

type ResumeRequest struct {
	AttemptID       string
	InputDigest     string
	RecoveryRef     string
	LastEventCursor string
}

// Execution is an active or recoverable attempt handle. Wait returns only after
// the executor has reached a terminal observation; core validation still decides
// whether candidate output makes the attempt successful.
type Execution interface {
	Reference() Reference
	Events() Events
	Wait(context.Context) (Result, error)
	Cancel(context.Context, CancelRequest) (CancelResult, error)
}

type Reference struct {
	AttemptID   string
	ExternalID  string
	RecoveryRef string
}

type Events interface {
	Receive() (Event, error)
	Close() error
}

type Event struct {
	Cursor      string
	Kind        string
	OccurredAt  time.Time
	Data        json.RawMessage
	EvidenceRef string
}

type Result struct {
	CandidateOutput json.RawMessage
	Artifacts       []Artifact
	Evidence        []Evidence
	RecoveryRef     string
}

type Artifact struct {
	Role      string
	Locator   string
	Digest    string
	MediaType string
}

type Evidence struct {
	Kind   string
	Ref    string
	Digest string
}

type CancelRequest struct {
	IdempotencyKey string
	GracePeriod    time.Duration
}

type CancelResult struct {
	Terminal    bool
	Forced      bool
	Uncertain   bool
	EvidenceRef string
}
