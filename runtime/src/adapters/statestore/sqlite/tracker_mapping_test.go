package sqlite

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"darkstar/src/ports/statestore"
	"darkstar/src/ports/tracker"
)

func TestTrackerMappingStoragePreservesImmutableRevisionsAndSourceScopedActivation(t *testing.T) {
	database, state, _ := backlogValidationFixture(t)
	ctx := context.Background()
	first := statestore.TrackerMappingRevision{ProjectID: state.ProjectID, Revision: 1, BindingRevision: 1, RulesJSON: json.RawMessage(`{"version":"test","id":"first"}`), CreatedAt: time.Now().UTC()}
	if err := database.SaveTrackerMapping(ctx, first); err != nil {
		t.Fatal(err)
	}
	if err := database.SaveTrackerMapping(ctx, first); err != nil {
		t.Fatal("exact retry failed:", err)
	}
	changed := first
	changed.RulesJSON = json.RawMessage(`{"version":"test","id":"changed"}`)
	if err := database.SaveTrackerMapping(ctx, changed); err == nil {
		t.Fatal("revision overwrite accepted")
	}
	if _, err := database.SQL().ExecContext(ctx, `UPDATE tracker_mapping_revisions SET rules_json='{}' WHERE project_id=?`, state.ProjectID); err == nil {
		t.Fatal("direct SQL mutation bypassed immutable history")
	}
	if err := database.ActivateTrackerMapping(ctx, state.ProjectID, 1, 0, time.Now()); err != nil {
		t.Fatal(err)
	}
	second := first
	second.Revision = 2
	if err := database.SaveTrackerMapping(ctx, second); err != nil {
		t.Fatal(err)
	}
	if err := database.ActivateTrackerMapping(ctx, state.ProjectID, 2, 0, time.Now()); err == nil {
		t.Fatal("stale compare-and-swap activated another revision")
	}
	if err := database.ActivateTrackerMapping(ctx, state.ProjectID, 2, 1, time.Now()); err != nil {
		t.Fatal(err)
	}
	var activations int
	if err := database.SQL().QueryRowContext(ctx, `SELECT COUNT(*) FROM tracker_mapping_activations WHERE project_id=?`, state.ProjectID).Scan(&activations); err != nil || activations != 2 {
		t.Fatalf("activation audit = %d, %v", activations, err)
	}
	source := statestore.ExternalBacklogSource{ConnectionID: "external", ConnectionRevision: "1", Scope: tracker.Scope{Namespace: tracker.Namespace{Provider: "linear", Host: "linear.app", TenantID: "workspace", ScopeID: "workspace"}, ContainerID: "team"}}
	if _, err := database.SelectBacklogSource(ctx, state.ProjectID, 1, source, time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := database.ActiveTrackerMapping(ctx, state.ProjectID, 2); err == nil {
		t.Fatal("source switch silently reused old mapping")
	}
	if err := database.ActivateTrackerMapping(ctx, state.ProjectID, 1, 2, time.Now()); err == nil {
		t.Fatal("old-source mapping activated on new source")
	}
	retained, err := database.ActiveTrackerMapping(ctx, state.ProjectID, 1)
	if err != nil || retained.Revision != 2 {
		t.Fatalf("old-source active pin was lost: %#v, %v", retained, err)
	}
}
