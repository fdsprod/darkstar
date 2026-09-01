// Package projection contains deterministic reducers for rebuildable current state.
package projection

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/fdsprod/darkstar/runtime/src/ports/statestore"
)

// ReducerVersion changes whenever replay semantics change incompatibly.
const ReducerVersion = "2"

// UnsupportedSchemaVersionError means replay cannot safely interpret an event.
type UnsupportedSchemaVersionError struct {
	EventID string
	Version uint64
}

func (e *UnsupportedSchemaVersionError) Error() string {
	return fmt.Sprintf("event %s uses unsupported schema version %d", e.EventID, e.Version)
}

// ReduceRun applies an event to a run projection. The boolean reports whether
// the event belongs to this projection.
func ReduceRun(current *statestore.RunProjection, event statestore.Event) (statestore.RunProjection, bool, error) {
	if event.SchemaVersion != 1 {
		return statestore.RunProjection{}, false, &UnsupportedSchemaVersionError{EventID: event.ID, Version: event.SchemaVersion}
	}
	if event.AggregateType != statestore.AggregateRun {
		return statestore.RunProjection{}, false, nil
	}

	if current == nil {
		if event.Kind != "run.created" {
			return statestore.RunProjection{}, true, fmt.Errorf("run %s first event is %s, want run.created", event.AggregateID, event.Kind)
		}
		var data struct {
			WorkItemID      string `json:"workItemId"`
			WorkflowID      string `json:"workflowId"`
			WorkflowVersion string `json:"workflowVersion"`
		}
		if err := decodeData(event, &data); err != nil {
			return statestore.RunProjection{}, true, err
		}
		if data.WorkItemID == "" || data.WorkflowID == "" || data.WorkflowVersion == "" {
			return statestore.RunProjection{}, true, errors.New("run.created requires workItemId, workflowId, and workflowVersion")
		}
		return statestore.RunProjection{
			RunID: event.AggregateID, WorkItemID: data.WorkItemID, WorkflowID: data.WorkflowID,
			WorkflowVersion: data.WorkflowVersion, Status: statestore.RunPending,
			ResourceVersion: event.AggregateRevision, LastGlobalPosition: event.GlobalPosition,
			CreatedAt: event.RecordedAt, UpdatedAt: event.RecordedAt,
		}, true, nil
	}
	if current.RunID != event.AggregateID {
		return statestore.RunProjection{}, true, fmt.Errorf("run projection %s cannot apply event for %s", current.RunID, event.AggregateID)
	}
	if event.AggregateRevision != current.ResourceVersion+1 {
		return statestore.RunProjection{}, true, fmt.Errorf("run %s projection revision %d cannot apply revision %d", current.RunID, current.ResourceVersion, event.AggregateRevision)
	}

	next := *current
	switch event.Kind {
	case "run.started", "run.resumed":
		next.Status = statestore.RunRunning
	case "run.waiting":
		next.Status = statestore.RunWaiting
	case "run.completed":
		next.Status = statestore.RunCompleted
	case "run.failed":
		next.Status = statestore.RunFailed
	case "run.cancelled":
		next.Status = statestore.RunCancelled
	case "run.reconcile_required":
		next.Status = statestore.RunReconcileRequired
	}
	next.ResourceVersion = event.AggregateRevision
	next.LastGlobalPosition = event.GlobalPosition
	next.UpdatedAt = event.RecordedAt
	return next, true, nil
}

// ReduceAttempt applies an event to an attempt projection.
func ReduceAttempt(current *statestore.AttemptProjection, event statestore.Event) (statestore.AttemptProjection, bool, error) {
	if event.SchemaVersion != 1 {
		return statestore.AttemptProjection{}, false, &UnsupportedSchemaVersionError{EventID: event.ID, Version: event.SchemaVersion}
	}
	if event.AggregateType != statestore.AggregateAttempt {
		return statestore.AttemptProjection{}, false, nil
	}
	if current == nil {
		if event.Kind != "attempt.created" {
			return statestore.AttemptProjection{}, true, fmt.Errorf("attempt %s first event is %s, want attempt.created", event.AggregateID, event.Kind)
		}
		var data struct {
			RunID    string `json:"runId"`
			NodeID   string `json:"nodeId"`
			Scenario string `json:"scenario"`
			Provider string `json:"provider"`
			LogRef   string `json:"logReference"`
		}
		if err := decodeData(event, &data); err != nil {
			return statestore.AttemptProjection{}, true, err
		}
		if data.RunID == "" || data.NodeID == "" || data.Scenario == "" || data.Provider == "" || data.LogRef == "" {
			return statestore.AttemptProjection{}, true, errors.New("attempt.created requires runId, nodeId, scenario, provider, and logReference")
		}
		return statestore.AttemptProjection{
			AttemptID: event.AggregateID, RunID: data.RunID, NodeID: data.NodeID,
			Scenario: data.Scenario, Provider: data.Provider, Status: statestore.AttemptStarting,
			LogReference: data.LogRef, ResourceVersion: event.AggregateRevision,
			LastGlobalPosition: event.GlobalPosition, CreatedAt: event.RecordedAt, UpdatedAt: event.RecordedAt,
		}, true, nil
	}
	if current.AttemptID != event.AggregateID {
		return statestore.AttemptProjection{}, true, fmt.Errorf("attempt projection %s cannot apply event for %s", current.AttemptID, event.AggregateID)
	}
	if event.AggregateRevision != current.ResourceVersion+1 {
		return statestore.AttemptProjection{}, true, fmt.Errorf("attempt %s projection revision %d cannot apply revision %d", current.AttemptID, current.ResourceVersion, event.AggregateRevision)
	}
	if current.Status.Terminal() {
		return statestore.AttemptProjection{}, true, fmt.Errorf("attempt %s is already terminal in state %s", current.AttemptID, current.Status)
	}

	next := *current
	switch event.Kind {
	case "attempt.started", "attempt.resumed":
		var data struct {
			ProviderThreadID string `json:"providerThreadId"`
			ProviderTurnID   string `json:"providerTurnId"`
			ProcessOwnerID   string `json:"processOwnerId"`
		}
		if err := decodeData(event, &data); err != nil {
			return statestore.AttemptProjection{}, true, err
		}
		if data.ProviderThreadID == "" || data.ProviderTurnID == "" || data.ProcessOwnerID == "" {
			return statestore.AttemptProjection{}, true, fmt.Errorf("%s requires complete provider recovery identity", event.Kind)
		}
		next.Status = statestore.AttemptRunning
		next.ProviderThreadID, next.ProviderTurnID, next.ProcessOwnerID = data.ProviderThreadID, data.ProviderTurnID, data.ProcessOwnerID
	case "attempt.provider_event":
		var data struct {
			Sequence uint64 `json:"sequence"`
		}
		if err := decodeData(event, &data); err != nil {
			return statestore.AttemptProjection{}, true, err
		}
		if next.Status != statestore.AttemptRunning || data.Sequence <= next.LastSequence {
			return statestore.AttemptProjection{}, true, fmt.Errorf("attempt %s provider sequence %d does not advance %d while running", current.AttemptID, data.Sequence, next.LastSequence)
		}
		next.LastSequence = data.Sequence
	case "attempt.completed":
		next.Status = statestore.AttemptCompleted
	case "attempt.failed":
		next.Status = statestore.AttemptFailed
	case "attempt.cancelled":
		next.Status = statestore.AttemptCancelled
	case "attempt.interrupted":
		next.Status = statestore.AttemptInterrupted
	case "attempt.reconcile_required":
		next.Status = statestore.AttemptReconcileRequired
	default:
		return statestore.AttemptProjection{}, true, fmt.Errorf("unsupported attempt event kind %q", event.Kind)
	}
	next.ResourceVersion = event.AggregateRevision
	next.LastGlobalPosition = event.GlobalPosition
	next.UpdatedAt = event.RecordedAt
	return next, true, nil
}

// ReduceApproval applies an event to an approval projection.
func ReduceApproval(current *statestore.ApprovalProjection, event statestore.Event) (statestore.ApprovalProjection, bool, error) {
	if event.SchemaVersion != 1 {
		return statestore.ApprovalProjection{}, false, &UnsupportedSchemaVersionError{EventID: event.ID, Version: event.SchemaVersion}
	}
	if event.AggregateType != statestore.AggregateApproval {
		return statestore.ApprovalProjection{}, false, nil
	}

	if current == nil {
		if event.Kind != "approval.requested" {
			return statestore.ApprovalProjection{}, true, fmt.Errorf("approval %s first event is %s, want approval.requested", event.AggregateID, event.Kind)
		}
		var data struct {
			RunID        string                   `json:"runId"`
			Class        statestore.ApprovalClass `json:"class"`
			ScopeDigest  string                   `json:"scopeDigest"`
			PolicyDigest string                   `json:"policyDigest"`
		}
		if err := decodeData(event, &data); err != nil {
			return statestore.ApprovalProjection{}, true, err
		}
		if data.RunID == "" || data.ScopeDigest == "" || data.PolicyDigest == "" || !validApprovalClass(data.Class) {
			return statestore.ApprovalProjection{}, true, errors.New("approval.requested requires runId, a valid class, scopeDigest, and policyDigest")
		}
		return statestore.ApprovalProjection{
			ApprovalID: event.AggregateID, RunID: data.RunID, Class: data.Class,
			Status: statestore.ApprovalPending, ScopeDigest: data.ScopeDigest, PolicyDigest: data.PolicyDigest,
			ResourceVersion: event.AggregateRevision, LastGlobalPosition: event.GlobalPosition,
			CreatedAt: event.RecordedAt, UpdatedAt: event.RecordedAt,
		}, true, nil
	}
	if current.ApprovalID != event.AggregateID {
		return statestore.ApprovalProjection{}, true, fmt.Errorf("approval projection %s cannot apply event for %s", current.ApprovalID, event.AggregateID)
	}
	if event.AggregateRevision != current.ResourceVersion+1 {
		return statestore.ApprovalProjection{}, true, fmt.Errorf("approval %s projection revision %d cannot apply revision %d", current.ApprovalID, current.ResourceVersion, event.AggregateRevision)
	}

	next := *current
	switch event.Kind {
	case "approval.decided":
		var data struct {
			Action string `json:"action"`
		}
		if err := decodeData(event, &data); err != nil {
			return statestore.ApprovalProjection{}, true, err
		}
		switch data.Action {
		case "approve":
			next.Status = statestore.ApprovalApproved
		case "request_changes":
			next.Status = statestore.ApprovalChangesRequested
		case "reject":
			next.Status = statestore.ApprovalRejected
		case "deny":
			next.Status = statestore.ApprovalDenied
		default:
			return statestore.ApprovalProjection{}, true, fmt.Errorf("approval.decided has invalid action %q", data.Action)
		}
	case "approval.cancelled":
		next.Status = statestore.ApprovalCancelled
	case "approval.expired":
		next.Status = statestore.ApprovalExpired
	}
	next.ResourceVersion = event.AggregateRevision
	next.LastGlobalPosition = event.GlobalPosition
	next.UpdatedAt = event.RecordedAt
	return next, true, nil
}

func decodeData(event statestore.Event, destination any) error {
	if err := json.Unmarshal(event.Data, destination); err != nil {
		return fmt.Errorf("decode %s data: %w", event.Kind, err)
	}
	return nil
}

func validApprovalClass(class statestore.ApprovalClass) bool {
	switch class {
	case statestore.ApprovalWorkflowCheckpoint, statestore.ApprovalWorkflowControl,
		statestore.ApprovalProviderPermission, statestore.ApprovalExternalDelivery:
		return true
	default:
		return false
	}
}
