package statestore

import (
	"context"
	"encoding/json"
	"time"
)

// TrackerMappingRevision is immutable configuration evidence. Activation is a
// separate revision-checked selection; old work retains its original revision.
type TrackerMappingRevision struct {
	ProjectID       string
	Revision        uint64
	BindingRevision uint64
	RulesJSON       json.RawMessage
	CreatedAt       time.Time
}

type TrackerMappingStore interface {
	SaveTrackerMapping(context.Context, TrackerMappingRevision) error
	TrackerMapping(context.Context, string, uint64) (TrackerMappingRevision, error)
	TrackerMappingHistory(context.Context, string) ([]TrackerMappingRevision, error)
	ActiveTrackerMapping(context.Context, string, uint64) (TrackerMappingRevision, error)
	ActivateTrackerMapping(context.Context, string, uint64, uint64, time.Time) error
}
