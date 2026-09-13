// Package builtin implements the shared tracker ports over durable native
// business records. It has no scheduler or execution mutation authority.
package builtin

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"darkstar/src/core/trackercontract"
	"darkstar/src/ports"
	"darkstar/src/ports/statestore"
	"darkstar/src/ports/ticketwriter"
	"darkstar/src/ports/tracker"
	"darkstar/src/ports/worksource"
)

const WorkflowRevision = "native-business-state/v1"
const sprintReason = "built-in tracker has no sprint concept"

type Adapter struct {
	store statestore.NativeTrackerStore
	scope tracker.Scope
	pin   tracker.Pin
	now   func() time.Time
}

var _ worksource.TrackerSourceV1 = (*Adapter)(nil)
var _ worksource.TrackerBrowserV1 = (*Adapter)(nil)
var _ ticketwriter.WriterV1 = (*Adapter)(nil)

func New(store statestore.NativeTrackerStore, projectID string) (*Adapter, error) {
	return NewForBinding(store, projectID, "1")
}

// NewForBinding freezes the selected project source revision independently of
// native namespace identity, retaining old work/source pins after a switch.
func NewForBinding(store statestore.NativeTrackerStore, projectID, bindingRevision string) (*Adapter, error) {
	if store == nil || strings.TrimSpace(projectID) == "" {
		return nil, failure(ports.FailureInvalidRequest, "native tracker requires store and project identity")
	}
	if strings.TrimSpace(bindingRevision) == "" {
		return nil, failure(ports.FailureInvalidRequest, "native tracker requires binding revision")
	}
	scope := tracker.Scope{Namespace: tracker.Namespace{Provider: "built_in", Host: "darkstar.local", TenantID: "local", ScopeID: projectID}, ContainerID: projectID}
	pin := tracker.Pin{AdapterConfigPin: tracker.AdapterConfigPin{ContractVersion: tracker.Version, AdapterID: "built_in", AdapterVersion: "1", InstallationID: "local", AccountID: "local", BindingRevision: bindingRevision, ConfigRevision: "1", ConfigDigest: digest(scope)}, CapabilitiesDigest: digest("native-tracker-capabilities/v1")}
	return &Adapter{store: store, scope: scope, pin: pin, now: time.Now}, nil
}

func (a *Adapter) ConfigPin() tracker.AdapterConfigPin {
	return a.pin.AdapterConfigPin
}

func (a *Adapter) Discover(ctx context.Context, config tracker.AdapterConfigPin) (tracker.Manifest, error) {
	if config != a.pin.AdapterConfigPin {
		return tracker.Manifest{}, failure(ports.FailureProtocolDrift, "native adapter configuration changed")
	}
	if _, err := a.store.NativeTickets(ctx, a.scope.ContainerID); err != nil {
		return tracker.Manifest{}, normalize(err)
	}
	capabilities := make(map[tracker.Capability]tracker.Knowledge[bool])
	for _, capability := range []tracker.Capability{tracker.Fetch, tracker.List, tracker.Search, tracker.Filter, tracker.Page, tracker.Refresh, tracker.Create, tracker.Edit, tracker.Progress, tracker.Transitions, tracker.Hierarchy, tracker.Dependencies, tracker.Reconciliation} {
		capabilities[capability] = tracker.Known[bool]{Value: true}
	}
	capabilities[tracker.ManagedLinks] = tracker.Unsupported[bool]{Reason: "native directed relationships are supported"}
	return tracker.Manifest{Pin: a.pin, Scope: a.scope, Capabilities: capabilities, Filters: map[string][]tracker.FilterOperator{"business_state": {tracker.Equals, tracker.In}, "priority": {tracker.Equals, tracker.In}}, MaxPageSize: 100, ObservedAt: a.now().UTC(), EvidenceRef: "native-capabilities:" + a.pin.CapabilitiesDigest}, nil
}

func (a *Adapter) validate(pin tracker.Pin, ref tracker.TicketRef) error {
	if err := trackercontract.ValidatePin(pin, a.pin); err != nil {
		return err
	}
	if ref.Namespace != a.scope.Namespace || ref.ID == "" {
		return failure(ports.FailureInvalidRequest, "ticket identity is outside native namespace")
	}
	return nil
}

func (a *Adapter) Read(ctx context.Context, request worksource.ReadTicketRequest) (tracker.ReadResult, error) {
	if err := a.validate(request.Pin, request.Ref); err != nil {
		return nil, err
	}
	value, err := a.store.NativeTicket(ctx, a.scope.ContainerID, request.Ref.ID)
	if isNotFound(err) {
		return tracker.Missing{Ref: request.Ref, ObservedAt: a.now().UTC(), EvidenceRef: "native-lookup:" + request.Ref.ID}, nil
	}
	if err != nil {
		return nil, normalize(err)
	}
	ticket := a.ticket(value)
	if ticket.Revision == request.KnownRevision {
		return tracker.Unchanged{Ref: ticket.Ref, Fresh: ticket.Freshness.(tracker.Fresh)}, nil
	}
	return tracker.Found{Ticket: ticket}, nil
}

func (a *Adapter) ticket(value statestore.NativeTicket) tracker.Ticket {
	revision := strconv.FormatUint(value.Revision, 10)
	return tracker.Ticket{Ref: tracker.TicketRef{Namespace: a.scope.Namespace, ID: value.ID}, Revision: revision, Key: value.ID, Title: value.Title, Description: value.Description, Archived: tracker.Unsupported[bool]{Reason: "native tracker has no archive operation"}, UpdatedAt: tracker.Known[time.Time]{Value: value.UpdatedAt}, Placement: tracker.Known[tracker.Scope]{Value: a.scope}, BusinessState: tracker.Known[tracker.NamedID]{Value: tracker.NamedID{ID: string(value.State), Name: stateName(value.State)}}, BusinessStateReason: tracker.Unsupported[tracker.NamedID]{Reason: "native states do not have separate reasons"}, IssueType: tracker.Known[tracker.NamedID]{Value: tracker.NamedID{ID: "ticket", Name: "Ticket"}}, Sprint: tracker.Unsupported[[]tracker.NamedID]{Reason: sprintReason}, Assignees: tracker.Known[[]tracker.NamedID]{Value: value.Assignees}, Labels: tracker.Known[[]tracker.NamedID]{Value: value.Labels}, Priority: tracker.Known[tracker.NamedID]{Value: tracker.NamedID{ID: strconv.Itoa(value.Priority), Name: strconv.Itoa(value.Priority)}}, Relationships: tracker.Known[[]tracker.Relation]{Value: value.Relationships}, Freshness: tracker.Fresh{ObservedAt: a.now().UTC(), Revision: revision}, EvidenceRef: fmt.Sprintf("native-history:%s:%d", value.ID, value.Revision)}
}

type cursor struct {
	QueryDigest string
	AfterID     string
}

func (a *Adapter) Browse(ctx context.Context, request worksource.BrowseTicketsRequest) (tracker.TicketPage, error) {
	manifest, err := a.Discover(ctx, request.Pin.AdapterConfigPin)
	if err != nil {
		return tracker.TicketPage{}, err
	}
	if err := trackercontract.ValidateQuery(request.Pin, manifest, request.Query); err != nil {
		return tracker.TicketPage{}, err
	}
	if request.Scope != a.scope {
		return tracker.TicketPage{}, failure(ports.FailureInvalidRequest, "browse scope differs from native namespace")
	}
	query := request.Query
	query.Cursor = ""
	queryDigest := digest(struct {
		Pin   tracker.Pin
		Scope tracker.Scope
		Query tracker.Query
	}{request.Pin, request.Scope, query})
	after := ""
	if request.Query.Cursor != "" {
		encoded, decodeErr := base64.RawURLEncoding.DecodeString(request.Query.Cursor)
		var continuation cursor
		if decodeErr != nil || json.Unmarshal(encoded, &continuation) != nil || continuation.QueryDigest != queryDigest || continuation.AfterID == "" {
			return tracker.TicketPage{}, failure(ports.FailureInvalidRequest, "cursor does not match complete query, scope and adapter pin")
		}
		after = continuation.AfterID
	}
	values, err := a.store.NativeTickets(ctx, a.scope.ContainerID)
	if err != nil {
		return tracker.TicketPage{}, normalize(err)
	}
	page := tracker.TicketPage{Tickets: make([]tracker.Ticket, 0), Next: tracker.End{}, Freshness: tracker.Fresh{ObservedAt: a.now().UTC(), Revision: digest(values)}}
	for _, value := range values {
		if value.ID <= after || !matches(value, query) {
			continue
		}
		if len(page.Tickets) == query.PageSize {
			encoded, _ := json.Marshal(cursor{QueryDigest: queryDigest, AfterID: page.Tickets[len(page.Tickets)-1].Ref.ID})
			page.Next = tracker.More{Cursor: base64.RawURLEncoding.EncodeToString(encoded)}
			break
		}
		page.Tickets = append(page.Tickets, a.ticket(value))
	}
	return page, nil
}

func matches(value statestore.NativeTicket, query tracker.Query) bool {
	if !strings.Contains(strings.ToLower(value.Title+"\n"+value.Description), strings.ToLower(query.Text)) {
		return false
	}
	for _, predicate := range query.Predicates {
		observed := string(value.State)
		if predicate.FieldID == "priority" {
			observed = strconv.Itoa(value.Priority)
		}
		if !slices.Contains(predicate.Values, observed) {
			return false
		}
	}
	return true
}

func stateName(state statestore.NativeBusinessState) string {
	switch state {
	case statestore.NativeOpen:
		return "Open"
	case statestore.NativeActive:
		return "Active"
	case statestore.NativeCompleted:
		return "Completed"
	case statestore.NativeCancelled:
		return "Cancelled"
	default:
		return "Unknown"
	}
}

func digest(value any) string {
	encoded, _ := json.Marshal(value)
	return fmt.Sprintf("%x", sha256.Sum256(encoded))
}

func failure(code ports.FailureCode, message string) error {
	return &ports.Failure{Code: code, Message: message}
}

func isNotFound(err error) bool {
	var typed *ports.Failure
	return errors.As(err, &typed) && typed.Code == ports.FailureNotFound
}

func normalize(err error) error {
	var typed *ports.Failure
	if errors.As(err, &typed) {
		return err
	}
	return failure(ports.FailureUnavailable, "native ticket storage is unavailable")
}
