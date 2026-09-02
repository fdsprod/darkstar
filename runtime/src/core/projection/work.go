package projection

import (
	"errors"
	"fmt"
	"regexp"
	"sort"
	"time"

	"darkstar/src/ports/statestore"
)

var sourceHashPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

// ReduceProject applies an event to a project projection.
func ReduceProject(current *statestore.ProjectProjection, event statestore.Event) (statestore.ProjectProjection, bool, error) {
	if event.SchemaVersion != 1 {
		return statestore.ProjectProjection{}, false, &UnsupportedSchemaVersionError{EventID: event.ID, Version: event.SchemaVersion}
	}
	if event.AggregateType != statestore.AggregateProject {
		return statestore.ProjectProjection{}, false, nil
	}
	if current == nil {
		if event.Kind != "project.created" {
			return statestore.ProjectProjection{}, true, fmt.Errorf("project %s first event is %s, want project.created", event.AggregateID, event.Kind)
		}
		var data struct{ Name, SourceHash string }
		if err := decodeData(event, &data); err != nil {
			return statestore.ProjectProjection{}, true, err
		}
		if data.Name == "" || !validSourceHash(data.SourceHash) {
			return statestore.ProjectProjection{}, true, errors.New("project.created requires name and a SHA-256 sourceHash")
		}
		return statestore.ProjectProjection{ProjectID: event.AggregateID, Name: data.Name, SourceHash: data.SourceHash, Status: statestore.ProjectActive,
			ResourceVersion: event.AggregateRevision, LastGlobalPosition: event.GlobalPosition, CreatedAt: event.RecordedAt, UpdatedAt: event.RecordedAt}, true, nil
	}
	if err := validateCurrent("project", current.ProjectID, current.ResourceVersion, event); err != nil {
		return statestore.ProjectProjection{}, true, err
	}
	next := *current
	switch event.Kind {
	case "project.archived":
		if current.Status != statestore.ProjectActive {
			return statestore.ProjectProjection{}, true, invalidTransition("project", current.ProjectID, string(current.Status), event.Kind)
		}
		next.Status = statestore.ProjectArchived
	default:
		return statestore.ProjectProjection{}, true, invalidTransition("project", current.ProjectID, string(current.Status), event.Kind)
	}
	advance(&next.ResourceVersion, &next.LastGlobalPosition, &next.UpdatedAt, event)
	return next, true, nil
}

// ReduceWorkItem applies an event to a work-item projection.
func ReduceWorkItem(current *statestore.WorkItemProjection, event statestore.Event) (statestore.WorkItemProjection, bool, error) {
	if event.SchemaVersion != 1 {
		return statestore.WorkItemProjection{}, false, &UnsupportedSchemaVersionError{EventID: event.ID, Version: event.SchemaVersion}
	}
	if event.AggregateType != statestore.AggregateWork {
		return statestore.WorkItemProjection{}, false, nil
	}
	if current == nil {
		if event.Kind != "work.created" {
			return statestore.WorkItemProjection{}, true, fmt.Errorf("work item %s first event is %s, want work.created", event.AggregateID, event.Kind)
		}
		var data struct {
			ProjectID  string `json:"projectId"`
			Title      string `json:"title"`
			SourceHash string `json:"sourceHash"`
			Priority   int    `json:"priority"`
		}
		if err := decodeData(event, &data); err != nil {
			return statestore.WorkItemProjection{}, true, err
		}
		if data.ProjectID == "" || data.Title == "" || !validSourceHash(data.SourceHash) || data.Priority < 0 {
			return statestore.WorkItemProjection{}, true, errors.New("work.created requires projectId, title, SHA-256 sourceHash, and non-negative priority")
		}
		return statestore.WorkItemProjection{WorkItemID: event.AggregateID, ProjectID: data.ProjectID, Title: data.Title, SourceHash: data.SourceHash, Priority: data.Priority, Status: statestore.WorkItemOpen,
			ResourceVersion: event.AggregateRevision, LastGlobalPosition: event.GlobalPosition, CreatedAt: event.RecordedAt, UpdatedAt: event.RecordedAt}, true, nil
	}
	if err := validateCurrent("work item", current.WorkItemID, current.ResourceVersion, event); err != nil {
		return statestore.WorkItemProjection{}, true, err
	}
	next := *current
	switch event.Kind {
	case "work.started":
		if current.Status != statestore.WorkItemOpen {
			return statestore.WorkItemProjection{}, true, invalidTransition("work item", current.WorkItemID, string(current.Status), event.Kind)
		}
		next.Status = statestore.WorkItemActive
	case "work.completed":
		if current.Status != statestore.WorkItemOpen && current.Status != statestore.WorkItemActive {
			return statestore.WorkItemProjection{}, true, invalidTransition("work item", current.WorkItemID, string(current.Status), event.Kind)
		}
		next.Status = statestore.WorkItemCompleted
	case "work.cancelled":
		if current.Status.Terminal() {
			return statestore.WorkItemProjection{}, true, invalidTransition("work item", current.WorkItemID, string(current.Status), event.Kind)
		}
		next.Status = statestore.WorkItemCancelled
	default:
		return statestore.WorkItemProjection{}, true, invalidTransition("work item", current.WorkItemID, string(current.Status), event.Kind)
	}
	advance(&next.ResourceVersion, &next.LastGlobalPosition, &next.UpdatedAt, event)
	return next, true, nil
}

// ReduceStory applies an event to a story projection.
func ReduceStory(current *statestore.StoryProjection, event statestore.Event) (statestore.StoryProjection, bool, error) {
	if event.SchemaVersion != 1 {
		return statestore.StoryProjection{}, false, &UnsupportedSchemaVersionError{EventID: event.ID, Version: event.SchemaVersion}
	}
	if event.AggregateType != statestore.AggregateStory {
		return statestore.StoryProjection{}, false, nil
	}
	if current == nil {
		if event.Kind != "story.created" {
			return statestore.StoryProjection{}, true, fmt.Errorf("story %s first event is %s, want story.created", event.AggregateID, event.Kind)
		}
		var data struct {
			WorkItemID string `json:"workItemId"`
			Title      string `json:"title"`
			SourceHash string `json:"sourceHash"`
			Priority   int    `json:"priority"`
			Position   int    `json:"position"`
		}
		if err := decodeData(event, &data); err != nil {
			return statestore.StoryProjection{}, true, err
		}
		if data.WorkItemID == "" || data.Title == "" || !validSourceHash(data.SourceHash) || data.Priority < 0 || data.Position < 0 {
			return statestore.StoryProjection{}, true, errors.New("story.created requires workItemId, title, SHA-256 sourceHash, non-negative priority, and non-negative position")
		}
		return statestore.StoryProjection{StoryID: event.AggregateID, WorkItemID: data.WorkItemID, Title: data.Title, SourceHash: data.SourceHash, Priority: data.Priority, Position: data.Position, Status: statestore.StoryPlanned, ResourceVersion: event.AggregateRevision, LastGlobalPosition: event.GlobalPosition, CreatedAt: event.RecordedAt, UpdatedAt: event.RecordedAt}, true, nil
	}
	if err := validateCurrent("story", current.StoryID, current.ResourceVersion, event); err != nil {
		return statestore.StoryProjection{}, true, err
	}
	next := *current
	switch event.Kind {
	case "story.ready":
		if current.Status != statestore.StoryPlanned {
			return statestore.StoryProjection{}, true, invalidTransition("story", current.StoryID, string(current.Status), event.Kind)
		}
		next.Status = statestore.StoryReady
	case "story.started":
		if current.Status != statestore.StoryReady {
			return statestore.StoryProjection{}, true, invalidTransition("story", current.StoryID, string(current.Status), event.Kind)
		}
		next.Status = statestore.StoryRunning
	case "story.completed":
		if current.Status != statestore.StoryRunning {
			return statestore.StoryProjection{}, true, invalidTransition("story", current.StoryID, string(current.Status), event.Kind)
		}
		next.Status = statestore.StoryCompleted
	case "story.cancelled":
		if current.Status.Terminal() {
			return statestore.StoryProjection{}, true, invalidTransition("story", current.StoryID, string(current.Status), event.Kind)
		}
		next.Status = statestore.StoryCancelled
	case "story.retired":
		if current.Status == statestore.StoryRunning || current.Status.Terminal() {
			return statestore.StoryProjection{}, true, invalidTransition("story", current.StoryID, string(current.Status), event.Kind)
		}
		next.Status = statestore.StoryRetired
	default:
		return statestore.StoryProjection{}, true, invalidTransition("story", current.StoryID, string(current.Status), event.Kind)
	}
	advance(&next.ResourceVersion, &next.LastGlobalPosition, &next.UpdatedAt, event)
	return next, true, nil
}

// ReducePoint applies an event to the current revision of an implementation point.
func ReducePoint(current *statestore.PointProjection, event statestore.Event) (statestore.PointProjection, bool, error) {
	if event.SchemaVersion != 1 {
		return statestore.PointProjection{}, false, &UnsupportedSchemaVersionError{EventID: event.ID, Version: event.SchemaVersion}
	}
	if event.AggregateType != statestore.AggregatePoint {
		return statestore.PointProjection{}, false, nil
	}
	if current == nil {
		if event.Kind != "point.created" {
			return statestore.PointProjection{}, true, fmt.Errorf("point %s first event is %s, want point.created", event.AggregateID, event.Kind)
		}
		contract, err := decodePointContract(event, event.AggregateID, 1)
		if err != nil {
			return statestore.PointProjection{}, true, err
		}
		contract.PointID = event.AggregateID
		contract.Status = statestore.PointPlanned
		contract.ResourceVersion = event.AggregateRevision
		contract.LastGlobalPosition = event.GlobalPosition
		contract.CreatedAt = event.RecordedAt
		contract.UpdatedAt = event.RecordedAt
		return contract, true, nil
	}
	if err := validateCurrent("point", current.PointID, current.ResourceVersion, event); err != nil {
		return statestore.PointProjection{}, true, err
	}
	next := *current
	switch event.Kind {
	case "point.ready":
		if current.Status != statestore.PointPlanned {
			return statestore.PointProjection{}, true, invalidTransition("point", current.PointID, string(current.Status), event.Kind)
		}
		next.Status = statestore.PointReady
	case "point.started":
		if current.Status != statestore.PointReady {
			return statestore.PointProjection{}, true, invalidTransition("point", current.PointID, string(current.Status), event.Kind)
		}
		next.Status = statestore.PointRunning
	case "point.candidate_produced":
		if current.Status != statestore.PointRunning {
			return statestore.PointProjection{}, true, invalidTransition("point", current.PointID, string(current.Status), event.Kind)
		}
		next.Status = statestore.PointValidating
	case "point.validation_failed":
		if current.Status != statestore.PointValidating {
			return statestore.PointProjection{}, true, invalidTransition("point", current.PointID, string(current.Status), event.Kind)
		}
		next.Status = statestore.PointFailed
	case "point.awaiting_approval":
		if current.Status != statestore.PointValidating {
			return statestore.PointProjection{}, true, invalidTransition("point", current.PointID, string(current.Status), event.Kind)
		}
		next.Status = statestore.PointAwaitingApproval
	case "point.changes_requested":
		if current.Status != statestore.PointAwaitingApproval {
			return statestore.PointProjection{}, true, invalidTransition("point", current.PointID, string(current.Status), event.Kind)
		}
		next.Status = statestore.PointRunning
	case "point.accepted":
		if current.Status != statestore.PointValidating && current.Status != statestore.PointAwaitingApproval {
			return statestore.PointProjection{}, true, invalidTransition("point", current.PointID, string(current.Status), event.Kind)
		}
		next.Status = statestore.PointAccepted
	case "point.rejected":
		if current.Status != statestore.PointAwaitingApproval {
			return statestore.PointProjection{}, true, invalidTransition("point", current.PointID, string(current.Status), event.Kind)
		}
		next.Status = statestore.PointRejected
	case "point.committed":
		if current.Status != statestore.PointAccepted {
			return statestore.PointProjection{}, true, invalidTransition("point", current.PointID, string(current.Status), event.Kind)
		}
		next.Status = statestore.PointCommitted
	case "point.published":
		if current.Status != statestore.PointCommitted {
			return statestore.PointProjection{}, true, invalidTransition("point", current.PointID, string(current.Status), event.Kind)
		}
		next.Status = statestore.PointPublished
	case "point.superseded":
		if current.Status != statestore.PointCommitted && current.Status != statestore.PointPublished {
			return statestore.PointProjection{}, true, invalidTransition("point", current.PointID, string(current.Status), event.Kind)
		}
		next.Status = statestore.PointSuperseded
	case "point.revised":
		if current.Status != statestore.PointFailed && current.Status != statestore.PointRejected && current.Status != statestore.PointSuperseded {
			return statestore.PointProjection{}, true, invalidTransition("point", current.PointID, string(current.Status), event.Kind)
		}
		contract, err := decodePointContract(event, event.AggregateID, current.Revision+1)
		if err != nil {
			return statestore.PointProjection{}, true, err
		}
		contract.PointID = current.PointID
		contract.Status = statestore.PointPlanned
		contract.CreatedAt = current.CreatedAt
		next = contract
	case "point.reconcile_required":
		if current.Status.Terminal() {
			return statestore.PointProjection{}, true, invalidTransition("point", current.PointID, string(current.Status), event.Kind)
		}
		next.Status = statestore.PointReconcileRequired
	default:
		return statestore.PointProjection{}, true, invalidTransition("point", current.PointID, string(current.Status), event.Kind)
	}
	advance(&next.ResourceVersion, &next.LastGlobalPosition, &next.UpdatedAt, event)
	return next, true, nil
}

func decodePointContract(event statestore.Event, pointID string, wantRevision uint64) (statestore.PointProjection, error) {
	var data struct {
		StoryID      string   `json:"storyId"`
		Revision     uint64   `json:"revision"`
		Title        string   `json:"title"`
		SourceHash   string   `json:"sourceHash"`
		Priority     int      `json:"priority"`
		Position     int      `json:"position"`
		Dependencies []string `json:"dependencies"`
	}
	if err := decodeData(event, &data); err != nil {
		return statestore.PointProjection{}, err
	}
	if data.StoryID == "" || data.Revision != wantRevision || data.Title == "" || !validSourceHash(data.SourceHash) || data.Priority < 0 || data.Position < 0 {
		return statestore.PointProjection{}, errors.New("point contract requires storyId, the next revision, title, SHA-256 sourceHash, non-negative priority, and non-negative position")
	}
	seen := map[string]bool{}
	dependencies := append([]string(nil), data.Dependencies...)
	sort.Strings(dependencies)
	for _, dependency := range dependencies {
		if dependency == "" || dependency == pointID || seen[dependency] {
			return statestore.PointProjection{}, errors.New("point dependencies must be distinct non-empty point IDs and cannot include self")
		}
		seen[dependency] = true
	}
	return statestore.PointProjection{StoryID: data.StoryID, Revision: data.Revision, Title: data.Title, SourceHash: data.SourceHash, Priority: data.Priority, Position: data.Position, Dependencies: dependencies}, nil
}

func validSourceHash(value string) bool { return sourceHashPattern.MatchString(value) }

func validateCurrent(kind, id string, revision uint64, event statestore.Event) error {
	if id != event.AggregateID {
		return fmt.Errorf("%s projection %s cannot apply event for %s", kind, id, event.AggregateID)
	}
	if event.AggregateRevision != revision+1 {
		return fmt.Errorf("%s %s projection revision %d cannot apply revision %d", kind, id, revision, event.AggregateRevision)
	}
	return nil
}

func advance(revision, position *uint64, updatedAt *time.Time, event statestore.Event) {
	*revision = event.AggregateRevision
	*position = event.GlobalPosition
	*updatedAt = event.RecordedAt
}
