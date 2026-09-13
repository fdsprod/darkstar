package trackercontract

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"strings"
	"time"

	"darkstar/src/ports"
	"darkstar/src/ports/tracker"
)

const ticketEncodingVersion = "darkstar.ticket-observation/v1"

type ticketEncoding struct {
	Version             string            `json:"version"`
	Ref                 tracker.TicketRef `json:"ref"`
	Revision            string            `json:"revision"`
	Key                 string            `json:"key"`
	URL                 string            `json:"url"`
	Title               string            `json:"title"`
	Description         string            `json:"description"`
	BusinessState       json.RawMessage   `json:"businessState"`
	BusinessStateReason json.RawMessage   `json:"businessStateReason"`
	IssueType           json.RawMessage   `json:"issueType"`
	Sprint              json.RawMessage   `json:"sprint"`
	Assignees           json.RawMessage   `json:"assignees"`
	Labels              json.RawMessage   `json:"labels"`
	Priority            json.RawMessage   `json:"priority"`
	Relationships       json.RawMessage   `json:"relationships"`
	Archived            json.RawMessage   `json:"archived"`
	UpdatedAt           json.RawMessage   `json:"updatedAt"`
	Placement           json.RawMessage   `json:"placement"`
	Freshness           freshnessEncoding `json:"freshness"`
	EvidenceRef         string            `json:"evidenceRef"`
}

type knowledgeEncoding struct {
	State  string          `json:"state"`
	Value  json.RawMessage `json:"value,omitempty"`
	Reason string          `json:"reason,omitempty"`
}

type freshnessEncoding struct {
	State          string     `json:"state"`
	ObservedAt     *time.Time `json:"observedAt,omitempty"`
	Revision       string     `json:"revision,omitempty"`
	LastObservedAt *time.Time `json:"lastObservedAt,omitempty"`
	Reason         string     `json:"reason,omitempty"`
}

// EncodeTicket is the explicit versioned persistence codec for the in-process
// port's closed variants. Native receipt and event encodings remain unchanged.
func EncodeTicket(ticket tracker.Ticket) (json.RawMessage, error) {
	value := ticketEncoding{Version: ticketEncodingVersion, Ref: ticket.Ref, Revision: ticket.Revision, Key: ticket.Key, URL: ticket.URL, Title: ticket.Title, Description: ticket.Description, EvidenceRef: ticket.EvidenceRef}
	var err error
	for _, field := range []struct {
		destination *json.RawMessage
		encode      func() (json.RawMessage, error)
	}{
		{&value.BusinessState, func() (json.RawMessage, error) {
			return encodeKnowledge(ticket.BusinessState)
		}},
		{&value.BusinessStateReason, func() (json.RawMessage, error) {
			return encodeKnowledge(ticket.BusinessStateReason)
		}},
		{&value.IssueType, func() (json.RawMessage, error) {
			return encodeKnowledge(ticket.IssueType)
		}},
		{&value.Sprint, func() (json.RawMessage, error) {
			return encodeKnowledge(ticket.Sprint)
		}},
		{&value.Assignees, func() (json.RawMessage, error) {
			return encodeKnowledge(ticket.Assignees)
		}},
		{&value.Labels, func() (json.RawMessage, error) {
			return encodeKnowledge(ticket.Labels)
		}},
		{&value.Priority, func() (json.RawMessage, error) {
			return encodeKnowledge(ticket.Priority)
		}},
		{&value.Relationships, func() (json.RawMessage, error) {
			return encodeKnowledge(ticket.Relationships)
		}},
		{&value.Archived, func() (json.RawMessage, error) {
			return encodeKnowledge(ticket.Archived)
		}},
		{&value.UpdatedAt, func() (json.RawMessage, error) {
			return encodeKnowledge(ticket.UpdatedAt)
		}},
		{&value.Placement, func() (json.RawMessage, error) {
			return encodeKnowledge(ticket.Placement)
		}},
	} {
		*field.destination, err = field.encode()
		if err != nil {
			return nil, err
		}
	}
	switch freshness := ticket.Freshness.(type) {
	case tracker.Fresh:
		value.Freshness = freshnessEncoding{State: "fresh", ObservedAt: &freshness.ObservedAt, Revision: freshness.Revision}
	case tracker.Stale:
		value.Freshness = freshnessEncoding{State: "stale", LastObservedAt: &freshness.LastObservedAt, Reason: freshness.Reason}
	case tracker.NeverObserved:
		value.Freshness = freshnessEncoding{State: "never_observed", Reason: freshness.Reason}
	default:
		return nil, codecError()
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, codecError()
	}
	if _, err := DecodeTicket(encoded); err != nil {
		return nil, err
	}
	return encoded, nil
}

func encodeKnowledge[T any](knowledge tracker.Knowledge[T]) (json.RawMessage, error) {
	var packet knowledgeEncoding
	switch value := knowledge.(type) {
	case tracker.Known[T]:
		encoded, err := json.Marshal(value.Value)
		if err != nil {
			return nil, codecError()
		}
		if string(encoded) == "null" && reflect.TypeOf(value.Value) != nil && reflect.TypeOf(value.Value).Kind() == reflect.Slice {
			encoded = []byte(`[]`)
		}
		packet = knowledgeEncoding{State: "known", Value: encoded}
	case tracker.Unknown[T]:
		packet = knowledgeEncoding{State: "unknown", Reason: value.Reason}
	case tracker.Unsupported[T]:
		packet = knowledgeEncoding{State: "unsupported", Reason: value.Reason}
	default:
		return nil, codecError()
	}
	encoded, err := json.Marshal(packet)
	if err != nil {
		return nil, codecError()
	}
	return encoded, nil
}

// DecodeTicket rejects unknown versions, nil variants, mixed sibling payloads,
// malformed identities and unsupported relationship/freshness variants.
func DecodeTicket(encoded json.RawMessage) (tracker.Ticket, error) {
	var value ticketEncoding
	if strictJSON(encoded, &value) != nil || value.Version != ticketEncodingVersion || value.Ref.ID == "" || validateNamespace(value.Ref.Namespace) != nil || value.Revision == "" || strings.TrimSpace(value.Title) == "" || value.EvidenceRef == "" {
		return tracker.Ticket{}, codecError()
	}
	ticket := tracker.Ticket{Ref: value.Ref, Revision: value.Revision, Key: value.Key, URL: value.URL, Title: value.Title, Description: value.Description, EvidenceRef: value.EvidenceRef}
	var err error
	ticket.BusinessState, err = decodeKnowledge[tracker.NamedID](value.BusinessState)
	if err != nil {
		return tracker.Ticket{}, err
	}
	ticket.BusinessStateReason, err = decodeKnowledge[tracker.NamedID](value.BusinessStateReason)
	if err != nil {
		return tracker.Ticket{}, err
	}
	ticket.IssueType, err = decodeKnowledge[tracker.NamedID](value.IssueType)
	if err != nil {
		return tracker.Ticket{}, err
	}
	ticket.Sprint, err = decodeKnowledge[[]tracker.NamedID](value.Sprint)
	if err != nil {
		return tracker.Ticket{}, err
	}
	ticket.Assignees, err = decodeKnowledge[[]tracker.NamedID](value.Assignees)
	if err != nil {
		return tracker.Ticket{}, err
	}
	ticket.Labels, err = decodeKnowledge[[]tracker.NamedID](value.Labels)
	if err != nil {
		return tracker.Ticket{}, err
	}
	ticket.Priority, err = decodeKnowledge[tracker.NamedID](value.Priority)
	if err != nil {
		return tracker.Ticket{}, err
	}
	ticket.Relationships, err = decodeKnowledge[[]tracker.Relation](value.Relationships)
	if err != nil {
		return tracker.Ticket{}, err
	}
	ticket.Archived, err = decodeKnowledge[bool](value.Archived)
	if err != nil {
		return tracker.Ticket{}, err
	}
	ticket.UpdatedAt, err = decodeKnowledge[time.Time](value.UpdatedAt)
	if err != nil {
		return tracker.Ticket{}, err
	}
	ticket.Placement, err = decodeKnowledge[tracker.Scope](value.Placement)
	if err != nil {
		return tracker.Ticket{}, err
	}
	freshness := value.Freshness
	switch freshness.State {
	case "fresh":
		if freshness.ObservedAt == nil || freshness.ObservedAt.IsZero() || freshness.Revision != ticket.Revision || freshness.LastObservedAt != nil || freshness.Reason != "" {
			return tracker.Ticket{}, codecError()
		}
		ticket.Freshness = tracker.Fresh{ObservedAt: *freshness.ObservedAt, Revision: freshness.Revision}
	case "stale":
		if freshness.LastObservedAt == nil || freshness.LastObservedAt.IsZero() || freshness.ObservedAt != nil || freshness.Revision != "" || strings.TrimSpace(freshness.Reason) == "" {
			return tracker.Ticket{}, codecError()
		}
		ticket.Freshness = tracker.Stale{LastObservedAt: *freshness.LastObservedAt, Reason: freshness.Reason}
	case "never_observed":
		if freshness.LastObservedAt != nil || freshness.ObservedAt != nil || freshness.Revision != "" || strings.TrimSpace(freshness.Reason) == "" {
			return tracker.Ticket{}, codecError()
		}
		ticket.Freshness = tracker.NeverObserved{Reason: freshness.Reason}
	default:
		return tracker.Ticket{}, codecError()
	}
	return ticket, nil
}

func decodeKnowledge[T any](encoded json.RawMessage) (tracker.Knowledge[T], error) {
	var packet knowledgeEncoding
	if strictJSON(encoded, &packet) != nil {
		return nil, codecError()
	}
	switch packet.State {
	case "known":
		if len(packet.Value) == 0 || string(packet.Value) == "null" || packet.Reason != "" {
			return nil, codecError()
		}
		var value T
		if strictJSON(packet.Value, &value) != nil || !validKnown(value) {
			return nil, codecError()
		}
		return tracker.Known[T]{Value: value}, nil
	case "unknown", "unsupported":
		if len(packet.Value) != 0 || strings.TrimSpace(packet.Reason) == "" {
			return nil, codecError()
		}
		if packet.State == "unknown" {
			return tracker.Unknown[T]{Reason: packet.Reason}, nil
		}
		return tracker.Unsupported[T]{Reason: packet.Reason}, nil
	default:
		return nil, codecError()
	}
}

func validKnown(value any) bool {
	switch typed := value.(type) {
	case bool:
		return true
	case time.Time:
		return !typed.IsZero()
	case tracker.NamedID:
		return strings.TrimSpace(typed.ID) != ""
	case tracker.Scope:
		return validateScope(typed) == nil
	case []tracker.NamedID:
		seen := make(map[string]bool)
		for _, item := range typed {
			if item.ID == "" || seen[item.ID] {
				return false
			}
			seen[item.ID] = true
		}
		return true
	case []tracker.Relation:
		for _, item := range typed {
			if item.Kind != tracker.ChildOf && item.Kind != tracker.DependsOn || item.Target.ID == "" || validateNamespace(item.Target.Namespace) != nil {
				return false
			}
		}
		return true
	default:
		return false
	}
}

func strictJSON(encoded []byte, destination any) error {
	if len(encoded) == 0 || len(encoded) > 16*1024*1024 {
		return errors.New("JSON observation exceeds supported bounds")
	}
	if err := uniqueJSONKeys(json.NewDecoder(bytes.NewReader(encoded)), 0); err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	var trailing any
	if !errors.Is(decoder.Decode(&trailing), io.EOF) {
		return errors.New("trailing JSON is unsupported")
	}
	return nil
}

func uniqueJSONKeys(decoder *json.Decoder, depth int) error {
	if depth > 64 {
		return errors.New("JSON observation is too deeply nested")
	}
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delimiter, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	seen := make(map[string]bool)
	for decoder.More() {
		if delimiter == '{' {
			key, err := decoder.Token()
			if err != nil {
				return err
			}
			name, ok := key.(string)
			if !ok || seen[strings.ToLower(name)] {
				return errors.New("duplicate JSON observation key")
			}
			seen[strings.ToLower(name)] = true
		}
		if err := uniqueJSONKeys(decoder, depth+1); err != nil {
			return err
		}
	}
	_, err = decoder.Token()
	return err
}

func codecError() error {
	return fail(ports.FailureProtocolDrift, "ticket observation encoding is invalid or unsupported")
}
