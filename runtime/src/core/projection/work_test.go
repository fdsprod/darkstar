package projection

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"darkstar/src/ports/statestore"
)

const testSourceHash = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func TestWorkHierarchyReducersPreserveStableRelationships(t *testing.T) {
	t.Parallel()

	project, applies, err := ReduceProject(nil, workEvent(statestore.AggregateProject, "project_A", 1, "project.created", map[string]any{
		"Name": "DARKSTAR", "SourceHash": testSourceHash,
	}))
	if err != nil || !applies || project.Status != statestore.ProjectActive || project.SourceHash != testSourceHash {
		t.Fatalf("project = (%#v, %v, %v)", project, applies, err)
	}

	work, _, err := ReduceWorkItem(nil, workEvent(statestore.AggregateWork, "work_A", 1, "work.created", map[string]any{
		"projectId": project.ProjectID, "title": "Ship aggregates", "sourceHash": testSourceHash, "priority": 90,
	}))
	if err != nil || work.ProjectID != project.ProjectID || work.Status != statestore.WorkItemOpen || work.Priority != 90 {
		t.Fatalf("work item = (%#v, %v)", work, err)
	}

	story, _, err := ReduceStory(nil, workEvent(statestore.AggregateStory, "story_A", 1, "story.created", map[string]any{
		"workItemId": work.WorkItemID, "title": "Persist hierarchy", "sourceHash": testSourceHash, "priority": 80, "position": 2,
	}))
	if err != nil || story.WorkItemID != work.WorkItemID || story.Status != statestore.StoryPlanned || story.Position != 2 {
		t.Fatalf("story = (%#v, %v)", story, err)
	}

	point, _, err := ReducePoint(nil, workEvent(statestore.AggregatePoint, "point_A", 1, "point.created", map[string]any{
		"storyId": story.StoryID, "revision": 1, "title": "Add projections", "sourceHash": testSourceHash,
		"priority": 70, "position": 3, "dependencies": []string{"point_C", "point_B"},
	}))
	if err != nil || point.StoryID != story.StoryID || point.Status != statestore.PointPlanned || point.Revision != 1 {
		t.Fatalf("point = (%#v, %v)", point, err)
	}
	if !reflect.DeepEqual(point.Dependencies, []string{"point_B", "point_C"}) {
		t.Fatalf("dependencies = %#v, want deterministic order", point.Dependencies)
	}
}

func TestPointRevisionTransitionTable(t *testing.T) {
	t.Parallel()
	current := statestore.PointProjection{PointID: "point_A", Revision: 1, Status: statestore.PointPlanned, ResourceVersion: 1}
	transitions := []struct {
		event string
		want  statestore.PointStatus
	}{
		{"point.ready", statestore.PointReady},
		{"point.started", statestore.PointRunning},
		{"point.candidate_produced", statestore.PointValidating},
		{"point.awaiting_approval", statestore.PointAwaitingApproval},
		{"point.changes_requested", statestore.PointRunning},
		{"point.candidate_produced", statestore.PointValidating},
		{"point.accepted", statestore.PointAccepted},
		{"point.committed", statestore.PointCommitted},
		{"point.published", statestore.PointPublished},
		{"point.superseded", statestore.PointSuperseded},
	}
	for _, transition := range transitions {
		event := workEvent(statestore.AggregatePoint, current.PointID, current.ResourceVersion+1, transition.event, map[string]any{})
		next, applies, err := ReducePoint(&current, event)
		if err != nil || !applies || next.Status != transition.want {
			t.Fatalf("%s = (%s, %v, %v), want %s", transition.event, next.Status, applies, err, transition.want)
		}
		current = next
	}

	revised, _, err := ReducePoint(&current, workEvent(statestore.AggregatePoint, current.PointID, current.ResourceVersion+1, "point.revised", map[string]any{
		"storyId": "story_A", "revision": 2, "title": "Correct projection", "sourceHash": strings.Repeat("a", 64),
		"priority": 75, "position": 3, "dependencies": []string{"point_B"},
	}))
	if err != nil || revised.Revision != 2 || revised.Status != statestore.PointPlanned || revised.SourceHash != strings.Repeat("a", 64) {
		t.Fatalf("revised point = (%#v, %v)", revised, err)
	}
}

func TestPointRejectsContradictoryDependencies(t *testing.T) {
	t.Parallel()
	_, _, err := ReducePoint(nil, workEvent(statestore.AggregatePoint, "point_A", 1, "point.created", map[string]any{
		"storyId": "story_A", "revision": 1, "title": "Bad point", "sourceHash": testSourceHash,
		"priority": 1, "position": 1, "dependencies": []string{"point_A"},
	}))
	if err == nil {
		t.Fatal("self-dependent point unexpectedly accepted")
	}
}

func TestRunFreezesOneRouteSnapshot(t *testing.T) {
	t.Parallel()
	current := statestore.RunProjection{RunID: "run_A", Status: statestore.RunDraft, ResourceVersion: 1}
	route := json.RawMessage(`{"entry":"design","terminals":["validated"]}`)
	event := workEvent(statestore.AggregateRun, current.RunID, 2, "run.route_frozen", map[string]any{
		"workflowDigest": testSourceHash, "routeDigest": strings.Repeat("b", 64), "routeSnapshot": route,
	})
	next, _, err := ReduceRun(&current, event)
	if err != nil || next.Status != statestore.RunReady || string(next.RouteSnapshot) != string(route) {
		t.Fatalf("frozen run = (%#v, %v)", next, err)
	}
}

func TestAttemptRequiresExactlyOneExecutionOwner(t *testing.T) {
	t.Parallel()
	base := map[string]any{"runId": "run_A", "scenario": "delivery", "provider": "fake", "logReference": "log"}
	for name, owner := range map[string]map[string]any{
		"visit":          {"visitId": "visit_A", "nodeId": "design"},
		"point revision": {"pointId": "point_A", "pointRevision": 2},
	} {
		t.Run(name, func(t *testing.T) {
			data := cloneAnyMap(base)
			for key, value := range owner {
				data[key] = value
			}
			attempt, _, err := ReduceAttempt(nil, workEvent(statestore.AggregateAttempt, "attempt_A", 1, "attempt.created", data))
			if err != nil {
				t.Fatalf("valid owner rejected: %v", err)
			}
			if name == "point revision" && (attempt.PointID != "point_A" || attempt.PointRevision != 2) {
				t.Fatalf("point owner = %#v", attempt)
			}
		})
	}
	contradictory := cloneAnyMap(base)
	contradictory["visitId"], contradictory["nodeId"] = "visit_A", "design"
	contradictory["pointId"], contradictory["pointRevision"] = "point_A", 1
	if _, _, err := ReduceAttempt(nil, workEvent(statestore.AggregateAttempt, "attempt_A", 1, "attempt.created", contradictory)); err == nil {
		t.Fatal("attempt with two owners unexpectedly accepted")
	}
}

func workEvent(kind statestore.AggregateType, id string, revision uint64, eventKind string, data any) statestore.Event {
	encoded, _ := json.Marshal(data)
	return statestore.Event{SchemaVersion: 1, ID: "event_A", AggregateType: kind, AggregateID: id,
		AggregateRevision: revision, Kind: eventKind, GlobalPosition: revision, RecordedAt: time.Unix(int64(revision), 0).UTC(), Data: encoded}
}

func cloneAnyMap(source map[string]any) map[string]any {
	result := make(map[string]any, len(source))
	for key, value := range source {
		result[key] = value
	}
	return result
}
