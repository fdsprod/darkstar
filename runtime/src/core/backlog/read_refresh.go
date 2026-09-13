package backlog

import (
	"context"
	"errors"
	"time"

	"darkstar/src/ports"
	"darkstar/src/ports/statestore"
	"darkstar/src/ports/tracker"
	"darkstar/src/ports/worksource"
)

// ReadRefresh reconciles one retained ticket by exact immutable identity. Browse
// absence never enters this path and therefore cannot mark a ticket missing.
func (s *Service) ReadRefresh(ctx context.Context, project string, expected uint64, ref tracker.TicketRef) (RefreshResult, error) {
	ctx, cancel := context.WithTimeout(ctx, s.options.Timeout)
	defer cancel()
	binding, state, err := s.prepare(ctx, project, expected)
	if err != nil {
		return RefreshResult{}, err
	}
	if !validRef(ref, Scope(binding)) {
		return RefreshResult{}, failure(ports.FailureInvalidRequest, "exact ticket refresh is outside the selected source namespace")
	}
	cached, err := s.store.BacklogTicket(ctx, project, binding.Revision, TicketKey(ref))
	if err != nil {
		return RefreshResult{}, err
	}
	if state.Phase == statestore.BacklogFailed && s.options.Now().Before(state.NextAttemptAt) {
		return refreshResult(binding, state, 0), nil
	}
	resolved, manifest, err := s.resolve(ctx, binding, state)
	if err != nil {
		return s.recordExactFailure(ctx, binding, state, cached, err)
	}
	state.Pin = manifest.Pin
	result, err := resolved.Source.Read(ctx, worksource.ReadTicketRequest{Pin: state.Pin, Ref: ref, KnownRevision: cached.Observation.NativeRevision})
	if err != nil {
		return s.recordExactFailure(ctx, binding, state, cached, err)
	}
	previousState, previousCached := state, cached
	observations := []statestore.BacklogObservation{}
	switch value := result.(type) {
	case tracker.Found:
		if value.Ticket.Ref != ref {
			return s.recordExactFailure(ctx, binding, state, cached, failure(ports.FailureProtocolDrift, "exact read returned another ticket"))
		}
		observation, updated, err := s.observation(binding, state, value.Ticket)
		if err != nil {
			return s.recordExactFailure(ctx, binding, state, cached, err)
		}
		// Exact reconciliation is independent from query membership.
		updated.SeenGeneration = cached.SeenGeneration
		cached = updated
		observations = append(observations, observation)
	case tracker.Unchanged:
		if value.Ref != ref || value.Fresh.Revision != cached.Observation.NativeRevision || value.Fresh.ObservedAt.IsZero() || value.Fresh.ObservedAt.After(s.options.Now().Add(time.Minute)) {
			return s.recordExactFailure(ctx, binding, state, cached, failure(ports.FailureProtocolDrift, "unchanged response did not prove the retained ticket revision"))
		}
		cached.State, cached.CheckedAt, cached.Reason = statestore.BacklogAvailable, value.Fresh.ObservedAt, ""
		cached.EvidenceRef = cached.Observation.EvidenceRef
	case tracker.Missing:
		if value.Ref != ref || value.EvidenceRef == "" || value.ObservedAt.IsZero() || value.ObservedAt.After(s.options.Now().Add(time.Minute)) {
			return s.recordExactFailure(ctx, binding, state, cached, failure(ports.FailureProtocolDrift, "missing response lacks exact source evidence"))
		}
		cached.State, cached.CheckedAt = statestore.BacklogMissing, value.ObservedAt
		cached.Reason, cached.EvidenceRef = "exact source lookup reports missing; last successful observation retained", value.EvidenceRef
	default:
		return s.recordExactFailure(ctx, binding, state, cached, failure(ports.FailureProtocolDrift, "source returned an unknown exact-read outcome"))
	}
	state.Failure, state.Failures = nil, 0
	if state.Phase == statestore.BacklogFailed {
		state.Phase = statestore.BacklogRefreshing
		state.NextAttemptAt = time.Time{}
	}
	if err := s.commit(ctx, &state, observations, []statestore.BacklogCachedTicket{cached}); err != nil {
		var problem *ports.Failure
		if errors.As(err, &problem) && problem.Code != ports.FailureConflict {
			return s.recordExactFailure(ctx, binding, previousState, previousCached, err)
		}
		return refreshResult(binding, previousState, 0), err
	}
	return refreshResult(binding, state, 1), nil
}

func (s *Service) recordExactFailure(ctx context.Context, binding statestore.BacklogBinding, state statestore.BacklogRefreshState, cached statestore.BacklogCachedTicket, err error) (RefreshResult, error) {
	problem := safeFailure(err)
	if ctx.Err() != nil {
		return refreshResult(binding, state, 0), problem
	}
	state.Phase, state.Failure = statestore.BacklogFailed, problem
	state.Failures++
	state.NextAttemptAt = s.retryAt(problem, state.Failures)
	updates := []statestore.BacklogCachedTicket{}
	if permissionFailure(problem) {
		cached.State, cached.CheckedAt, cached.Reason = statestore.BacklogInaccessible, s.options.Now().UTC(), "exact source access is unavailable; last successful observation retained"
		updates = append(updates, cached)
	}
	if err := s.commit(ctx, &state, nil, updates); err != nil {
		return refreshResult(binding, state, 0), err
	}
	return refreshResult(binding, state, 0), problem
}
