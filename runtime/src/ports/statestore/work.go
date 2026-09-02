package statestore

import "time"

// ProjectStatus is the closed lifecycle of a registered project.
type ProjectStatus string

const (
	ProjectActive   ProjectStatus = "active"
	ProjectArchived ProjectStatus = "archived"
)

func (status ProjectStatus) Terminal() bool { return status == ProjectArchived }

// ProjectProjection is the rebuildable current state of one repository-backed project.
type ProjectProjection struct {
	ProjectID          string        `json:"id"`
	Name               string        `json:"name"`
	SourceHash         string        `json:"sourceHash"`
	Status             ProjectStatus `json:"status"`
	ResourceVersion    uint64        `json:"resourceVersion"`
	LastGlobalPosition uint64        `json:"lastGlobalPosition"`
	CreatedAt          time.Time     `json:"createdAt"`
	UpdatedAt          time.Time     `json:"updatedAt"`
}

// WorkItemStatus is the closed lifecycle of one requested outcome.
type WorkItemStatus string

const (
	WorkItemOpen      WorkItemStatus = "open"
	WorkItemActive    WorkItemStatus = "active"
	WorkItemCompleted WorkItemStatus = "completed"
	WorkItemCancelled WorkItemStatus = "cancelled"
)

func (status WorkItemStatus) Terminal() bool {
	return status == WorkItemCompleted || status == WorkItemCancelled
}

// WorkItemProjection is the rebuildable current state of one project outcome.
type WorkItemProjection struct {
	WorkItemID         string         `json:"id"`
	ProjectID          string         `json:"projectId"`
	Title              string         `json:"title"`
	SourceHash         string         `json:"sourceHash"`
	Priority           int            `json:"priority"`
	Status             WorkItemStatus `json:"status"`
	ResourceVersion    uint64         `json:"resourceVersion"`
	LastGlobalPosition uint64         `json:"lastGlobalPosition"`
	CreatedAt          time.Time      `json:"createdAt"`
	UpdatedAt          time.Time      `json:"updatedAt"`
}

// StoryStatus is the closed lifecycle of one accepted-plan outcome.
type StoryStatus string

const (
	StoryPlanned   StoryStatus = "planned"
	StoryReady     StoryStatus = "ready"
	StoryRunning   StoryStatus = "running"
	StoryCompleted StoryStatus = "completed"
	StoryCancelled StoryStatus = "cancelled"
	StoryRetired   StoryStatus = "retired"
)

func (status StoryStatus) Terminal() bool {
	return status == StoryCompleted || status == StoryCancelled || status == StoryRetired
}

// StoryProjection is the rebuildable current state of one versioned story.
type StoryProjection struct {
	StoryID            string      `json:"id"`
	WorkItemID         string      `json:"workItemId"`
	Title              string      `json:"title"`
	SourceHash         string      `json:"sourceHash"`
	Priority           int         `json:"priority"`
	Position           int         `json:"position"`
	Status             StoryStatus `json:"status"`
	ResourceVersion    uint64      `json:"resourceVersion"`
	LastGlobalPosition uint64      `json:"lastGlobalPosition"`
	CreatedAt          time.Time   `json:"createdAt"`
	UpdatedAt          time.Time   `json:"updatedAt"`
}

// PointStatus follows the point-revision state machine in the accepted work model.
type PointStatus string

const (
	PointPlanned           PointStatus = "planned"
	PointReady             PointStatus = "ready"
	PointRunning           PointStatus = "running"
	PointValidating        PointStatus = "validating"
	PointAwaitingApproval  PointStatus = "awaiting_approval"
	PointAccepted          PointStatus = "accepted"
	PointCommitted         PointStatus = "committed"
	PointPublished         PointStatus = "published"
	PointFailed            PointStatus = "failed"
	PointRejected          PointStatus = "rejected"
	PointSuperseded        PointStatus = "superseded"
	PointReconcileRequired PointStatus = "reconcile_required"
)

// Terminal reports whether a point revision has no normal forward transition.
func (status PointStatus) Terminal() bool {
	switch status {
	case PointPublished, PointFailed, PointRejected, PointSuperseded, PointReconcileRequired:
		return true
	default:
		return false
	}
}

// PointProjection is the current revision of one stable implementation point.
// Dependencies are point IDs in deterministic order and belong to this revision.
type PointProjection struct {
	PointID            string      `json:"id"`
	StoryID            string      `json:"storyId"`
	Revision           uint64      `json:"revision"`
	Title              string      `json:"title"`
	SourceHash         string      `json:"sourceHash"`
	Priority           int         `json:"priority"`
	Position           int         `json:"position"`
	Dependencies       []string    `json:"dependencies"`
	Status             PointStatus `json:"status"`
	ResourceVersion    uint64      `json:"resourceVersion"`
	LastGlobalPosition uint64      `json:"lastGlobalPosition"`
	CreatedAt          time.Time   `json:"createdAt"`
	UpdatedAt          time.Time   `json:"updatedAt"`
}
