package linear

import (
	"context"
	"time"

	"darkstar/src/core/trackercontract"
	"darkstar/src/ports"
	"darkstar/src/ports/tracker"
)

type HealthObservation struct {
	Account, Workspace, Team tracker.NamedID
	ObservedAt               time.Time
	EvidenceRef              string
}

type TeamScope struct {
	Team     tracker.NamedID
	Projects []tracker.NamedID
}

type ScopeDiscovery struct {
	Account, Workspace tracker.NamedID
	Teams              []TeamScope
	ObservedAt         time.Time
	EvidenceRefs       []string
}

type pageInfo struct {
	HasNextPage *bool  `json:"hasNextPage"`
	EndCursor   string `json:"endCursor"`
}

type connection[T any] struct {
	Nodes    []T      `json:"nodes"`
	PageInfo pageInfo `json:"pageInfo"`
}

func nextPage[T any](page connection[T], previous string, seen map[string]bool) (string, error) {
	if page.Nodes == nil || page.PageInfo.HasNextPage == nil {
		return "", fail(ports.FailureProtocolDrift, "Linear connection omitted nodes or page information")
	}
	if !*page.PageInfo.HasNextPage {
		return "", nil
	}
	if page.PageInfo.EndCursor == "" || page.PageInfo.EndCursor == previous || seen[page.PageInfo.EndCursor] {
		return "", fail(ports.FailureProtocolDrift, "Linear pagination cursor did not advance")
	}
	seen[page.PageInfo.EndCursor] = true
	return page.PageInfo.EndCursor, nil
}

func (a *Adapter) Health(ctx context.Context) (HealthObservation, error) {
	var result struct {
		identityData
		Team *tracker.NamedID `json:"team"`
	}
	query := `query DarkstarHealth { ` + authoritySelection + ` }`
	variables := map[string]any{}
	if a.config.TeamID != "" {
		query = `query DarkstarHealth($team: String!) { ` + authoritySelection + ` team(id: $team) { id name } }`
		variables["team"] = a.config.TeamID
	}
	raw, err := a.query(ctx, query, variables, &result)
	if err != nil {
		return HealthObservation{}, err
	}
	if a.config.TeamID != "" && (result.Team == nil || result.Team.ID != a.config.TeamID) {
		return HealthObservation{}, fail(ports.FailurePermissionDenied, "Linear selected team is not accessible")
	}
	ref, err := a.retain(ctx, "connection-health", a.config.Endpoint, raw)
	if err != nil {
		return HealthObservation{}, err
	}
	observation := HealthObservation{Account: result.Viewer, Workspace: result.Organization, ObservedAt: a.now().UTC(), EvidenceRef: ref}
	if result.Team != nil {
		observation.Team = *result.Team
	}
	return observation, nil
}

func (a *Adapter) DiscoverScopes(ctx context.Context) (ScopeDiscovery, error) {
	health, err := a.Health(ctx)
	if err != nil {
		return ScopeDiscovery{}, err
	}
	result := ScopeDiscovery{Account: health.Account, Workspace: health.Workspace, Teams: []TeamScope{}, ObservedAt: health.ObservedAt, EvidenceRefs: []string{health.EvidenceRef}}
	// Bootstrap credentials may rotate while discovery is in progress. Freeze
	// the first observed authority for all remaining pages in this discovery.
	bound := *a
	bound.config.AccountID = health.Account.ID
	bound.config.WorkspaceID = health.Workspace.ID
	after := ""
	seen := make(map[string]bool)
	teamIDs := make(map[string]bool)
	for count := 0; count < 100; count++ {
		var data struct {
			Teams connection[tracker.NamedID] `json:"teams"`
		}
		raw, err := bound.query(ctx, `query DarkstarTeams($after: String) { `+authoritySelection+` teams(first: 50, after: $after) { nodes { id name } pageInfo { hasNextPage endCursor } } }`, map[string]any{"after": nullableCursor(after)}, &data)
		if err != nil {
			return ScopeDiscovery{}, err
		}
		next, err := nextPage(data.Teams, after, seen)
		if err != nil {
			return ScopeDiscovery{}, err
		}
		ref, err := a.retain(ctx, "team-discovery", a.config.Endpoint, raw)
		if err != nil {
			return ScopeDiscovery{}, err
		}
		result.EvidenceRefs = append(result.EvidenceRefs, ref)
		for _, team := range data.Teams.Nodes {
			if team.ID == "" || teamIDs[team.ID] {
				return ScopeDiscovery{}, fail(ports.FailureProtocolDrift, "Linear team discovery returned missing or duplicate identities")
			}
			teamIDs[team.ID] = true
			projects, refs, err := bound.projects(ctx, team.ID)
			if err != nil {
				return ScopeDiscovery{}, err
			}
			result.Teams = append(result.Teams, TeamScope{Team: team, Projects: projects})
			result.EvidenceRefs = append(result.EvidenceRefs, refs...)
		}
		if next == "" {
			return result, nil
		}
		after = next
	}
	return ScopeDiscovery{}, fail(ports.FailureResourceExhausted, "Linear team discovery exceeded the supported page limit")
}

func (a *Adapter) projects(ctx context.Context, teamID string) ([]tracker.NamedID, []string, error) {
	projects := make([]tracker.NamedID, 0)
	refs := make([]string, 0)
	after := ""
	seen := make(map[string]bool)
	ids := make(map[string]bool)
	for count := 0; count < 100; count++ {
		var data struct {
			Team *struct {
				ID       string                      `json:"id"`
				Projects connection[tracker.NamedID] `json:"projects"`
			} `json:"team"`
		}
		raw, err := a.query(ctx, `query DarkstarProjects($team: String!, $after: String) { `+authoritySelection+` team(id: $team) { id projects(first: 50, after: $after) { nodes { id name } pageInfo { hasNextPage endCursor } } } }`, map[string]any{"team": teamID, "after": nullableCursor(after)}, &data)
		if err != nil {
			return nil, nil, err
		}
		if data.Team == nil || data.Team.ID != teamID {
			return nil, nil, fail(ports.FailurePermissionDenied, "Linear team became inaccessible during discovery")
		}
		next, err := nextPage(data.Team.Projects, after, seen)
		if err != nil {
			return nil, nil, err
		}
		ref, err := a.retain(ctx, "project-discovery", a.config.Endpoint, raw)
		if err != nil {
			return nil, nil, err
		}
		refs = append(refs, ref)
		for _, project := range data.Team.Projects.Nodes {
			if project.ID == "" || ids[project.ID] {
				return nil, nil, fail(ports.FailureProtocolDrift, "Linear project discovery returned missing or duplicate identities")
			}
			ids[project.ID] = true
			projects = append(projects, project)
		}
		if next == "" {
			return projects, refs, nil
		}
		after = next
	}
	return nil, nil, fail(ports.FailureResourceExhausted, "Linear project discovery exceeded the supported page limit")
}

func (a *Adapter) Discover(ctx context.Context, config tracker.AdapterConfigPin) (tracker.Manifest, error) {
	if config != a.pin.AdapterConfigPin {
		return tracker.Manifest{}, fail(ports.FailureProtocolDrift, "Linear adapter configuration differs from the requested pin")
	}
	if err := a.requireBound(); err != nil {
		return tracker.Manifest{}, err
	}
	health, err := a.Health(ctx)
	if err != nil {
		return tracker.Manifest{}, err
	}
	if a.config.ProjectID != "" {
		projects, _, err := a.projects(ctx, a.config.TeamID)
		if err != nil {
			return tracker.Manifest{}, err
		}
		found := false
		for _, project := range projects {
			if project.ID == a.config.ProjectID {
				found = true
			}
		}
		if !found {
			return tracker.Manifest{}, fail(ports.FailurePermissionDenied, "Linear selected project is not accessible in the selected team")
		}
	}
	return a.manifest(health.ObservedAt, health.EvidenceRef), nil
}

func (a *Adapter) manifest(observedAt time.Time, evidenceRef string) tracker.Manifest {
	capabilities := make(map[tracker.Capability]tracker.Knowledge[bool])
	for _, capability := range []tracker.Capability{tracker.Fetch, tracker.List, tracker.Search, tracker.Filter, tracker.Page, tracker.Refresh} {
		capabilities[capability] = tracker.Known[bool]{Value: true}
	}
	for _, capability := range []tracker.Capability{tracker.Create, tracker.Edit, tracker.Progress, tracker.Transitions, tracker.Hierarchy, tracker.Dependencies, tracker.ManagedLinks, tracker.Reconciliation} {
		capabilities[capability] = tracker.Unsupported[bool]{Reason: "this Linear installation exposes source reads only"}
	}
	return tracker.Manifest{Pin: a.pin, Scope: a.scope, Capabilities: capabilities, Filters: map[string][]tracker.FilterOperator{"business_state": {tracker.Equals, tracker.In}, "assignee": {tracker.Equals, tracker.In}, "label": {tracker.Equals, tracker.In}, "priority": {tracker.Equals, tracker.In}, "project": {tracker.Equals, tracker.In}, "updated_at": {tracker.After}}, MaxPageSize: 50, ObservedAt: observedAt, EvidenceRef: evidenceRef}
}

func (a *Adapter) requireBound() error {
	if a.config.WorkspaceID == "" || a.config.TeamID == "" || a.config.AccountID == "" {
		return fail(ports.FailureInvalidRequest, "Linear source reads require exact account, workspace and team selection")
	}
	return trackercontract.ValidatePin(a.pin, a.pin)
}

func nullableCursor(value string) any {
	if value == "" {
		return nil
	}
	return value
}
