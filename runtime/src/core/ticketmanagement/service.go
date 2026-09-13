// Package ticketmanagement exposes native business tickets independently of execution.
package ticketmanagement

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"darkstar/src/core/identity"
	"darkstar/src/core/trackercontract"
	"darkstar/src/ports"
	"darkstar/src/ports/statestore"
	"darkstar/src/ports/ticketwriter"
	"darkstar/src/ports/tracker"
	"darkstar/src/ports/worksource"
)

// Binding contains only ticket ports. It cannot schedule or prepare execution.
type Binding struct {
	Source  worksource.TrackerSourceV1
	Browser worksource.TrackerBrowserV1
	Writer  ticketwriter.WriterV1
	Config  tracker.AdapterConfigPin
}

type Resolver func(context.Context, string) (Binding, error)

type Service struct {
	store   statestore.Store
	history statestore.NativeTrackerStore
	resolve Resolver
}

func New(store statestore.Store, history statestore.NativeTrackerStore, resolve Resolver) (*Service, error) {
	if store == nil || history == nil || resolve == nil {
		return nil, errors.New("native tickets require state, history, and a tracker binding")
	}
	return &Service{store: store, history: history, resolve: resolve}, nil
}

func (s *Service) binding(ctx context.Context, projectID string) (Binding, tracker.Manifest, error) {
	if _, err := s.store.Project(ctx, projectID); err != nil {
		return Binding{}, tracker.Manifest{}, err
	}
	binding, err := s.resolve(ctx, projectID)
	if err != nil {
		return Binding{}, tracker.Manifest{}, err
	}
	if binding.Source == nil || binding.Browser == nil || binding.Writer == nil {
		return Binding{}, tracker.Manifest{}, failure(ports.FailureUnavailable, "native tracker ports are unavailable")
	}
	manifest, err := binding.Source.Discover(ctx, binding.Config)
	if err != nil {
		return Binding{}, tracker.Manifest{}, err
	}
	if manifest.Scope.Namespace.Provider != "built_in" || manifest.Scope.Namespace.ScopeID != projectID {
		return Binding{}, tracker.Manifest{}, failure(ports.FailureConflict, "native ticket binding does not match project")
	}
	return binding, manifest, nil
}

func (s *Service) Browse(ctx context.Context, projectID string, query tracker.Query) (Page, error) {
	binding, manifest, err := s.binding(ctx, projectID)
	if err != nil {
		return Page{}, err
	}
	if query.PageSize == 0 {
		query.PageSize = 50
	}
	if err := trackercontract.ValidateQuery(manifest.Pin, manifest, query); err != nil {
		return Page{}, err
	}
	page, err := binding.Browser.Browse(ctx, worksource.BrowseTicketsRequest{Pin: manifest.Pin, Scope: manifest.Scope, Query: query})
	if err != nil {
		return Page{}, err
	}
	result := Page{SchemaVersion: 1, Tickets: make([]Ticket, 0, len(page.Tickets))}
	for _, value := range page.Tickets {
		item, err := projectTicket(projectID, value)
		if err != nil {
			return Page{}, err
		}
		result.Tickets = append(result.Tickets, item)
	}
	switch next := page.Next.(type) {
	case tracker.End:
	case tracker.More:
		result.NextCursor = next.Cursor
	default:
		return Page{}, failure(ports.FailureProtocolDrift, "unknown native page continuation")
	}
	return result, nil
}

func (s *Service) Detail(ctx context.Context, projectID, ticketID string) (Detail, error) {
	binding, manifest, err := s.binding(ctx, projectID)
	if err != nil {
		return Detail{}, err
	}
	ref := tracker.TicketRef{Namespace: manifest.Scope.Namespace, ID: ticketID}
	read, err := binding.Source.Read(ctx, worksource.ReadTicketRequest{Pin: manifest.Pin, Ref: ref})
	if err != nil {
		return Detail{}, err
	}
	found, ok := read.(tracker.Found)
	if !ok {
		if _, missing := read.(tracker.Missing); missing {
			return Detail{}, statestore.ErrNotFound
		}
		return Detail{}, failure(ports.FailureProtocolDrift, "native ticket read returned no snapshot")
	}
	ticket, err := projectTicket(projectID, found.Ticket)
	if err != nil {
		return Detail{}, err
	}
	options, err := binding.Writer.Inspect(ctx, ticketwriter.InspectRequest{Pin: manifest.Pin, Destination: manifest.Scope, Scope: tracker.TicketScope{Ref: ref}})
	if err != nil {
		return Detail{}, err
	}
	target, ok := options.Scope.(tracker.TicketScope)
	if !ok || target.Ref != ref || target.Revision != ticket.Revision || options.Pin != manifest.Pin {
		return Detail{}, failure(ports.FailureConflict, "ticket changed while loading capabilities; reload the detail")
	}
	result := Detail{SchemaVersion: 1, Ticket: ticket, Fields: []Field{}, Transitions: []Transition{}, History: []History{}}
	result.Capabilities.Edit = trackercontract.Require(manifest, tracker.Edit) == nil
	result.Capabilities.Transitions = trackercontract.Require(manifest, tracker.Transitions) == nil
	if fields, ok := options.Fields.(tracker.Known[[]tracker.Field]); ok {
		for _, value := range fields.Value {
			field := Field{ID: value.Identity.ID, Name: value.Identity.Name, Required: value.Required}
			switch value.Constraint.(type) {
			case tracker.TextConstraint:
				field.Kind = "text"
			case tracker.NumberConstraint:
				field.Kind = "number"
			case tracker.IDsConstraint:
				field.Kind = "ids"
			default:
				return Detail{}, failure(ports.FailureProtocolDrift, "unknown native field constraint")
			}
			result.Fields = append(result.Fields, field)
		}
	} else {
		result.Capabilities.Edit = false
	}
	if transitions, ok := options.Transitions.(tracker.Known[[]tracker.Transition]); ok {
		for _, value := range transitions.Value {
			result.Transitions = append(result.Transitions, Transition{ID: value.Identity.ID, Name: value.Identity.Name, ToState: named(value.ToState)})
		}
	} else {
		result.Capabilities.Transitions = false
	}
	history, err := s.history.NativeTicketHistory(ctx, projectID, ticketID)
	if err != nil {
		return Detail{}, err
	}
	for _, value := range history {
		result.History = append(result.History, History{Revision: strconv.FormatUint(value.Revision, 10), Kind: value.Kind, RecordedAt: value.RecordedAt, EvidenceRef: value.EvidenceRef, Title: value.Snapshot.Title, Description: value.Snapshot.Description, BusinessState: string(value.Snapshot.State), Priority: value.Snapshot.Priority})
	}
	return result, nil
}

func failure(code ports.FailureCode, message string) error {
	return &ports.Failure{Code: code, Message: message}
}

// preparedTarget is a versioned, durable native target snapshot, saved before any
// mutation. Replays reconstruct the original intent rather than adopting new state.
type preparedTarget struct {
	SchemaVersion     int               `json:"schemaVersion"`
	Pin               tracker.Pin       `json:"pin"`
	Destination       tracker.Scope     `json:"destination"`
	Ref               tracker.TicketRef `json:"ref"`
	Revision          string            `json:"revision"`
	WorkflowRevision  string            `json:"workflowRevision"`
	IssueTypeID       string            `json:"issueTypeId"`
	StateID           string            `json:"stateId"`
	SprintUnsupported string            `json:"sprintUnsupported"`
}

func (s *Service) Edit(ctx context.Context, projectID, ticketID string, request EditRequest, key string) (Detail, error) {
	if request.SchemaVersion != 1 {
		return Detail{}, failure(ports.FailureInvalidRequest, "schemaVersion must be 1")
	}
	fields := map[string]tracker.FieldValue{}
	if request.Title != nil {
		fields["title"] = tracker.TextValue(*request.Title)
	}
	if request.Description != nil {
		fields["description"] = tracker.TextValue(*request.Description)
	}
	if request.Priority != nil {
		fields["priority"] = tracker.NumberValue(*request.Priority)
	}
	if request.Assignees != nil {
		fields["assignees"] = tracker.IDsValue(*request.Assignees)
	}
	if request.Labels != nil {
		fields["labels"] = tracker.IDsValue(*request.Labels)
	}
	if len(fields) == 0 {
		return Detail{}, failure(ports.FailureInvalidRequest, "at least one ticket field is required")
	}
	return s.mutate(ctx, projectID, ticketID, request.Revision, "edit", request, key, func(target tracker.TicketScope) tracker.Effect {
		return tracker.EditTicket{Target: target, Fields: fields}
	})
}

func (s *Service) Transition(ctx context.Context, projectID, ticketID string, request TransitionRequest, key string) (Detail, error) {
	if request.SchemaVersion != 1 || request.TransitionID == "" {
		return Detail{}, failure(ports.FailureInvalidRequest, "schemaVersion 1 and transitionId are required")
	}
	return s.mutate(ctx, projectID, ticketID, request.Revision, "transition", request, key, func(target tracker.TicketScope) tracker.Effect {
		return tracker.TakeTransition{Target: target, TransitionID: request.TransitionID, Fields: map[string]tracker.FieldValue{}}
	})
}

func (s *Service) mutate(ctx context.Context, projectID, ticketID, revision, kind string, request any, key string, effect func(tracker.TicketScope) tracker.Effect) (Detail, error) {
	if revision == "" || strings.TrimSpace(key) != key || len(key) < 8 || len(key) > 128 {
		return Detail{}, failure(ports.FailureInvalidRequest, "ticket revision and an 8-128 byte idempotency key are required")
	}
	binding, manifest, err := s.binding(ctx, projectID)
	if err != nil {
		return Detail{}, err
	}
	encoded, err := json.Marshal(request)
	if err != nil {
		return Detail{}, err
	}
	digest := fmt.Sprintf("%x", sha256.Sum256(encoded))
	scope := "native-tickets/v1/" + projectID + "/" + ticketID + "/" + kind
	command, _, err := s.store.BeginCommand(ctx, statestore.BeginCommandRequest{Scope: scope, IdempotencyKey: key, RequestDigest: digest, CreatedAt: time.Now().UTC()})
	if err != nil {
		return Detail{}, err
	}
	if command.Status == "completed" {
		var detail Detail
		err := json.Unmarshal(command.Response, &detail)
		return detail, err
	}
	prepared, _, err := s.store.BeginCommand(ctx, statestore.BeginCommandRequest{Scope: scope + "/authorized-target", IdempotencyKey: key, RequestDigest: digest, CreatedAt: time.Now().UTC()})
	if err != nil {
		return Detail{}, err
	}
	var plan preparedTarget
	if prepared.Status == "completed" {
		if err := json.Unmarshal(prepared.Response, &plan); err != nil {
			return Detail{}, err
		}
	} else {
		ref := tracker.TicketRef{Namespace: manifest.Scope.Namespace, ID: ticketID}
		options, err := binding.Writer.Inspect(ctx, ticketwriter.InspectRequest{Pin: manifest.Pin, Destination: manifest.Scope, Scope: tracker.TicketScope{Ref: ref}})
		if err != nil {
			return Detail{}, err
		}
		target, ok := options.Scope.(tracker.TicketScope)
		if !ok || target.Revision != revision {
			return Detail{}, failure(ports.FailureConflict, "ticket changed; reload before editing")
		}
		sprint, ok := target.Sprint.(tracker.Unsupported[[]tracker.NamedID])
		if !ok {
			return Detail{}, failure(ports.FailureProtocolDrift, "native target has an unexpected sprint model")
		}
		plan = preparedTarget{SchemaVersion: 1, Pin: manifest.Pin, Destination: manifest.Scope, Ref: target.Ref, Revision: target.Revision, WorkflowRevision: target.WorkflowRevision, IssueTypeID: target.IssueTypeID, StateID: target.StateID, SprintUnsupported: sprint.Reason}
		// The authenticated local command is the explicit human authorization.
		// Validate its exact desired effect before retaining its dispatch snapshot.
		intent := makeIntent(scope, key, digest, plan, effect(target))
		if err := trackercontract.ValidateIntent(intent, manifest, options, time.Now().UTC()); err != nil {
			return Detail{}, err
		}
		data, err := json.Marshal(plan)
		if err != nil {
			return Detail{}, err
		}
		if _, err := s.store.CompleteCommand(ctx, statestore.CompleteCommandRequest{Scope: scope + "/authorized-target", IdempotencyKey: key, ResponseStatus: 200, Response: data, CompletedAt: time.Now().UTC()}); err != nil {
			return Detail{}, err
		}
	}
	if plan.SchemaVersion != 1 || plan.Pin != manifest.Pin || plan.Destination != manifest.Scope {
		return Detail{}, failure(ports.FailureConflict, "native tracker binding changed since authorization")
	}
	target := tracker.TicketScope{Ref: plan.Ref, Revision: plan.Revision, WorkflowRevision: plan.WorkflowRevision, IssueTypeID: plan.IssueTypeID, StateID: plan.StateID, Sprint: tracker.Unsupported[[]tracker.NamedID]{Reason: plan.SprintUnsupported}}
	intent := makeIntent(scope, key, digest, plan, effect(target))
	result, err := binding.Writer.Reconcile(ctx, intent)
	if err != nil {
		return Detail{}, err
	}
	if _, absent := result.(tracker.NotApplied); absent {
		if !trackercontract.RetryAfterReconciliation(intent, result) {
			return Detail{}, failure(ports.FailureConflict, "reconciliation did not prove absence for this exact operation")
		}
		options, err := binding.Writer.Inspect(ctx, ticketwriter.InspectRequest{Pin: intent.Pin, Destination: intent.Destination, Scope: target})
		if err != nil {
			return Detail{}, err
		}
		if err := trackercontract.ValidateIntent(intent, manifest, options, time.Now().UTC()); err != nil {
			return Detail{}, err
		}
		result, err = binding.Writer.Apply(ctx, intent)
		if err != nil {
			return Detail{}, err
		}
	}
	applied, ok := result.(tracker.Applied)
	if !ok {
		return Detail{}, failure(ports.FailureUncertain, "ticket operation is awaiting reconciliation; retry the same request key")
	}
	if err := trackercontract.ValidateReceipt(intent, applied.Receipt); err != nil {
		return Detail{}, err
	}
	detail, err := s.Detail(ctx, projectID, ticketID)
	if err != nil {
		return Detail{}, err
	}
	data, err := json.Marshal(detail)
	if err != nil {
		return Detail{}, err
	}
	_, err = s.store.CompleteCommand(ctx, statestore.CompleteCommandRequest{Scope: scope, IdempotencyKey: key, ResponseStatus: 200, Response: data, CompletedAt: time.Now().UTC()})
	return detail, err
}

func makeIntent(scope, key, digest string, plan preparedTarget, effect tracker.Effect) tracker.Intent {
	return tracker.Intent{OperationID: identity.Deterministic("ticketop_", scope+"\x00"+key), DesiredDigest: digest, AuthorizationRef: "command:" + scope + "/authorized-target/" + key, Pin: plan.Pin, Destination: plan.Destination, Effect: effect}
}
