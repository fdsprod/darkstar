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
	"sort"
	"strings"
	"time"

	"darkstar/src/ports/statestore"
)

const (
	defaultLimit = 50
	maximumLimit = 200
)

var (
	ErrInvalidRequest = errors.New("invalid attention projection request")
	ErrInvalidCursor  = errors.New("invalid attention projection cursor")
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

func (WorkflowCheckpoint) checkpoint()           {}
func (item WorkflowCheckpoint) Common() Envelope { return item.Envelope }

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

func (WorkflowControl) checkpoint()           {}
func (item WorkflowControl) Common() Envelope { return item.Envelope }

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

func (ProviderPermission) checkpoint()           {}
func (item ProviderPermission) Common() Envelope { return item.Envelope }

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

func (ExternalDelivery) checkpoint()           {}
func (item ExternalDelivery) Common() Envelope { return item.Envelope }

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

func (InputRequired) checkpoint()           {}
func (item InputRequired) Common() Envelope { return item.Envelope }

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
	NextCursor    string      `json:"nextCursor,omitempty"`
}

type ListRequest struct {
	IncludePreparation bool
	Kinds              []Kind
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

type Service struct{ source Source }

func New(source Source) (*Service, error) {
	if source == nil {
		return nil, errors.New("attention projection requires durable state")
	}
	return &Service{source: source}, nil
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
	sort.Slice(items, func(i, j int) bool { return before(items[i].Common(), items[j].Common()) })
	position, err := decodeCursor(request.Cursor, fingerprint)
	if err != nil {
		return Page{}, err
	}
	if position != nil {
		first := sort.Search(len(items), func(index int) bool { return after(items[index].Common(), *position) })
		items = items[first:]
	}
	page := Page{SchemaVersion: 1, Items: items}
	if len(page.Items) > request.Limit {
		page.Items = page.Items[:request.Limit]
		page.NextCursor, err = encodeCursor(page.Items[len(page.Items)-1].Common(), fingerprint)
		if err != nil {
			return Page{}, err
		}
	}
	return page, nil
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
	envelope := Envelope{Kind: Kind(value.Class), ID: value.ApprovalID, Context: contextValue, Urgency: urgency, CreatedAt: value.CreatedAt, ResourceVersion: value.ResourceVersion, Summary: approvalSummary(value.Class, contextValue.WorkTitle), DeepLink: "/checkpoints?itemId=" + url.QueryEscape(value.ApprovalID)}
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
		Envelope:       Envelope{Kind: KindProviderPermission, ID: value.PermissionRequestID, Context: contextValue, Urgency: urgency, CreatedAt: value.CreatedAt, ResourceVersion: value.ResourceVersion, Summary: "Authorize provider interaction for " + contextValue.WorkTitle, DeepLink: "/checkpoints?itemId=" + url.QueryEscape(value.PermissionRequestID)},
		Subject:        ProviderPermissionSubject{AttemptID: value.AttemptID, NodeID: value.NodeID, ProviderThreadID: value.ProviderThreadID, ProviderTurnID: value.ProviderTurnID, ProviderRequestID: value.ProviderRequestID, InteractionKind: value.InteractionKind, Scope: value.Scope, ScopeDigest: value.ScopeDigest, PolicyDigest: value.PolicyDigest, Evidence: value.Evidence, Status: value.Status},
		AllowedActions: actions,
	}
}

func inputCheckpoint(value statestore.InputRequestProjection, contextValue Context, urgency int) Checkpoint {
	actions := []InputRequiredAction{InputRequiredAnswer}
	if value.Status == statestore.InputRequestAnswerRecorded {
		actions = []InputRequiredAction{InputRequiredRetryDelivery}
	}
	return InputRequired{Envelope: Envelope{Kind: KindInputRequired, ID: value.InputRequestID, Context: contextValue, Urgency: urgency, CreatedAt: value.CreatedAt, ResourceVersion: value.ResourceVersion, Summary: "Input required for " + contextValue.WorkTitle, DeepLink: "/checkpoints?itemId=" + url.QueryEscape(value.InputRequestID)}, Subject: InputRequiredSubject{AttemptID: value.AttemptID, NodeID: value.NodeID, ProviderRequestID: value.ProviderRequestID, ScopeDigest: value.ScopeDigest, Request: value.Request, Status: value.Status}, AllowedActions: actions}
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
	fingerprint := strings.Join([]string{strings.Join(kinds, ","), request.ProjectID, request.WorkItemID, request.RunID}, "|")
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
