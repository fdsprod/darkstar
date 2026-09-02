package sqlite

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"darkstar/src/ports/statestore"
)

const aggregateSourceHash = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func TestWorkAggregateHierarchyPersistsAndRebuilds(t *testing.T) {
	t.Parallel()
	database := openEventTestDatabase(t)
	ctx := context.Background()

	projectID := testID("project", 'A')
	workID := testID("work", 'B')
	storyID := testID("story", 'C')
	dependencyID := testID("point", 'D')
	pointID := testID("point", 'E')
	runID := testID("run", 'F')
	attemptID := testID("attempt", 'G')

	_, err := database.Append(ctx,
		pendingEvent(testID("event", 'A'), statestore.AggregateProject, projectID, 0, "project.created",
			`{"name":"DARKSTAR","sourceHash":"`+aggregateSourceHash+`"}`),
		pendingEvent(testID("event", 'B'), statestore.AggregateWork, workID, 0, "work.created",
			`{"projectId":"`+projectID+`","title":"Ship aggregates","sourceHash":"`+aggregateSourceHash+`","priority":90}`),
		pendingEvent(testID("event", 'C'), statestore.AggregateStory, storyID, 0, "story.created",
			`{"workItemId":"`+workID+`","title":"Persist relationships","sourceHash":"`+aggregateSourceHash+`","priority":80,"position":1}`),
		pendingEvent(testID("event", 'D'), statestore.AggregatePoint, dependencyID, 0, "point.created",
			`{"storyId":"`+storyID+`","revision":1,"title":"Create schema","sourceHash":"`+aggregateSourceHash+`","priority":70,"position":1,"dependencies":[]}`),
		pendingEvent(testID("event", 'E'), statestore.AggregatePoint, pointID, 0, "point.created",
			`{"storyId":"`+storyID+`","revision":1,"title":"Expose queries","sourceHash":"`+aggregateSourceHash+`","priority":60,"position":2,"dependencies":["`+dependencyID+`"]}`),
		pendingEvent(testID("event", 'F'), statestore.AggregateRun, runID, 0, "run.created",
			`{"workItemId":"`+workID+`","workflowId":"delivery","workflowVersion":"1","priority":50}`),
		pendingEvent(testID("event", 'G'), statestore.AggregateRun, runID, 1, "run.route_frozen",
			`{"workflowDigest":"`+aggregateSourceHash+`","routeDigest":"`+strings.Repeat("a", 64)+`","routeSnapshot":{"entry":"plan","terminals":["validated"]}}`),
		pendingEvent(testID("event", 'H'), statestore.AggregateAttempt, attemptID, 0, "attempt.created",
			`{"runId":"`+runID+`","pointId":"`+pointID+`","pointRevision":1,"priority":60,"scenario":"delivery","provider":"fake","logReference":"attempt.log"}`),
	)
	if err != nil {
		t.Fatalf("append hierarchy: %v", err)
	}

	assertWorkHierarchy(t, database, projectID, workID, storyID, dependencyID, pointID, runID, attemptID)
	if err := database.RebuildProjections(ctx); err != nil {
		t.Fatalf("rebuild projections: %v", err)
	}
	assertWorkHierarchy(t, database, projectID, workID, storyID, dependencyID, pointID, runID, attemptID)
}

func assertWorkHierarchy(t *testing.T, database *Database, projectID, workID, storyID, dependencyID, pointID, runID, attemptID string) {
	t.Helper()
	ctx := context.Background()
	project, err := database.Project(ctx, projectID)
	if err != nil || project.SourceHash != aggregateSourceHash || project.Status != statestore.ProjectActive {
		t.Fatalf("project = (%#v, %v)", project, err)
	}
	workItems, err := database.WorkItemsForProject(ctx, projectID)
	if err != nil || len(workItems) != 1 || workItems[0].WorkItemID != workID || workItems[0].Priority != 90 {
		t.Fatalf("work items = (%#v, %v)", workItems, err)
	}
	stories, err := database.StoriesForWorkItem(ctx, workID)
	if err != nil || len(stories) != 1 || stories[0].StoryID != storyID {
		t.Fatalf("stories = (%#v, %v)", stories, err)
	}
	points, err := database.PointsForStory(ctx, storyID)
	if err != nil || len(points) != 2 || points[1].PointID != pointID || !reflect.DeepEqual(points[1].Dependencies, []string{dependencyID}) {
		t.Fatalf("points = (%#v, %v)", points, err)
	}
	runs, err := database.RunsForWorkItem(ctx, workID)
	if err != nil || len(runs) != 1 || runs[0].RunID != runID || runs[0].Priority != 50 || runs[0].RouteDigest != strings.Repeat("a", 64) {
		t.Fatalf("runs = (%#v, %v)", runs, err)
	}
	if string(runs[0].RouteSnapshot) != `{"entry":"plan","terminals":["validated"]}` {
		t.Fatalf("route snapshot = %s", runs[0].RouteSnapshot)
	}
	attempts, err := database.AttemptsForPoint(ctx, pointID, 1)
	if err != nil || len(attempts) != 1 || attempts[0].AttemptID != attemptID || attempts[0].PointRevision != 1 {
		t.Fatalf("point attempts = (%#v, %v)", attempts, err)
	}
}

func TestWorkHierarchyRejectsMissingParent(t *testing.T) {
	t.Parallel()
	database := openEventTestDatabase(t)
	_, err := database.Append(context.Background(), pendingEvent(testID("event", 'J'), statestore.AggregateWork, testID("work", 'J'), 0, "work.created",
		`{"projectId":"`+testID("project", 'J')+`","title":"Orphan","sourceHash":"`+aggregateSourceHash+`","priority":1}`))
	if err == nil {
		t.Fatal("orphan work item unexpectedly persisted")
	}
}
