package linear

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"darkstar/src/ports"
	"darkstar/src/ports/ticketwriter"
	"darkstar/src/ports/tracker"
	"darkstar/src/ports/worksource"
)

const issueID = "11111111-1111-4111-8111-111111111111"
const parentID = "22222222-2222-4222-8222-222222222222"

type credentials struct {
	value string
}

func (c *credentials) Resolve(context.Context, string) (string, error) {
	return c.value, nil
}

type evidenceStore struct {
	records [][]byte
	err     error
}

func (e *evidenceStore) Retain(_ context.Context, body []byte) (string, error) {
	if e.err != nil {
		return "", e.err
	}
	e.records = append(e.records, append([]byte(nil), body...))
	return fmt.Sprintf("evidence:%d", len(e.records)), nil
}

type queryRequest struct {
	Query     string         `json:"query"`
	Variables map[string]any `json:"variables"`
}

func fixture(t *testing.T, handler func(http.ResponseWriter, *http.Request, queryRequest)) (*Adapter, *evidenceStore, *credentials) {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request queryRequest
		if r.Method != http.MethodPost || json.NewDecoder(r.Body).Decode(&request) != nil || strings.Contains(strings.ToLower(request.Query), "mutation") {
			t.Error("adapter issued something other than a GraphQL read")
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		handler(w, r, request)
	}))
	t.Cleanup(server.Close)
	store := &evidenceStore{}
	credential := &credentials{value: "protected-linear-secret"}
	a, err := New(Config{Endpoint: server.URL, InstallationID: "installation", AccountID: "account", WorkspaceID: "workspace", TeamID: "team", BindingRevision: "1", ConfigRevision: "1", CredentialRef: "protected-reference", Authentication: PersonalAPIKey}, server.Client(), credential, store)
	if err != nil {
		t.Fatal(err)
	}
	return a, store, credential
}

func respond(w http.ResponseWriter, fields map[string]any) {
	fields["viewer"] = map[string]any{"id": "account", "name": "Person"}
	fields["organization"] = map[string]any{"id": "workspace", "name": "Workspace"}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"data": fields})
}

func page(nodes any, next string) map[string]any {
	return map[string]any{"nodes": nodes, "pageInfo": map[string]any{"hasNextPage": next != "", "endCursor": next}}
}

func issue() map[string]any {
	return map[string]any{"id": issueID, "identifier": "TEAM-1", "url": "https://linear.app/workspace/issue/TEAM-1", "title": "Keep authoritative title", "description": "Human description with a [reference](https://example.com/proof)", "updatedAt": "2026-09-12T12:00:00Z", "archivedAt": nil, "priority": 2, "priorityLabel": "High", "team": map[string]any{"id": "team", "name": "Team"}, "project": map[string]any{"id": "project", "name": "Project"}, "state": map[string]any{"id": "state-in-progress", "name": "In Progress"}, "assignee": map[string]any{"id": "person", "name": "Assigned person"}, "cycle": map[string]any{"id": "cycle", "number": 7, "name": "Cycle Seven"}, "parent": map[string]any{"id": parentID}, "labels": page([]any{map[string]any{"id": "label", "name": "Infrastructure"}}, ""), "inverseRelations": page([]any{map[string]any{"id": "relationship", "type": "blocks", "issue": map[string]any{"id": parentID}, "relatedIssue": map[string]any{"id": issueID}}}, ""), "attachments": page([]any{map[string]any{"id": "attachment", "title": "Retained evidence", "url": "https://example.com/proof"}}, ""), "comments": page([]any{map[string]any{"id": "comment", "body": "Original clarification", "url": "https://linear.app/comment", "updatedAt": "2026-09-12T12:00:00Z"}}, "")}
}

func TestLinearExactReadPreservesIdentityOriginalsAndIndependentBusinessData(t *testing.T) {
	value := issue()
	a, evidence, _ := fixture(t, func(w http.ResponseWriter, r *http.Request, request queryRequest) {
		if r.Header.Get("Authorization") != "protected-linear-secret" || request.Variables["id"] != issueID {
			t.Error("personal API key header or native UUID lookup is incorrect")
		}
		respond(w, map[string]any{"issue": value})
	})
	if _, ok := any(a).(ticketwriter.WriterV1); ok {
		t.Fatal("read-only adapter acquired writer authority")
	}
	request := worksource.ReadTicketRequest{Pin: a.pin, Ref: tracker.TicketRef{Namespace: a.scope.Namespace, ID: issueID}}
	result, err := a.Read(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	ticket := result.(tracker.Found).Ticket
	if ticket.Ref != request.Ref || ticket.Title != value["title"] || ticket.BusinessState.(tracker.Known[tracker.NamedID]).Value.ID != "state-in-progress" || ticket.Sprint.(tracker.Known[[]tracker.NamedID]).Value[0].ID != "cycle" || len(ticket.Relationships.(tracker.Known[[]tracker.Relation]).Value) != 2 {
		t.Fatalf("incorrect normalization: %#v", ticket)
	}
	if len(evidence.records) != 2 || !strings.Contains(string(evidence.records[0]), "Original clarification") || !strings.Contains(string(evidence.records[0]), "Retained evidence") || strings.Contains(string(evidence.records[0]), "protected-linear-secret") {
		t.Fatalf("source original/provenance was lost or credential was retained")
	}
	var original struct {
		Body       string `json:"body"`
		BodySHA256 string `json:"bodySHA256"`
	}
	if json.Unmarshal(evidence.records[0], &original) != nil || original.BodySHA256 != digestBytes([]byte(original.Body)) || !strings.HasSuffix(original.Body, "\n") {
		t.Fatal("original JSON bytes were reformatted or no longer match their retained digest")
	}
	request.KnownRevision = ticket.Revision
	result, err = a.Read(context.Background(), request)
	if _, ok := result.(tracker.Unchanged); err != nil || !ok {
		t.Fatalf("unchanged source was not recognized: %#v %v", result, err)
	}
	value["team"] = map[string]any{"id": "moved-team", "name": "Moved team"}
	value["project"] = nil
	value["identifier"] = "MOVED-12"
	value["archivedAt"] = "2026-09-12T13:00:00Z"
	result, err = a.Read(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	moved := result.(tracker.Found).Ticket
	if moved.Ref != ticket.Ref || moved.Placement.(tracker.Known[tracker.Scope]).Value.ContainerID != "moved-team" || !moved.Archived.(tracker.Known[bool]).Value || moved.Revision == ticket.Revision {
		t.Fatalf("team move altered identity or lost observation: %#v", moved)
	}
}

func TestLinearBootstrapDiscoveryPaginatesTeamsAndProjects(t *testing.T) {
	teamPages, projectPages := 0, 0
	a, _, _ := fixture(t, func(w http.ResponseWriter, r *http.Request, request queryRequest) {
		if r.Header.Get("Authorization") != "Bearer protected-linear-secret" {
			t.Error("OAuth header is incorrect")
		}
		switch {
		case strings.Contains(request.Query, "DarkstarHealth"):
			respond(w, map[string]any{})
		case strings.Contains(request.Query, "DarkstarTeams"):
			teamPages++
			if request.Variables["after"] == nil {
				respond(w, map[string]any{"teams": page([]any{map[string]any{"id": "team", "name": "Team"}}, "team-next")})
			} else {
				respond(w, map[string]any{"teams": page([]any{map[string]any{"id": "second-team", "name": "Second Team"}}, "")})
			}
		case strings.Contains(request.Query, "DarkstarProjects"):
			projectPages++
			team := request.Variables["team"]
			next := ""
			id := "project-two"
			if request.Variables["after"] == nil {
				next = "project-next"
				id = "project-one"
			}
			respond(w, map[string]any{"team": map[string]any{"id": team, "projects": page([]any{map[string]any{"id": id, "name": id}}, next)}})
		}
	})
	a.config.AccountID = ""
	a.config.WorkspaceID = ""
	a.config.TeamID = ""
	a.config.Authentication = OAuth
	scopes, err := a.DiscoverScopes(context.Background())
	if err != nil || scopes.Account.ID != "account" || scopes.Workspace.ID != "workspace" || len(scopes.Teams) != 2 || len(scopes.Teams[1].Projects) != 2 || teamPages != 2 || projectPages != 4 {
		t.Fatalf("discovery incomplete: %#v pages=%d/%d %v", scopes, teamPages, projectPages, err)
	}
	if _, err := a.Discover(context.Background(), a.ConfigPin()); err == nil {
		t.Fatal("bootstrap config was allowed to become a bound source")
	}
}

func TestLinearBrowseScopeFiltersWatermarksPagingAndReadRevisionAgree(t *testing.T) {
	requests := make([]queryRequest, 0)
	a, _, _ := fixture(t, func(w http.ResponseWriter, _ *http.Request, request queryRequest) {
		requests = append(requests, request)
		if strings.Contains(request.Query, "DarkstarIssue(") {
			respond(w, map[string]any{"issue": issue()})
			return
		}
		next := ""
		if request.Variables["after"] == nil {
			next = "provider-next"
		}
		respond(w, map[string]any{"issues": page([]any{issue()}, next)})
	})
	request := worksource.BrowseTicketsRequest{Pin: a.pin, Scope: a.scope, Query: tracker.Query{PageSize: 1, Text: "authoritative", Predicates: []tracker.Predicate{{FieldID: "business_state", Operator: tracker.In, Values: []string{"state-in-progress", "state-backlog"}}, {FieldID: "updated_at", Operator: tracker.After, Values: []string{"2026-09-10T12:00:00Z"}}}}}
	first, err := a.Browse(context.Background(), request)
	if err != nil || len(first.Tickets) != 1 {
		t.Fatalf("browse failed: %#v %v", first, err)
	}
	encoded, _ := json.Marshal(requests[0].Variables["filter"])
	if !strings.Contains(string(encoded), `"team":{"id":{"eq":"team"}}`) || !strings.Contains(string(encoded), `"updatedAt":{"gt":"2026-09-10T12:00:00Z"}`) || !strings.Contains(requests[0].Query, "includeArchived: true") || !strings.Contains(requests[0].Query, "orderBy: updatedAt") {
		t.Fatalf("scope/update filter lost: %s", encoded)
	}
	request.Query.Cursor = first.Next.(tracker.More).Cursor
	if _, err := a.Browse(context.Background(), request); err != nil || requests[1].Variables["after"] != "provider-next" {
		t.Fatalf("provider pagination failed: %v", err)
	}
	request.Query.Text = "changed query"
	if _, err := a.Browse(context.Background(), request); err == nil || len(requests) != 2 {
		t.Fatal("cursor escaped its complete query binding")
	}
	result, err := a.Read(context.Background(), worksource.ReadTicketRequest{Pin: a.pin, Ref: first.Tickets[0].Ref, KnownRevision: first.Tickets[0].Revision})
	if _, ok := result.(tracker.Unchanged); err != nil || !ok {
		t.Fatalf("browse/read revision disagree: %#v %v", result, err)
	}
}

func TestLinearPaginatesReferencedEvidenceBeforeReturningExactObservation(t *testing.T) {
	requests := 0
	a, store, _ := fixture(t, func(w http.ResponseWriter, _ *http.Request, request queryRequest) {
		requests++
		if strings.Contains(request.Query, "DarkstarIssueEvidence") {
			respond(w, map[string]any{"issue": map[string]any{"id": issueID, "comments": page([]any{map[string]any{"id": "later-comment", "body": "Second page clarification"}}, "")}})
			return
		}
		value := issue()
		value["comments"] = page([]any{map[string]any{"id": "first-comment", "body": "First clarification"}}, "comments-next")
		respond(w, map[string]any{"issue": value})
	})
	_, err := a.Read(context.Background(), worksource.ReadTicketRequest{Pin: a.pin, Ref: tracker.TicketRef{Namespace: a.scope.Namespace, ID: issueID}})
	if err != nil || requests != 2 || len(store.records) != 3 || !strings.Contains(string(store.records[1]), "Second page clarification") {
		t.Fatalf("referenced evidence pagination failed: %d %d %v", requests, len(store.records), err)
	}
}

func TestLinearContentDigestDetectsLabelAndCommentChangesWithoutTimestampChanges(t *testing.T) {
	value := issue()
	a, store, _ := fixture(t, func(w http.ResponseWriter, _ *http.Request, _ queryRequest) {
		respond(w, map[string]any{"issue": value})
	})
	request := worksource.ReadTicketRequest{Pin: a.pin, Ref: tracker.TicketRef{Namespace: a.scope.Namespace, ID: issueID}}
	result, err := a.Read(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	request.KnownRevision = result.(tracker.Found).Ticket.Revision
	original := append([]byte(nil), store.records[0]...)
	value["labels"] = page([]any{map[string]any{"id": "new-label", "name": "New label"}}, "")
	result, err = a.Read(context.Background(), request)
	if found, ok := result.(tracker.Found); err != nil || !ok || found.Ticket.Revision == request.KnownRevision {
		t.Fatalf("label change was hidden by parent timestamp: %#v %v", result, err)
	}
	request.KnownRevision = result.(tracker.Found).Ticket.Revision
	value["comments"] = page([]any{map[string]any{"id": "comment", "body": "Edited clarification"}}, "")
	result, err = a.Read(context.Background(), request)
	if found, ok := result.(tracker.Found); err != nil || !ok || found.Ticket.Revision == request.KnownRevision || !reflect.DeepEqual(original, store.records[0]) {
		t.Fatalf("comment change was hidden or old original overwritten: %#v %v", result, err)
	}
}

func TestLinearUnsupportedFiltersAndBrokenDiscoveryCannotReturnCompleteEmpty(t *testing.T) {
	requests := 0
	a, _, _ := fixture(t, func(w http.ResponseWriter, _ *http.Request, request queryRequest) {
		requests++
		if strings.Contains(request.Query, "DarkstarHealth") {
			respond(w, map[string]any{"team": map[string]any{"id": "team", "name": "Team"}})
			return
		}
		respond(w, map[string]any{"teams": map[string]any{"nodes": []any{}}})
	})
	_, err := a.Browse(context.Background(), worksource.BrowseTicketsRequest{Pin: a.pin, Scope: a.scope, Query: tracker.Query{PageSize: 10, Predicates: []tracker.Predicate{{FieldID: "invented", Operator: tracker.Equals, Values: []string{"x"}}}}})
	if !isFailure(err, ports.FailureUnsupported) || requests != 0 {
		t.Fatalf("unsupported filter reached provider: %v %d", err, requests)
	}
	if result, err := a.DiscoverScopes(context.Background()); !isFailure(err, ports.FailureProtocolDrift) || len(result.Teams) != 0 {
		t.Fatalf("partial discovery became a complete scope list: %#v %v", result, err)
	}
}

func TestLinearErrorsAreTypedRedactedAndNeverSuccessfulEmptyRefreshes(t *testing.T) {
	tests := []struct {
		name   string
		status int
		body   string
		code   ports.FailureCode
	}{
		{"revoked", 401, `{"message":"protected-linear-secret"}`, ports.FailureUnauthenticated},
		{"denied", 403, `{"message":"protected-linear-secret"}`, ports.FailurePermissionDenied},
		{"partial", 200, `{"data":{"issues":{"nodes":[]}},"errors":[{"message":"protected-linear-secret","extensions":{"code":"INTERNAL_SERVER_ERROR"}}]}`, ports.FailureUnavailable},
		{"rate", 400, `{"errors":[{"message":"protected-linear-secret","extensions":{"code":"RATELIMITED"}}]}`, ports.FailureResourceExhausted},
		{"invalid", 200, `not json protected-linear-secret`, ports.FailureProtocolDrift},
		{"authority", 200, `{"data":{"viewer":{"id":"wrong"},"organization":{"id":"workspace"},"issues":{"nodes":[]}}}`, ports.FailureProtocolDrift},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			a, store, _ := fixture(t, func(w http.ResponseWriter, _ *http.Request, _ queryRequest) {
				w.Header().Set("X-RateLimit-Requests-Reset", "1893456000000")
				w.Header().Set("Retry-After", "19")
				w.WriteHeader(test.status)
				_, _ = w.Write([]byte(test.body))
			})
			_, err := a.Browse(context.Background(), worksource.BrowseTicketsRequest{Pin: a.pin, Scope: a.scope, Query: tracker.Query{PageSize: 10}})
			var failure *ports.Failure
			if !errors.As(err, &failure) || failure.Code != test.code || strings.Contains(err.Error(), "protected-linear-secret") || len(store.records) != 0 {
				t.Fatalf("unsafe/misclassified error: %#v, evidence=%d", err, len(store.records))
			}
			if test.name == "rate" && (failure.Details["retry_after_seconds"] != "19" || failure.Details["retry_at_utc"] != "2030-01-01T00:00:00Z") {
				t.Fatalf("rate metadata lost: %#v", failure.Details)
			}
		})
	}
}

func TestLinearMissingRequiresExplicitAuthorizedIdentityProof(t *testing.T) {
	mode := "null"
	a, _, _ := fixture(t, func(w http.ResponseWriter, _ *http.Request, _ queryRequest) {
		if mode == "null" {
			respond(w, map[string]any{"issue": nil})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"viewer": map[string]any{"id": "account"}, "organization": map[string]any{"id": "workspace"}, "issue": nil}, "errors": []any{map[string]any{"path": []string{"issue"}, "extensions": map[string]any{"code": "ENTITY_NOT_FOUND"}}}})
	})
	request := worksource.ReadTicketRequest{Pin: a.pin, Ref: tracker.TicketRef{Namespace: a.scope.Namespace, ID: issueID}}
	if _, err := a.Read(context.Background(), request); !isFailure(err, ports.FailurePermissionDenied) {
		t.Fatalf("ambiguous null became absence proof: %v", err)
	}
	mode = "explicit"
	result, err := a.Read(context.Background(), request)
	if missing, ok := result.(tracker.Missing); err != nil || !ok || missing.EvidenceRef == "" || missing.Ref != request.Ref {
		t.Fatalf("explicit missing observation lost: %#v %v", result, err)
	}
}

func TestLinearCredentialRotationPreservesPinButChecksAuthority(t *testing.T) {
	a, _, credential := fixture(t, func(w http.ResponseWriter, r *http.Request, _ queryRequest) {
		if r.Header.Get("Authorization") != "rotated-secret" {
			t.Error("credential was cached instead of resolved per request")
		}
		respond(w, map[string]any{"team": map[string]any{"id": "team", "name": "Team"}})
	})
	before := a.ConfigPin()
	credential.value = "rotated-secret"
	health, err := a.Health(context.Background())
	if err != nil || health.Account.ID != "account" || !reflect.DeepEqual(before, a.ConfigPin()) {
		t.Fatalf("credential rotation altered pinned identity: %#v %v", health, err)
	}
}

func TestLinearRejectsRedirectsAndMissingEvidenceStorage(t *testing.T) {
	forwarded := false
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		forwarded = true
	}))
	defer target.Close()
	a, _, _ := fixture(t, func(w http.ResponseWriter, r *http.Request, _ queryRequest) {
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	})
	if _, err := a.Health(context.Background()); err == nil || forwarded {
		t.Fatal("Linear followed a credential-bearing redirect")
	}
	b, store, _ := fixture(t, func(w http.ResponseWriter, _ *http.Request, _ queryRequest) {
		respond(w, map[string]any{"team": map[string]any{"id": "team", "name": "Team"}})
	})
	store.err = errors.New("sensitive storage coordinates")
	if _, err := b.Health(context.Background()); err == nil || strings.Contains(err.Error(), "sensitive") {
		t.Fatalf("failed evidence retention was not normalized: %v", err)
	}
	if _, err := New(Config{Endpoint: "https://untrusted.example/graphql"}, nil, nil, nil); err == nil {
		t.Fatal("accepted credential endpoint outside the official API")
	}
}
