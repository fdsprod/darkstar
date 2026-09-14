package githubissues

import (
	"context"
	"testing"
)

func TestIssueCatalogKeepsBoundedStateAndReasonWithoutSprint(t *testing.T) {
	adapter, _, _ := setup(t)
	fields, err := adapter.DiscoverFieldCatalog(context.Background(), adapter.pin)
	if err != nil || len(fields) != 2 || fields[0].Identity.ID != "state" || fields[1].Identity.ID != "state_reason" || fields[0].Values[0].ID != "open" {
		t.Fatalf("GitHub metadata: %#v %v", fields, err)
	}
	changed := adapter.pin
	changed.CapabilitiesDigest = "changed"
	if _, err := adapter.DiscoverFieldCatalog(context.Background(), changed); err == nil {
		t.Fatal("metadata discovery accepted a changed pin")
	}
}
