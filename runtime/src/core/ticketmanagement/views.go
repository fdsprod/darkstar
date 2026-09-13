package ticketmanagement

import (
	"strconv"
	"time"

	"darkstar/src/ports"
	"darkstar/src/ports/tracker"
)

// These versioned native API projections contain no sealed tracker interfaces.
// Unknown provider data cannot accidentally appear as known empty business data.
type NamedID struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type Ticket struct {
	ID            string    `json:"id"`
	ProjectID     string    `json:"projectId"`
	Revision      string    `json:"revision"`
	Key           string    `json:"key"`
	URL           string    `json:"url"`
	Title         string    `json:"title"`
	Description   string    `json:"description"`
	BusinessState NamedID   `json:"businessState"`
	Priority      int       `json:"priority"`
	Assignees     []NamedID `json:"assignees"`
	Labels        []NamedID `json:"labels"`
	EvidenceRef   string    `json:"evidenceRef"`
	ObservedAt    time.Time `json:"observedAt"`
}

type Page struct {
	SchemaVersion int      `json:"schemaVersion"`
	Tickets       []Ticket `json:"tickets"`
	NextCursor    string   `json:"nextCursor"`
}

type Capabilities struct {
	Edit        bool `json:"edit"`
	Transitions bool `json:"transitions"`
}

type Field struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Kind     string `json:"kind"`
	Required bool   `json:"required"`
}

type Transition struct {
	ID      string  `json:"id"`
	Name    string  `json:"name"`
	ToState NamedID `json:"toState"`
}

type History struct {
	Revision      string    `json:"revision"`
	Kind          string    `json:"kind"`
	RecordedAt    time.Time `json:"recordedAt"`
	EvidenceRef   string    `json:"evidenceRef"`
	Title         string    `json:"title"`
	Description   string    `json:"description"`
	BusinessState string    `json:"businessState"`
	Priority      int       `json:"priority"`
}

type Detail struct {
	SchemaVersion int          `json:"schemaVersion"`
	Ticket        Ticket       `json:"ticket"`
	Capabilities  Capabilities `json:"capabilities"`
	Fields        []Field      `json:"fields"`
	Transitions   []Transition `json:"transitions"`
	History       []History    `json:"history"`
}

type EditRequest struct {
	SchemaVersion int       `json:"schemaVersion"`
	Revision      string    `json:"revision"`
	Title         *string   `json:"title,omitempty"`
	Description   *string   `json:"description,omitempty"`
	Priority      *int      `json:"priority,omitempty"`
	Assignees     *[]string `json:"assignees,omitempty"`
	Labels        *[]string `json:"labels,omitempty"`
}

type TransitionRequest struct {
	SchemaVersion int    `json:"schemaVersion"`
	Revision      string `json:"revision"`
	TransitionID  string `json:"transitionId"`
}

func named(value tracker.NamedID) NamedID {
	return NamedID{ID: value.ID, Name: value.Name}
}

func projectTicket(projectID string, value tracker.Ticket) (Ticket, error) {
	state, stateOK := value.BusinessState.(tracker.Known[tracker.NamedID])
	priority, priorityOK := value.Priority.(tracker.Known[tracker.NamedID])
	assignees, assigneesOK := value.Assignees.(tracker.Known[[]tracker.NamedID])
	labels, labelsOK := value.Labels.(tracker.Known[[]tracker.NamedID])
	fresh, freshOK := value.Freshness.(tracker.Fresh)
	if !stateOK || !priorityOK || !assigneesOK || !labelsOK || !freshOK {
		return Ticket{}, failure(ports.FailureProtocolDrift, "native business fields must have an observed value")
	}
	number, err := strconv.Atoi(priority.Value.ID)
	if err != nil {
		return Ticket{}, failure(ports.FailureProtocolDrift, "native priority must be an integer")
	}
	result := Ticket{ID: value.Ref.ID, ProjectID: projectID, Revision: value.Revision, Key: value.Key, URL: value.URL, Title: value.Title, Description: value.Description, BusinessState: named(state.Value), Priority: number, Assignees: []NamedID{}, Labels: []NamedID{}, EvidenceRef: value.EvidenceRef, ObservedAt: fresh.ObservedAt}
	for _, item := range assignees.Value {
		result.Assignees = append(result.Assignees, named(item))
	}
	for _, item := range labels.Value {
		result.Labels = append(result.Labels, named(item))
	}
	return result, nil
}
