package builtin

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"time"

	"darkstar/src/core/trackercontract"
	"darkstar/src/ports"
	"darkstar/src/ports/statestore"
	"darkstar/src/ports/ticketwriter"
	"darkstar/src/ports/tracker"
)

func (a *Adapter) Inspect(ctx context.Context, request ticketwriter.InspectRequest) (tracker.WriteOptions, error) {
	if err := trackercontract.ValidatePin(request.Pin, a.pin); err != nil {
		return tracker.WriteOptions{}, err
	}
	if request.Destination != a.scope {
		return tracker.WriteOptions{}, failure(ports.FailureInvalidRequest, "writer destination differs from native scope")
	}
	creation := false
	var scope tracker.OptionScope
	transitions := make([]tracker.Transition, 0)
	switch requested := request.Scope.(type) {
	case tracker.CreationScope:
		if requested.IssueTypeID != "ticket" {
			return tracker.WriteOptions{}, failure(ports.FailureUnsupported, "native issue type is unsupported")
		}
		creation = true
		scope = requested
	case tracker.TicketScope:
		if err := a.validate(request.Pin, requested.Ref); err != nil {
			return tracker.WriteOptions{}, err
		}
		value, err := a.store.NativeTicket(ctx, a.scope.ContainerID, requested.Ref.ID)
		if err != nil {
			return tracker.WriteOptions{}, normalize(err)
		}
		scope = tracker.TicketScope{Ref: requested.Ref, Revision: strconv.FormatUint(value.Revision, 10), WorkflowRevision: WorkflowRevision, IssueTypeID: "ticket", StateID: string(value.State), Sprint: tracker.Unsupported[[]tracker.NamedID]{Reason: sprintReason}}
		for _, state := range []statestore.NativeBusinessState{statestore.NativeOpen, statestore.NativeActive, statestore.NativeCompleted, statestore.NativeCancelled} {
			if state != value.State {
				transitions = append(transitions, tracker.Transition{Identity: tracker.NamedID{ID: "set-state:" + string(state), Name: "Set to " + stateName(state)}, ToState: tracker.NamedID{ID: string(state), Name: stateName(state)}})
			}
		}
	default:
		return tracker.WriteOptions{}, failure(ports.FailureInvalidRequest, "writer requires a creation or exact ticket scope")
	}
	now := a.now().UTC()
	fields := []tracker.Field{
		{Identity: tracker.NamedID{ID: "title", Name: "Title"}, Required: creation, Constraint: tracker.TextConstraint{}},
		{Identity: tracker.NamedID{ID: "description", Name: "Description"}, Constraint: tracker.TextConstraint{}},
		{Identity: tracker.NamedID{ID: "priority", Name: "Priority"}, Constraint: tracker.NumberConstraint{}},
		{Identity: tracker.NamedID{ID: "labels", Name: "Labels"}, Constraint: tracker.IDsConstraint{Choices: tracker.AnyID{}}},
		{Identity: tracker.NamedID{ID: "assignees", Name: "Assignees"}, Constraint: tracker.IDsConstraint{Choices: tracker.AnyID{}}},
	}
	return tracker.WriteOptions{Pin: a.pin, Scope: scope, Fields: tracker.Known[[]tracker.Field]{Value: fields}, Transitions: tracker.Known[[]tracker.Transition]{Value: transitions}, ObservedAt: now, ValidUntil: now.Add(5 * time.Minute), EvidenceRef: "native-options:" + digest(scope)}, nil
}

func operationScope(effect tracker.Effect) (tracker.OptionScope, string, error) {
	switch value := effect.(type) {
	case tracker.CreateTicket:
		return tracker.CreationScope{IssueTypeID: value.IssueTypeID}, "create", nil
	case tracker.EditTicket:
		return value.Target, "edit", nil
	case tracker.TakeTransition:
		return value.Target, "transition", nil
	case tracker.ReportProgress:
		return value.Target, "progress", nil
	case tracker.LinkStories:
		return value.Target, "relationship", nil
	default:
		return nil, "", failure(ports.FailureUnsupported, "native effect is unsupported")
	}
}

func operationFingerprint(intent tracker.Intent) (string, []byte, error) {
	_, kind, err := operationScope(intent.Effect)
	if err != nil {
		return "", nil, err
	}
	request := struct {
		Kind   string
		Intent tracker.Intent
	}{Kind: kind, Intent: intent}
	encoded, err := json.Marshal(request)
	if err != nil {
		return "", nil, failure(ports.FailureInvalidRequest, "native effect cannot be encoded")
	}
	return digest(request), encoded, nil
}

func (a *Adapter) Apply(ctx context.Context, intent tracker.Intent) (tracker.EffectResult, error) {
	if err := trackercontract.ValidatePin(intent.Pin, a.pin); err != nil {
		return nil, err
	}
	if intent.Destination != a.scope {
		return nil, failure(ports.FailureInvalidRequest, "native intent destination differs from binding")
	}
	fingerprint, requestJSON, err := operationFingerprint(intent)
	if err != nil {
		return nil, err
	}
	prior, err := a.store.NativeOperation(ctx, intent.OperationID, fingerprint)
	if err == nil {
		if err := trackercontract.ValidateReceipt(intent, prior); err != nil {
			return nil, err
		}
		return tracker.Applied{Receipt: prior}, nil
	}
	if !isNotFound(err) {
		return nil, normalize(err)
	}
	scope, kind, err := operationScope(intent.Effect)
	if err != nil {
		return nil, err
	}
	options, err := a.Inspect(ctx, ticketwriter.InspectRequest{Pin: intent.Pin, Destination: intent.Destination, Scope: scope})
	if err != nil {
		return nil, err
	}
	manifest, err := a.Discover(ctx, intent.Pin.AdapterConfigPin)
	if err != nil {
		return nil, err
	}
	if err := trackercontract.ValidateIntent(intent, manifest, options, a.now().UTC()); err != nil {
		return nil, err
	}
	now := a.now().UTC()
	value := statestore.NativeTicket{ID: "ticket_" + digest(intent.OperationID), ProjectID: a.scope.ContainerID, State: statestore.NativeOpen, CreatedAt: now, UpdatedAt: now, Assignees: []tracker.NamedID{}, Labels: []tracker.NamedID{}, Relationships: []tracker.Relation{}, Evidence: []string{}}
	if target, ok := scope.(tracker.TicketScope); ok {
		value, err = a.store.NativeTicket(ctx, a.scope.ContainerID, target.Ref.ID)
		if err != nil {
			return nil, normalize(err)
		}
		// Inspect and mutation are separate reads; enforce the inspected revision
		// again before deriving the desired replacement record.
		if strconv.FormatUint(value.Revision, 10) != target.Revision {
			return nil, failure(ports.FailureConflict, "ticket changed after writer inspection")
		}
	}
	expected := value.Revision
	if err := a.applyEffect(ctx, &value, intent.Effect); err != nil {
		return nil, err
	}
	value.Revision++
	value.UpdatedAt = now
	receipt := tracker.Receipt{OperationID: intent.OperationID, DesiredDigest: intent.DesiredDigest, Pin: intent.Pin, Destination: intent.Destination, Target: tracker.TicketRef{Namespace: a.scope.Namespace, ID: value.ID}, ObservedRevision: strconv.FormatUint(value.Revision, 10), ObservedAt: now, EvidenceRef: fmt.Sprintf("native-history:%s:%d", value.ID, value.Revision)}
	committed, err := a.store.MutateNativeTicket(ctx, statestore.NativeTicketMutation{Ticket: value, ExpectedRevision: expected, Kind: kind, OperationFingerprint: fingerprint, Request: requestJSON, Receipt: receipt})
	if err != nil {
		return nil, normalize(err)
	}
	if err := trackercontract.ValidateReceipt(intent, committed); err != nil {
		return nil, err
	}
	return tracker.Applied{Receipt: committed}, nil
}

func (a *Adapter) applyEffect(ctx context.Context, value *statestore.NativeTicket, effect tracker.Effect) error {
	switch operation := effect.(type) {
	case tracker.CreateTicket:
		return updateFields(value, operation.Fields)
	case tracker.EditTicket:
		return updateFields(value, operation.Fields)
	case tracker.TakeTransition:
		value.State = statestore.NativeBusinessState(strings.TrimPrefix(operation.TransitionID, "set-state:"))
	case tracker.ReportProgress:
		// The complete report body and evidence are retained in immutable history;
		// a progress report never changes business or execution state.
	case tracker.LinkStories:
		if _, ok := operation.Representation.(tracker.NativeRelation); !ok {
			return failure(ports.FailureUnsupported, "built-in relationships require native representation")
		}
		if _, err := a.store.NativeTicket(ctx, value.ProjectID, operation.Relation.Target.ID); err != nil {
			return normalize(err)
		}
		if !slices.ContainsFunc(value.Relationships, func(relation tracker.Relation) bool {
			return reflect.DeepEqual(relation, operation.Relation)
		}) {
			value.Relationships = append(value.Relationships, operation.Relation)
		}
	default:
		return failure(ports.FailureUnsupported, "native effect is unsupported")
	}
	return nil
}

func updateFields(value *statestore.NativeTicket, fields map[string]tracker.FieldValue) error {
	for id, field := range fields {
		switch id {
		case "title":
			title := strings.TrimSpace(string(field.(tracker.TextValue)))
			if title == "" {
				return failure(ports.FailureInvalidRequest, "native title cannot be empty")
			}
			value.Title = title
		case "description":
			value.Description = string(field.(tracker.TextValue))
		case "priority":
			number := float64(field.(tracker.NumberValue))
			if number < 0 || number > math.MaxInt32 || number != math.Trunc(number) {
				return failure(ports.FailureInvalidRequest, "native priority must be a non-negative 32-bit integer")
			}
			value.Priority = int(number)
		case "labels", "assignees":
			items := make([]tracker.NamedID, 0)
			seen := make(map[string]bool)
			for _, selected := range field.(tracker.IDsValue) {
				if seen[selected] {
					return failure(ports.FailureInvalidRequest, "native IDs must be unique")
				}
				seen[selected] = true
				items = append(items, tracker.NamedID{ID: selected, Name: selected})
			}
			if id == "labels" {
				value.Labels = items
			} else {
				value.Assignees = items
			}
		default:
			return failure(ports.FailureUnsupported, "native field is unsupported")
		}
	}
	return nil
}

func (a *Adapter) Reconcile(ctx context.Context, intent tracker.Intent) (tracker.EffectResult, error) {
	if err := trackercontract.ValidatePin(intent.Pin, a.pin); err != nil {
		return nil, err
	}
	if intent.Destination != a.scope || intent.OperationID == "" || intent.DesiredDigest == "" || intent.AuthorizationRef == "" {
		return nil, failure(ports.FailureInvalidRequest, "reconciliation requires the original native intent")
	}
	fingerprint, _, err := operationFingerprint(intent)
	if err != nil {
		return nil, err
	}
	receipt, err := a.store.NativeOperation(ctx, intent.OperationID, fingerprint)
	if isNotFound(err) {
		// Mutation, immutable history and receipt commit in one SQLite transaction.
		// Exact journal absence therefore positively proves no committed effect.
		return tracker.NotApplied{OperationID: intent.OperationID, DesiredDigest: intent.DesiredDigest, Pin: intent.Pin, Destination: intent.Destination, EvidenceRef: "native-operation-absence:" + intent.OperationID}, nil
	}
	if err != nil {
		return nil, normalize(err)
	}
	if err := trackercontract.ValidateReceipt(intent, receipt); err != nil {
		return nil, err
	}
	return tracker.Applied{Receipt: receipt}, nil
}
