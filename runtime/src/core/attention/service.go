// Package attention builds the operator Checkpoints queue from authoritative
// approval, input-request, and provider-permission projections. It does not
// persist attention state: resolving a source removes it from the next read.
package attention

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"time"

	"darkstar/src/core/identity"
	"darkstar/src/ports/statestore"
)

const (
	defaultLimit = 50
	maximumLimit = 200
)

var (
	ErrInvalidRequest = errors.New("invalid attention projection request")
	ErrInvalidCursor  = errors.New("invalid attention projection cursor")
	digestPattern     = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

// Kind is the closed set of operator-attention variants.
type Kind string

const (
	KindWorkflowCheckpoint Kind = Kind(statestore.ApprovalWorkflowCheckpoint)
	KindWorkflowControl    Kind = Kind(statestore.ApprovalWorkflowControl)
	KindProviderPermission Kind = Kind(statestore.ApprovalProviderPermission)
	KindExternalDelivery   Kind = Kind(statestore.ApprovalExternalDelivery)
	KindInputRequired      Kind = "input_required"
)

// Context identifies the complete project/work/run lineage shared by every
// queue item. Names are immutable projection snapshots for presentation.
type Context struct {
	ProjectID   string `json:"projectId"`
	ProjectName string `json:"projectName"`
	WorkItemID  string `json:"workItemId"`
	WorkTitle   string `json:"workTitle"`
	RunID       string `json:"runId"`
}

// Envelope is the immutable common portion of every queue variant. Urgency is
// the authoritative work priority; larger values sort before smaller values.
type Envelope struct {
	Kind            Kind      `json:"kind"`
	ID              string    `json:"id"`
	Context         Context   `json:"context"`
	Urgency         int       `json:"urgency"`
	CreatedAt       time.Time `json:"createdAt"`
	UpdatedAt       time.Time `json:"updatedAt"`
	ResourceVersion uint64    `json:"resourceVersion"`
	Summary         string    `json:"summary"`
	DeepLink        string    `json:"deepLink"`
}

// Checkpoint is a member of the closed discriminated queue union.
type Checkpoint interface {
	checkpoint()
	Common() Envelope
}

type WorkflowCheckpointAction string

const (
	WorkflowCheckpointApprove        WorkflowCheckpointAction = "approve"
	WorkflowCheckpointRequestChanges WorkflowCheckpointAction = "request_changes"
	WorkflowCheckpointReject         WorkflowCheckpointAction = "reject"
)

type WorkflowCheckpointSubject struct {
	CheckpointID        string `json:"checkpointId"`
	VisitID             string `json:"visitId"`
	NodeID              string `json:"nodeId"`
	AttemptID           string `json:"attemptId"`
	Revision            uint64 `json:"revision"`
	CandidateArtifactID string `json:"candidateArtifactId"`
	CandidateVersion    uint64 `json:"candidateVersion"`
	CandidateDigest     string `json:"candidateDigest"`
	Mode                string `json:"mode"`
	ScopeDigest         string `json:"scopeDigest"`
	PolicyDigest        string `json:"policyDigest"`
}

type WorkflowCheckpoint struct {
	Envelope
	Subject        WorkflowCheckpointSubject  `json:"subject"`
	AllowedActions []WorkflowCheckpointAction `json:"allowedActions"`
}

func (WorkflowCheckpoint) checkpoint() {}
func (item WorkflowCheckpoint) Common() Envelope {
	return item.Envelope
}

type WorkflowControlAction string

const (
	WorkflowControlApprove WorkflowControlAction = "approve"
	WorkflowControlDeny    WorkflowControlAction = "deny"
	WorkflowControlCancel  WorkflowControlAction = "cancel"
)

type WorkflowControlSubject struct {
	VisitID      string `json:"visitId,omitempty"`
	NodeID       string `json:"nodeId,omitempty"`
	AttemptID    string `json:"attemptId,omitempty"`
	ScopeDigest  string `json:"scopeDigest"`
	PolicyDigest string `json:"policyDigest"`
}

type WorkflowControl struct {
	Envelope
	Subject        WorkflowControlSubject  `json:"subject"`
	AllowedActions []WorkflowControlAction `json:"allowedActions"`
}

func (WorkflowControl) checkpoint() {}
func (item WorkflowControl) Common() Envelope {
	return item.Envelope
}

type ProviderPermissionAction string

const (
	ProviderPermissionAllowOnce     ProviderPermissionAction = "allow_once"
	ProviderPermissionDeny          ProviderPermissionAction = "deny"
	ProviderPermissionCancel        ProviderPermissionAction = "cancel"
	ProviderPermissionRetryDelivery ProviderPermissionAction = "retry_delivery"
)

type ProviderPermissionSubject struct {
	AttemptID         string                              `json:"attemptId"`
	NodeID            string                              `json:"nodeId"`
	ProviderThreadID  string                              `json:"providerThreadId"`
	ProviderTurnID    string                              `json:"providerTurnId"`
	ProviderRequestID string                              `json:"providerRequestId"`
	InteractionKind   string                              `json:"interactionKind"`
	Scope             statestore.JSONSnapshot             `json:"scope"`
	ScopeDigest       string                              `json:"scopeDigest"`
	PolicyDigest      string                              `json:"policyDigest"`
	Evidence          statestore.JSONSnapshot             `json:"evidence"`
	Status            statestore.ProviderPermissionStatus `json:"status"`
}

type ProviderPermission struct {
	Envelope
	Subject        ProviderPermissionSubject  `json:"subject"`
	AllowedActions []ProviderPermissionAction `json:"allowedActions"`
}

func (ProviderPermission) checkpoint() {}
func (item ProviderPermission) Common() Envelope {
	return item.Envelope
}

type ExternalDeliveryAction string

const (
	ExternalDeliveryApprove ExternalDeliveryAction = "approve"
	ExternalDeliveryDeny    ExternalDeliveryAction = "deny"
	ExternalDeliveryCancel  ExternalDeliveryAction = "cancel"
)

type ExternalDeliverySubject struct {
	VisitID      string `json:"visitId,omitempty"`
	NodeID       string `json:"nodeId,omitempty"`
	AttemptID    string `json:"attemptId,omitempty"`
	ScopeDigest  string `json:"scopeDigest"`
	PolicyDigest string `json:"policyDigest"`
}

type ExternalDelivery struct {
	Envelope
	Subject        ExternalDeliverySubject  `json:"subject"`
	AllowedActions []ExternalDeliveryAction `json:"allowedActions"`
}

func (ExternalDelivery) checkpoint() {}
func (item ExternalDelivery) Common() Envelope {
	return item.Envelope
}

type InputRequiredAction string

const (
	InputRequiredAnswer        InputRequiredAction = "answer"
	InputRequiredRetryDelivery InputRequiredAction = "retry_delivery"
)

type InputRequiredSubject struct {
	AttemptID         string                        `json:"attemptId"`
	NodeID            string                        `json:"nodeId"`
	ProviderRequestID string                        `json:"providerRequestId"`
	ScopeDigest       string                        `json:"scopeDigest"`
	Request           statestore.JSONSnapshot       `json:"request"`
	Status            statestore.InputRequestStatus `json:"status"`
}

type InputRequired struct {
	Envelope
	Subject        InputRequiredSubject  `json:"subject"`
	AllowedActions []InputRequiredAction `json:"allowedActions"`
}

func (InputRequired) checkpoint() {}
func (item InputRequired) Common() Envelope {
	return item.Envelope
}

// Checkpoints is a JSON-aware union that rejects unknown sibling variants.
type Checkpoints []Checkpoint

func (items *Checkpoints) UnmarshalJSON(encoded []byte) error {
	var raw []json.RawMessage
	if err := json.Unmarshal(encoded, &raw); err != nil {
		return err
	}
	result := make(Checkpoints, 0, len(raw))
	for _, candidate := range raw {
		var discriminator struct {
			Kind    Kind `json:"kind"`
			Subject struct {
				Source string `json:"source"`
			} `json:"subject"`
		}
		if err := json.Unmarshal(candidate, &discriminator); err != nil {
			return err
		}
		var item Checkpoint
		switch discriminator.Kind {
		case KindWorkflowCheckpoint:
			item = new(WorkflowCheckpoint)
		case KindWorkflowControl:
			item = new(WorkflowControl)
		case KindProviderPermission:
			item = new(ProviderPermission)
		case KindExternalDelivery:
			item = new(ExternalDelivery)
		case KindInputRequired:
			switch discriminator.Subject.Source {
			case "":
				item = new(InputRequired)
			case "route_preparation":
				item = new(PreparationInputRequired)
			default:
				return fmt.Errorf("unknown input-required source %q", discriminator.Subject.Source)
			}
		default:
			return fmt.Errorf("unknown attention checkpoint kind %q", discriminator.Kind)
		}
		if err := json.Unmarshal(candidate, item); err != nil {
			return err
		}
		result = append(result, item)
	}
	*items = result
	return nil
}

type Page struct {
	SchemaVersion int         `json:"schemaVersion"`
	Items         Checkpoints `json:"items"`
	TotalCount    int         `json:"totalCount"`
	NextCursor    string      `json:"nextCursor,omitempty"`
}

// DecisionAction is the closed set shared by workflow-control and external-
// delivery approvals. Artifact, input, and provider-permission decisions keep
// their specialized command boundaries.
type DecisionAction string

const (
	DecisionApprove DecisionAction = "approve"
	DecisionDeny    DecisionAction = "deny"
	DecisionCancel  DecisionAction = "cancel"
)

type DecisionRequest struct {
	Kind                    Kind
	ID                      string
	ExpectedResourceVersion uint64
	Action                  DecisionAction
	ScopeDigest             string
	PolicyDigest            string
	Comment                 string
	IdempotencyKey          string
	Actor                   statestore.Actor
}

type Resolution struct {
	Kind            Kind             `json:"kind"`
	ID              string           `json:"id"`
	Action          DecisionAction   `json:"action"`
	ResourceVersion uint64           `json:"resourceVersion"`
	Actor           statestore.Actor `json:"actor"`
	DecidedAt       time.Time        `json:"decidedAt"`
}

type ListRequest struct {
	IncludePreparation bool
	Kinds              []Kind
	ItemID             string
	ProjectID          string
	WorkItemID         string
	RunID              string
	Limit              int
	Cursor             string
}

// Source is exactly the durable state needed to rebuild the attention queue.
type Source interface {
	Approvals(context.Context, statestore.ApprovalStatus) ([]statestore.ApprovalProjection, error)
	InputRequests(context.Context, statestore.InputRequestStatus) ([]statestore.InputRequestProjection, error)
	ProviderPermissions(context.Context, statestore.ProviderPermissionStatus) ([]statestore.ProviderPermissionProjection, error)
	Run(context.Context, string) (statestore.RunProjection, error)
	Attempt(context.Context, string) (statestore.AttemptProjection, error)
	WorkItem(context.Context, string) (statestore.WorkItemProjection, error)
	Project(context.Context, string) (statestore.ProjectProjection, error)
}

type Service struct {
	source          Source
	now             func() time.Time
	decisionHandler func(context.Context, statestore.ApprovalProjection, statestore.PendingEvent) ([]statestore.Event, bool, error)
}

// SetDecisionHandler binds execution checkpoints to the scheduler before serving requests.
func (service *Service) SetDecisionHandler(handler func(context.Context, statestore.ApprovalProjection, statestore.PendingEvent) ([]statestore.Event, bool, error)) {
	service.decisionHandler = handler
}

func New(source Source) (*Service, error) {
	if source == nil {
		return nil, errors.New("attention projection requires durable state")
	}
	return &Service{source: source, now: time.Now}, nil
}

// List derives a snapshot page. It intentionally fetches only unresolved
// source states and therefore cannot drift from approval/input truth.
func (service *Service) List(ctx context.Context, request ListRequest) (Page, error) {
	request, selected, fingerprint, err := normalizeRequest(request)
	if err != nil {
		return Page{}, err
	}
	items := make(Checkpoints, 0)
	approvals, err := service.source.Approvals(ctx, statestore.ApprovalPending)
	if err != nil {
		return Page{}, fmt.Errorf("read pending approvals: %w", err)
	}
	for _, approval := range approvals {
		if approval.Status != statestore.ApprovalPending {
			continue
		}
		kind := Kind(approval.Class)
		if !knownKind(kind) {
			return Page{}, fmt.Errorf("unknown pending approval class %q", approval.Class)
		}
		// Provider permissions have their own authoritative projection with
		// delivery and owner-activity state. A generic approval row must never
		// duplicate or replace that source.
		if kind == KindProviderPermission {
			continue
		}
		if !selected[kind] {
			continue
		}
		contextValue, urgency, err := service.context(ctx, approval.RunID)
		if err != nil {
			return Page{}, err
		}
		if !matches(request, contextValue) {
			continue
		}
		items = append(items, approvalCheckpoint(approval, contextValue, urgency))
	}
	if selected[KindProviderPermission] {
		for _, status := range []statestore.ProviderPermissionStatus{statestore.ProviderPermissionPending, statestore.ProviderPermissionDecisionRecorded} {
			permissions, readErr := service.source.ProviderPermissions(ctx, status)
			if readErr != nil {
				return Page{}, fmt.Errorf("read unresolved provider permissions: %w", readErr)
			}
			for _, permission := range permissions {
				if permission.Status != status {
					continue
				}
				active, activeErr := service.permissionOwnerActive(ctx, permission)
				if activeErr != nil {
					return Page{}, activeErr
				}
				if !active {
					continue
				}
				contextValue, urgency, contextErr := service.context(ctx, permission.RunID)
				if contextErr != nil {
					return Page{}, contextErr
				}
				if !matches(request, contextValue) {
					continue
				}
				items = append(items, providerPermissionCheckpoint(permission, contextValue, urgency))
			}
		}
	}
	if selected[KindInputRequired] {
		if request.IncludePreparation {
			preparationItems, preparationErr := service.preparationInputs(ctx, request)
			if preparationErr != nil {
				return Page{}, preparationErr
			}
			items = append(items, preparationItems...)
		}
		for _, status := range []statestore.InputRequestStatus{statestore.InputRequestPending, statestore.InputRequestAnswerRecorded} {
			inputs, readErr := service.source.InputRequests(ctx, status)
			if readErr != nil {
				return Page{}, fmt.Errorf("read unresolved input requests: %w", readErr)
			}
			for _, input := range inputs {
				if input.Status != status {
					continue
				}
				contextValue, urgency, contextErr := service.context(ctx, input.RunID)
				if contextErr != nil {
					return Page{}, contextErr
				}
				if !matches(request, contextValue) {
					continue
				}
				items = append(items, inputCheckpoint(input, contextValue, urgency))
			}
		}
	}
	// Deleted work retains raw requests, but they no longer ask the user to act.
	retained := make(Checkpoints, 0, len(items))
	for _, item := range items {
		work, err := service.source.WorkItem(ctx, item.Common().Context.WorkItemID)
		if err != nil {
			return Page{}, err
		}
		if work.Deletion == statestore.WorkRetained {
			retained = append(retained, item)
		}
	}
	items = retained
	if request.ItemID != "" {
		exact := make(Checkpoints, 0, 1)
		for _, item := range items {
			if item.Common().ID == request.ItemID {
				exact = append(exact, item)
			}
		}
		items = exact
	}
	sort.Slice(items, func(i, j int) bool {
		return before(items[i].Common(), items[j].Common())
	})
	totalCount := len(items)
	position, err := decodeCursor(request.Cursor, fingerprint)
	if err != nil {
		return Page{}, err
	}
	if position != nil {
		first := sort.Search(len(items), func(index int) bool {
			return after(items[index].Common(), *position)
		})
		items = items[first:]
	}
	page := Page{SchemaVersion: 1, Items: items, TotalCount: totalCount}
	if len(page.Items) > request.Limit {
		page.Items = page.Items[:request.Limit]
		page.NextCursor, err = encodeCursor(page.Items[len(page.Items)-1].Common(), fingerprint)
		if err != nil {
			return Page{}, err
		}
	}
	return page, nil
}

type decisionSource interface {
	Approval(context.Context, string) (statestore.ApprovalProjection, error)
	Append(context.Context, ...statestore.PendingEvent) ([]statestore.Event, error)
	EventByCommand(context.Context, string, string) (statestore.Event, error)
}

// Decide resolves a workflow-control or external-delivery approval by
// appending to its authoritative approval stream. It never mutates the derived
// attention page itself.
func (service *Service) Decide(ctx context.Context, request DecisionRequest) (Resolution, error) {
	if err := validateDecision(request); err != nil {
		return Resolution{}, err
	}
	source, ok := service.source.(decisionSource)
	if !ok {
		return Resolution{}, errors.New("attention decisions require durable approval authority")
	}
	approval, err := source.Approval(ctx, request.ID)
	if err != nil {
		return Resolution{}, err
	}
	payload := decisionPayload(request)
	eventKind := "approval.decided"
	if request.Action == DecisionCancel {
		eventKind = "approval.cancelled"
	}
	if Kind(approval.Class) != request.Kind || (request.Kind != KindWorkflowControl && request.Kind != KindExternalDelivery) {
		return Resolution{}, fmt.Errorf("%w: attention kind does not match approval authority", ErrInvalidRequest)
	}
	if approval.Status != statestore.ApprovalPending {
		committed, readErr := source.EventByCommand(ctx, request.ID, request.IdempotencyKey)
		if readErr == nil && committed.Kind == eventKind && string(committed.Data) == string(payload) && committed.Actor == request.Actor {
			return resolutionFromEvent(request, committed), nil
		}
		return Resolution{}, fmt.Errorf("attention item is no longer pending")
	}
	if approval.ResourceVersion != request.ExpectedResourceVersion || approval.ScopeDigest != request.ScopeDigest || approval.PolicyDigest != request.PolicyDigest {
		return Resolution{}, fmt.Errorf("attention decision binding is stale")
	}
	pending := statestore.PendingEvent{
		SchemaVersion: 1, ID: identity.Random("event_"), AggregateType: statestore.AggregateApproval,
		AggregateID: request.ID, ExpectedRevision: approval.ResourceVersion, Kind: eventKind,
		OccurredAt: service.now().UTC().Round(0), CorrelationID: approval.RunID,
		CommandID: request.IdempotencyKey, Actor: request.Actor, Data: payload, Metadata: json.RawMessage(`{}`),
	}
	var events []statestore.Event
	handled := false
	if service.decisionHandler != nil {
		events, handled, err = service.decisionHandler(ctx, approval, pending)
	}
	if !handled && err == nil {
		events, err = source.Append(ctx, pending)
	}
	if err != nil {
		return Resolution{}, fmt.Errorf("resolve attention approval: %w", err)
	}
	if len(events) != 1 {
		return Resolution{}, errors.New("resolve attention approval: durable store returned no decision event")
	}
	return resolutionFromEvent(request, events[0]), nil
}

func validateDecision(request DecisionRequest) error {
	if !strings.HasPrefix(request.ID, "approval_") || request.ExpectedResourceVersion == 0 ||
		(request.Kind != KindWorkflowControl && request.Kind != KindExternalDelivery) ||
		(request.Action != DecisionApprove && request.Action != DecisionDeny && request.Action != DecisionCancel) ||
		!digestPattern.MatchString(request.ScopeDigest) || !digestPattern.MatchString(request.PolicyDigest) || strings.TrimSpace(request.IdempotencyKey) == "" ||
		request.Actor.Type != statestore.ActorUser || strings.TrimSpace(request.Actor.ID) == "" || strings.TrimSpace(request.Comment) != request.Comment || len(request.Comment) > 4096 {
		return ErrInvalidRequest
	}
	return nil
}

func decisionPayload(request DecisionRequest) json.RawMessage {
	if request.Action == DecisionCancel {
		value, _ := json.Marshal(struct {
			Kind         Kind   `json:"kind"`
			ScopeDigest  string `json:"scopeDigest"`
			PolicyDigest string `json:"policyDigest"`
			Comment      string `json:"comment,omitempty"`
		}{request.Kind, request.ScopeDigest, request.PolicyDigest, request.Comment})
		return value
	}
	value, _ := json.Marshal(struct {
		Kind         Kind           `json:"kind"`
		Action       DecisionAction `json:"action"`
		ScopeDigest  string         `json:"scopeDigest"`
		PolicyDigest string         `json:"policyDigest"`
		Comment      string         `json:"comment,omitempty"`
	}{request.Kind, request.Action, request.ScopeDigest, request.PolicyDigest, request.Comment})
	return value
}

func resolutionFromEvent(request DecisionRequest, event statestore.Event) Resolution {
	return Resolution{Kind: request.Kind, ID: request.ID, Action: request.Action, ResourceVersion: event.AggregateRevision, Actor: event.Actor, DecidedAt: event.RecordedAt}
}

func (service *Service) permissionOwnerActive(ctx context.Context, value statestore.ProviderPermissionProjection) (bool, error) {
	attempt, err := service.source.Attempt(ctx, value.AttemptID)
	if err != nil {
		return false, fmt.Errorf("resolve provider-permission attempt %s: %w", value.AttemptID, err)
	}
	run, err := service.source.Run(ctx, value.RunID)
	if err != nil {
		return false, fmt.Errorf("resolve provider-permission run %s: %w", value.RunID, err)
	}
	attemptActive := attempt.Status == statestore.AttemptRunning || attempt.Status == statestore.AttemptValidating
	runActive := run.Status == statestore.RunRunning || run.Status == statestore.RunWaiting || run.Status == statestore.RunBlocked
	return attemptActive && runActive, nil
}

func (service *Service) context(ctx context.Context, runID string) (Context, int, error) {
	run, err := service.source.Run(ctx, runID)
	if err != nil {
		return Context{}, 0, fmt.Errorf("resolve attention run %s: %w", runID, err)
	}
	work, err := service.source.WorkItem(ctx, run.WorkItemID)
	if err != nil {
		return Context{}, 0, fmt.Errorf("resolve attention work %s: %w", run.WorkItemID, err)
	}
	project, err := service.source.Project(ctx, work.ProjectID)
	if err != nil {
		return Context{}, 0, fmt.Errorf("resolve attention project %s: %w", work.ProjectID, err)
	}
	return Context{ProjectID: project.ProjectID, ProjectName: project.Name, WorkItemID: work.WorkItemID, WorkTitle: work.Title, RunID: run.RunID}, work.Priority, nil
}

func approvalCheckpoint(value statestore.ApprovalProjection, contextValue Context, urgency int) Checkpoint {
	envelope := Envelope{Kind: Kind(value.Class), ID: value.ApprovalID, Context: contextValue, Urgency: urgency, CreatedAt: value.CreatedAt, UpdatedAt: sourceUpdatedAt(value.CreatedAt, value.UpdatedAt), ResourceVersion: value.ResourceVersion, Summary: approvalSummary(value.Class, contextValue.WorkTitle), DeepLink: "/checkpoints?itemId=" + url.QueryEscape(value.ApprovalID)}
	switch value.Class {
	case statestore.ApprovalWorkflowCheckpoint:
		return WorkflowCheckpoint{Envelope: envelope, Subject: WorkflowCheckpointSubject{CheckpointID: value.CheckpointID, VisitID: value.VisitID, NodeID: value.NodeID, AttemptID: value.AttemptID, Revision: value.CheckpointRevision, CandidateArtifactID: value.CandidateArtifactID, CandidateVersion: value.CandidateArtifactVersion, CandidateDigest: value.CandidateDigest, Mode: value.CheckpointMode, ScopeDigest: value.ScopeDigest, PolicyDigest: value.PolicyDigest}, AllowedActions: []WorkflowCheckpointAction{WorkflowCheckpointApprove, WorkflowCheckpointRequestChanges, WorkflowCheckpointReject}}
	case statestore.ApprovalWorkflowControl:
		return WorkflowControl{Envelope: envelope, Subject: WorkflowControlSubject{VisitID: value.VisitID, NodeID: value.NodeID, AttemptID: value.AttemptID, ScopeDigest: value.ScopeDigest, PolicyDigest: value.PolicyDigest}, AllowedActions: []WorkflowControlAction{WorkflowControlApprove, WorkflowControlDeny, WorkflowControlCancel}}
	case statestore.ApprovalExternalDelivery:
		return ExternalDelivery{Envelope: envelope, Subject: ExternalDeliverySubject{VisitID: value.VisitID, NodeID: value.NodeID, AttemptID: value.AttemptID, ScopeDigest: value.ScopeDigest, PolicyDigest: value.PolicyDigest}, AllowedActions: []ExternalDeliveryAction{ExternalDeliveryApprove, ExternalDeliveryDeny, ExternalDeliveryCancel}}
	default:
		panic("approvalCheckpoint called with unknown class")
	}
}

func providerPermissionCheckpoint(value statestore.ProviderPermissionProjection, contextValue Context, urgency int) Checkpoint {
	actions := []ProviderPermissionAction{ProviderPermissionAllowOnce, ProviderPermissionDeny, ProviderPermissionCancel}
	if value.Status == statestore.ProviderPermissionDecisionRecorded {
		actions = []ProviderPermissionAction{ProviderPermissionRetryDelivery}
	}
	return ProviderPermission{
		Envelope:       Envelope{Kind: KindProviderPermission, ID: value.PermissionRequestID, Context: contextValue, Urgency: urgency, CreatedAt: value.CreatedAt, UpdatedAt: sourceUpdatedAt(value.CreatedAt, value.UpdatedAt), ResourceVersion: value.ResourceVersion, Summary: "Authorize provider interaction for " + contextValue.WorkTitle, DeepLink: "/checkpoints?itemId=" + url.QueryEscape(value.PermissionRequestID)},
		Subject:        ProviderPermissionSubject{AttemptID: value.AttemptID, NodeID: value.NodeID, ProviderThreadID: value.ProviderThreadID, ProviderTurnID: value.ProviderTurnID, ProviderRequestID: value.ProviderRequestID, InteractionKind: value.InteractionKind, Scope: value.Scope, ScopeDigest: value.ScopeDigest, PolicyDigest: value.PolicyDigest, Evidence: value.Evidence, Status: value.Status},
		AllowedActions: actions,
	}
}

func inputCheckpoint(value statestore.InputRequestProjection, contextValue Context, urgency int) Checkpoint {
	actions := []InputRequiredAction{InputRequiredAnswer}
	if value.Status == statestore.InputRequestAnswerRecorded {
		actions = []InputRequiredAction{InputRequiredRetryDelivery}
	}
	return InputRequired{Envelope: Envelope{Kind: KindInputRequired, ID: value.InputRequestID, Context: contextValue, Urgency: urgency, CreatedAt: value.CreatedAt, UpdatedAt: sourceUpdatedAt(value.CreatedAt, value.UpdatedAt), ResourceVersion: value.ResourceVersion, Summary: "Input required for " + contextValue.WorkTitle, DeepLink: "/checkpoints?itemId=" + url.QueryEscape(value.InputRequestID)}, Subject: InputRequiredSubject{AttemptID: value.AttemptID, NodeID: value.NodeID, ProviderRequestID: value.ProviderRequestID, ScopeDigest: value.ScopeDigest, Request: value.Request, Status: value.Status}, AllowedActions: actions}
}

func sourceUpdatedAt(createdAt, updatedAt time.Time) time.Time {
	if updatedAt.IsZero() {
		return createdAt
	}
	return updatedAt
}

func approvalSummary(class statestore.ApprovalClass, title string) string {
	switch class {
	case statestore.ApprovalWorkflowCheckpoint:
		return "Review checkpoint for " + title
	case statestore.ApprovalWorkflowControl:
		return "Review workflow control for " + title
	case statestore.ApprovalProviderPermission:
		return "Authorize provider interaction for " + title
	case statestore.ApprovalExternalDelivery:
		return "Authorize external delivery for " + title
	default:
		return ""
	}
}

func knownKind(kind Kind) bool {
	switch kind {
	case KindWorkflowCheckpoint, KindWorkflowControl, KindProviderPermission, KindExternalDelivery, KindInputRequired:
		return true
	default:
		return false
	}
}

func normalizeRequest(request ListRequest) (ListRequest, map[Kind]bool, string, error) {
	if request.Limit == 0 {
		request.Limit = defaultLimit
	}
	if request.Limit < 1 || request.Limit > maximumLimit {
		return request, nil, "", ErrInvalidRequest
	}
	if strings.TrimSpace(request.ItemID) != request.ItemID || (request.ItemID != "" && request.Cursor != "") {
		return request, nil, "", ErrInvalidRequest
	}
	selected := make(map[Kind]bool)
	if len(request.Kinds) == 0 {
		for _, kind := range []Kind{KindWorkflowCheckpoint, KindInputRequired, KindProviderPermission, KindWorkflowControl, KindExternalDelivery} {
			selected[kind] = true
		}
	} else {
		for _, kind := range request.Kinds {
			if !knownKind(kind) {
				return request, nil, "", fmt.Errorf("%w: unknown kind %q", ErrInvalidRequest, kind)
			}
			selected[kind] = true
		}
	}
	kinds := make([]string, 0, len(selected))
	for kind := range selected {
		kinds = append(kinds, string(kind))
	}
	sort.Strings(kinds)
	fingerprint := strings.Join([]string{strings.Join(kinds, ","), request.ItemID, request.ProjectID, request.WorkItemID, request.RunID}, "|")
	if request.IncludePreparation {
		fingerprint += "|preparation-v2"
	}
	return request, selected, fingerprint, nil
}

func matches(request ListRequest, contextValue Context) bool {
	return (request.ProjectID == "" || request.ProjectID == contextValue.ProjectID) && (request.WorkItemID == "" || request.WorkItemID == contextValue.WorkItemID) && (request.RunID == "" || request.RunID == contextValue.RunID)
}

func before(left, right Envelope) bool {
	if left.Urgency != right.Urgency {
		return left.Urgency > right.Urgency
	}
	if !left.CreatedAt.Equal(right.CreatedAt) {
		return left.CreatedAt.Before(right.CreatedAt)
	}
	return left.ID < right.ID
}

type cursor struct {
	Version     int       `json:"v"`
	Urgency     int       `json:"u"`
	CreatedAt   time.Time `json:"t"`
	ID          string    `json:"i"`
	Fingerprint string    `json:"f"`
}

func encodeCursor(envelope Envelope, fingerprint string) (string, error) {
	encoded, err := json.Marshal(cursor{Version: 1, Urgency: envelope.Urgency, CreatedAt: envelope.CreatedAt, ID: envelope.ID, Fingerprint: fingerprint})
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(encoded), nil
}

func decodeCursor(value, fingerprint string) (*cursor, error) {
	if value == "" {
		return nil, nil
	}
	encoded, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil {
		return nil, ErrInvalidCursor
	}
	var result cursor
	decoder := json.NewDecoder(strings.NewReader(string(encoded)))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&result) != nil || result.Version != 1 || result.ID == "" || result.CreatedAt.IsZero() || result.Fingerprint != fingerprint {
		return nil, ErrInvalidCursor
	}
	return &result, nil
}

func after(value Envelope, position cursor) bool {
	return before(Envelope{Urgency: position.Urgency, CreatedAt: position.CreatedAt, ID: position.ID}, value)
}
