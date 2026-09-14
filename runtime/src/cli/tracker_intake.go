package cli

import (
	"context"
	"time"

	"darkstar/src/core/backlog"
)

// Intake runs in the daemon polling loop, never from a board read. Each pass is
// bounded and only activated automatic rules can create work. Failures retain
// the source observation for later inspection and retry.
func (service *daemonAPIService) evaluateTrackerIntake(ctx context.Context, project string) {
	if service.trackerMapping == nil || service.trackerMapping.intake == nil {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if service.trackerIntakeCursors == nil {
		service.trackerIntakeCursors = map[string]string{}
	}
	view, err := service.backlog.View(ctx, project, backlog.ViewRequest{Limit: 20, Cursor: service.trackerIntakeCursors[project]})
	if err != nil {
		delete(service.trackerIntakeCursors, project)
		return
	}
	// This is a disposable scan position, not admission authority. Rotate even
	// after a timeout; durable observations and cursors are retried next cycle.
	service.trackerIntakeCursors[project] = view.NextCursor
	for _, entry := range view.Tickets {
		if ctx.Err() != nil {
			return
		}
		if entry.CurrentSource && entry.CurrentQueryMatch && entry.Status == backlog.Fresh {
			result, err := service.trackerMapping.intake.EvaluateIntake(ctx, project, entry.ObservationID)
			if err == nil && (result.State == "automatic" || result.State == "duplicate") {
				service.prepareTrackerIntake(ctx, project, entry.ObservationID)
			}
		}
	}
}

func (service *daemonAPIService) prepareTrackerIntake(ctx context.Context, project, observation string) {
	if service.executions == nil {
		return
	}
	views, err := service.trackerMapping.intake.TicketView(ctx, project, observation)
	if err != nil {
		return
	}
	for _, view := range views {
		if view.Approval == nil || view.Approval.Actor != "tracker-intake" || view.Approval.ObservationID != observation {
			continue
		}
		prepared := false
		for _, run := range view.Runs {
			snapshot, err := service.database.RunSourceSnapshot(ctx, run.RunID)
			if err == nil && snapshot.AdmissionID == view.Approval.ID {
				prepared = true
			}
		}
		if !prepared {
			_, _ = service.executions.PrepareIntake(ctx, view.Work.WorkItemID, observation, "tracker-prepare:"+view.Approval.ID)
		}
	}
}
