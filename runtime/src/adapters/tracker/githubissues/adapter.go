// Package githubissues implements a read-only GitHub Issues source. Credentials
// and provider query syntax never cross the tracker port; it has no writer or
// execution authority.
package githubissues

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"time"

	"darkstar/src/core/trackercontract"
	"darkstar/src/ports"
	"darkstar/src/ports/tracker"
	"darkstar/src/ports/trackerconnection"
	"darkstar/src/ports/worksource"
)

// CredentialResolver resolves only a protected installation reference. Returned
// credentials are used for one request and never persisted in source evidence.
type CredentialResolver = trackerconnection.CredentialResolver

// EvidenceStore must retain immutable source bytes durably before returning.
type EvidenceStore = trackerconnection.EvidenceStore

// Config holds immutable provider node IDs, not a local code repository or issue
// number. Credential rotation does not change the non-secret configuration pin.
type Config struct {
	Host, InstallationID, AccountID, TenantID, RepositoryID string
	BindingRevision, ConfigRevision, CredentialRef          string
}

type Options struct {
	Credentials CredentialResolver
	Evidence    EvidenceStore
	HTTPClient  *http.Client
	Now         func() time.Time
}

type Adapter struct {
	config  Config
	pin     tracker.Pin
	scope   tracker.Scope
	options Options
}

var _ worksource.TrackerSourceV1 = (*Adapter)(nil)
var _ worksource.TrackerBrowserV1 = (*Adapter)(nil)
var hostPattern = regexp.MustCompile(`^[a-z0-9]+(?:[.-][a-z0-9]+)*$`)

func New(config Config, options Options) (*Adapter, error) {
	if !hostPattern.MatchString(config.Host) || options.Credentials == nil || options.Evidence == nil {
		return nil, fail(ports.FailureInvalidRequest, "GitHub source requires a valid host, protected credentials and durable evidence storage")
	}
	for _, value := range []string{config.InstallationID, config.AccountID, config.TenantID, config.RepositoryID, config.BindingRevision, config.ConfigRevision, config.CredentialRef} {
		if strings.TrimSpace(value) == "" {
			return nil, fail(ports.FailureInvalidRequest, "GitHub source requires exact account, repository and configuration identities")
		}
	}
	if options.Now == nil {
		options.Now = time.Now
	}
	if options.HTTPClient == nil {
		options.HTTPClient = &http.Client{Timeout: 30 * time.Second}
	}
	client := *options.HTTPClient
	// Never follow a provider redirect with credentials or silently retarget a host.
	client.CheckRedirect = func(_ *http.Request, _ []*http.Request) error {
		return http.ErrUseLastResponse
	}
	options.HTTPClient = &client
	scope := tracker.Scope{Namespace: tracker.Namespace{Provider: "github_issues", Host: config.Host, TenantID: config.TenantID, ScopeID: config.RepositoryID}, ContainerID: config.RepositoryID}
	nonsecret := config
	nonsecret.CredentialRef = ""
	pin := tracker.Pin{AdapterConfigPin: tracker.AdapterConfigPin{ContractVersion: tracker.Version, AdapterID: "github_issues", AdapterVersion: "1", InstallationID: config.InstallationID, AccountID: config.AccountID, BindingRevision: config.BindingRevision, ConfigRevision: config.ConfigRevision, ConfigDigest: digest(nonsecret)}, CapabilitiesDigest: digest("github-issues-read/v1;substring-search;state-filter;native-cursor;no-writes")}
	return &Adapter{config: config, scope: scope, pin: pin, options: options}, nil
}

func (a *Adapter) ConfigPin() tracker.AdapterConfigPin {
	return a.pin.AdapterConfigPin
}

func (a *Adapter) Scope() tracker.Scope {
	return a.scope
}

func (a *Adapter) manifest(evidence string) tracker.Manifest {
	capabilities := map[tracker.Capability]tracker.Knowledge[bool]{}
	for _, capability := range []tracker.Capability{tracker.Fetch, tracker.List, tracker.Search, tracker.Filter, tracker.Page, tracker.Refresh} {
		capabilities[capability] = tracker.Known[bool]{Value: true}
	}
	for _, capability := range []tracker.Capability{tracker.Create, tracker.Edit, tracker.Progress, tracker.Transitions, tracker.Hierarchy, tracker.Dependencies, tracker.ManagedLinks, tracker.Reconciliation} {
		capabilities[capability] = tracker.Unsupported[bool]{Reason: "read-only GitHub Issues adapter does not grant writer authority"}
	}
	return tracker.Manifest{Pin: a.pin, Scope: a.scope, Capabilities: capabilities, Filters: map[string][]tracker.FilterOperator{"business_state": {tracker.Equals, tracker.In}, "business_state_reason": {tracker.Equals, tracker.In}}, MaxPageSize: 100, ObservedAt: a.options.Now().UTC(), EvidenceRef: evidence}
}

func (a *Adapter) Discover(ctx context.Context, config tracker.AdapterConfigPin) (tracker.Manifest, error) {
	if config != a.pin.AdapterConfigPin {
		return tracker.Manifest{}, fail(ports.FailureProtocolDrift, "GitHub source configuration changed")
	}
	evidence, err := a.probe(ctx)
	if err != nil {
		return tracker.Manifest{}, err
	}
	return a.manifest(evidence), nil
}

func (a *Adapter) Read(ctx context.Context, request worksource.ReadTicketRequest) (tracker.ReadResult, error) {
	if err := trackercontract.ValidatePin(request.Pin, a.pin); err != nil {
		return nil, err
	}
	if request.Ref.Namespace != a.scope.Namespace || request.Ref.ID == "" {
		return nil, fail(ports.FailureInvalidRequest, "issue identity is outside the selected GitHub repository")
	}
	if _, err := a.probe(ctx); err != nil {
		return nil, err
	}
	var data struct {
		Node *issue `json:"node"`
	}
	var sourceEvidence string
	if err := a.graphql(ctx, "query($id:ID!){node(id:$id){__typename ... on Issue {"+issueFields+"}}}", map[string]any{"id": request.Ref.ID}, &data, &sourceEvidence); err != nil {
		return nil, err
	}
	if data.Node == nil {
		evidence, err := a.retain(ctx, "exact-lookup-missing", struct {
			Ref            tracker.TicketRef
			SourceEvidence string
		}{request.Ref, sourceEvidence})
		if err != nil {
			return nil, err
		}
		return tracker.Missing{Ref: request.Ref, ObservedAt: a.options.Now().UTC(), EvidenceRef: evidence}, nil
	}
	if data.Node.Type != "Issue" || data.Node.ID != request.Ref.ID {
		return nil, fail(ports.FailureInvalidRequest, "requested GitHub node is not the exact issue")
	}
	ticket, err := a.ticket(ctx, *data.Node, sourceEvidence)
	if err != nil {
		return nil, err
	}
	if ticket.Revision == request.KnownRevision {
		return tracker.Unchanged{Ref: ticket.Ref, Fresh: ticket.Freshness.(tracker.Fresh)}, nil
	}
	return tracker.Found{Ticket: ticket}, nil
}

type cursor struct {
	QueryDigest string
	After       string
}

// Browse performs literal, case-insensitive title/body search while walking the
// repository's native issue connection. Empty filtered pages can have More;
// callers must exhaust the cursor rather than interpreting a page as absence.
func (a *Adapter) Browse(ctx context.Context, request worksource.BrowseTicketsRequest) (tracker.TicketPage, error) {
	if err := trackercontract.ValidateQuery(request.Pin, a.manifest(""), request.Query); err != nil {
		return tracker.TicketPage{}, err
	}
	if request.Scope != a.scope {
		return tracker.TicketPage{}, fail(ports.FailureInvalidRequest, "browse scope differs from GitHub repository")
	}
	for _, predicate := range request.Query.Predicates {
		allowed := []string{"open", "closed"}
		if predicate.FieldID == "business_state_reason" {
			allowed = []string{"completed", "not_planned", "reopened", "none"}
		}
		for _, value := range predicate.Values {
			if !slices.Contains(allowed, value) {
				return tracker.TicketPage{}, fail(ports.FailureInvalidRequest, "unknown GitHub state or reason filter")
			}
		}
	}
	query := request.Query
	query.Cursor = ""
	queryDigest := digest(struct {
		Pin   tracker.Pin
		Scope tracker.Scope
		Query tracker.Query
	}{request.Pin, request.Scope, query})
	var after any
	if request.Query.Cursor != "" {
		encoded, err := base64.RawURLEncoding.DecodeString(request.Query.Cursor)
		var next cursor
		if err != nil || json.Unmarshal(encoded, &next) != nil || next.QueryDigest != queryDigest || next.After == "" {
			return tracker.TicketPage{}, fail(ports.FailureInvalidRequest, "cursor does not match GitHub query, scope and pin")
		}
		after = next.After
	}
	if _, err := a.probe(ctx); err != nil {
		return tracker.TicketPage{}, err
	}
	var data struct {
		Node *struct {
			ID     string `json:"id"`
			Issues struct {
				Nodes    []issue  `json:"nodes"`
				PageInfo pageInfo `json:"pageInfo"`
			} `json:"issues"`
		} `json:"node"`
	}
	var sourceEvidence string
	err := a.graphql(ctx, "query($id:ID!,$first:Int!,$after:String){node(id:$id){... on Repository{id issues(first:$first,after:$after,orderBy:{field:CREATED_AT,direction:ASC}){nodes{__typename "+issueFields+"} pageInfo{hasNextPage endCursor}}}}}", map[string]any{"id": a.config.RepositoryID, "first": query.PageSize, "after": after}, &data, &sourceEvidence)
	if err != nil {
		return tracker.TicketPage{}, err
	}
	if data.Node == nil || data.Node.ID != a.config.RepositoryID || data.Node.Issues.Nodes == nil {
		return tracker.TicketPage{}, fail(ports.FailurePermissionDenied, "GitHub repository issues are unavailable")
	}
	page := tracker.TicketPage{Tickets: []tracker.Ticket{}, Next: tracker.End{}, Freshness: tracker.Fresh{ObservedAt: a.options.Now().UTC(), Revision: digest(data.Node)}}
	for _, value := range data.Node.Issues.Nodes {
		if value.Type != "Issue" {
			continue
		}
		ticket, err := a.ticket(ctx, value, sourceEvidence)
		if err != nil {
			return tracker.TicketPage{}, err
		}
		if matches(value, query) {
			page.Tickets = append(page.Tickets, ticket)
		}
	}
	if data.Node.Issues.PageInfo.HasNextPage {
		if data.Node.Issues.PageInfo.EndCursor == "" || data.Node.Issues.PageInfo.EndCursor == after {
			return tracker.TicketPage{}, fail(ports.FailureProtocolDrift, "GitHub continuation is missing or did not advance")
		}
		encoded, _ := json.Marshal(cursor{QueryDigest: queryDigest, After: data.Node.Issues.PageInfo.EndCursor})
		page.Next = tracker.More{Cursor: base64.RawURLEncoding.EncodeToString(encoded)}
	}
	return page, nil
}

func matches(value issue, query tracker.Query) bool {
	if !strings.Contains(strings.ToLower(value.Title+"\n"+value.Body), strings.ToLower(query.Text)) {
		return false
	}
	for _, predicate := range query.Predicates {
		observed := strings.ToLower(value.State)
		if predicate.FieldID == "business_state_reason" {
			observed = reasonID(value.StateReason)
		}
		if !slices.Contains(predicate.Values, observed) {
			return false
		}
	}
	return true
}

func digest(value any) string {
	encoded, _ := json.Marshal(value)
	return fmt.Sprintf("%x", sha256.Sum256(encoded))
}

func fail(code ports.FailureCode, message string) *ports.Failure {
	return &ports.Failure{Code: code, Message: message}
}
