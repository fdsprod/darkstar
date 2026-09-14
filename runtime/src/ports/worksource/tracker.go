package worksource

import (
	"context"

	"darkstar/src/ports/tracker"
)

// TrackerSourceV1 is a read-only sibling of the legacy Source import port.
// It has no writer, daemon, scheduler, or run-creation handle.
type TrackerSourceV1 interface {
	Discover(context.Context, tracker.AdapterConfigPin) (tracker.Manifest, error)
	Read(context.Context, ReadTicketRequest) (tracker.ReadResult, error)
}

type ReadTicketRequest struct {
	Pin           tracker.Pin
	Ref           tracker.TicketRef
	KnownRevision string
}

// TrackerBrowserV1 is optional; capabilities describe exact query support.
type TrackerBrowserV1 interface {
	Browse(context.Context, BrowseTicketsRequest) (tracker.TicketPage, error)
}

// TrackerMetadataV1 discovers stable field IDs and readable labels independently
// of ticket listing, including empty backlogs. It grants no writer capability.
type TrackerMetadataV1 interface {
	DiscoverFieldCatalog(context.Context, tracker.Pin) ([]tracker.FieldCatalog, error)
}

type BrowseTicketsRequest struct {
	Pin   tracker.Pin
	Scope tracker.Scope
	Query tracker.Query
}
