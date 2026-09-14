package builtin_test

import (
	"context"
	"testing"
)

func TestNativeCatalogExposesAllBusinessStatesIndependentlyOfCurrentTickets(t *testing.T) {
	_, adapter, manifest, _, _ := fixture(t)
	fields, err := adapter.DiscoverFieldCatalog(context.Background(), manifest.Pin)
	if err != nil || len(fields) != 2 || len(fields[0].Values) != 4 || fields[1].Identity.ID != "issue_type" {
		t.Fatalf("native catalog: %#v %v", fields, err)
	}
	seen := map[string]bool{}
	for _, value := range fields[0].Values {
		seen[value.ID] = true
	}
	if !seen["open"] || !seen["active"] || !seen["completed"] || !seen["cancelled"] {
		t.Fatalf("native states were inferred from the current ticket: %#v", seen)
	}
}
