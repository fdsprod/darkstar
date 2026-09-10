// Package worksource defines read-only import and refresh of external work.
// Source adapters cannot create runs or publish changes to the source system.
package worksource

import (
	"context"
	"encoding/json"
)

type Request struct {
	Reference string
	// Revision is the exact previously imported source revision; empty on import.
	Revision string
}

type Source interface {
	Fetch(context.Context, Request) (Observation, error)
}

type Observation interface{ isObservation() }

type Item struct {
	Reference   string
	Revision    string
	Title       string
	Description string
	Metadata    json.RawMessage
}

func (Item) isObservation() {}

type Unchanged struct{ Revision string }

func (Unchanged) isObservation() {}

type Missing struct{ Reference string }

func (Missing) isObservation() {}
