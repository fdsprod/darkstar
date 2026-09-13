package api

import (
	"bytes"
	"encoding/json"
	"time"

	"darkstar/src/core/backlog"
	"darkstar/src/ports"
	"darkstar/src/ports/statestore"
	"darkstar/src/ports/tracker"
)

type BacklogNamespace struct {
	Provider string `json:"provider"`
	Host     string `json:"host"`
	TenantID string `json:"tenantId"`
	ScopeID  string `json:"scopeId"`
}

type BacklogScope struct {
	Namespace   BacklogNamespace `json:"namespace"`
	ContainerID string           `json:"containerId"`
}

type BacklogTicketRef struct {
	Namespace BacklogNamespace `json:"namespace"`
	ID        string           `json:"id"`
}

// Source variants are validated before conversion into the sealed application
// union. Native requests may omit namespace; the canonical project namespace is
// then resolved by the API without consulting repository membership.
type BacklogSource struct {
	Kind               string            `json:"kind"`
	Namespace          *BacklogNamespace `json:"namespace,omitempty"`
	ConnectionID       string            `json:"connectionId,omitempty"`
	ConnectionRevision string            `json:"connectionRevision,omitempty"`
	Scope              *BacklogScope     `json:"scope,omitempty"`
}

type BacklogBinding struct {
	ProjectID  string        `json:"projectId"`
	Revision   uint64        `json:"revision"`
	Source     BacklogSource `json:"source"`
	SelectedAt string        `json:"selectedAt"`
}

type BacklogSourceResponse struct {
	SchemaVersion int              `json:"schemaVersion"`
	Binding       BacklogBinding   `json:"binding"`
	History       []BacklogBinding `json:"history"`
}

type BacklogSourceRequest struct {
	SchemaVersion    int           `json:"schemaVersion"`
	ExpectedRevision uint64        `json:"expectedRevision"`
	Source           BacklogSource `json:"source"`
}

type BacklogPredicate struct {
	FieldID  string                 `json:"fieldId"`
	Operator tracker.FilterOperator `json:"operator"`
	Values   []string               `json:"values"`
}

type BacklogQuery struct {
	Text       string             `json:"text"`
	Predicates []BacklogPredicate `json:"predicates"`
	PageSize   int                `json:"pageSize"`
}

type BacklogRefreshRequest struct {
	SchemaVersion           int          `json:"schemaVersion"`
	ExpectedBindingRevision uint64       `json:"expectedBindingRevision"`
	Query                   BacklogQuery `json:"query"`
}

type BacklogTicketRefreshRequest struct {
	SchemaVersion           int              `json:"schemaVersion"`
	ExpectedBindingRevision uint64           `json:"expectedBindingRevision"`
	Ref                     BacklogTicketRef `json:"ref"`
}

type BacklogFailure struct {
	Code      ports.FailureCode `json:"code"`
	Message   string            `json:"message"`
	Retryable bool              `json:"retryable"`
	Details   map[string]string `json:"details"`
}

type BacklogRefresh struct {
	BindingRevision uint64                         `json:"bindingRevision"`
	Phase           statestore.BacklogRefreshPhase `json:"phase"`
	Generation      uint64                         `json:"generation"`
	Query           BacklogQuery                   `json:"query"`
	StartedAt       string                         `json:"startedAt"`
	UpdatedAt       string                         `json:"updatedAt"`
	LastSuccessAt   string                         `json:"lastSuccessAt"`
	NextAttemptAt   string                         `json:"nextAttemptAt"`
	Failures        uint32                         `json:"failures"`
	Error           *BacklogFailure                `json:"error,omitempty"`
}

type BacklogRefreshResponse struct {
	SchemaVersion int            `json:"schemaVersion"`
	Refresh       BacklogRefresh `json:"refresh"`
}

type BacklogNamedID struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type BacklogKnowledge[T any] struct {
	State  string `json:"state"`
	Value  *T     `json:"value,omitempty"`
	Reason string `json:"reason,omitempty"`
}

type BacklogTicket struct {
	Ref               BacklogTicketRef                   `json:"ref"`
	TicketKey         string                             `json:"ticketKey"`
	ObservationID     string                             `json:"observationId"`
	BindingRevision   uint64                             `json:"bindingRevision"`
	CurrentSource     bool                               `json:"currentSource"`
	CurrentQueryMatch bool                               `json:"currentQueryMatch"`
	Revision          string                             `json:"revision"`
	Key               string                             `json:"key"`
	URL               string                             `json:"url"`
	Title             string                             `json:"title"`
	Description       string                             `json:"description"`
	Status            string                             `json:"status"`
	Reason            string                             `json:"reason"`
	ObservedAt        string                             `json:"observedAt"`
	CheckedAt         string                             `json:"checkedAt"`
	EvidenceRef       string                             `json:"evidenceRef"`
	BusinessState     BacklogKnowledge[BacklogNamedID]   `json:"businessState"`
	Priority          BacklogKnowledge[BacklogNamedID]   `json:"priority"`
	Assignees         BacklogKnowledge[[]BacklogNamedID] `json:"assignees"`
	Labels            BacklogKnowledge[[]BacklogNamedID] `json:"labels"`
	Archived          BacklogKnowledge[bool]             `json:"archived"`
	Freshness         string                             `json:"freshness"`
}

type BacklogView struct {
	SchemaVersion   int             `json:"schemaVersion"`
	Binding         BacklogBinding  `json:"binding"`
	Query           BacklogQuery    `json:"query"`
	Refresh         *BacklogRefresh `json:"refresh"`
	Tickets         []BacklogTicket `json:"tickets"`
	NextCursor      string          `json:"nextCursor"`
	IncludePrevious bool            `json:"includePrevious"`
}

func backlogNamespace(value tracker.Namespace) BacklogNamespace {
	return BacklogNamespace{Provider: value.Provider, Host: value.Host, TenantID: value.TenantID, ScopeID: value.ScopeID}
}

func (value BacklogNamespace) native() tracker.Namespace {
	return tracker.Namespace{Provider: value.Provider, Host: value.Host, TenantID: value.TenantID, ScopeID: value.ScopeID}
}

func backlogBinding(value statestore.BacklogBinding) BacklogBinding {
	result := BacklogBinding{ProjectID: value.ProjectID, Revision: value.Revision, SelectedAt: backlogTime(value.SelectedAt)}
	switch source := value.Source.(type) {
	case statestore.NativeBacklogSource:
		namespace := backlogNamespace(source.Namespace)
		result.Source = BacklogSource{Kind: "built_in", Namespace: &namespace}
	case statestore.ExternalBacklogSource:
		result.Source = BacklogSource{Kind: "external", ConnectionID: source.ConnectionID, ConnectionRevision: source.ConnectionRevision, Scope: &BacklogScope{Namespace: backlogNamespace(source.Scope.Namespace), ContainerID: source.Scope.ContainerID}}
	}
	return result
}

func (value BacklogSource) native(projectID string) (statestore.BacklogSource, error) {
	switch value.Kind {
	case "built_in":
		namespace := tracker.Namespace{Provider: "built_in", Host: "darkstar.local", TenantID: "local", ScopeID: projectID}
		if value.ConnectionID != "" || value.ConnectionRevision != "" || value.Scope != nil || value.Namespace != nil && value.Namespace.native() != namespace {
			return nil, backlogInvalid("Built-in source must use this project's native namespace.")
		}
		return statestore.NativeBacklogSource{Namespace: namespace}, nil
	case "external":
		if value.Namespace != nil || value.ConnectionID == "" || value.ConnectionRevision == "" || value.Scope == nil {
			return nil, backlogInvalid("External source requires exact connection revision and scope.")
		}
		return statestore.ExternalBacklogSource{ConnectionID: value.ConnectionID, ConnectionRevision: value.ConnectionRevision, Scope: tracker.Scope{Namespace: value.Scope.Namespace.native(), ContainerID: value.Scope.ContainerID}}, nil
	default:
		return nil, backlogInvalid("Source kind must be built_in or external.")
	}
}

func backlogQuery(value tracker.Query) BacklogQuery {
	result := BacklogQuery{Text: value.Text, Predicates: make([]BacklogPredicate, 0), PageSize: value.PageSize}
	for _, predicate := range value.Predicates {
		result.Predicates = append(result.Predicates, BacklogPredicate{FieldID: predicate.FieldID, Operator: predicate.Operator, Values: predicate.Values})
	}
	return result
}

func (value BacklogQuery) native() tracker.Query {
	query := tracker.Query{Text: value.Text, PageSize: value.PageSize, Predicates: make([]tracker.Predicate, 0)}
	for _, predicate := range value.Predicates {
		query.Predicates = append(query.Predicates, tracker.Predicate{FieldID: predicate.FieldID, Operator: predicate.Operator, Values: predicate.Values})
	}
	return query
}

func backlogRefresh(value statestore.BacklogRefreshState) BacklogRefresh {
	result := BacklogRefresh{BindingRevision: value.BindingRevision, Phase: value.Phase, Generation: value.Generation, Query: backlogQuery(value.Query), StartedAt: backlogTime(value.StartedAt), UpdatedAt: backlogTime(value.UpdatedAt), LastSuccessAt: backlogTime(value.LastSuccessAt), NextAttemptAt: backlogTime(value.NextAttemptAt), Failures: value.Failures}
	if value.Failure != nil {
		details := value.Failure.Details
		if details == nil {
			details = make(map[string]string)
		}
		result.Error = &BacklogFailure{Code: value.Failure.Code, Message: value.Failure.Message, Retryable: value.Failure.Retryable, Details: details}
	}
	return result
}

func backlogTime(value time.Time) string {
	if value.IsZero() {
		return ""
	}
	return value.UTC().Format(time.RFC3339Nano)
}

func backlogInvalid(message string) error {
	return &ports.Failure{Code: ports.FailureInvalidRequest, Message: message}
}

func backlogView(value backlog.View, includePrevious bool) BacklogView {
	result := BacklogView{SchemaVersion: 1, Binding: backlogBinding(value.Binding), Query: BacklogQuery{PageSize: 50, Predicates: []BacklogPredicate{}}, Tickets: make([]BacklogTicket, 0), NextCursor: value.NextCursor, IncludePrevious: includePrevious}
	if value.Refresh != nil {
		state := backlogRefresh(*value.Refresh)
		result.Refresh = &state
		result.Query = state.Query
	}
	for _, entry := range value.Tickets {
		ticket := entry.Ticket
		projection := BacklogTicket{Ref: BacklogTicketRef{Namespace: backlogNamespace(ticket.Ref.Namespace), ID: ticket.Ref.ID}, TicketKey: entry.Key, ObservationID: entry.ObservationID, BindingRevision: entry.BindingRevision, CurrentSource: entry.CurrentSource, CurrentQueryMatch: entry.CurrentQueryMatch, Revision: ticket.Revision, Key: ticket.Key, URL: ticket.URL, Title: ticket.Title, Description: ticket.Description, Status: string(entry.Status), Reason: entry.Reason, CheckedAt: backlogTime(entry.CheckedAt), EvidenceRef: ticket.EvidenceRef, BusinessState: mapBacklogKnowledge(ticket.BusinessState, backlogNamed), Priority: mapBacklogKnowledge(ticket.Priority, backlogNamed), Assignees: mapBacklogKnowledge(ticket.Assignees, backlogNames), Labels: mapBacklogKnowledge(ticket.Labels, backlogNames), Archived: mapBacklogKnowledge(ticket.Archived, func(value bool) bool {
			return value
		})}
		switch freshness := ticket.Freshness.(type) {
		case tracker.Fresh:
			projection.Freshness = "fresh"
			projection.ObservedAt = backlogTime(freshness.ObservedAt)
		case tracker.Stale:
			projection.Freshness = "stale"
			projection.ObservedAt = backlogTime(freshness.LastObservedAt)
		case tracker.NeverObserved:
			projection.Freshness = "never_observed"
		}
		result.Tickets = append(result.Tickets, projection)
	}
	return result
}

func mapBacklogKnowledge[T, U any](source tracker.Knowledge[T], convert func(T) U) BacklogKnowledge[U] {
	switch value := source.(type) {
	case tracker.Known[T]:
		converted := convert(value.Value)
		return BacklogKnowledge[U]{State: "known", Value: &converted}
	case tracker.Unsupported[T]:
		return BacklogKnowledge[U]{State: "unsupported", Reason: value.Reason}
	case tracker.Unknown[T]:
		return BacklogKnowledge[U]{State: "unknown", Reason: value.Reason}
	default:
		return BacklogKnowledge[U]{State: "unknown", Reason: "No observation was retained."}
	}
}

func backlogNamed(value tracker.NamedID) BacklogNamedID {
	return BacklogNamedID{ID: value.ID, Name: value.Name}
}

func backlogNames(values []tracker.NamedID) []BacklogNamedID {
	result := make([]BacklogNamedID, 0, len(values))
	for _, value := range values {
		result = append(result, backlogNamed(value))
	}
	return result
}

// Keep request decoding strict even if this DTO is used outside the HTTP helper.
func (value *BacklogSource) UnmarshalJSON(raw []byte) error {
	type sourceWire BacklogSource
	var source sourceWire
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&source); err != nil {
		return err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return err
	}
	for key := range fields {
		if key != "kind" && key != "namespace" && key != "connectionId" && key != "connectionRevision" && key != "scope" {
			return backlogInvalid("Unsupported source field.")
		}
	}
	*value = BacklogSource(source)
	return nil
}
