// Package backlog owns bounded read-side source refresh and durable business
// observations. Loading a board never creates work, starts runs or writes tickets.
package backlog

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"darkstar/src/core/trackercontract"
	"darkstar/src/ports"
	"darkstar/src/ports/statestore"
	"darkstar/src/ports/tracker"
	"darkstar/src/ports/worksource"
)

type ResolvedSource struct {
	Source  worksource.TrackerSourceV1
	Browser worksource.TrackerBrowserV1
	Config  tracker.AdapterConfigPin
}

type Resolver interface {
	Resolve(context.Context, statestore.BacklogBinding) (ResolvedSource, error)
}

type Options struct {
	Now                               func() time.Time
	MaxPages, MaxTickets              int
	Timeout, PollInterval, MaxBackoff time.Duration
}

type Service struct {
	store    statestore.BacklogStore
	resolver Resolver
	options  Options
}

type EntryStatus string

const (
	Cached       EntryStatus = "cached"
	Fresh        EntryStatus = "fresh"
	Incomplete   EntryStatus = "incomplete"
	Missing      EntryStatus = "missing"
	Inaccessible EntryStatus = "inaccessible"
	Archived     EntryStatus = "archived"
	OutOfScope   EntryStatus = "out_of_scope"
)

type Entry struct {
	Key                              string
	BindingRevision                  uint64
	Ticket                           tracker.Ticket
	ObservationID                    string
	Status                           EntryStatus
	Reason                           string
	CheckedAt                        time.Time
	CurrentSource, CurrentQueryMatch bool
}

type ViewRequest struct {
	Limit           int
	Cursor          string
	IncludePrevious bool
}

type View struct {
	Binding    statestore.BacklogBinding
	Refresh    *statestore.BacklogRefreshState
	Tickets    []Entry
	NextCursor string
}

type RefreshResult struct {
	Binding   statestore.BacklogBinding
	Refresh   statestore.BacklogRefreshState
	Processed int
	Complete  bool
}

func New(store statestore.BacklogStore, resolver Resolver, options Options) (*Service, error) {
	if store == nil || resolver == nil {
		return nil, failure(ports.FailureInvalidRequest, "backlog requires storage and a source resolver")
	}
	if options.Now == nil {
		options.Now = time.Now
	}
	if options.MaxPages == 0 {
		options.MaxPages = 4
	}
	if options.MaxTickets == 0 {
		options.MaxTickets = 200
	}
	if options.Timeout == 0 {
		options.Timeout = 15 * time.Second
	}
	if options.PollInterval == 0 {
		options.PollInterval = time.Minute
	}
	if options.MaxBackoff == 0 {
		options.MaxBackoff = 15 * time.Minute
	}
	if options.MaxPages < 1 || options.MaxPages > 20 || options.MaxTickets < 1 || options.MaxTickets > 1000 || options.Timeout < time.Millisecond || options.Timeout > time.Minute || options.PollInterval < time.Second || options.MaxBackoff < time.Second {
		return nil, failure(ports.FailureInvalidRequest, "backlog refresh bounds are invalid")
	}
	return &Service{store: store, resolver: resolver, options: options}, nil
}

func (s *Service) Binding(ctx context.Context, project string) (statestore.BacklogBinding, error) {
	return s.store.BacklogBinding(ctx, project)
}

func (s *Service) History(ctx context.Context, project string) ([]statestore.BacklogBinding, error) {
	return s.store.BacklogBindingHistory(ctx, project)
}

func (s *Service) SelectSource(ctx context.Context, project string, expected uint64, source statestore.BacklogSource) (statestore.BacklogBinding, error) {
	// Selection records user intent; connection failures remain visible on refresh
	// and never cause fallback or discard observations from the previous source.
	return s.store.SelectBacklogSource(ctx, project, expected, source, s.options.Now().UTC())
}

func Scope(binding statestore.BacklogBinding) tracker.Scope {
	switch source := binding.Source.(type) {
	case statestore.NativeBacklogSource:
		return tracker.Scope{Namespace: source.Namespace, ContainerID: source.Namespace.ScopeID}
	case statestore.ExternalBacklogSource:
		return source.Scope
	default:
		return tracker.Scope{}
	}
}

type cacheCursor struct {
	RequestDigest, After string
}

func (s *Service) View(ctx context.Context, project string, request ViewRequest) (View, error) {
	binding, err := s.store.BacklogBinding(ctx, project)
	if err != nil {
		return View{}, err
	}
	if request.Limit == 0 {
		request.Limit = 100
	}
	if request.Limit < 1 || request.Limit > 1000 {
		return View{}, failure(ports.FailureInvalidRequest, "backlog page size is outside limits")
	}
	digest := hash(struct {
		Project  string
		Binding  uint64
		Previous bool
		Limit    int
	}{project, binding.Revision, request.IncludePrevious, request.Limit})
	after := ""
	if request.Cursor != "" {
		encoded, err := base64.RawURLEncoding.DecodeString(request.Cursor)
		var cursor cacheCursor
		if err != nil || json.Unmarshal(encoded, &cursor) != nil || cursor.RequestDigest != digest || cursor.After == "" {
			return View{}, failure(ports.FailureInvalidRequest, "backlog cursor differs from the selected source and request")
		}
		after = cursor.After
	}
	result := View{Binding: binding, Tickets: []Entry{}}
	state, err := s.store.BacklogRefresh(ctx, project, binding.Revision)
	if err == nil {
		result.Refresh = &state
	} else if !notFound(err) {
		return View{}, err
	}
	values, err := s.store.BacklogTickets(ctx, project, binding.Revision, request.IncludePrevious, after, request.Limit+1)
	if err != nil {
		return View{}, err
	}
	if len(values) > request.Limit {
		values = values[:request.Limit]
		encoded, _ := json.Marshal(cacheCursor{RequestDigest: digest, After: cacheKey(values[len(values)-1])})
		result.NextCursor = base64.RawURLEncoding.EncodeToString(encoded)
	}
	for _, value := range values {
		ticket, err := trackercontract.DecodeTicket(value.Observation.Ticket)
		if err != nil {
			return View{}, err
		}
		entry := Entry{Key: value.TicketKey, BindingRevision: value.BindingRevision, Ticket: ticket, ObservationID: value.ObservationID, CheckedAt: value.CheckedAt, Status: Cached, Reason: value.Reason, CurrentSource: value.BindingRevision == binding.Revision, CurrentQueryMatch: value.BindingRevision == binding.Revision && result.Refresh != nil && value.SeenGeneration == state.Generation}
		switch {
		case !entry.CurrentSource:
			entry.Status, entry.Reason = OutOfScope, "retained from a previous source selection"
		case value.State == statestore.BacklogMissing:
			entry.Status = Missing
		case value.State == statestore.BacklogInaccessible || permissionFailure(state.Failure):
			entry.Status, entry.Reason = Inaccessible, "source access is unavailable; last successful observation retained"
		case !placementMatches(ticket, Scope(binding)):
			entry.Status, entry.Reason = OutOfScope, "observed ticket placement differs from the selected source scope"
		case archived(ticket):
			entry.Status = Archived
		case state.Phase == statestore.BacklogRefreshing && entry.CurrentQueryMatch:
			entry.Status, entry.Reason = Incomplete, "this page is observed; the source scan is still incomplete"
		case state.Phase == statestore.BacklogComplete && entry.CurrentQueryMatch && s.options.Now().Sub(value.CheckedAt) <= 2*s.options.PollInterval:
			entry.Status = Fresh
		}
		if _, stale := ticket.Freshness.(tracker.Fresh); !stale && entry.Status == Fresh {
			entry.Status, entry.Reason = Incomplete, "provider returned an incomplete freshness observation"
		}
		result.Tickets = append(result.Tickets, entry)
	}
	return result, nil
}

func (s *Service) Poll(ctx context.Context, project string) (RefreshResult, error) {
	binding, err := s.Binding(ctx, project)
	if err != nil {
		return RefreshResult{}, err
	}
	state, err := s.store.BacklogRefresh(ctx, project, binding.Revision)
	if err != nil && !notFound(err) {
		return RefreshResult{}, err
	}
	if err == nil && s.options.Now().Before(state.NextAttemptAt) {
		return refreshResult(binding, state, 0), nil
	}
	query := state.Query
	if query.PageSize == 0 {
		query.PageSize = min(50, s.options.MaxTickets)
	}
	return s.Refresh(ctx, project, binding.Revision, query)
}

func hash(value any) string {
	encoded, _ := json.Marshal(value)
	return fmt.Sprintf("%x", sha256.Sum256(encoded))
}

func TicketKey(ref tracker.TicketRef) string {
	return hash(ref)
}

func cacheKey(value statestore.BacklogCachedTicket) string {
	return fmt.Sprintf("%020d:%s", value.BindingRevision, value.TicketKey)
}

func failure(code ports.FailureCode, message string) *ports.Failure {
	return &ports.Failure{Code: code, Message: message}
}

func notFound(err error) bool {
	var value *ports.Failure
	return errors.Is(err, statestore.ErrNotFound) || errors.As(err, &value) && value.Code == ports.FailureNotFound
}

func permissionFailure(err *ports.Failure) bool {
	return err != nil && (err.Code == ports.FailureUnauthenticated || err.Code == ports.FailurePermissionDenied)
}

func archived(ticket tracker.Ticket) bool {
	value, ok := ticket.Archived.(tracker.Known[bool])
	return ok && value.Value
}

func placementMatches(ticket tracker.Ticket, scope tracker.Scope) bool {
	value, ok := ticket.Placement.(tracker.Known[tracker.Scope])
	return !ok || value.Value == scope
}

func refreshResult(binding statestore.BacklogBinding, state statestore.BacklogRefreshState, processed int) RefreshResult {
	return RefreshResult{Binding: binding, Refresh: state, Processed: processed, Complete: state.Phase == statestore.BacklogComplete}
}

func validRef(ref tracker.TicketRef, scope tracker.Scope) bool {
	return ref.Namespace == scope.Namespace && strings.TrimSpace(ref.ID) != ""
}
