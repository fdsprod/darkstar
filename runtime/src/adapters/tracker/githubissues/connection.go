package githubissues

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"strings"
	"time"

	"darkstar/src/ports"
	"darkstar/src/ports/tracker"
)

type repository struct {
	Type             string   `json:"__typename"`
	ID               string   `json:"id"`
	NameWithOwner    string   `json:"nameWithOwner"`
	URL              string   `json:"url"`
	HasIssuesEnabled bool     `json:"hasIssuesEnabled"`
	Owner            identity `json:"owner"`
	Issues           *struct {
		Nodes []identity `json:"nodes"`
	} `json:"issues"`
}

type Health struct {
	Account     tracker.NamedID
	ObservedAt  time.Time
	EvidenceRef string
}

type Destination struct {
	Scope     tracker.Scope
	Name, URL string
}

type DestinationPage struct {
	Destinations []Destination
	Next         tracker.Continuation
	ObservedAt   time.Time
	EvidenceRef  string
}

// Connection supports account health and accessible destination discovery before
// a repository is selected. Neither operation grants publication authority.
type Connection struct {
	adapter *Adapter
}

func NewConnection(config Config, options Options) (*Connection, error) {
	bootstrapAccount := config.AccountID == ""
	if bootstrapAccount {
		config.AccountID = "unselected"
	}
	config.RepositoryID = "unselected"
	config.TenantID = "unselected"
	if config.BindingRevision == "" {
		config.BindingRevision = "unselected"
	}
	if config.ConfigRevision == "" {
		config.ConfigRevision = "1"
	}
	adapter, err := New(config, options)
	if err != nil {
		return nil, err
	}
	if bootstrapAccount {
		adapter.config.AccountID = ""
	}
	return &Connection{adapter: adapter}, nil
}

func (c *Connection) ProbeHealth(ctx context.Context) (Health, error) {
	return c.adapter.ProbeHealth(ctx)
}

func (a *Adapter) ProbeHealth(ctx context.Context) (Health, error) {
	var data struct {
		Viewer identity `json:"viewer"`
	}
	if err := a.graphql(ctx, "query{viewer{id login}}", nil, &data); err != nil {
		return Health{}, err
	}
	if (a.config.AccountID != "" && data.Viewer.ID != a.config.AccountID) || data.Viewer.ID == "" || data.Viewer.Login == "" {
		return Health{}, fail(ports.FailurePermissionDenied, "GitHub credential authority differs from the configured account")
	}
	evidence, err := a.retain(ctx, "account-health", data)
	if err != nil {
		return Health{}, err
	}
	return Health{Account: tracker.NamedID{ID: data.Viewer.ID, Name: data.Viewer.Login}, ObservedAt: a.options.Now().UTC(), EvidenceRef: evidence}, nil
}

func (a *Adapter) probe(ctx context.Context) (string, error) {
	// The identity and repository observation use a single credential resolution,
	// so credential rotation cannot split this authority check across accounts.
	var data struct {
		Viewer identity    `json:"viewer"`
		Node   *repository `json:"node"`
	}
	query := "query($id:ID!){viewer{id login} node(id:$id){__typename ... on Repository{id nameWithOwner url hasIssuesEnabled owner{id} issues(first:1){nodes{id}}}}}"
	if err := a.graphql(ctx, query, map[string]any{"id": a.config.RepositoryID}, &data); err != nil {
		return "", err
	}
	if data.Viewer.ID != a.config.AccountID || data.Viewer.Login == "" {
		return "", fail(ports.FailurePermissionDenied, "GitHub credential authority differs from the configured account")
	}
	if data.Node == nil {
		return "", fail(ports.FailurePermissionDenied, "selected GitHub repository is missing or inaccessible")
	}
	if data.Node.ID != a.config.RepositoryID || data.Node.Type != "Repository" || data.Node.Owner.ID != a.config.TenantID {
		return "", fail(ports.FailureProtocolDrift, "GitHub repository identity or owner changed; explicit rebinding is required")
	}
	if !data.Node.HasIssuesEnabled {
		return "", fail(ports.FailureUnsupported, "GitHub Issues is disabled for the selected repository")
	}
	if data.Node.Issues == nil || data.Node.Issues.Nodes == nil {
		return "", fail(ports.FailurePermissionDenied, "GitHub repository issue read access could not be verified")
	}
	return a.retain(ctx, "source-capabilities", data)
}

func (c *Connection) DiscoverDestinations(ctx context.Context, continuation string, pageSize int) (DestinationPage, error) {
	a := c.adapter
	if pageSize < 1 || pageSize > 100 {
		return DestinationPage{}, fail(ports.FailureInvalidRequest, "GitHub discovery page size must be between 1 and 100")
	}
	queryDigest := digest(struct {
		Pin      tracker.Pin
		PageSize int
	}{a.pin, pageSize})
	var after any
	if continuation != "" {
		encoded, err := base64.RawURLEncoding.DecodeString(continuation)
		var next cursor
		if err != nil || json.Unmarshal(encoded, &next) != nil || next.QueryDigest != queryDigest || strings.TrimSpace(next.After) == "" {
			return DestinationPage{}, fail(ports.FailureInvalidRequest, "GitHub discovery cursor differs from the configured connection")
		}
		after = next.After
	}
	var data struct {
		Viewer struct {
			ID           string `json:"id"`
			Repositories struct {
				Nodes    []repository `json:"nodes"`
				PageInfo pageInfo     `json:"pageInfo"`
			} `json:"repositories"`
		} `json:"viewer"`
	}
	query := "query($first:Int!,$after:String){viewer{id repositories(first:$first,after:$after,affiliations:[OWNER,COLLABORATOR,ORGANIZATION_MEMBER],orderBy:{field:NAME,direction:ASC}){nodes{id nameWithOwner url hasIssuesEnabled owner{id}} pageInfo{hasNextPage endCursor}}}}"
	if err := a.graphql(ctx, query, map[string]any{"first": pageSize, "after": after}, &data); err != nil {
		return DestinationPage{}, err
	}
	if data.Viewer.ID != a.config.AccountID {
		return DestinationPage{}, fail(ports.FailurePermissionDenied, "GitHub credential authority differs from the configured account")
	}
	if data.Viewer.Repositories.Nodes == nil {
		return DestinationPage{}, fail(ports.FailureProtocolDrift, "GitHub repository discovery is incomplete")
	}
	evidence, err := a.retain(ctx, "accessible-destinations", data)
	if err != nil {
		return DestinationPage{}, err
	}
	page := DestinationPage{Destinations: []Destination{}, Next: tracker.End{}, ObservedAt: a.options.Now().UTC(), EvidenceRef: evidence}
	for _, repository := range data.Viewer.Repositories.Nodes {
		if !repository.HasIssuesEnabled {
			continue
		}
		if repository.ID == "" || repository.Owner.ID == "" || repository.NameWithOwner == "" {
			return DestinationPage{}, fail(ports.FailureProtocolDrift, "GitHub destination identity is incomplete")
		}
		scope := tracker.Scope{Namespace: tracker.Namespace{Provider: "github_issues", Host: a.config.Host, TenantID: repository.Owner.ID, ScopeID: repository.ID}, ContainerID: repository.ID}
		page.Destinations = append(page.Destinations, Destination{Scope: scope, Name: repository.NameWithOwner, URL: repository.URL})
	}
	if data.Viewer.Repositories.PageInfo.HasNextPage {
		end := data.Viewer.Repositories.PageInfo.EndCursor
		if end == "" || end == after {
			return DestinationPage{}, fail(ports.FailureProtocolDrift, "GitHub discovery continuation is missing or did not advance")
		}
		encoded, _ := json.Marshal(cursor{QueryDigest: queryDigest, After: end})
		page.Next = tracker.More{Cursor: base64.RawURLEncoding.EncodeToString(encoded)}
	}
	return page, nil
}
