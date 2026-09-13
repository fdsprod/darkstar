package backlog

import (
	"context"
	"errors"
	"strconv"
	"time"

	"darkstar/src/core/trackercontract"
	"darkstar/src/ports"
	"darkstar/src/ports/statestore"
	"darkstar/src/ports/tracker"
	"darkstar/src/ports/worksource"
)

func (s *Service) Refresh(ctx context.Context, project string, expected uint64, query tracker.Query) (RefreshResult, error) {
	ctx, cancel := context.WithTimeout(ctx, s.options.Timeout)
	defer cancel()
	binding, state, err := s.prepare(ctx, project, expected)
	if err != nil {
		return RefreshResult{}, err
	}
	if query.PageSize == 0 {
		query.PageSize = min(50, s.options.MaxTickets)
	}
	if query.Cursor != "" || query.PageSize < 1 || query.PageSize > s.options.MaxTickets {
		return RefreshResult{}, failure(ports.FailureInvalidRequest, "refresh query must use a bounded page size and daemon-owned cursor")
	}
	changedQuery := hash(query) != hash(state.Query)
	if changedQuery || state.Phase == statestore.BacklogComplete {
		state.Query, state.Cursor = query, ""
		state.Generation++
		state.StartedAt = s.options.Now().UTC()
	}
	if state.Generation == 0 {
		state.Generation = 1
	}
	state.Query = query
	if state.StartedAt.IsZero() {
		state.StartedAt = s.options.Now().UTC()
	}
	if state.Phase == statestore.BacklogFailed && s.options.Now().Before(state.NextAttemptAt) {
		// A changed query is durable immediately, but cannot bypass the source's
		// rate-limit or reconnect schedule. Poll resumes the new query later.
		if changedQuery {
			if err := s.commit(ctx, &state, nil, nil); err != nil {
				return RefreshResult{}, err
			}
		}
		return refreshResult(binding, state, 0), nil
	}
	resolved, manifest, err := s.resolve(ctx, binding, state)
	if err != nil {
		return s.recordFailure(ctx, binding, state, err, 0)
	}
	if err := trackercontract.ValidateQuery(manifest.Pin, manifest, query); err != nil {
		return s.recordFailure(ctx, binding, state, err, 0)
	}
	state.Pin, state.Phase, state.Failure = manifest.Pin, statestore.BacklogRefreshing, nil
	state.NextAttemptAt = time.Time{}
	if err := s.commit(ctx, &state, nil, nil); err != nil {
		return RefreshResult{}, err
	}
	processed := 0
	for pages := 0; pages < s.options.MaxPages && processed+query.PageSize <= s.options.MaxTickets; pages++ {
		if err := ctx.Err(); err != nil {
			return refreshResult(binding, state, processed), safeFailure(err)
		}
		pageQuery := query
		pageQuery.Cursor = state.Cursor
		page, err := resolved.Browser.Browse(ctx, worksource.BrowseTicketsRequest{Pin: state.Pin, Scope: Scope(binding), Query: pageQuery})
		if err != nil {
			return s.recordFailure(ctx, binding, state, err, processed)
		}
		if len(page.Tickets) > query.PageSize {
			return s.recordFailure(ctx, binding, state, failure(ports.FailureProtocolDrift, "source exceeded its requested page bound"), processed)
		}
		if fresh, ok := page.Freshness.(tracker.Fresh); !ok || fresh.ObservedAt.IsZero() || fresh.Revision == "" || fresh.ObservedAt.After(s.options.Now().Add(time.Minute)) {
			return s.recordFailure(ctx, binding, state, failure(ports.FailureUnavailable, "source page is stale or incomplete"), processed)
		}
		observations := make([]statestore.BacklogObservation, 0, len(page.Tickets))
		tickets := make([]statestore.BacklogCachedTicket, 0, len(page.Tickets))
		seen := map[string]string{}
		for _, ticket := range page.Tickets {
			observation, cached, err := s.observation(binding, state, ticket)
			if err != nil {
				return s.recordFailure(ctx, binding, state, err, processed)
			}
			if previous, exists := seen[observation.ID]; exists {
				if previous != observation.ContentDigest {
					return s.recordFailure(ctx, binding, state, failure(ports.FailureProtocolDrift, "source reused a revision for conflicting observations in one page"), processed)
				}
				continue
			}
			seen[observation.ID] = observation.ContentDigest
			observations = append(observations, observation)
			tickets = append(tickets, cached)
		}
		previousCheckpoint := state
		switch next := page.Next.(type) {
		case tracker.More:
			if next.Cursor == "" || next.Cursor == state.Cursor {
				return s.recordFailure(ctx, binding, state, failure(ports.FailureProtocolDrift, "source cursor did not advance"), processed)
			}
			state.Cursor = next.Cursor
		case tracker.End:
			state.Cursor, state.Phase = "", statestore.BacklogComplete
			state.LastSuccessAt = s.options.Now().UTC()
			state.NextAttemptAt = state.LastSuccessAt.Add(s.options.PollInterval)
		default:
			return s.recordFailure(ctx, binding, state, failure(ports.FailureProtocolDrift, "source omitted its continuation outcome"), processed)
		}
		state.Failures, state.Failure = 0, nil
		if err := s.commit(ctx, &state, observations, tickets); err != nil {
			var problem *ports.Failure
			if errors.As(err, &problem) && problem.Code != ports.FailureConflict {
				return s.recordFailure(ctx, binding, previousCheckpoint, err, processed)
			}
			return refreshResult(binding, previousCheckpoint, processed), err
		}
		processed += len(page.Tickets)
		if state.Phase == statestore.BacklogComplete {
			break
		}
	}
	return refreshResult(binding, state, processed), nil
}

func (s *Service) prepare(ctx context.Context, project string, expected uint64) (statestore.BacklogBinding, statestore.BacklogRefreshState, error) {
	binding, err := s.Binding(ctx, project)
	if err != nil {
		return binding, statestore.BacklogRefreshState{}, err
	}
	if expected == 0 || binding.Revision != expected {
		return binding, statestore.BacklogRefreshState{}, failure(ports.FailureConflict, "selected backlog source changed")
	}
	state, err := s.store.BacklogRefresh(ctx, project, binding.Revision)
	if notFound(err) {
		state = statestore.BacklogRefreshState{ProjectID: project, BindingRevision: binding.Revision}
		return binding, state, nil
	}
	return binding, state, err
}

func (s *Service) resolve(ctx context.Context, binding statestore.BacklogBinding, state statestore.BacklogRefreshState) (ResolvedSource, tracker.Manifest, error) {
	resolved, err := s.resolver.Resolve(ctx, binding)
	if err != nil {
		return resolved, tracker.Manifest{}, safeFailure(err)
	}
	if resolved.Source == nil || resolved.Browser == nil {
		return resolved, tracker.Manifest{}, failure(ports.FailureUnsupported, "selected source does not support backlog browsing")
	}
	manifest, err := resolved.Source.Discover(ctx, resolved.Config)
	if err != nil {
		return resolved, manifest, safeFailure(err)
	}
	if manifest.Scope != Scope(binding) || manifest.Pin.AdapterConfigPin != resolved.Config || manifest.Pin.BindingRevision != strconv.FormatUint(binding.Revision, 10) || manifest.EvidenceRef == "" || manifest.ObservedAt.IsZero() {
		return resolved, manifest, failure(ports.FailureProtocolDrift, "source discovery differs from the selected binding")
	}
	if err := trackercontract.ValidatePin(manifest.Pin, manifest.Pin); err != nil {
		return resolved, manifest, err
	}
	if state.Pin.ContractVersion != "" {
		if err := trackercontract.ValidatePin(state.Pin, manifest.Pin); err != nil {
			return resolved, manifest, err
		}
	}
	return resolved, manifest, nil
}

func (s *Service) observation(binding statestore.BacklogBinding, state statestore.BacklogRefreshState, ticket tracker.Ticket) (statestore.BacklogObservation, statestore.BacklogCachedTicket, error) {
	if !validRef(ticket.Ref, Scope(binding)) || ticket.Revision == "" || ticket.EvidenceRef == "" {
		return statestore.BacklogObservation{}, statestore.BacklogCachedTicket{}, failure(ports.FailureProtocolDrift, "source returned an unbound or unversioned ticket observation")
	}
	encoded, err := trackercontract.EncodeTicket(ticket)
	if err != nil {
		return statestore.BacklogObservation{}, statestore.BacklogCachedTicket{}, err
	}
	id, key, content, err := trackercontract.ObservationIdentity(ticket)
	if err != nil {
		return statestore.BacklogObservation{}, statestore.BacklogCachedTicket{}, err
	}
	observedAt := s.options.Now().UTC()
	if fresh, ok := ticket.Freshness.(tracker.Fresh); ok {
		if fresh.Revision != ticket.Revision || fresh.ObservedAt.IsZero() || fresh.ObservedAt.After(observedAt.Add(time.Minute)) {
			return statestore.BacklogObservation{}, statestore.BacklogCachedTicket{}, failure(ports.FailureProtocolDrift, "ticket freshness does not match its observed revision")
		}
		observedAt = fresh.ObservedAt
	} else {
		return statestore.BacklogObservation{}, statestore.BacklogCachedTicket{}, failure(ports.FailureUnavailable, "ticket source returned stale or incomplete content")
	}
	observation := statestore.BacklogObservation{ID: id, TicketKey: key, NativeRevision: ticket.Revision, ContentDigest: content, Ref: ticket.Ref, Ticket: encoded, ObservedAt: observedAt, EvidenceRef: ticket.EvidenceRef}
	cached := statestore.BacklogCachedTicket{ProjectID: binding.ProjectID, BindingRevision: binding.Revision, TicketKey: key, ObservationID: id, Observation: observation, State: statestore.BacklogAvailable, CheckedAt: s.options.Now().UTC(), SeenGeneration: state.Generation, EvidenceRef: ticket.EvidenceRef}
	return observation, cached, nil
}

func (s *Service) commit(ctx context.Context, state *statestore.BacklogRefreshState, observations []statestore.BacklogObservation, tickets []statestore.BacklogCachedTicket) error {
	previous := state.Revision
	state.Revision++
	state.UpdatedAt = s.options.Now().UTC()
	if err := s.store.CommitBacklogRefresh(ctx, statestore.BacklogCommit{State: *state, ExpectedRevision: previous, Observations: observations, Tickets: tickets}); err != nil {
		state.Revision = previous
		return safeFailure(err)
	}
	return nil
}

func (s *Service) recordFailure(ctx context.Context, binding statestore.BacklogBinding, state statestore.BacklogRefreshState, err error, processed int) (RefreshResult, error) {
	problem := safeFailure(err)
	if ctx.Err() != nil {
		// The previous atomic page/cursor remains resumable when cancellation
		// prevents another transaction. Do not detach work from caller lifetime.
		return refreshResult(binding, state, processed), problem
	}
	state.Phase, state.Failure = statestore.BacklogFailed, problem
	state.Failures++
	if state.Generation == 0 {
		state.Generation = 1
	}
	state.NextAttemptAt = s.retryAt(problem, state.Failures)
	if err := s.commit(ctx, &state, nil, nil); err != nil {
		return refreshResult(binding, state, processed), err
	}
	return refreshResult(binding, state, processed), problem
}

func (s *Service) retryAt(problem *ports.Failure, attempts uint32) time.Time {
	delay := min(5*time.Second*time.Duration(uint64(1)<<min(attempts-1, 16)), s.options.MaxBackoff)
	if !problem.Retryable {
		delay = s.options.MaxBackoff
	}
	now := s.options.Now().UTC()
	retry := now.Add(delay)
	if seconds, err := strconv.ParseInt(problem.Details["retry_after_seconds"], 10, 64); err == nil && seconds >= 0 && seconds <= 365*24*60*60 {
		if advised := now.Add(time.Duration(seconds) * time.Second); advised.After(retry) {
			retry = advised
		}
	}
	if advised, err := time.Parse(time.RFC3339, problem.Details["retry_at_utc"]); err == nil && advised.After(retry) {
		retry = advised
	}
	return retry
}

func safeFailure(err error) *ports.Failure {
	if errors.Is(err, context.Canceled) {
		return failure(ports.FailureCancelled, "source refresh was cancelled")
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return &ports.Failure{Code: ports.FailureTimeout, Message: "source refresh reached its time bound", Retryable: true}
	}
	var source *ports.Failure
	if !errors.As(err, &source) {
		return &ports.Failure{Code: ports.FailureUnavailable, Message: "source refresh is unavailable", Retryable: true}
	}
	result := &ports.Failure{Code: source.Code, Message: "source refresh: " + string(source.Code), Retryable: source.Retryable, Details: map[string]string{}}
	if seconds, err := strconv.ParseInt(source.Details["retry_after_seconds"], 10, 64); err == nil && seconds >= 0 && seconds <= 365*24*60*60 {
		result.Details["retry_after_seconds"] = strconv.FormatInt(seconds, 10)
	}
	if retry, err := time.Parse(time.RFC3339, source.Details["retry_at_utc"]); err == nil {
		result.Details["retry_at_utc"] = retry.UTC().Format(time.RFC3339)
	}
	return result
}
