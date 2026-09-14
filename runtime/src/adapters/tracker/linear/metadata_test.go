package linear

import (
	"context"
	"net/http"
	"strings"
	"testing"
)

func TestFieldCatalogPreservesCustomStatusIDsAndSeparatesCycles(t *testing.T) {
	adapter, evidence, _ := fixture(t, func(w http.ResponseWriter, r *http.Request, request queryRequest) {
		if request.Variables["team"] != "team" {
			t.Error("catalog escaped its selected team")
		}
		values := []any{}
		next := ""
		if strings.Contains(request.Query, "values: states") {
			if request.Variables["after"] == nil {
				values = []any{map[string]any{"id": "uuid-ready", "name": "Ready for Dev"}}
				next = "second"
			} else {
				values = []any{map[string]any{"id": "uuid-test", "name": "Ready for Test"}}
			}
		} else if strings.Contains(request.Query, "values: cycles") {
			values = []any{map[string]any{"id": "uuid-cycle", "name": nil, "number": 42}}
		} else {
			t.Error("unexpected metadata query")
		}
		respond(w, map[string]any{"team": map[string]any{"id": "team", "values": page(values, next)}})
	})
	fields, err := adapter.DiscoverFieldCatalog(context.Background(), adapter.pin)
	if err != nil || len(fields) != 2 || len(fields[0].Values) != 2 || fields[0].Values[0].ID != "uuid-ready" || fields[0].Values[0].Name != "Ready for Dev" || fields[1].Identity.ID != "sprint" || fields[1].Values[0].Name != "Cycle 42" {
		t.Fatalf("catalog lost stable metadata: %#v %v", fields, err)
	}
	if len(evidence.records) != 3 {
		t.Fatalf("metadata pages lost evidence: %d", len(evidence.records))
	}
}

func TestFieldCatalogRejectsDuplicatesAndWrongTeam(t *testing.T) {
	for _, wrongTeam := range []bool{false, true} {
		adapter, _, _ := fixture(t, func(w http.ResponseWriter, r *http.Request, request queryRequest) {
			team := "team"
			if wrongTeam {
				team = "another-team"
			}
			respond(w, map[string]any{"team": map[string]any{"id": team, "values": page([]any{map[string]any{"id": "state", "name": "First"}, map[string]any{"id": "state", "name": "Second"}}, "")}})
		})
		if _, err := adapter.DiscoverFieldCatalog(context.Background(), adapter.pin); err == nil {
			t.Fatalf("contradictory metadata accepted, wrong team: %v", wrongTeam)
		}
	}
}
