package cli

import "darkstar/src/core/ticketexecution"

func (service *daemonAPIService) configureTicketExecution() error {
	engine, err := ticketexecution.New(service.database, daemonBacklogResolver{native: service.database, connections: service.trackerConnectionManager}, ticketexecution.Options{Workspaces: service.workspaces})
	if err != nil {
		return err
	}
	return service.server.SetTicketExecution(engine)
}
