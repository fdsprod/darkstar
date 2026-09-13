package linear

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"time"

	"darkstar/src/core/trackercontract"
	"darkstar/src/ports"
	"darkstar/src/ports/tracker"
	"darkstar/src/ports/worksource"
)

var nativeID = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

const scalarSelection = `id identifier url title description updatedAt archivedAt priority priorityLabel team { id name } project { id name } state { id name } assignee { id name } cycle { id name number } parent { id }`
const labelSelection = `nodes { id name } pageInfo { hasNextPage endCursor }`
const relationSelection = `nodes { id type issue { id } relatedIssue { id } } pageInfo { hasNextPage endCursor }`
const attachmentSelection = `nodes { id title url } pageInfo { hasNextPage endCursor }`
const commentSelection = `nodes { id body updatedAt url } pageInfo { hasNextPage endCursor }`
const fullIssueSelection = scalarSelection + ` labels(first: 50) { ` + labelSelection + ` } inverseRelations(first: 50) { ` + relationSelection + ` } attachments(first: 50) { ` + attachmentSelection + ` } comments(first: 50) { ` + commentSelection + ` }`
const browseIssueSelection = scalarSelection + ` labels(first: 10) { ` + labelSelection + ` } inverseRelations(first: 10) { ` + relationSelection + ` } attachments(first: 10) { ` + attachmentSelection + ` } comments(first: 10) { ` + commentSelection + ` }`

type issueRelation struct {
	ID, Type            string
	Issue, RelatedIssue struct{ ID string }
}

type linearIssue struct {
	ID, Identifier, URL, Title string
	Description                *string
	UpdatedAt                  time.Time
	ArchivedAt                 *time.Time
	Priority                   *int
	PriorityLabel              string
	Team                       tracker.NamedID
	Project, State, Assignee   *tracker.NamedID
	Cycle                      *struct {
		ID, Name string
		Number   int
	}
	Parent                *struct{ ID string }
	Labels                *connection[tracker.NamedID]
	InverseRelations      *connection[issueRelation]
	Attachments, Comments *connection[json.RawMessage]
}

var _ worksource.TrackerSourceV1 = (*Adapter)(nil)
var _ worksource.TrackerBrowserV1 = (*Adapter)(nil)

func (a *Adapter) Read(ctx context.Context, request worksource.ReadTicketRequest) (tracker.ReadResult, error) {
	if err := a.requireBound(); err != nil {
		return nil, err
	}
	if err := trackercontract.ValidatePin(request.Pin, a.pin); err != nil {
		return nil, err
	}
	if request.Ref.Namespace != a.scope.Namespace || !nativeID.MatchString(request.Ref.ID) {
		return nil, fail(ports.FailureInvalidRequest, "Linear exact reads require a native issue UUID in the selected workspace")
	}
	var data struct {
		Issue json.RawMessage `json:"issue"`
	}
	raw, err := a.query(ctx, `query DarkstarIssue($id: String!) { `+authoritySelection+` issue(id: $id) { `+fullIssueSelection+` } }`, map[string]any{"id": request.Ref.ID}, &data)
	if err != nil {
		if isFailure(err, ports.FailureNotFound) {
			proof, _ := json.Marshal(map[string]any{"nativeIssueID": request.Ref.ID, "result": "explicit_not_found", "accountID": a.config.AccountID, "workspaceID": a.config.WorkspaceID})
			evidence, retainErr := a.retain(ctx, "issue-missing", a.config.Endpoint, proof)
			if retainErr != nil {
				return nil, retainErr
			}
			return tracker.Missing{Ref: request.Ref, ObservedAt: a.now().UTC(), EvidenceRef: evidence}, nil
		}
		// Linear may deliberately hide an inaccessible issue. An unqualified
		// missing/null response is never promoted to a proof of deletion.
		return nil, err
	}
	if len(data.Issue) == 0 || string(data.Issue) == "null" {
		return nil, fail(ports.FailurePermissionDenied, "Linear did not expose this issue; missing and inaccessible cannot be distinguished")
	}
	var issue linearIssue
	if json.Unmarshal(data.Issue, &issue) != nil || issue.ID != request.Ref.ID {
		return nil, fail(ports.FailureProtocolDrift, "Linear exact issue response changed native identity")
	}
	ref, err := a.retain(ctx, "issue-original", issue.URL, raw)
	if err != nil {
		return nil, err
	}
	evidenceRefs := []string{ref}
	if err := a.hydrate(ctx, &issue, &evidenceRefs); err != nil {
		return nil, err
	}
	revision := issueRevision(issue)
	manifestRaw, _ := json.Marshal(map[string]any{"issueId": issue.ID, "revision": revision, "originalEvidence": evidenceRefs})
	evidence, err := a.retain(ctx, "issue-observation", issue.URL, manifestRaw)
	if err != nil {
		return nil, err
	}
	ticket, err := a.normalizeIssue(issue, data.Issue, revision, evidence)
	if err != nil {
		return nil, err
	}
	if revision == request.KnownRevision {
		return tracker.Unchanged{Ref: request.Ref, Fresh: ticket.Freshness.(tracker.Fresh)}, nil
	}
	return tracker.Found{Ticket: ticket}, nil
}

func (a *Adapter) hydrate(ctx context.Context, issue *linearIssue, refs *[]string) error {
	for _, field := range []string{"labels", "inverseRelations", "attachments", "comments"} {
		var page pageInfo
		switch field {
		case "labels":
			if issue.Labels == nil || issue.Labels.Nodes == nil {
				return fail(ports.FailureProtocolDrift, "Linear exact issue omitted labels")
			}
			page = issue.Labels.PageInfo
		case "inverseRelations":
			if issue.InverseRelations == nil || issue.InverseRelations.Nodes == nil {
				return fail(ports.FailureProtocolDrift, "Linear exact issue omitted relationships")
			}
			page = issue.InverseRelations.PageInfo
		case "attachments":
			if issue.Attachments == nil || issue.Attachments.Nodes == nil {
				return fail(ports.FailureProtocolDrift, "Linear exact issue omitted attachments")
			}
			page = issue.Attachments.PageInfo
		case "comments":
			if issue.Comments == nil || issue.Comments.Nodes == nil {
				return fail(ports.FailureProtocolDrift, "Linear exact issue omitted comments")
			}
			page = issue.Comments.PageInfo
		}
		if page.HasNextPage == nil {
			return fail(ports.FailureProtocolDrift, "Linear issue evidence omitted page information")
		}
		seen := make(map[string]bool)
		for count := 0; *page.HasNextPage; count++ {
			if count >= 100 {
				return fail(ports.FailureResourceExhausted, "Linear issue evidence exceeded the supported page limit")
			}
			if page.EndCursor == "" || seen[page.EndCursor] {
				return fail(ports.FailureProtocolDrift, "Linear issue evidence cursor did not advance")
			}
			seen[page.EndCursor] = true
			selection := map[string]string{"labels": labelSelection, "inverseRelations": relationSelection, "attachments": attachmentSelection, "comments": commentSelection}[field]
			var data struct {
				Issue json.RawMessage `json:"issue"`
			}
			raw, err := a.query(ctx, `query DarkstarIssueEvidence($id: String!, $after: String!) { `+authoritySelection+` issue(id: $id) { id `+field+`(first: 50, after: $after) { `+selection+` } } }`, map[string]any{"id": issue.ID, "after": page.EndCursor}, &data)
			if err != nil {
				return err
			}
			var tail linearIssue
			if json.Unmarshal(data.Issue, &tail) != nil || tail.ID != issue.ID {
				return fail(ports.FailureProtocolDrift, "Linear issue evidence changed native identity")
			}
			ref, err := a.retain(ctx, "issue-"+field, issue.URL, raw)
			if err != nil {
				return err
			}
			*refs = append(*refs, ref)
			switch field {
			case "labels":
				if tail.Labels == nil || tail.Labels.Nodes == nil {
					return fail(ports.FailureProtocolDrift, "Linear label page is missing")
				}
				issue.Labels.Nodes = append(issue.Labels.Nodes, tail.Labels.Nodes...)
				page = tail.Labels.PageInfo
			case "inverseRelations":
				if tail.InverseRelations == nil || tail.InverseRelations.Nodes == nil {
					return fail(ports.FailureProtocolDrift, "Linear relationship page is missing")
				}
				issue.InverseRelations.Nodes = append(issue.InverseRelations.Nodes, tail.InverseRelations.Nodes...)
				page = tail.InverseRelations.PageInfo
			case "attachments":
				if tail.Attachments == nil || tail.Attachments.Nodes == nil {
					return fail(ports.FailureProtocolDrift, "Linear attachment page is missing")
				}
				issue.Attachments.Nodes = append(issue.Attachments.Nodes, tail.Attachments.Nodes...)
				page = tail.Attachments.PageInfo
			case "comments":
				if tail.Comments == nil || tail.Comments.Nodes == nil {
					return fail(ports.FailureProtocolDrift, "Linear comment page is missing")
				}
				issue.Comments.Nodes = append(issue.Comments.Nodes, tail.Comments.Nodes...)
				page = tail.Comments.PageInfo
			}
			if page.HasNextPage == nil {
				return fail(ports.FailureProtocolDrift, "Linear issue evidence page omitted continuation state")
			}
		}
		// The complete normalized collection no longer carries a partial cursor.
		switch field {
		case "labels":
			issue.Labels.PageInfo = page
		case "inverseRelations":
			issue.InverseRelations.PageInfo = page
		case "attachments":
			issue.Attachments.PageInfo = page
		case "comments":
			issue.Comments.PageInfo = page
		}
	}
	return nil
}

func (a *Adapter) normalizeIssue(issue linearIssue, original json.RawMessage, revision, evidence string) (tracker.Ticket, error) {
	if !nativeID.MatchString(issue.ID) || issue.Title == "" || issue.Team.ID == "" || issue.State == nil || issue.State.ID == "" || issue.UpdatedAt.IsZero() || issue.Priority == nil {
		return tracker.Ticket{}, fail(ports.FailureProtocolDrift, "Linear issue lacks required authoritative fields")
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(original, &fields) != nil {
		return tracker.Ticket{}, fail(ports.FailureProtocolDrift, "Linear original issue is invalid")
	}
	for _, required := range []string{"description", "archivedAt", "assignee", "cycle", "parent"} {
		if _, ok := fields[required]; !ok {
			return tracker.Ticket{}, fail(ports.FailureProtocolDrift, "Linear issue omitted requested nullable fields")
		}
	}
	assignees := make([]tracker.NamedID, 0)
	if issue.Assignee != nil {
		if issue.Assignee.ID == "" {
			return tracker.Ticket{}, fail(ports.FailureProtocolDrift, "Linear assignee lacks native identity")
		}
		assignees = append(assignees, *issue.Assignee)
	}
	cycles := make([]tracker.NamedID, 0)
	if issue.Cycle != nil {
		name := issue.Cycle.Name
		if name == "" {
			name = fmt.Sprintf("Cycle %d", issue.Cycle.Number)
		}
		if issue.Cycle.ID == "" {
			return tracker.Ticket{}, fail(ports.FailureProtocolDrift, "Linear cycle lacks native identity")
		}
		cycles = append(cycles, tracker.NamedID{ID: issue.Cycle.ID, Name: name})
	}
	var labels tracker.Knowledge[[]tracker.NamedID] = tracker.Unknown[[]tracker.NamedID]{Reason: "Linear labels were not completely observed"}
	if issue.Labels != nil && issue.Labels.Nodes != nil && issue.Labels.PageInfo.HasNextPage != nil && !*issue.Labels.PageInfo.HasNextPage {
		seen := make(map[string]bool)
		for _, label := range issue.Labels.Nodes {
			if label.ID == "" || seen[label.ID] {
				return tracker.Ticket{}, fail(ports.FailureProtocolDrift, "Linear label identities are missing or duplicated")
			}
			seen[label.ID] = true
		}
		sort.Slice(issue.Labels.Nodes, func(i, j int) bool {
			return issue.Labels.Nodes[i].ID < issue.Labels.Nodes[j].ID
		})
		labels = tracker.Known[[]tracker.NamedID]{Value: issue.Labels.Nodes}
	}
	var relationships tracker.Knowledge[[]tracker.Relation] = tracker.Unknown[[]tracker.Relation]{Reason: "Linear relationships require exact issue observation"}
	if issue.InverseRelations != nil && issue.InverseRelations.Nodes != nil && issue.InverseRelations.PageInfo.HasNextPage != nil && !*issue.InverseRelations.PageInfo.HasNextPage {
		relations := make([]tracker.Relation, 0)
		if issue.Parent != nil {
			if !nativeID.MatchString(issue.Parent.ID) {
				return tracker.Ticket{}, fail(ports.FailureProtocolDrift, "Linear parent lacks native issue identity")
			}
			relations = append(relations, tracker.Relation{Kind: tracker.ChildOf, Target: tracker.TicketRef{Namespace: a.scope.Namespace, ID: issue.Parent.ID}})
		}
		for _, relation := range issue.InverseRelations.Nodes {
			if relation.Type == "blocks" {
				if !nativeID.MatchString(relation.Issue.ID) || relation.RelatedIssue.ID != issue.ID {
					return tracker.Ticket{}, fail(ports.FailureProtocolDrift, "Linear dependency endpoints are inconsistent")
				}
				relations = append(relations, tracker.Relation{Kind: tracker.DependsOn, Target: tracker.TicketRef{Namespace: a.scope.Namespace, ID: relation.Issue.ID}})
			}
		}
		relationships = tracker.Known[[]tracker.Relation]{Value: relations}
	}
	description := ""
	if issue.Description != nil {
		description = *issue.Description
	}
	return tracker.Ticket{Ref: tracker.TicketRef{Namespace: a.scope.Namespace, ID: issue.ID}, Revision: revision, Key: issue.Identifier, URL: issue.URL, Title: issue.Title, Description: description, BusinessState: tracker.Known[tracker.NamedID]{Value: *issue.State}, BusinessStateReason: tracker.Unsupported[tracker.NamedID]{Reason: "Linear workflow states have no separate business-state reason"}, Archived: tracker.Known[bool]{Value: issue.ArchivedAt != nil}, UpdatedAt: tracker.Known[time.Time]{Value: issue.UpdatedAt}, Placement: tracker.Known[tracker.Scope]{Value: tracker.Scope{Namespace: a.scope.Namespace, ContainerID: issue.Team.ID}}, IssueType: tracker.Known[tracker.NamedID]{Value: tracker.NamedID{ID: "linear.issue", Name: "Issue"}}, Sprint: tracker.Known[[]tracker.NamedID]{Value: cycles}, Assignees: tracker.Known[[]tracker.NamedID]{Value: assignees}, Labels: labels, Priority: tracker.Known[tracker.NamedID]{Value: tracker.NamedID{ID: strconv.Itoa(*issue.Priority), Name: issue.PriorityLabel}}, Relationships: relationships, Freshness: tracker.Fresh{ObservedAt: a.now().UTC(), Revision: revision}, EvidenceRef: evidence}, nil
}
