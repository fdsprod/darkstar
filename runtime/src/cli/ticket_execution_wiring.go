package cli

import "darkstar/src/core/ticketexecution"

func (service *daemonAPIService) configureTicketExecution() error {
	engine, err := ticketexecution.New(service.database, daemonBacklogResolver{native: service.database, connections: service.trackerConnectionManager}, ticketexecution.Options{Workspaces: service.workspaces, Rules: service.trackerMapping})
	if err != nil {
		return err
	}
	if service.trackerMapping != nil {
		service.trackerMapping.intake = engine
	}
	return service.server.SetTicketExecution(engine)
}
