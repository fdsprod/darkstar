// Package projection contains deterministic reducers for rebuildable current state.
package projection

import (
	"encoding/json"
	"errors"
	"fmt"

	"darkstar/src/ports/statestore"
)

// ReducerVersion changes whenever replay semantics change incompatibly.
const ReducerVersion = "8"

// UnsupportedSchemaVersionError means replay cannot safely interpret an event.
type UnsupportedSchemaVersionError struct {
	EventID string
	Version uint64
}

func (e *UnsupportedSchemaVersionError) Error() string {
	return fmt.Sprintf("event %s uses unsupported schema version %d", e.EventID, e.Version)
}

// InvalidTransitionError identifies a rejected lifecycle transition without
// leaking adapter-specific errors across the core boundary.
type InvalidTransitionError struct {
	Machine string
	ID      string
	From    string
	Event   string
}

func (e *InvalidTransitionError) Error() string {
	return fmt.Sprintf("%s %s cannot apply %s from state %s", e.Machine, e.ID, e.Event, e.From)
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
			Priority        int    `json:"priority"`
		}
		if err := decodeData(event, &data); err != nil {
			return statestore.RunProjection{}, true, err
		}
		if data.WorkItemID == "" || data.WorkflowID == "" || data.WorkflowVersion == "" {
			return statestore.RunProjection{}, true, errors.New("run.created requires workItemId, workflowId, and workflowVersion")
		}
		return statestore.RunProjection{
			RunID: event.AggregateID, WorkItemID: data.WorkItemID, WorkflowID: data.WorkflowID,
			WorkflowVersion: data.WorkflowVersion, Priority: data.Priority, Status: statestore.RunDraft,
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
	case "context.frozen":
		// Schema v1 histories created before the workflow state machines used a
		// context event here. Preserve it as a replay-only draft self-loop.
		if err := requireRunState(current, event, statestore.RunDraft); err != nil {
			return statestore.RunProjection{}, true, err
		}
	case "run.route_frozen":
		if err := requireRunState(current, event, statestore.RunDraft); err != nil {
			return statestore.RunProjection{}, true, err
		}
		var data struct {
			WorkflowDigest string          `json:"workflowDigest"`
			RouteDigest    string          `json:"routeDigest"`
			RouteSnapshot  json.RawMessage `json:"routeSnapshot"`
		}
		if err := decodeData(event, &data); err != nil {
			return statestore.RunProjection{}, true, err
		}
		completeSnapshot := data.WorkflowDigest != "" && data.RouteDigest != "" && len(data.RouteSnapshot) != 0
		emptySnapshot := data.WorkflowDigest == "" && data.RouteDigest == "" && len(data.RouteSnapshot) == 0
		if !completeSnapshot && !emptySnapshot {
			return statestore.RunProjection{}, true, errors.New("run.route_frozen requires workflowDigest, routeDigest, and routeSnapshot together")
		}
		if completeSnapshot && (!validSourceHash(data.WorkflowDigest) || !validSourceHash(data.RouteDigest)) {
			return statestore.RunProjection{}, true, errors.New("run.route_frozen digests must be SHA-256 hashes")
		}
		var boundaries struct {
			Entry     string   `json:"entry"`
			Terminals []string `json:"terminals"`
		}
		if completeSnapshot && (json.Unmarshal(data.RouteSnapshot, &boundaries) != nil || boundaries.Entry == "" || len(boundaries.Terminals) == 0) {
			return statestore.RunProjection{}, true, errors.New("run.route_frozen routeSnapshot requires an entry and at least one terminal boundary")
		}
		next.WorkflowDigest, next.RouteDigest = data.WorkflowDigest, data.RouteDigest
		next.RouteSnapshot = statestore.JSONSnapshot(string(data.RouteSnapshot))
		next.Status = statestore.RunReady
	case "run.started":
		if err := requireRunState(current, event, statestore.RunDraft, statestore.RunReady); err != nil {
			return statestore.RunProjection{}, true, err
		}
		if current.Status == statestore.RunDraft {
			// Compatibility for pre-state-machine schema v1 event histories.
			next.Status = statestore.RunRunning
		} else {
			next.Status = statestore.RunQueued
		}
	case "run.visit_ready":
		if err := requireRunState(current, event, statestore.RunQueued); err != nil {
			return statestore.RunProjection{}, true, err
		}
		next.Status = statestore.RunRunning
	case "run.waiting":
		if err := requireRunState(current, event, statestore.RunRunning); err != nil {
			return statestore.RunProjection{}, true, err
		}
		next.Status = statestore.RunWaiting
	case "run.paused":
		if err := requireRunState(current, event, statestore.RunQueued, statestore.RunRunning); err != nil {
			return statestore.RunProjection{}, true, err
		}
		next.Status = statestore.RunWaiting
	case "run.blocked":
		if err := requireRunState(current, event, statestore.RunRunning); err != nil {
			return statestore.RunProjection{}, true, err
		}
		next.Status = statestore.RunBlocked
	case "run.resumed":
		if err := requireRunState(current, event, statestore.RunWaiting, statestore.RunBlocked, statestore.RunFailed); err != nil {
			return statestore.RunProjection{}, true, err
		}
		next.Status = statestore.RunQueued
	case "run.retried":
		if err := requireRunState(current, event, statestore.RunFailed); err != nil {
			return statestore.RunProjection{}, true, err
		}
		next.Status = statestore.RunQueued
	case "run.continued":
		if err := requireRunState(current, event, statestore.RunCompleted); err != nil {
			return statestore.RunProjection{}, true, err
		}
		var data struct {
			WorkflowDigest string          `json:"workflowDigest"`
			RouteDigest    string          `json:"routeDigest"`
			RouteSnapshot  json.RawMessage `json:"routeSnapshot"`
			Until          string          `json:"until"`
		}
		if err := decodeData(event, &data); err != nil {
			return statestore.RunProjection{}, true, err
		}
		if !validSourceHash(data.WorkflowDigest) || !validSourceHash(data.RouteDigest) || len(data.RouteSnapshot) == 0 || data.Until == "" {
			return statestore.RunProjection{}, true, errors.New("run.continued requires workflow and route digests, a route snapshot, and an until boundary")
		}
		var route struct {
			Entry     string   `json:"entry"`
			Terminals []string `json:"terminals"`
		}
		if json.Unmarshal(data.RouteSnapshot, &route) != nil || route.Entry == "" || len(route.Terminals) == 0 {
			return statestore.RunProjection{}, true, errors.New("run.continued routeSnapshot requires an entry and at least one terminal boundary")
		}
		next.WorkflowDigest, next.RouteDigest = data.WorkflowDigest, data.RouteDigest
		next.RouteSnapshot = statestore.JSONSnapshot(string(data.RouteSnapshot))
		next.Status = statestore.RunQueued
	case "run.completed":
		if err := requireRunState(current, event, statestore.RunRunning); err != nil {
			return statestore.RunProjection{}, true, err
		}
		next.Status = statestore.RunCompleted
	case "run.failed":
		if err := requireRunState(current, event, statestore.RunRunning); err != nil {
			return statestore.RunProjection{}, true, err
		}
		next.Status = statestore.RunFailed
	case "run.cancelled":
		if current.Status.Terminal() {
			return statestore.RunProjection{}, true, invalidTransition("run", current.RunID, string(current.Status), event.Kind)
		}
		next.Status = statestore.RunCancelled
	case "run.reconcile_required":
		if current.Status.Terminal() {
			return statestore.RunProjection{}, true, invalidTransition("run", current.RunID, string(current.Status), event.Kind)
		}
		next.Status = statestore.RunReconcileRequired
	default:
		return statestore.RunProjection{}, true, invalidTransition("run", current.RunID, string(current.Status), event.Kind)
	}
	next.ResourceVersion = event.AggregateRevision
	next.LastGlobalPosition = event.GlobalPosition
	next.UpdatedAt = event.RecordedAt
	return next, true, nil
}

// ReduceNode applies an event to a workflow node-visit projection.
func ReduceNode(current *statestore.NodeProjection, event statestore.Event) (statestore.NodeProjection, bool, error) {
	if event.SchemaVersion != 1 {
		return statestore.NodeProjection{}, false, &UnsupportedSchemaVersionError{EventID: event.ID, Version: event.SchemaVersion}
	}
	if event.AggregateType != statestore.AggregateVisit {
		return statestore.NodeProjection{}, false, nil
	}
	if current == nil {
		if event.Kind != "visit.created" {
			return statestore.NodeProjection{}, true, fmt.Errorf("node visit %s first event is %s, want visit.created", event.AggregateID, event.Kind)
		}
		var data struct {
			RunID  string `json:"runId"`
			NodeID string `json:"nodeId"`
		}
		if err := decodeData(event, &data); err != nil {
			return statestore.NodeProjection{}, true, err
		}
		if data.RunID == "" || data.NodeID == "" {
			return statestore.NodeProjection{}, true, errors.New("visit.created requires runId and nodeId")
		}
		return statestore.NodeProjection{
			VisitID: event.AggregateID, RunID: data.RunID, NodeID: data.NodeID, Status: statestore.NodePending,
			ResourceVersion: event.AggregateRevision, LastGlobalPosition: event.GlobalPosition,
			CreatedAt: event.RecordedAt, UpdatedAt: event.RecordedAt,
		}, true, nil
	}
	if current.VisitID != event.AggregateID {
		return statestore.NodeProjection{}, true, fmt.Errorf("node projection %s cannot apply event for %s", current.VisitID, event.AggregateID)
	}
	if event.AggregateRevision != current.ResourceVersion+1 {
		return statestore.NodeProjection{}, true, fmt.Errorf("node visit %s projection revision %d cannot apply revision %d", current.VisitID, current.ResourceVersion, event.AggregateRevision)
	}

	next := *current
	switch event.Kind {
	case "visit.ready":
		if err := requireNodeState(current, event, statestore.NodePending); err != nil {
			return statestore.NodeProjection{}, true, err
		}
		next.Status = statestore.NodeReady
	case "visit.started":
		if err := requireNodeState(current, event, statestore.NodeReady); err != nil {
			return statestore.NodeProjection{}, true, err
		}
		next.Status = statestore.NodeRunning
	case "visit.result_received":
		if err := requireNodeState(current, event, statestore.NodeRunning); err != nil {
			return statestore.NodeProjection{}, true, err
		}
		next.Status = statestore.NodeValidating
	case "visit.succeeded":
		if err := requireNodeState(current, event, statestore.NodeValidating, statestore.NodeWaitingCheckpoint); err != nil {
			return statestore.NodeProjection{}, true, err
		}
		next.Status = statestore.NodeSucceeded
	case "visit.waiting_checkpoint":
		if err := requireNodeState(current, event, statestore.NodeValidating); err != nil {
			return statestore.NodeProjection{}, true, err
		}
		next.Status = statestore.NodeWaitingCheckpoint
	case "visit.changes_requested":
		if err := requireNodeState(current, event, statestore.NodeWaitingCheckpoint); err != nil {
			return statestore.NodeProjection{}, true, err
		}
		next.Status = statestore.NodeRunning
	case "visit.rejected":
		if err := requireNodeState(current, event, statestore.NodeWaitingCheckpoint); err != nil {
			return statestore.NodeProjection{}, true, err
		}
		next.Status = statestore.NodeRejected
	case "visit.retrying":
		if err := requireNodeState(current, event, statestore.NodeRunning, statestore.NodeValidating, statestore.NodeFailed); err != nil {
			return statestore.NodeProjection{}, true, err
		}
		next.Status = statestore.NodeReady
	case "visit.failed":
		if err := requireNodeState(current, event, statestore.NodeRunning, statestore.NodeValidating); err != nil {
			return statestore.NodeProjection{}, true, err
		}
		next.Status = statestore.NodeFailed
	case "visit.cancelled":
		if current.Status.Terminal() {
			return statestore.NodeProjection{}, true, invalidTransition("node visit", current.VisitID, string(current.Status), event.Kind)
		}
		next.Status = statestore.NodeCancelled
	default:
		return statestore.NodeProjection{}, true, invalidTransition("node visit", current.VisitID, string(current.Status), event.Kind)
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
			RunID         string `json:"runId"`
			VisitID       string `json:"visitId"`
			NodeID        string `json:"nodeId"`
			Scenario      string `json:"scenario"`
			Provider      string `json:"provider"`
			LogRef        string `json:"logReference"`
			PointID       string `json:"pointId"`
			PointRevision uint64 `json:"pointRevision"`
			Priority      int    `json:"priority"`
		}
		if err := decodeData(event, &data); err != nil {
			return statestore.AttemptProjection{}, true, err
		}
		visitOwned := data.NodeID != "" && data.PointID == "" && data.PointRevision == 0
		pointOwned := data.NodeID == "" && data.VisitID == "" && data.PointID != "" && data.PointRevision > 0
		if data.RunID == "" || data.Scenario == "" || data.Provider == "" || data.LogRef == "" || data.Priority < 0 || (!visitOwned && !pointOwned) {
			return statestore.AttemptProjection{}, true, errors.New("attempt.created requires runId, provider data, non-negative priority, and exactly one visit or point-revision owner")
		}
		return statestore.AttemptProjection{
			AttemptID: event.AggregateID, RunID: data.RunID, VisitID: data.VisitID, NodeID: data.NodeID,
			Scenario: data.Scenario, Provider: data.Provider, PointID: data.PointID, PointRevision: data.PointRevision,
			Priority: data.Priority, Status: statestore.AttemptCreated,
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
	next := *current
	switch event.Kind {
	case "attempt.resources_acquired":
		if err := requireAttemptState(current, event, statestore.AttemptCreated); err != nil {
			return statestore.AttemptProjection{}, true, err
		}
		next.Status = statestore.AttemptStarting
	case "attempt.started":
		if err := requireAttemptState(current, event, statestore.AttemptCreated, statestore.AttemptStarting); err != nil {
			return statestore.AttemptProjection{}, true, err
		}
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
	case "attempt.resumed":
		if err := requireAttemptState(current, event, statestore.AttemptRunning); err != nil {
			return statestore.AttemptProjection{}, true, err
		}
		var data struct {
			ProviderThreadID string `json:"providerThreadId"`
			ProviderTurnID   string `json:"providerTurnId"`
			ProcessOwnerID   string `json:"processOwnerId"`
		}
		if err := decodeData(event, &data); err != nil {
			return statestore.AttemptProjection{}, true, err
		}
		if data.ProviderThreadID != current.ProviderThreadID || data.ProviderTurnID != current.ProviderTurnID || data.ProcessOwnerID == "" {
			return statestore.AttemptProjection{}, true, errors.New("attempt.resumed recovery identity does not match the running attempt")
		}
		next.ProcessOwnerID = data.ProcessOwnerID
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
	case "attempt.result_received":
		if err := requireAttemptState(current, event, statestore.AttemptRunning); err != nil {
			return statestore.AttemptProjection{}, true, err
		}
		next.Status = statestore.AttemptValidating
	case "attempt.succeeded":
		if err := requireAttemptState(current, event, statestore.AttemptValidating); err != nil {
			return statestore.AttemptProjection{}, true, err
		}
		next.Status = statestore.AttemptSucceeded
	case "attempt.completed":
		// Compatibility for pre-state-machine schema v1 event histories.
		if err := requireAttemptState(current, event, statestore.AttemptRunning, statestore.AttemptValidating); err != nil {
			return statestore.AttemptProjection{}, true, err
		}
		next.Status = statestore.AttemptSucceeded
	case "attempt.failed":
		if err := requireAttemptState(current, event, statestore.AttemptCreated, statestore.AttemptStarting, statestore.AttemptRunning, statestore.AttemptValidating); err != nil {
			return statestore.AttemptProjection{}, true, err
		}
		next.Status = statestore.AttemptFailed
	case "attempt.cancelled":
		if current.Status.Terminal() {
			return statestore.AttemptProjection{}, true, invalidTransition("attempt", current.AttemptID, string(current.Status), event.Kind)
		}
		next.Status = statestore.AttemptCancelled
	case "attempt.interrupted":
		if current.Status.Terminal() {
			return statestore.AttemptProjection{}, true, invalidTransition("attempt", current.AttemptID, string(current.Status), event.Kind)
		}
		next.Status = statestore.AttemptInterrupted
	case "attempt.reconcile_required":
		if current.Status.Terminal() {
			return statestore.AttemptProjection{}, true, invalidTransition("attempt", current.AttemptID, string(current.Status), event.Kind)
		}
		next.Status = statestore.AttemptReconcileRequired
	default:
		return statestore.AttemptProjection{}, true, invalidTransition("attempt", current.AttemptID, string(current.Status), event.Kind)
	}
	next.ResourceVersion = event.AggregateRevision
	next.LastGlobalPosition = event.GlobalPosition
	next.UpdatedAt = event.RecordedAt
	return next, true, nil
}

func requireRunState(current *statestore.RunProjection, event statestore.Event, allowed ...statestore.RunStatus) error {
	for _, state := range allowed {
		if current.Status == state {
			return nil
		}
	}
	return invalidTransition("run", current.RunID, string(current.Status), event.Kind)
}

func requireNodeState(current *statestore.NodeProjection, event statestore.Event, allowed ...statestore.NodeStatus) error {
	for _, state := range allowed {
		if current.Status == state {
			return nil
		}
	}
	return invalidTransition("node visit", current.VisitID, string(current.Status), event.Kind)
}

func requireAttemptState(current *statestore.AttemptProjection, event statestore.Event, allowed ...statestore.AttemptStatus) error {
	for _, state := range allowed {
		if current.Status == state {
			return nil
		}
	}
	return invalidTransition("attempt", current.AttemptID, string(current.Status), event.Kind)
}

func invalidTransition(machine, id, from, event string) error {
	return &InvalidTransitionError{Machine: machine, ID: id, From: from, Event: event}
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
			RunID                    string                   `json:"runId"`
			Class                    statestore.ApprovalClass `json:"class"`
			CheckpointID             string                   `json:"checkpointId"`
			VisitID                  string                   `json:"visitId"`
			NodeID                   string                   `json:"nodeId"`
			AttemptID                string                   `json:"attemptId"`
			CheckpointRevision       uint64                   `json:"checkpointRevision"`
			CandidateArtifactID      string                   `json:"candidateArtifactId"`
			CandidateArtifactVersion uint64                   `json:"candidateArtifactVersion"`
			CandidateDigest          string                   `json:"candidateDigest"`
			CheckpointMode           string                   `json:"checkpointMode"`
			MaxRevisions             *uint64                  `json:"maxRevisions"`
			ScopeDigest              string                   `json:"scopeDigest"`
			PolicyDigest             string                   `json:"policyDigest"`
		}
		if err := decodeData(event, &data); err != nil {
			return statestore.ApprovalProjection{}, true, err
		}
		if data.RunID == "" || data.ScopeDigest == "" || data.PolicyDigest == "" || !validApprovalClass(data.Class) {
			return statestore.ApprovalProjection{}, true, errors.New("approval.requested requires runId, a valid class, scopeDigest, and policyDigest")
		}
		checkpointFields := data.CheckpointID != "" || data.VisitID != "" || data.NodeID != "" || data.AttemptID != "" ||
			data.CheckpointRevision != 0 || data.CandidateArtifactID != "" || data.CandidateArtifactVersion != 0 ||
			data.CandidateDigest != "" || data.CheckpointMode != "" || data.MaxRevisions != nil
		if checkpointFields {
			complete := data.Class == statestore.ApprovalWorkflowCheckpoint && data.CheckpointID != "" && data.VisitID != "" &&
				data.NodeID != "" && data.AttemptID != "" && data.CheckpointRevision > 0 && data.CandidateArtifactID != "" &&
				data.CandidateArtifactVersion > 0 && data.CandidateDigest != "" &&
				(data.CheckpointMode == "approve" || data.CheckpointMode == "approve_on_change") &&
				(data.MaxRevisions == nil || *data.MaxRevisions > 0)
			if !complete {
				return statestore.ApprovalProjection{}, true, errors.New("artifact checkpoint approval.requested requires one complete checkpoint subject")
			}
		}
		return statestore.ApprovalProjection{
			ApprovalID: event.AggregateID, RunID: data.RunID, Class: data.Class,
			Status: statestore.ApprovalPending, ScopeDigest: data.ScopeDigest, PolicyDigest: data.PolicyDigest,
			CheckpointID: data.CheckpointID, VisitID: data.VisitID, NodeID: data.NodeID, AttemptID: data.AttemptID,
			CheckpointRevision: data.CheckpointRevision, CandidateArtifactID: data.CandidateArtifactID,
			CandidateArtifactVersion: data.CandidateArtifactVersion, CandidateDigest: data.CandidateDigest,
			CheckpointMode: data.CheckpointMode, MaxRevisions: data.MaxRevisions,
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
		if current.Status != statestore.ApprovalPending {
			return statestore.ApprovalProjection{}, true, invalidTransition("approval", current.ApprovalID, string(current.Status), event.Kind)
		}
		var data struct {
			Action       string `json:"action"`
			ScopeDigest  string `json:"scopeDigest"`
			PolicyDigest string `json:"policyDigest"`
			Comment      string `json:"comment"`
		}
		if err := decodeData(event, &data); err != nil {
			return statestore.ApprovalProjection{}, true, err
		}
		if current.CheckpointID != "" && (data.ScopeDigest != current.ScopeDigest || data.PolicyDigest != current.PolicyDigest) {
			return statestore.ApprovalProjection{}, true, errors.New("approval.decided scope or policy digest does not match the pending request")
		}
		switch data.Action {
		case "approve":
			next.Status = statestore.ApprovalApproved
		case "acknowledge", "satisfy_external", "allow_once", "allow_for_session":
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
		next.Decision = &statestore.ApprovalDecisionProjection{
			Action: data.Action, ActionKey: event.CommandID, Comment: data.Comment,
			Actor: event.Actor, DecidedAt: event.RecordedAt,
		}
	case "approval.cancelled":
		if current.Status != statestore.ApprovalPending {
			return statestore.ApprovalProjection{}, true, invalidTransition("approval", current.ApprovalID, string(current.Status), event.Kind)
		}
		next.Status = statestore.ApprovalCancelled
	case "approval.expired":
		if current.Status != statestore.ApprovalPending {
			return statestore.ApprovalProjection{}, true, invalidTransition("approval", current.ApprovalID, string(current.Status), event.Kind)
		}
		next.Status = statestore.ApprovalExpired
	default:
		return statestore.ApprovalProjection{}, true, invalidTransition("approval", current.ApprovalID, string(current.Status), event.Kind)
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
