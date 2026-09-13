package trackercontract

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"darkstar/src/ports/tracker"
)

func codecTicket() tracker.Ticket {
	now := time.Date(2026, 9, 12, 12, 0, 0, 0, time.UTC)
	scope := tracker.Scope{Namespace: tracker.Namespace{Provider: "linear", Host: "linear.app", TenantID: "workspace", ScopeID: "workspace"}, ContainerID: "team"}
	return tracker.Ticket{Ref: tracker.TicketRef{Namespace: scope.Namespace, ID: "native-issue"}, Revision: "revision", Title: "Observed title", Description: "Original description", BusinessState: tracker.Known[tracker.NamedID]{Value: tracker.NamedID{ID: "state", Name: "State"}}, BusinessStateReason: tracker.Unsupported[tracker.NamedID]{Reason: "no separate reason"}, IssueType: tracker.Unknown[tracker.NamedID]{Reason: "not queried"}, Sprint: tracker.Known[[]tracker.NamedID]{Value: []tracker.NamedID{}}, Assignees: tracker.Known[[]tracker.NamedID]{Value: []tracker.NamedID{{ID: "person", Name: "Person"}}}, Labels: tracker.Known[[]tracker.NamedID]{Value: []tracker.NamedID{}}, Priority: tracker.Unsupported[tracker.NamedID]{Reason: "not supported"}, Relationships: tracker.Known[[]tracker.Relation]{Value: []tracker.Relation{{Kind: tracker.ChildOf, Target: tracker.TicketRef{Namespace: scope.Namespace, ID: "parent"}}}}, Archived: tracker.Known[bool]{Value: false}, UpdatedAt: tracker.Known[time.Time]{Value: now}, Placement: tracker.Known[tracker.Scope]{Value: scope}, Freshness: tracker.Fresh{ObservedAt: now, Revision: "revision"}, EvidenceRef: "immutable:original"}
}

func TestTicketCodecRoundTripsClosedVariantsAndRetainsFalseAndEmpty(t *testing.T) {
	for _, freshness := range []tracker.Freshness{tracker.Fresh{ObservedAt: time.Now().UTC().Round(0), Revision: "revision"}, tracker.Stale{LastObservedAt: time.Now().UTC().Round(0), Reason: "provider unavailable"}, tracker.NeverObserved{Reason: "pending observation"}} {
		ticket := codecTicket()
		ticket.Freshness = freshness
		encoded, err := EncodeTicket(ticket)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(encoded), `"archived":{"state":"known","value":false}`) || !strings.Contains(string(encoded), `"labels":{"state":"known","value":[]}`) {
			t.Fatalf("observed false/empty became missing: %s", encoded)
		}
		decoded, err := DecodeTicket(encoded)
		if err != nil || !reflect.DeepEqual(ticket, decoded) {
			t.Fatalf("roundtrip failed: %#v %v", decoded, err)
		}
	}
}

func TestTicketCodecRejectsUnknownMixedNilAndContradictoryVariants(t *testing.T) {
	encoded, err := EncodeTicket(codecTicket())
	if err != nil {
		t.Fatal(err)
	}
	tests := []string{
		strings.Replace(string(encoded), ticketEncodingVersion, "darkstar.ticket-observation/v2", 1),
		strings.Replace(string(encoded), `"archived":{"state":"known","value":false}`, `"archived":null`, 1),
		strings.Replace(string(encoded), `"archived":{"state":"known","value":false}`, `"archived":{"state":"known","value":null}`, 1),
		strings.Replace(string(encoded), `"archived":{"state":"known","value":false}`, `"archived":{"state":"unsupported","value":false,"reason":"no"}`, 1),
		strings.Replace(string(encoded), `"archived":{"state":"known","value":false}`, `"archived":{"state":"future","reason":"no"}`, 1),
		strings.Replace(string(encoded), `"archived":{"state":"known","value":false}`, `"archived":{"state":"known","value":false,"State":"unknown"}`, 1),
		strings.Replace(string(encoded), `"revision":"revision"`, `"revision":"other"`, 1),
		strings.Replace(string(encoded), `"Kind":"child_of"`, `"Kind":"related"`, 1),
		string(encoded) + ` {}`,
	}
	for index, input := range tests {
		if _, err := DecodeTicket(json.RawMessage(input)); err == nil {
			t.Fatalf("accepted contradictory encoding case %d", index)
		}
	}
	ticket := codecTicket()
	ticket.Labels = nil
	if _, err := EncodeTicket(ticket); err == nil {
		t.Fatal("encoded nil knowledge variant")
	}
}
