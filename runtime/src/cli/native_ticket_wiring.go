package cli

import (
	"context"

	"darkstar/src/adapters/tracker/builtin"
	"darkstar/src/api"
	"darkstar/src/core/ticketmanagement"
	"darkstar/src/ports/statestore"
)

func configureNativeTickets(server *api.Server, store statestore.Store, native statestore.NativeTrackerStore) error {
	service, err := ticketmanagement.New(store, native, func(ctx context.Context, projectID string) (ticketmanagement.Binding, error) {
		adapter, err := builtin.New(native, projectID)
		if err != nil {
			return ticketmanagement.Binding{}, err
		}
		return ticketmanagement.Binding{Source: adapter, Browser: adapter, Writer: adapter, Config: adapter.ConfigPin()}, nil
	})
	if err != nil {
		return err
	}
	return server.SetNativeTickets(service)
}
