// Package ticketwriter defines deterministic ticket effects. It is separate
// from read-only worksource and Git/change-request delivery authority.
package ticketwriter

import (
	"context"

	"darkstar/src/ports/tracker"
)

type CapabilityObserverV1 interface {
	Discover(context.Context, tracker.AdapterConfigPin) (tracker.Manifest, error)
	Inspect(context.Context, InspectRequest) (tracker.WriteOptions, error)
}

type InspectRequest struct {
	Pin         tracker.Pin
	Destination tracker.Scope
	Scope       tracker.OptionScope
}

type PublisherV1 interface {
	Apply(context.Context, tracker.Intent) (tracker.EffectResult, error)
}

type ReconcilerV1 interface {
	Reconcile(context.Context, tracker.Intent) (tracker.EffectResult, error)
}

type WriterV1 interface {
	CapabilityObserverV1
	PublisherV1
	ReconcilerV1
}
