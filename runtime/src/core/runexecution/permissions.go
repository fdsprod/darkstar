package runexecution

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"darkstar/src/core/identity"
	"darkstar/src/ports/provider"
	"darkstar/src/ports/statestore"
)

var (
	ErrPermissionInvalidRequest      = errors.New("invalid provider permission request")
	ErrPermissionScopeConflict       = errors.New("provider permission scope conflict")
	ErrPermissionAlreadyDecided      = errors.New("provider permission is already decided")
	ErrPermissionDecisionInProgress  = errors.New("a different provider permission decision is recorded")
	ErrPermissionDeliveryUnavailable = errors.New("provider permission delivery is unavailable")
	ErrPermissionInactiveAttempt     = errors.New("provider permission owner is no longer active")
	permissionIDPattern              = regexp.MustCompile(`^permission_[0-9A-HJKMNP-TV-Z]{26}$`)
)

type ProviderPermissionAction string

const (
	ProviderPermissionAllowOnce     ProviderPermissionAction = "allow_once"
	ProviderPermissionDeny          ProviderPermissionAction = "deny"
	ProviderPermissionCancel        ProviderPermissionAction = "cancel"
	ProviderPermissionRetryDelivery ProviderPermissionAction = "retry_delivery"
)

type ProviderPermissionView struct {
	ID                 string                                           `json:"id"`
	RunID              string                                           `json:"runId"`
	AttemptID          string                                           `json:"attemptId"`
	NodeID             string                                           `json:"nodeId"`
	ProviderThreadID   string                                           `json:"providerThreadId"`
	ProviderTurnID     string                                           `json:"providerTurnId"`
	ProviderRequestID  string                                           `json:"providerRequestId"`
	InteractionKind    string                                           `json:"interactionKind"`
	Scope              provider.InteractionScope                        `json:"scope"`
	ScopeDigest        string                                           `json:"scopeDigest"`
	PolicyDigest       string                                           `json:"policyDigest"`
	Evidence           statestore.JSONSnapshot                          `json:"evidence"`
	Status             statestore.ProviderPermissionStatus              `json:"status"`
	Decision           *statestore.ProviderPermissionDecisionProjection `json:"decision,omitempty"`
	Receipt            *statestore.ProviderPermissionReceiptProjection  `json:"receipt,omitempty"`
	AllowedActions     []ProviderPermissionAction                       `json:"allowedActions"`
	ResourceVersion    uint64                                           `json:"resourceVersion"`
	LastGlobalPosition uint64                                           `json:"lastGlobalPosition"`
	CreatedAt          time.Time                                        `json:"createdAt"`
	UpdatedAt          time.Time                                        `json:"updatedAt"`
}
type ProviderPermissionList struct {
	SchemaVersion int                      `json:"schemaVersion"`
	Items         []ProviderPermissionView `json:"items"`
}
type DecideProviderPermissionRequest struct {
	PermissionRequestID     string
	ExpectedResourceVersion uint64
	ScopeDigest             string
	Decision                provider.PermissionDecision
	IdempotencyKey          string
	Actor                   statestore.Actor
}

type ProviderPermissionVersionConflictError struct {
	PermissionRequestID string
	Expected            uint64
	Current             uint64
}

func (e *ProviderPermissionVersionConflictError) Error() string {
	return fmt.Sprintf("provider permission %s resource version is %d, expected %d", e.PermissionRequestID, e.Current, e.Expected)
}

func (s *Service) ProviderPermission(ctx context.Context, id string) (ProviderPermissionView, error) {
	value, err := s.store.ProviderPermission(ctx, id)
	if err != nil {
		return ProviderPermissionView{}, err
	}
	return s.providerPermissionView(ctx, value)
}
func (s *Service) ProviderPermissions(ctx context.Context, status statestore.ProviderPermissionStatus) (ProviderPermissionList, error) {
	if status == "" {
		status = statestore.ProviderPermissionPending
	}
	if !validPermissionStatus(status) {
		return ProviderPermissionList{}, ErrPermissionInvalidRequest
	}
	values, err := s.store.ProviderPermissions(ctx, status)
	if err != nil {
		return ProviderPermissionList{}, err
	}
	views, err := s.providerPermissionViews(ctx, values)
	return ProviderPermissionList{1, views}, err
}
func (s *Service) ProviderPermissionsForAttempt(ctx context.Context, attemptID string, status statestore.ProviderPermissionStatus) (ProviderPermissionList, error) {
	if status == "" {
		status = statestore.ProviderPermissionPending
	}
	if !validPermissionStatus(status) {
		return ProviderPermissionList{}, ErrPermissionInvalidRequest
	}
	values, err := s.store.ProviderPermissionsForAttempt(ctx, attemptID)
	if err != nil {
		return ProviderPermissionList{}, err
	}
	filtered := make([]statestore.ProviderPermissionProjection, 0)
	for _, value := range values {
		if value.Status == status {
			filtered = append(filtered, value)
		}
	}
	views, err := s.providerPermissionViews(ctx, filtered)
	return ProviderPermissionList{1, views}, err
}

func (s *Service) DecideProviderPermission(ctx context.Context, request DecideProviderPermissionRequest) (ProviderPermissionView, error) {
	if !permissionIDPattern.MatchString(request.PermissionRequestID) || request.ExpectedResourceVersion == 0 || !inputDigestPattern.MatchString(request.ScopeDigest) || !validPermissionDecision(request.Decision) || strings.TrimSpace(request.IdempotencyKey) == "" || !validInputActor(request.Actor) {
		return ProviderPermissionView{}, ErrPermissionInvalidRequest
	}
	value, err := s.store.ProviderPermission(ctx, request.PermissionRequestID)
	if err != nil {
		return ProviderPermissionView{}, err
	}
	if value.ScopeDigest != request.ScopeDigest {
		return ProviderPermissionView{}, ErrPermissionScopeConflict
	}
	ownerActive, err := s.providerPermissionOwnerActive(ctx, value)
	if err != nil {
		return ProviderPermissionView{}, err
	}
	if !ownerActive && value.Status != statestore.ProviderPermissionResponded {
		return ProviderPermissionView{}, ErrPermissionInactiveAttempt
	}
	if value.Decision != nil {
		same := value.Decision.ActionKey == request.IdempotencyKey && value.Decision.Decision == string(request.Decision) && value.Decision.Actor == request.Actor
		if !same {
			if value.Status == statestore.ProviderPermissionResponded {
				return ProviderPermissionView{}, ErrPermissionAlreadyDecided
			}
			return ProviderPermissionView{}, ErrPermissionDecisionInProgress
		}
		if value.Status == statestore.ProviderPermissionResponded {
			return s.providerPermissionView(ctx, value)
		}
	}
	if value.Decision == nil {
		if value.ResourceVersion != request.ExpectedResourceVersion {
			return ProviderPermissionView{}, &ProviderPermissionVersionConflictError{PermissionRequestID: value.PermissionRequestID, Expected: request.ExpectedResourceVersion, Current: value.ResourceVersion}
		}
		if value.Status != statestore.ProviderPermissionPending {
			return ProviderPermissionView{}, ErrPermissionScopeConflict
		}
		_, err = s.store.Append(ctx, pendingEvent("permission.decision_recorded", statestore.AggregatePermission, value.PermissionRequestID, value.ResourceVersion, value.RunID, request.IdempotencyKey, request.Actor.Type, request.Actor.ID, s.now(), map[string]any{"scopeDigest": request.ScopeDigest, "decision": request.Decision}))
		if err != nil {
			return ProviderPermissionView{}, err
		}
		value, err = s.store.ProviderPermission(ctx, value.PermissionRequestID)
		if err != nil {
			return ProviderPermissionView{}, err
		}
	}
	return s.deliverProviderPermission(ctx, value)
}
func (s *Service) RetryProviderPermissionDelivery(ctx context.Context, id string, expected uint64) (ProviderPermissionView, error) {
	value, err := s.store.ProviderPermission(ctx, id)
	if err != nil {
		return ProviderPermissionView{}, err
	}
	if expected == 0 {
		return ProviderPermissionView{}, ErrPermissionScopeConflict
	}
	if value.ResourceVersion != expected {
		return ProviderPermissionView{}, &ProviderPermissionVersionConflictError{PermissionRequestID: value.PermissionRequestID, Expected: expected, Current: value.ResourceVersion}
	}
	if value.Status == statestore.ProviderPermissionResponded {
		return s.providerPermissionView(ctx, value)
	}
	ownerActive, err := s.providerPermissionOwnerActive(ctx, value)
	if err != nil {
		return ProviderPermissionView{}, err
	}
	if !ownerActive {
		return ProviderPermissionView{}, ErrPermissionInactiveAttempt
	}
	if value.Status != statestore.ProviderPermissionDecisionRecorded || value.Decision == nil {
		return ProviderPermissionView{}, ErrPermissionInvalidRequest
	}
	return s.deliverProviderPermission(ctx, value)
}
func (s *Service) deliverProviderPermission(ctx context.Context, value statestore.ProviderPermissionProjection) (ProviderPermissionView, error) {
	s.mu.Lock()
	active := s.workers[value.AttemptID]
	var adapter provider.Provider
	var handle provider.AttemptHandle
	if active != nil {
		adapter, handle = active.adapter, active.handle
	}
	s.mu.Unlock()
	if adapter == nil || handle.AttemptID != value.AttemptID {
		view, _ := s.providerPermissionView(ctx, value)
		return view, ErrPermissionDeliveryUnavailable
	}
	receipt, err := adapter.Respond(ctx, provider.PermissionResponse{InteractionContext: provider.InteractionContext{AttemptID: value.AttemptID, ProviderThreadID: value.ProviderThreadID, ProviderRequestID: value.ProviderRequestID, IdempotencyKey: value.Decision.ActionKey, ScopeDigest: value.ScopeDigest}, Decision: provider.PermissionDecision(value.Decision.Decision)})
	if err != nil {
		view, _ := s.providerPermissionView(ctx, value)
		return view, fmt.Errorf("%w: %v", ErrPermissionDeliveryUnavailable, err)
	}
	if receipt.ProviderRequestID != value.ProviderRequestID {
		view, _ := s.providerPermissionView(ctx, value)
		return view, ErrPermissionDeliveryUnavailable
	}
	_, err = s.store.Append(ctx, pendingEvent("permission.response_delivered", statestore.AggregatePermission, value.PermissionRequestID, value.ResourceVersion, value.RunID, "deliver:"+value.Decision.ActionKey, statestore.ActorSystem, "daemon", s.now(), map[string]any{"providerRequestId": receipt.ProviderRequestID}))
	if err != nil {
		return ProviderPermissionView{}, err
	}
	value, err = s.store.ProviderPermission(ctx, value.PermissionRequestID)
	if err != nil {
		return ProviderPermissionView{}, err
	}
	return s.providerPermissionView(ctx, value)
}

func permissionRequestedEvent(attempt statestore.AttemptProjection, handle provider.AttemptHandle, event provider.Event, checkpoint provider.InteractionCheckpoint) statestore.PendingEvent {
	id := identity.Deterministic("permission_", attempt.AttemptID+"\x00"+checkpoint.ProviderRequestID)
	digest := fmt.Sprintf("%x", sha256.Sum256(event.Payload))
	itemDigest := fmt.Sprintf("%x", sha256.Sum256([]byte(event.ProviderItemID)))
	evidence := map[string]any{"kind": checkpoint.Kind, "summary": "Provider requested a " + string(checkpoint.Kind) + " interaction", "payloadDigest": digest, "providerItemDigest": itemDigest}
	return pendingEvent("permission.requested", statestore.AggregatePermission, id, 0, attempt.RunID, fmt.Sprintf("permission:%s:%d", attempt.AttemptID, event.Sequence), statestore.ActorProvider, event.Provider, event.OccurredAt, map[string]any{"runId": attempt.RunID, "attemptId": attempt.AttemptID, "nodeId": attempt.NodeID, "providerThreadId": handle.ProviderThreadID, "providerTurnId": checkpoint.ProviderTurnID, "providerRequestId": checkpoint.ProviderRequestID, "interactionKind": checkpoint.Kind, "scope": checkpoint.Scope, "scopeDigest": checkpoint.ScopeDigest, "policyDigest": checkpoint.PolicyDigest, "evidence": evidence})
}
func validPermissionDecision(value provider.PermissionDecision) bool {
	switch value {
	case provider.PermissionAllowOnce, provider.PermissionDenied, provider.PermissionCancelled:
		return true
	}
	return false
}
func validPermissionStatus(value statestore.ProviderPermissionStatus) bool {
	return value == statestore.ProviderPermissionPending || value == statestore.ProviderPermissionDecisionRecorded || value == statestore.ProviderPermissionResponded
}
func (s *Service) providerPermissionViews(ctx context.Context, values []statestore.ProviderPermissionProjection) ([]ProviderPermissionView, error) {
	views := make([]ProviderPermissionView, len(values))
	for index := range values {
		view, err := s.providerPermissionView(ctx, values[index])
		if err != nil {
			return nil, err
		}
		views[index] = view
	}
	return views, nil
}
func (s *Service) providerPermissionView(ctx context.Context, value statestore.ProviderPermissionProjection) (ProviderPermissionView, error) {
	ownerActive, err := s.providerPermissionOwnerActive(ctx, value)
	if err != nil {
		return ProviderPermissionView{}, err
	}
	return providerPermissionView(value, ownerActive), nil
}

func (s *Service) providerPermissionOwnerActive(ctx context.Context, value statestore.ProviderPermissionProjection) (bool, error) {
	attempt, err := s.store.Attempt(ctx, value.AttemptID)
	if err != nil {
		return false, err
	}
	run, err := s.store.Run(ctx, value.RunID)
	if err != nil {
		return false, err
	}
	attemptActive := attempt.Status == statestore.AttemptRunning || attempt.Status == statestore.AttemptValidating
	runActive := run.Status == statestore.RunRunning || run.Status == statestore.RunWaiting || run.Status == statestore.RunBlocked
	return attemptActive && runActive, nil
}

func providerPermissionView(value statestore.ProviderPermissionProjection, ownerActive bool) ProviderPermissionView {
	actions := []ProviderPermissionAction{}
	if ownerActive && value.Status == statestore.ProviderPermissionPending {
		actions = []ProviderPermissionAction{ProviderPermissionAllowOnce, ProviderPermissionDeny, ProviderPermissionCancel}
	} else if ownerActive && value.Status == statestore.ProviderPermissionDecisionRecorded {
		actions = []ProviderPermissionAction{ProviderPermissionRetryDelivery}
	}
	var scope provider.InteractionScope
	_ = json.Unmarshal([]byte(value.Scope), &scope)
	return ProviderPermissionView{value.PermissionRequestID, value.RunID, value.AttemptID, value.NodeID, value.ProviderThreadID, value.ProviderTurnID, value.ProviderRequestID, value.InteractionKind, scope, value.ScopeDigest, value.PolicyDigest, value.Evidence, value.Status, value.Decision, value.Receipt, actions, value.ResourceVersion, value.LastGlobalPosition, value.CreatedAt, value.UpdatedAt}
}
