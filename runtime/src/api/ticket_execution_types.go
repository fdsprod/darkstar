package api

import "darkstar/src/ports/statestore"

// Source observations are immutable provider facts. They are projected beside
// execution activity, never merged into a synthetic ticket lifecycle.
type SourceTicketObservation struct {
	ObservationID string                           `json:"observationId"`
	Ref           BacklogTicketRef                 `json:"ref"`
	Revision      string                           `json:"revision"`
	Title         string                           `json:"title"`
	Description   string                           `json:"description"`
	Key           string                           `json:"key"`
	URL           string                           `json:"url"`
	BusinessState BacklogKnowledge[BacklogNamedID] `json:"businessState"`
	ObservedAt    string                           `json:"observedAt"`
	EvidenceRef   string                           `json:"evidenceRef"`
}

type SourceAssessment struct {
	State   SourceAssessmentState `json:"state"`
	Reasons []string              `json:"reasons"`
}

type SourceAssessmentState string

const (
	SourceReady          SourceAssessmentState = "ready"
	SourceActionRequired SourceAssessmentState = "action_required"
	SourceUnresolved     SourceAssessmentState = "unresolved_source"
)

type WorkSourceView struct {
	SchemaVersion        int                        `json:"schemaVersion"`
	WorkItemID           string                     `json:"workItemId"`
	ProjectID            string                     `json:"projectId"`
	LocalActivity        string                     `json:"localActivity"`
	RunOutcome           string                     `json:"runOutcome"`
	ApprovedTicket       *SourceTicketObservation   `json:"approvedTicket"`
	CurrentTicket        *SourceTicketObservation   `json:"currentTicket"`
	CurrentObservationID string                     `json:"currentObservationId"`
	Lineage              *SourceLineage             `json:"lineage"`
	Lineages             []SourceLineage            `json:"lineages"`
	Approval             *TicketAdmission           `json:"approval"`
	Assessment           SourceAssessment           `json:"assessment"`
	Runs                 []statestore.RunProjection `json:"runs"`
	ExternalAcceptance   BacklogKnowledge[string]   `json:"externalAcceptance"`
}

type WorkSourceViews struct {
	SchemaVersion int              `json:"schemaVersion"`
	Items         []WorkSourceView `json:"items"`
}

type TicketAdmissionRequest struct {
	SchemaVersion           int                           `json:"schemaVersion"`
	ExpectedBindingRevision uint64                        `json:"expectedBindingRevision"`
	ObservationID           string                        `json:"observationId"`
	RoutingIntent           *statestore.WorkRoutingIntent `json:"routingIntent,omitempty"`
}

type SourceApprovalRequest struct {
	SchemaVersion int    `json:"schemaVersion"`
	ObservationID string `json:"observationId"`
}

type SourceRebindRequest struct {
	SchemaVersion           int    `json:"schemaVersion"`
	ExpectedLineageRevision uint64 `json:"expectedLineageRevision"`
	ExpectedBindingRevision uint64 `json:"expectedBindingRevision"`
	ObservationID           string `json:"observationId"`
}

type TicketAdmissionResponse struct {
	SchemaVersion       int            `json:"schemaVersion"`
	WorkItemID          string         `json:"workItemId"`
	SourceObservationID string         `json:"sourceObservationId"`
	Source              WorkSourceView `json:"source"`
}

type SourceLineage struct {
	WorkItemID      string                         `json:"workItemId"`
	ProjectID       string                         `json:"projectId"`
	TicketKey       string                         `json:"ticketKey"`
	Revision        uint64                         `json:"revision"`
	BindingRevision uint64                         `json:"bindingRevision"`
	Ref             BacklogTicketRef               `json:"ref"`
	Origin          statestore.SourceLineageOrigin `json:"origin"`
	CreatedAt       string                         `json:"createdAt"`
}

type TicketAdmission struct {
	ID              string `json:"id"`
	WorkItemID      string `json:"workItemId"`
	ProjectID       string `json:"projectId"`
	ObservationID   string `json:"observationId"`
	LineageRevision uint64 `json:"lineageRevision"`
	BindingRevision uint64 `json:"bindingRevision"`
	ApprovedAt      string `json:"approvedAt"`
	Actor           string `json:"actor"`
}
