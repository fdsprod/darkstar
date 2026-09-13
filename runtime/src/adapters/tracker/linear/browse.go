package linear

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"sort"
	"strconv"
	"strings"
	"time"

	"darkstar/src/core/trackercontract"
	"darkstar/src/ports"
	"darkstar/src/ports/tracker"
	"darkstar/src/ports/worksource"
)

type browseCursor struct {
	QueryDigest    string
	ProviderCursor string
}

func (a *Adapter) Browse(ctx context.Context, request worksource.BrowseTicketsRequest) (tracker.TicketPage, error) {
	if err := a.requireBound(); err != nil {
		return tracker.TicketPage{}, err
	}
	if request.Scope != a.scope {
		return tracker.TicketPage{}, fail(ports.FailureInvalidRequest, "Linear browse scope differs from selected team")
	}
	if err := trackercontract.ValidateQuery(request.Pin, a.manifest(a.now().UTC(), "query-preflight"), request.Query); err != nil {
		return tracker.TicketPage{}, err
	}
	query := request.Query
	query.Cursor = ""
	queryDigest := digestJSON(struct {
		Pin   tracker.Pin
		Scope tracker.Scope
		Query tracker.Query
	}{request.Pin, request.Scope, query})
	after := ""
	if request.Query.Cursor != "" {
		encoded, err := base64.RawURLEncoding.DecodeString(request.Query.Cursor)
		var cursor browseCursor
		if err != nil || json.Unmarshal(encoded, &cursor) != nil || cursor.QueryDigest != queryDigest || cursor.ProviderCursor == "" {
			return tracker.TicketPage{}, fail(ports.FailureInvalidRequest, "Linear cursor is not bound to this complete query and scope")
		}
		after = cursor.ProviderCursor
	}
	filter, err := a.issueFilter(query)
	if err != nil {
		return tracker.TicketPage{}, err
	}
	var data struct {
		Issues connection[json.RawMessage] `json:"issues"`
	}
	raw, err := a.query(ctx, `query DarkstarIssues($first: Int!, $after: String, $filter: IssueFilter!) { `+authoritySelection+` issues(first: $first, after: $after, filter: $filter, orderBy: updatedAt, includeArchived: true) { nodes { `+browseIssueSelection+` } pageInfo { hasNextPage endCursor } } }`, map[string]any{"first": query.PageSize, "after": nullableCursor(after), "filter": filter}, &data)
	if err != nil {
		return tracker.TicketPage{}, err
	}
	next, err := nextPage(data.Issues, after, map[string]bool{})
	if err != nil {
		return tracker.TicketPage{}, err
	}
	if len(data.Issues.Nodes) > query.PageSize {
		return tracker.TicketPage{}, fail(ports.FailureProtocolDrift, "Linear exceeded the requested page size")
	}
	evidence, err := a.retain(ctx, "issue-page", a.config.Endpoint, raw)
	if err != nil {
		return tracker.TicketPage{}, err
	}
	page := tracker.TicketPage{Tickets: make([]tracker.Ticket, 0), Next: tracker.End{}, Freshness: tracker.Fresh{ObservedAt: a.now().UTC(), Revision: digestJSON(data.Issues.Nodes)}}
	seen := make(map[string]bool)
	for _, original := range data.Issues.Nodes {
		var issue linearIssue
		if json.Unmarshal(original, &issue) != nil || issue.Team.ID != a.config.TeamID || a.config.ProjectID != "" && (issue.Project == nil || issue.Project.ID != a.config.ProjectID) || seen[issue.ID] {
			return tracker.TicketPage{}, fail(ports.FailureProtocolDrift, "Linear returned duplicate issues or issues outside the selected source binding")
		}
		seen[issue.ID] = true
		refs := []string{evidence}
		if err := a.hydrate(ctx, &issue, &refs); err != nil {
			return tracker.TicketPage{}, err
		}
		revision := issueRevision(issue)
		proof, _ := json.Marshal(map[string]any{"issueId": issue.ID, "revision": revision, "originalEvidence": refs})
		observation, err := a.retain(ctx, "issue-observation", issue.URL, proof)
		if err != nil {
			return tracker.TicketPage{}, err
		}
		ticket, err := a.normalizeIssue(issue, original, revision, observation)
		if err != nil {
			return tracker.TicketPage{}, err
		}
		page.Tickets = append(page.Tickets, ticket)
	}
	if next != "" {
		encoded, _ := json.Marshal(browseCursor{QueryDigest: queryDigest, ProviderCursor: next})
		page.Next = tracker.More{Cursor: base64.RawURLEncoding.EncodeToString(encoded)}
	}
	return page, nil
}

func (a *Adapter) issueFilter(query tracker.Query) (map[string]any, error) {
	clauses := []any{map[string]any{"team": map[string]any{"id": map[string]any{"eq": a.config.TeamID}}}}
	if a.config.ProjectID != "" {
		clauses = append(clauses, map[string]any{"project": map[string]any{"id": map[string]any{"eq": a.config.ProjectID}}})
	}
	if strings.TrimSpace(query.Text) != "" {
		clauses = append(clauses, map[string]any{"or": []any{map[string]any{"title": map[string]any{"containsIgnoreCase": query.Text}}, map[string]any{"description": map[string]any{"containsIgnoreCase": query.Text}}}})
	}
	for _, predicate := range query.Predicates {
		operator := "eq"
		var value any = predicate.Values[0]
		if predicate.Operator == tracker.In {
			operator = "in"
			value = predicate.Values
		}
		field := map[string]string{"business_state": "state", "assignee": "assignee", "label": "labels", "project": "project"}[predicate.FieldID]
		if field != "" {
			clauses = append(clauses, map[string]any{field: map[string]any{"id": map[string]any{operator: value}}})
			continue
		}
		switch predicate.FieldID {
		case "priority":
			numbers := make([]int, 0, len(predicate.Values))
			for _, selected := range predicate.Values {
				number, err := strconv.Atoi(selected)
				if err != nil || number < 0 || number > 4 {
					return nil, fail(ports.FailureInvalidRequest, "Linear priority filter must use values 0 through 4")
				}
				numbers = append(numbers, number)
			}
			value = numbers[0]
			if predicate.Operator == tracker.In {
				value = numbers
			}
			clauses = append(clauses, map[string]any{"priority": map[string]any{operator: value}})
		case "updated_at":
			updated, err := time.Parse(time.RFC3339Nano, predicate.Values[0])
			if err != nil {
				return nil, fail(ports.FailureInvalidRequest, "Linear update watermark must be an RFC3339 timestamp")
			}
			clauses = append(clauses, map[string]any{"updatedAt": map[string]any{"gt": updated.UTC().Format(time.RFC3339Nano)}})
		default:
			return nil, fail(ports.FailureUnsupported, "Linear filter field is unsupported")
		}
	}
	return map[string]any{"and": clauses}, nil
}

// Both browse and exact reads complete their bounded child connections before
// computing a revision. Collection order/cursors are transport details; labels,
// relationships and referenced evidence content remain part of the digest even
// when editing them does not advance the parent issue's updatedAt.
func issueRevision(issue linearIssue) string {
	complete := false
	if issue.Labels != nil {
		labels := *issue.Labels
		labels.Nodes = append([]tracker.NamedID{}, labels.Nodes...)
		sort.Slice(labels.Nodes, func(i, j int) bool {
			return labels.Nodes[i].ID < labels.Nodes[j].ID
		})
		labels.PageInfo = pageInfo{HasNextPage: &complete}
		issue.Labels = &labels
	}
	if issue.InverseRelations != nil {
		relations := *issue.InverseRelations
		relations.Nodes = append([]issueRelation{}, relations.Nodes...)
		sort.Slice(relations.Nodes, func(i, j int) bool {
			return relations.Nodes[i].ID < relations.Nodes[j].ID
		})
		relations.PageInfo = pageInfo{HasNextPage: &complete}
		issue.InverseRelations = &relations
	}
	issue.Attachments = canonicalEvidence(issue.Attachments)
	issue.Comments = canonicalEvidence(issue.Comments)
	return digestJSON(issue)
}

func canonicalEvidence(original *connection[json.RawMessage]) *connection[json.RawMessage] {
	if original == nil {
		return nil
	}
	complete := false
	result := &connection[json.RawMessage]{Nodes: make([]json.RawMessage, 0, len(original.Nodes)), PageInfo: pageInfo{HasNextPage: &complete}}
	for _, raw := range original.Nodes {
		var value map[string]any
		if json.Unmarshal(raw, &value) == nil {
			encoded, _ := json.Marshal(value)
			result.Nodes = append(result.Nodes, encoded)
		} else {
			result.Nodes = append(result.Nodes, raw)
		}
	}
	sort.Slice(result.Nodes, func(i, j int) bool {
		return string(result.Nodes[i]) < string(result.Nodes[j])
	})
	return result
}
