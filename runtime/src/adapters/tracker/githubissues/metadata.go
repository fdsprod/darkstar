package githubissues

import (
	"context"

	"darkstar/src/core/trackercontract"
	"darkstar/src/ports/tracker"
	"darkstar/src/ports/worksource"
)

var _ worksource.TrackerMetadataV1 = (*Adapter)(nil)

func (a *Adapter) DiscoverFieldCatalog(ctx context.Context, pin tracker.Pin) ([]tracker.FieldCatalog, error) {
	if err := trackercontract.ValidatePin(pin, a.pin); err != nil {
		return nil, err
	}
	if _, err := a.probe(ctx); err != nil {
		return nil, err
	}
	// These are GitHub's bounded issue API values, not a team's custom workflow.
	// Projects fields and sprint semantics are deliberately absent.
	return []tracker.FieldCatalog{
		{Identity: tracker.NamedID{ID: "state", Name: "State"}, Values: []tracker.NamedID{{ID: "open", Name: "Open"}, {ID: "closed", Name: "Closed"}}},
		{Identity: tracker.NamedID{ID: "state_reason", Name: "State reason"}, Values: []tracker.NamedID{{ID: "completed", Name: "Completed"}, {ID: "not_planned", Name: "Not planned"}, {ID: "reopened", Name: "Reopened"}}},
	}, nil
}
