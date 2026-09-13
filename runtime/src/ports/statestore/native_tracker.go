package statestore

import (
	"context"
	"encoding/json"
	"time"

	"darkstar/src/ports/tracker"
)

// NativeTicket is authoritative business data. Execution projections are retained
// separately; their lifecycle must never update State.
type NativeBusinessState string

const (
	NativeOpen      NativeBusinessState = "open"
	NativeActive    NativeBusinessState = "active"
	NativeCompleted NativeBusinessState = "completed"
	NativeCancelled NativeBusinessState = "cancelled"
)

type NativeTicket struct {
	ID, ProjectID, Title, Description string
	State                             NativeBusinessState
	Priority                          int
	Revision                          uint64
	Assignees, Labels                 []tracker.NamedID
	Relationships                     []tracker.Relation
	Evidence                          []string
	CreatedAt, UpdatedAt              time.Time
}

type NativeTicketHistory struct {
	Revision          uint64
	Kind, EvidenceRef string
	Snapshot          NativeTicket
	Request           json.RawMessage
	RecordedAt        time.Time
}

// NativeTicketMutation atomically compares the previous revision, retains the
// new snapshot and records one durable operation receipt. OperationFingerprint
// includes the complete intent, including the typed desired effect.
type NativeTicketMutation struct {
	Ticket                     NativeTicket
	ExpectedRevision           uint64
	Kind, OperationFingerprint string
	Request                    json.RawMessage
	Receipt                    tracker.Receipt
}

// NativeTrackerStore is deliberately separate from execution authority.
type NativeTrackerStore interface {
	NativeTicket(context.Context, string, string) (NativeTicket, error)
	NativeTickets(context.Context, string) ([]NativeTicket, error)
	NativeTicketHistory(context.Context, string, string) ([]NativeTicketHistory, error)
	NativeOperation(context.Context, string, string) (tracker.Receipt, error)
	MutateNativeTicket(context.Context, NativeTicketMutation) (tracker.Receipt, error)
}
