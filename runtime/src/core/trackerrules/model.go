// Package trackerrules evaluates configured tracker rules without granting run,
// tool, approval, or writer authority. The daemon persists and applies decisions.
package trackerrules

import (
	"context"
	"time"

	"darkstar/src/ports/tracker"
)

const Version = "darkstar.tracker-rules/v1alpha1"

type RuleScope struct {
	ProjectID       string        `json:"projectId"`
	BindingRevision uint64        `json:"bindingRevision"`
	Pin             tracker.Pin   `json:"pin"`
	Source          tracker.Scope `json:"source"`
}

type RuleSet struct {
	Version  string         `json:"version"`
	ID       string         `json:"id"`
	Revision uint64         `json:"revision"`
	Scope    RuleScope      `json:"scope"`
	Intake   []IntakeRule   `json:"intake"`
	Outbound []OutboundRule `json:"outbound"`
	Display  DisplayMapping `json:"display"`
}

type WorkflowPin struct {
	ID      string `json:"id"`
	Version string `json:"version"`
	Digest  string `json:"digest"`
}

// Predicates use stable discovered IDs, with conjunctive fields and disjunctive
// values. Sprint is separate and never implied by a business-state predicate.
type Predicate struct {
	FieldID string   `json:"fieldId"`
	Values  []string `json:"values"`
}

type Conditions struct {
	Fields    []Predicate `json:"fields"`
	SprintIDs []string    `json:"sprintIds,omitempty"`
}

type IntakeRule struct {
	ID     string       `json:"id"`
	When   Conditions   `json:"when"`
	Action IntakeAction `json:"action"`
}

type IntakeAction interface{ isIntakeAction() }

type Noop struct{}

func (Noop) isIntakeAction() {}

func (Noop) isOutboundAction() {}

type AdmissionMode string

const (
	Manual    AdmissionMode = "manual"
	Automatic AdmissionMode = "automatic"
)

// MaxAdmissions includes initial admission; one disables automatic reopen.
// Reopen requires a new eligibility episode and settled previous execution.
type RepairPolicy struct {
	MaxAdmissions uint32 `json:"maxAdmissions"`
}

type Admit struct {
	Workflow        WorkflowPin   `json:"workflow"`
	ReadinessPolicy string        `json:"readinessPolicy"`
	Mode            AdmissionMode `json:"mode"`
	Repair          RepairPolicy  `json:"repair"`
}

func (Admit) isIntakeAction() {}

type OutboundRule struct {
	ID        string            `json:"id"`
	When      Conditions        `json:"when"`
	Milestone MilestoneContract `json:"milestone"`
	Action    OutboundAction    `json:"action"`
}

// Milestone declarations are scoped to exact workflow bytes and evidence types.
// There is deliberately no generic run-completed milestone.
type MilestoneContract struct {
	ID            string      `json:"id"`
	Workflow      WorkflowPin `json:"workflow"`
	EvidenceTypes []string    `json:"evidenceTypes"`
}

type OutboundAction interface{ isOutboundAction() }

type Report struct {
	Body string `json:"body"`
}

func (Report) isOutboundAction() {}

type Transition struct {
	TransitionID string                        `json:"transitionId"`
	Fields       map[string]tracker.FieldValue `json:"-"`
}

func (Transition) isOutboundAction() {}

type DisplayGroup struct {
	ID       string   `json:"id"`
	Name     string   `json:"name"`
	StateIDs []string `json:"stateIds"`
}

type DisplayMapping struct {
	Groups       []DisplayGroup `json:"groups"`
	UnknownGroup DisplayGroup   `json:"unknownGroup"`
}

type GroupResult struct {
	GroupID   string `json:"groupId"`
	GroupName string `json:"groupName"`
	StateID   string `json:"stateId,omitempty"`
	StateName string `json:"stateName,omitempty"`
	Unmapped  bool   `json:"unmapped"`
	Reason    string `json:"reason,omitempty"`
}

// Definitions are daemon observations from provider discovery, not user claims.
// Unsupported concepts are absent, including sprint on GitHub Issues.
type FieldDefinition struct {
	ID     string            `json:"id"`
	Values []tracker.NamedID `json:"values"`
}

type Discovery struct {
	Manifest          tracker.Manifest     `json:"manifest"`
	Fields            []FieldDefinition    `json:"fields"`
	Sprints           []tracker.NamedID    `json:"sprints"`
	Transitions       []tracker.Transition `json:"transitions"`
	Workflows         []WorkflowPin        `json:"workflows"`
	ReadinessPolicies []string             `json:"readinessPolicies"`
	Milestones        []MilestoneContract  `json:"milestones"`
}

type Observation struct {
	ProjectID       string
	BindingRevision uint64
	Pin             tracker.Pin
	Ticket          tracker.Ticket
	WorkflowID      tracker.Knowledge[tracker.NamedID]
}

type IntakePreview struct {
	RuleID       string       `json:"ruleId,omitempty"`
	Matched      bool         `json:"matched"`
	Action       IntakeAction `json:"action,omitempty"`
	RuleSetID    string       `json:"ruleSetId"`
	RuleRevision uint64       `json:"ruleRevision"`
	Group        GroupResult  `json:"group"`
}

type Evidence struct {
	Type      string              `json:"type"`
	Artifact  tracker.ArtifactRef `json:"artifact"`
	Authority string              `json:"authority"`
	Revision  string              `json:"revision"`
}

// EvidenceValidator must verify retained bytes and authority against durable
// daemon records; a model-authored string or nonempty artifact ID is not proof.
type EvidenceValidator interface {
	ValidateMilestoneEvidence(context.Context, MilestoneContract, Evidence) error
}

type MilestoneObservation struct {
	EventID     string
	RunID       string
	Workflow    WorkflowPin
	MilestoneID string
	Evidence    []Evidence
}

type OutboundPreview struct {
	RuleID       string         `json:"ruleId,omitempty"`
	Matched      bool           `json:"matched"`
	Effect       tracker.Effect `json:"-"`
	OperationID  string         `json:"operationId,omitempty"`
	RuleSetID    string         `json:"ruleSetId"`
	RuleRevision uint64         `json:"ruleRevision"`
}

type Event struct {
	ID                string
	Observation       Observation
	OriginOperationID string
}

// Cursor is persisted atomically with the admission decision. It is scoped to
// stable project/ticket identity, not mutable rule or binding revision.
type Cursor struct {
	Identity       string    `json:"identity"`
	SeenEventIDs   []string  `json:"seenEventIds"`
	SeenRevisions  []string  `json:"seenRevisions"`
	Eligible       bool      `json:"eligible"`
	Admissions     uint32    `json:"admissions"`
	LastObservedAt time.Time `json:"lastObservedAt"`
}

type AdmissionDecision struct {
	State       string        `json:"state"`
	AdmissionID string        `json:"admissionId,omitempty"`
	Preview     IntakePreview `json:"preview"`
	Cursor      Cursor        `json:"cursor"`
}
