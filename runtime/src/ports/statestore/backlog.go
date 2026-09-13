package statestore

import (
	"context"
	"encoding/json"
	"time"

	"darkstar/src/ports"
	"darkstar/src/ports/tracker"
)

// BacklogSource is the sole selected business source, independent of repository
// membership and publication destinations. External selections freeze the
// connection revision rather than silently adopting later credential authority.
type BacklogSource interface{ isBacklogSource() }

type NativeBacklogSource struct {
	Namespace tracker.Namespace
}

func (NativeBacklogSource) isBacklogSource() {}

type ExternalBacklogSource struct {
	ConnectionID, ConnectionRevision string
	Scope                            tracker.Scope
}

func (ExternalBacklogSource) isBacklogSource() {}

type BacklogBinding struct {
	ProjectID  string
	Revision   uint64
	Source     BacklogSource
	SelectedAt time.Time
}

// BacklogRefreshState is a durable scan checkpoint. Query.Cursor is always
// empty: Cursor alone owns continuation. A page and its checkpoint commit in
// the same transaction. Phase-specific invariants are checked at storage entry.
type BacklogRefreshPhase string

const (
	BacklogRefreshing BacklogRefreshPhase = "refreshing"
	BacklogComplete   BacklogRefreshPhase = "complete"
	BacklogFailed     BacklogRefreshPhase = "failed"
)

type BacklogRefreshState struct {
	ProjectID                                          string
	BindingRevision, Revision, Generation              uint64
	Phase                                              BacklogRefreshPhase
	Query                                              tracker.Query
	Pin                                                tracker.Pin
	Cursor                                             string
	StartedAt, UpdatedAt, LastSuccessAt, NextAttemptAt time.Time
	Failures                                           uint32
	Failure                                            *ports.Failure
}

// BacklogObservation contains a strict, versioned tracker ticket codec and the
// original source's evidence reference. ID is stable for ref+native revision;
// ContentDigest excludes fresh-check time and evidence locator metadata.
type BacklogObservation struct {
	ID, TicketKey, NativeRevision, ContentDigest string
	Ref                                          tracker.TicketRef
	Ticket                                       json.RawMessage
	ObservedAt                                   time.Time
	EvidenceRef                                  string
}

type BacklogCacheState string

const (
	BacklogAvailable    BacklogCacheState = "available"
	BacklogMissing      BacklogCacheState = "missing"
	BacklogInaccessible BacklogCacheState = "inaccessible"
)

// A cache row always retains its last successful observation, even while a
// subsequent exact lookup reports missing or permission loss.
type BacklogCachedTicket struct {
	ProjectID                string
	BindingRevision          uint64
	TicketKey, ObservationID string
	Observation              BacklogObservation
	State                    BacklogCacheState
	CheckedAt                time.Time
	SeenGeneration           uint64
	Reason, EvidenceRef      string
}

type BacklogCommit struct {
	State            BacklogRefreshState
	ExpectedRevision uint64
	Observations     []BacklogObservation
	Tickets          []BacklogCachedTicket
}

// BacklogStore is a read-side business cache and binding journal. It has no
// work-creation, run-start, scheduler or provider-writer method.
type BacklogStore interface {
	BacklogBinding(context.Context, string) (BacklogBinding, error)
	BacklogBindingHistory(context.Context, string) ([]BacklogBinding, error)
	SelectBacklogSource(context.Context, string, uint64, BacklogSource, time.Time) (BacklogBinding, error)
	BacklogRefresh(context.Context, string, uint64) (BacklogRefreshState, error)
	CommitBacklogRefresh(context.Context, BacklogCommit) error
	BacklogTickets(context.Context, string, uint64, bool, string, int) ([]BacklogCachedTicket, error)
	BacklogTicket(context.Context, string, uint64, string) (BacklogCachedTicket, error)
	BacklogObservation(context.Context, string) (BacklogObservation, error)
}
