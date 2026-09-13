package statestore

import (
	"context"
	"encoding/json"
	"time"

	"darkstar/src/ports/tracker"
)

type SourceLineageOrigin string

const (
	SourceAdmitted     SourceLineageOrigin = "admitted"
	SourceNative       SourceLineageOrigin = "native"
	SourceLegacyNative SourceLineageOrigin = "legacy_native"
)

// SourceLineage is immutable. Project selection changes never retarget a work
// record; an explicit settled-work rebind appends a new lineage revision.
type SourceLineage struct {
	WorkID, ProjectID, TicketKey string
	Revision, BindingRevision    uint64
	Ref                          tracker.TicketRef
	Origin                       SourceLineageOrigin
	CreatedAt                    time.Time
}

// TicketAdmission records an explicit human approval of one exact observation,
// not a provider business-state transition or authority for an external effect.
type TicketAdmission struct {
	ID, WorkID, ProjectID, ObservationID string
	LineageRevision, BindingRevision     uint64
	ApprovedAt                           time.Time
	Actor                                string
}

type SourceAdmissionMutation struct {
	ProjectID, ObservationID, WorkID string
	BindingRevision                  uint64
	// ExistingWorkID is populated only when approving a later run for pinned work.
	ExistingWorkID                                    string
	IdempotencyKey, RequestDigest, AdmissionID, Actor string
	ApprovedAt                                        time.Time
	ObservedNotBefore                                 time.Time
	// NewWork is a daemon-created event, used only if no local work exists yet.
	NewWork PendingEvent
}

type SourceRebindMutation struct {
	WorkID, ObservationID                             string
	ExpectedLineageRevision, BindingRevision          uint64
	IdempotencyKey, RequestDigest, AdmissionID, Actor string
	ApprovedAt                                        time.Time
	ObservedNotBefore                                 time.Time
}

// RunSourceSnapshot is assembled inside the run.created transaction from an
// approved admission. Ticket is the strict versioned tracker observation codec.
type RunSourceSnapshot struct {
	RunID           string            `json:"runId"`
	WorkID          string            `json:"workItemId"`
	AdmissionID     string            `json:"admissionId"`
	ObservationID   string            `json:"observationId"`
	LineageRevision uint64            `json:"lineageRevision"`
	BindingRevision uint64            `json:"bindingRevision"`
	Ref             tracker.TicketRef `json:"ref"`
	Pin             tracker.Pin       `json:"pin"`
	Ticket          json.RawMessage   `json:"ticket"`
	ApprovedAt      time.Time         `json:"approvedAt"`
	CapturedAt      time.Time         `json:"capturedAt"`
}

type SourceCheckOutcome interface{ isSourceCheckOutcome() }

type SourceObserved struct{ Observation BacklogObservation }

func (SourceObserved) isSourceCheckOutcome() {}

type SourceUnchanged struct{ ObservationID string }

func (SourceUnchanged) isSourceCheckOutcome() {}

type SourceMissing struct{ EvidenceRef string }

func (SourceMissing) isSourceCheckOutcome() {}

type SourceInaccessible struct{ Reason string }

func (SourceInaccessible) isSourceCheckOutcome() {}

type SourceCheckMutation struct {
	WorkID                  string
	ExpectedLineageRevision uint64
	CheckedAt               time.Time
	Pin                     tracker.Pin
	Outcome                 SourceCheckOutcome
}

// TicketExecutionStore owns explicit admission and immutable source lineage.
// Source reading alone uses BacklogStore and has none of these mutation methods.
type TicketExecutionStore interface {
	AdmitSourceTicket(context.Context, SourceAdmissionMutation) (TicketAdmission, error)
	RebindSourceTicket(context.Context, SourceRebindMutation) (TicketAdmission, error)
	WorkTicketLineage(context.Context, string) (SourceLineage, error)
	WorkTicketLineages(context.Context, string) ([]SourceLineage, error)
	LatestTicketAdmission(context.Context, string) (TicketAdmission, error)
	CurrentSourceObservation(context.Context, string) (BacklogCachedTicket, error)
	RecordSourceCheck(context.Context, SourceCheckMutation) error
	ApprovedRunSource(context.Context, string, string) (RunSourceSnapshot, error)
	RunSourceSnapshot(context.Context, string) (RunSourceSnapshot, error)
}
