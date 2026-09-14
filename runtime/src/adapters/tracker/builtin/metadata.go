package builtin

import (
	"context"

	"darkstar/src/core/trackercontract"
	"darkstar/src/ports/statestore"
	"darkstar/src/ports/tracker"
	"darkstar/src/ports/worksource"
)

var _ worksource.TrackerMetadataV1 = (*Adapter)(nil)

func (a *Adapter) DiscoverFieldCatalog(ctx context.Context, pin tracker.Pin) ([]tracker.FieldCatalog, error) {
	if err := trackercontract.ValidatePin(pin, a.pin); err != nil {
		return nil, err
	}
	if _, err := a.Discover(ctx, pin.AdapterConfigPin); err != nil {
		return nil, err
	}
	states := []tracker.NamedID{}
	for _, state := range []statestore.NativeBusinessState{statestore.NativeOpen, statestore.NativeActive, statestore.NativeCompleted, statestore.NativeCancelled} {
		states = append(states, tracker.NamedID{ID: string(state), Name: stateName(state)})
	}
	return []tracker.FieldCatalog{{Identity: tracker.NamedID{ID: "state", Name: "Business status"}, Values: states}, {Identity: tracker.NamedID{ID: "issue_type", Name: "Issue type"}, Values: []tracker.NamedID{{ID: "ticket", Name: "Ticket"}}}}, nil
}
