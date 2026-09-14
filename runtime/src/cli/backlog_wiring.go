package cli

import (
	"context"
	"errors"
	"strconv"
	"time"

	"darkstar/src/adapters/tracker/builtin"
	"darkstar/src/adapters/tracker/connectionsetup"
	"darkstar/src/core/backlog"
	"darkstar/src/ports"
	"darkstar/src/ports/statestore"
)

type backlogService = backlog.Service

type daemonBacklogResolver struct {
	native      statestore.NativeTrackerStore
	connections *connectionsetup.Manager
}

func (resolver daemonBacklogResolver) Resolve(ctx context.Context, binding statestore.BacklogBinding) (backlog.ResolvedSource, error) {
	revision := strconv.FormatUint(binding.Revision, 10)
	switch source := binding.Source.(type) {
	case statestore.NativeBacklogSource:
		adapter, err := builtin.NewForBinding(resolver.native, binding.ProjectID, revision)
		if err != nil {
			return backlog.ResolvedSource{}, err
		}
		// Native scope is determined by the durable project namespace. A saved
		// source must never redirect an existing project to another namespace.
		manifest, err := adapter.Discover(ctx, adapter.ConfigPin())
		if err != nil {
			return backlog.ResolvedSource{}, err
		}
		if manifest.Scope.Namespace != source.Namespace {
			return backlog.ResolvedSource{}, &ports.Failure{Code: ports.FailureProtocolDrift, Message: "Native source namespace does not match its project."}
		}
		return backlog.ResolvedSource{Source: adapter, Browser: adapter, Config: adapter.ConfigPin()}, nil
	case statestore.ExternalBacklogSource:
		if resolver.connections == nil {
			return backlog.ResolvedSource{}, &ports.Failure{Code: ports.FailureUnavailable, Message: "External tracker connections are not configured."}
		}
		resolved, err := resolver.connections.ResolveSource(ctx, source.ConnectionID, source.ConnectionRevision, source.Scope, revision)
		if err != nil {
			return backlog.ResolvedSource{}, err
		}
		return backlog.ResolvedSource{Source: resolved.Source, Browser: resolved.Browser, Config: resolved.Config}, nil
	default:
		return backlog.ResolvedSource{}, &ports.Failure{Code: ports.FailureUnsupported, Message: "The selected backlog source is unsupported."}
	}
}

func (service *daemonAPIService) configureBacklog() error {
	if service.database == nil || service.trackerConnectionManager == nil {
		return errors.New("backlog requires state and tracker connections")
	}
	engine, err := backlog.New(service.database, daemonBacklogResolver{native: service.database, connections: service.trackerConnectionManager}, backlog.Options{MaxPages: 4, MaxTickets: 200, Timeout: 30 * time.Second, PollInterval: 2 * time.Minute, MaxBackoff: time.Hour})
	if err != nil {
		return err
	}
	service.backlog = engine
	return service.server.SetBacklog(engine)
}

func (service *daemonAPIService) startBacklogPolling(parent context.Context) {
	ctx, cancel := context.WithCancel(parent)
	service.backlogCancel = cancel
	service.backlogDone = make(chan struct{})
	go func() {
		defer close(service.backlogDone)
		ticker := time.NewTicker(15 * time.Second)
		defer ticker.Stop()
		offset := 0
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				projects, err := service.database.Projects(ctx)
				if err != nil || len(projects) == 0 {
					continue
				}
				// Bound one pass and rotate the start index so a large project
				// registry cannot starve later sources or create a polling burst.
				budget := min(len(projects), 8)
				for count := 0; count < budget; count++ {
					project := projects[(offset+count)%len(projects)]
					if project.Status == statestore.ProjectActive {
						_, _ = service.backlog.Poll(ctx, project.ProjectID)
						service.evaluateTrackerIntake(ctx, project.ProjectID)
					}
					if ctx.Err() != nil {
						return
					}
				}
				offset = (offset + budget) % len(projects)
			}
		}
	}()
}

func (service *daemonAPIService) stopBacklogPolling() {
	if service.backlogCancel == nil {
		return
	}
	service.backlogCancel()
	<-service.backlogDone
	service.backlogCancel = nil
	service.backlogDone = nil
}
