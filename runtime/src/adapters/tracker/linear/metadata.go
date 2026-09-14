package linear

import (
	"context"
	"fmt"
	"strings"

	"darkstar/src/core/trackercontract"
	"darkstar/src/ports"
	"darkstar/src/ports/tracker"
	"darkstar/src/ports/worksource"
)

var _ worksource.TrackerMetadataV1 = (*Adapter)(nil)

func (a *Adapter) DiscoverFieldCatalog(ctx context.Context, pin tracker.Pin) ([]tracker.FieldCatalog, error) {
	if err := trackercontract.ValidatePin(pin, a.pin); err != nil {
		return nil, err
	}
	if err := a.requireBound(); err != nil {
		return nil, err
	}
	states, err := a.discoverFieldValues(ctx, "states")
	if err != nil {
		return nil, err
	}
	cycles, err := a.discoverFieldValues(ctx, "cycles")
	if err != nil {
		return nil, err
	}
	return []tracker.FieldCatalog{{Identity: tracker.NamedID{ID: "state", Name: "Workflow status"}, Values: states}, {Identity: tracker.NamedID{ID: "sprint", Name: "Cycle"}, Values: cycles}}, nil
}

func (a *Adapter) discoverFieldValues(ctx context.Context, field string) ([]tracker.NamedID, error) {
	selection := "id name"
	if field == "cycles" {
		selection += " number"
	} else if field != "states" {
		return nil, fail(ports.FailureUnsupported, "unsupported Linear metadata field")
	}
	values := []tracker.NamedID{}
	ids := map[string]bool{}
	seen := map[string]bool{}
	after := ""
	for pageIndex := 0; pageIndex < 20; pageIndex++ {
		var result struct {
			Team *struct {
				ID     string `json:"id"`
				Values connection[struct {
					ID     string `json:"id"`
					Name   string `json:"name"`
					Number int    `json:"number"`
				}] `json:"values"`
			} `json:"team"`
		}
		query := `query DarkstarFieldCatalog($team: String!, $after: String) { ` + authoritySelection + ` team(id: $team) { id values: ` + field + `(first: 50, after: $after) { nodes { ` + selection + ` } pageInfo { hasNextPage endCursor } } } }`
		raw, err := a.query(ctx, query, map[string]any{"team": a.config.TeamID, "after": nullableCursor(after)}, &result)
		if err != nil {
			return nil, err
		}
		if result.Team == nil || result.Team.ID != a.config.TeamID {
			return nil, fail(ports.FailurePermissionDenied, "Linear metadata is not from the selected team")
		}
		next, err := nextPage(result.Team.Values, after, seen)
		if err != nil {
			return nil, err
		}
		if _, err := a.retain(ctx, "field-catalog-"+field, a.config.Endpoint, raw); err != nil {
			return nil, err
		}
		for _, value := range result.Team.Values.Nodes {
			if value.ID == "" || ids[value.ID] {
				return nil, fail(ports.FailureProtocolDrift, "Linear metadata contains missing or duplicate stable IDs")
			}
			name := value.Name
			if strings.TrimSpace(name) == "" && field == "cycles" && value.Number > 0 {
				name = fmt.Sprintf("Cycle %d", value.Number)
			}
			if strings.TrimSpace(name) == "" {
				return nil, fail(ports.FailureProtocolDrift, "Linear metadata omitted its readable label")
			}
			ids[value.ID] = true
			values = append(values, tracker.NamedID{ID: value.ID, Name: name})
		}
		if next == "" {
			return values, nil
		}
		after = next
	}
	return nil, fail(ports.FailureResourceExhausted, "Linear metadata exceeded the bounded discovery window")
}
