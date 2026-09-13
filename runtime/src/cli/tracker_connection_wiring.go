package cli

import (
	"path/filepath"

	"darkstar/src/adapters/tracker/connectionsetup"
)

type trackerConnectionManager = connectionsetup.Manager

func (service *daemonAPIService) configureTrackerConnections() error {
	connections, err := connectionsetup.New(filepath.Join(service.paths.Data, "tracker"), connectionsetup.Options{})
	if err != nil {
		return err
	}
	service.trackerConnectionManager = connections
	return service.server.SetTrackerConnections(connections)
}
