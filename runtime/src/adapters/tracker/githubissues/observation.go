package githubissues

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"darkstar/src/ports"
	"darkstar/src/ports/tracker"
)

const issueFields = `id number url title body state stateReason updatedAt repository{id owner{id}} assignees(first:100){nodes{id login} pageInfo{hasNextPage}} labels(first:100){nodes{id name} pageInfo{hasNextPage}}`

type pageInfo struct {
	HasNextPage bool   `json:"hasNextPage"`
	EndCursor   string `json:"endCursor"`
}

type identity struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Login string `json:"login"`
}

type namedConnection struct {
	Nodes    []identity `json:"nodes"`
	PageInfo pageInfo   `json:"pageInfo"`
}

type issue struct {
	Type        string  `json:"__typename"`
	ID          string  `json:"id"`
	Number      int     `json:"number"`
	URL         string  `json:"url"`
	Title       string  `json:"title"`
	Body        string  `json:"body"`
	State       string  `json:"state"`
	StateReason *string `json:"stateReason"`
	UpdatedAt   string  `json:"updatedAt"`
	Repository  struct {
		ID    string   `json:"id"`
		Owner identity `json:"owner"`
	} `json:"repository"`
	Assignees namedConnection `json:"assignees"`
	Labels    namedConnection `json:"labels"`
}

func (a *Adapter) ticket(ctx context.Context, value issue, sourceEvidence string) (tracker.Ticket, error) {
	if value.Repository.ID != a.config.RepositoryID || value.Repository.Owner.ID != a.config.TenantID {
		return tracker.Ticket{}, fail(ports.FailureProtocolDrift, "GitHub issue moved outside the pinned repository namespace")
	}
	if value.ID == "" || value.Number < 1 || value.Title == "" || value.UpdatedAt == "" || (value.State != "OPEN" && value.State != "CLOSED") {
		return tracker.Ticket{}, fail(ports.FailureProtocolDrift, "GitHub issue observation is incomplete")
	}
	updatedAt, err := time.Parse(time.RFC3339, value.UpdatedAt)
	if err != nil {
		return tracker.Ticket{}, fail(ports.FailureProtocolDrift, "GitHub issue revision timestamp is invalid")
	}
	revision := digest(value)
	evidence, err := a.retain(ctx, "issue-observation", struct {
		Ticket         issue
		SourceEvidence string
	}{value, sourceEvidence})
	if err != nil {
		return tracker.Ticket{}, err
	}
	reason := reasonID(value.StateReason)
	var reasonObservation tracker.Knowledge[tracker.NamedID] = tracker.Known[tracker.NamedID]{Value: tracker.NamedID{ID: reason, Name: reason}}
	if reason != "none" && reason != "completed" && reason != "not_planned" && reason != "reopened" {
		reasonObservation = tracker.Unknown[tracker.NamedID]{Reason: "GitHub returned an unrecognized state reason"}
	}
	return tracker.Ticket{
		Ref:                 tracker.TicketRef{Namespace: a.scope.Namespace, ID: value.ID},
		Revision:            revision,
		Key:                 fmt.Sprintf("#%d", value.Number),
		URL:                 value.URL,
		Title:               value.Title,
		Description:         value.Body,
		BusinessState:       tracker.Known[tracker.NamedID]{Value: tracker.NamedID{ID: strings.ToLower(value.State), Name: strings.ToLower(value.State)}},
		BusinessStateReason: reasonObservation,
		IssueType:           tracker.Unknown[tracker.NamedID]{Reason: "GitHub organization issue types were not queried"},
		Sprint:              tracker.Unsupported[[]tracker.NamedID]{Reason: "GitHub Issues has no native sprint; milestones and Projects are not sprints"},
		Assignees:           namedValues(value.Assignees),
		Labels:              namedValues(value.Labels),
		Priority:            tracker.Unsupported[tracker.NamedID]{Reason: "GitHub Issues has no native priority; Projects custom fields are outside this adapter"},
		Relationships:       tracker.Unknown[[]tracker.Relation]{Reason: "native sub-issue and dependency endpoints were not queried"},
		Archived:            tracker.Unsupported[bool]{Reason: "GitHub repository or Projects archival is not native issue archival"},
		UpdatedAt:           tracker.Known[time.Time]{Value: updatedAt},
		Placement:           tracker.Known[tracker.Scope]{Value: a.scope},
		Freshness:           tracker.Fresh{ObservedAt: a.options.Now().UTC(), Revision: revision},
		EvidenceRef:         evidence,
	}, nil
}

func namedValues(connection namedConnection) tracker.Knowledge[[]tracker.NamedID] {
	if connection.Nodes == nil || connection.PageInfo.HasNextPage {
		return tracker.Unknown[[]tracker.NamedID]{Reason: "GitHub metadata is incomplete or has additional pages"}
	}
	values := make([]tracker.NamedID, 0, len(connection.Nodes))
	for _, node := range connection.Nodes {
		name := node.Name
		if name == "" {
			name = node.Login
		}
		if node.ID == "" || name == "" {
			return tracker.Unknown[[]tracker.NamedID]{Reason: "GitHub metadata is missing a stable identity"}
		}
		values = append(values, tracker.NamedID{ID: node.ID, Name: name})
	}
	return tracker.Known[[]tracker.NamedID]{Value: values}
}

func reasonID(value *string) string {
	if value == nil {
		return "none"
	}
	return strings.ToLower(*value)
}

func (a *Adapter) retain(ctx context.Context, kind string, value any) (string, error) {
	body, err := json.Marshal(value)
	if original, ok := value.(json.RawMessage); ok {
		body = append([]byte(nil), original...)
	}
	if err != nil {
		return "", fail(ports.FailureProtocolDrift, "GitHub evidence could not be encoded")
	}
	envelope, err := json.Marshal(struct {
		Provider    string          `json:"provider"`
		Kind        string          `json:"kind"`
		SourceURL   string          `json:"sourceURL"`
		MediaType   string          `json:"mediaType"`
		ObservedAt  time.Time       `json:"observedAt"`
		Pin         tracker.Pin     `json:"pin"`
		Scope       tracker.Scope   `json:"scope"`
		BodySHA256  string          `json:"bodySHA256"`
		Body        json.RawMessage `json:"body"`
		SourceBytes []byte          `json:"sourceBytes"`
	}{"github_issues", kind, "https://" + a.config.Host, "application/json", a.options.Now().UTC(), a.pin, a.scope, fmt.Sprintf("%x", sha256.Sum256(body)), body, body})
	if err != nil {
		return "", fail(ports.FailureProtocolDrift, "GitHub evidence envelope could not be encoded")
	}
	ref, err := a.options.Evidence.Retain(ctx, envelope)
	if err != nil || strings.TrimSpace(ref) == "" {
		return "", fail(ports.FailureUnavailable, "GitHub source evidence could not be retained")
	}
	return ref, nil
}
