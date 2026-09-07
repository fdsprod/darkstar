package attention

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"darkstar/src/ports/statestore"
)

func TestListBuildsClosedFiveVariantProjectionWithOnlyLegalActions(t *testing.T) {
	when := time.Date(2026, 9, 7, 18, 0, 0, 0, time.UTC)
	source := attentionSource{
		projects: map[string]statestore.ProjectProjection{"project_1": {ProjectID: "project_1", Name: "Darkstar"}},
		works:    map[string]statestore.WorkItemProjection{"work_1": {WorkItemID: "work_1", ProjectID: "project_1", Title: "Typed checkpoints", Priority: 80}},
		runs:     map[string]statestore.RunProjection{"run_1": {RunID: "run_1", WorkItemID: "work_1", Status: statestore.RunRunning}},
		attempts: map[string]statestore.AttemptProjection{"attempt_1": {AttemptID: "attempt_1", RunID: "run_1", Status: statestore.AttemptRunning}},
		approvals: []statestore.ApprovalProjection{
			approval("approval_checkpoint", statestore.ApprovalWorkflowCheckpoint, when),
			approval("approval_control", statestore.ApprovalWorkflowControl, when.Add(time.Second)),
			// A legacy generic approval must not duplicate the dedicated provider
			// permission projection below.
			approval("approval_permission_duplicate", statestore.ApprovalProviderPermission, when.Add(2*time.Second)),
			approval("approval_delivery", statestore.ApprovalExternalDelivery, when.Add(3*time.Second)),
		},
		permissions: map[statestore.ProviderPermissionStatus][]statestore.ProviderPermissionProjection{
			statestore.ProviderPermissionPending: {providerPermission("permission_1", statestore.ProviderPermissionPending, when.Add(2*time.Second))},
		},
		inputs: map[statestore.InputRequestStatus][]statestore.InputRequestProjection{
			statestore.InputRequestPending: {{InputRequestID: "input_1", RunID: "run_1", AttemptID: "attempt_1", NodeID: "build", ProviderRequestID: "provider_input", ScopeDigest: "input-scope", Request: `{"questions":[]}`, Status: statestore.InputRequestPending, ResourceVersion: 2, CreatedAt: when.Add(4 * time.Second)}},
		},
	}
	service, err := New(&source)
	if err != nil {
		t.Fatal(err)
	}
	page, err := service.List(context.Background(), ListRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if page.SchemaVersion != 1 || len(page.Items) != 5 {
		t.Fatalf("page = %#v, want all five variants", page)
	}
	if _, ok := page.Items[0].(WorkflowCheckpoint); !ok {
		t.Fatalf("item 0 = %T", page.Items[0])
	}
	if _, ok := page.Items[1].(WorkflowControl); !ok {
		t.Fatalf("item 1 = %T", page.Items[1])
	}
	if _, ok := page.Items[2].(ProviderPermission); !ok {
		t.Fatalf("item 2 = %T", page.Items[2])
	}
	if _, ok := page.Items[3].(ExternalDelivery); !ok {
		t.Fatalf("item 3 = %T", page.Items[3])
	}
	if _, ok := page.Items[4].(InputRequired); !ok {
		t.Fatalf("item 4 = %T", page.Items[4])
	}

	checkpoint := page.Items[0].(WorkflowCheckpoint)
	if checkpoint.Context.ProjectName != "Darkstar" || checkpoint.Urgency != 80 || len(checkpoint.AllowedActions) != 3 || checkpoint.AllowedActions[0] != WorkflowCheckpointApprove {
		t.Fatalf("workflow checkpoint = %#v", checkpoint)
	}
	control := page.Items[1].(WorkflowControl)
	if len(control.AllowedActions) != 3 || control.AllowedActions[1] != WorkflowControlDeny {
		t.Fatalf("control actions = %#v", control.AllowedActions)
	}
	permission := page.Items[2].(ProviderPermission)
	if len(permission.AllowedActions) != 3 || permission.AllowedActions[0] != ProviderPermissionAllowOnce {
		t.Fatalf("permission actions = %#v", permission.AllowedActions)
	}
	if permission.Subject.ProviderRequestID != "provider_request_1" || permission.Subject.Status != statestore.ProviderPermissionPending || permission.Subject.InteractionKind != "tool" {
		t.Fatalf("permission subject = %#v", permission.Subject)
	}
	delivery := page.Items[3].(ExternalDelivery)
	if len(delivery.AllowedActions) != 3 || delivery.AllowedActions[0] != ExternalDeliveryApprove {
		t.Fatalf("delivery actions = %#v", delivery.AllowedActions)
	}
	input := page.Items[4].(InputRequired)
	if len(input.AllowedActions) != 1 || input.AllowedActions[0] != InputRequiredAnswer {
		t.Fatalf("input actions = %#v", input.AllowedActions)
	}
}

func TestListDerivesProviderPermissionActionsFromRealSourceAndOwnerActivity(t *testing.T) {
	when := time.Date(2026, 9, 7, 18, 0, 0, 0, time.UTC)
	source := attentionSource{
		projects: map[string]statestore.ProjectProjection{"project_1": {ProjectID: "project_1", Name: "Darkstar"}},
		works:    map[string]statestore.WorkItemProjection{"work_1": {WorkItemID: "work_1", ProjectID: "project_1", Title: "Provider authority", Priority: 40}},
		runs:     map[string]statestore.RunProjection{"run_1": {RunID: "run_1", WorkItemID: "work_1", Status: statestore.RunBlocked}},
		attempts: map[string]statestore.AttemptProjection{"attempt_1": {AttemptID: "attempt_1", RunID: "run_1", Status: statestore.AttemptValidating}},
		permissions: map[statestore.ProviderPermissionStatus][]statestore.ProviderPermissionProjection{
			statestore.ProviderPermissionPending:          {providerPermission("permission_pending", statestore.ProviderPermissionPending, when)},
			statestore.ProviderPermissionDecisionRecorded: {providerPermission("permission_delivery", statestore.ProviderPermissionDecisionRecorded, when.Add(time.Second))},
		},
	}
	service, _ := New(&source)
	page, err := service.List(context.Background(), ListRequest{Kinds: []Kind{KindProviderPermission}})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 2 {
		t.Fatalf("items = %#v, want both unresolved permission states", page.Items)
	}
	pending := page.Items[0].(ProviderPermission)
	if got := pending.AllowedActions; len(got) != 3 || got[0] != ProviderPermissionAllowOnce || got[1] != ProviderPermissionDeny || got[2] != ProviderPermissionCancel {
		t.Fatalf("pending actions = %#v", got)
	}
	delivery := page.Items[1].(ProviderPermission)
	if got := delivery.AllowedActions; len(got) != 1 || got[0] != ProviderPermissionRetryDelivery {
		t.Fatalf("decision-recorded actions = %#v", got)
	}

	// Responded permission requests are terminal and owner inactivity removes
	// even otherwise unresolved requests from the operator queue.
	source.permissions = map[statestore.ProviderPermissionStatus][]statestore.ProviderPermissionProjection{
		statestore.ProviderPermissionResponded: {providerPermission("permission_responded", statestore.ProviderPermissionResponded, when)},
		statestore.ProviderPermissionPending:   {providerPermission("permission_inactive", statestore.ProviderPermissionPending, when)},
	}
	source.attempts["attempt_1"] = statestore.AttemptProjection{AttemptID: "attempt_1", RunID: "run_1", Status: statestore.AttemptSucceeded}
	page, err = service.List(context.Background(), ListRequest{Kinds: []Kind{KindProviderPermission}})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 0 {
		t.Fatalf("terminal/inactive permissions remained visible: %#v", page.Items)
	}
}

func TestListFiltersSortsAndResumesAfterSourceResolution(t *testing.T) {
	when := time.Date(2026, 9, 7, 18, 0, 0, 0, time.UTC)
	source := attentionSource{
		projects: map[string]statestore.ProjectProjection{"project_1": {ProjectID: "project_1", Name: "One"}, "project_2": {ProjectID: "project_2", Name: "Two"}},
		works:    map[string]statestore.WorkItemProjection{"work_low": {WorkItemID: "work_low", ProjectID: "project_1", Title: "Low", Priority: 1}, "work_high": {WorkItemID: "work_high", ProjectID: "project_1", Title: "High", Priority: 100}, "work_other": {WorkItemID: "work_other", ProjectID: "project_2", Title: "Other", Priority: 200}},
		runs:     map[string]statestore.RunProjection{"run_low": {RunID: "run_low", WorkItemID: "work_low"}, "run_high": {RunID: "run_high", WorkItemID: "work_high"}, "run_other": {RunID: "run_other", WorkItemID: "work_other"}},
		approvals: []statestore.ApprovalProjection{
			approvalForRun("approval_low", statestore.ApprovalWorkflowCheckpoint, "run_low", when),
			approvalForRun("approval_high_a", statestore.ApprovalWorkflowCheckpoint, "run_high", when.Add(time.Second)),
			approvalForRun("approval_high_b", statestore.ApprovalWorkflowCheckpoint, "run_high", when.Add(2*time.Second)),
			approvalForRun("approval_other", statestore.ApprovalWorkflowCheckpoint, "run_other", when),
		},
		inputs: map[statestore.InputRequestStatus][]statestore.InputRequestProjection{},
	}
	service, _ := New(&source)
	first, err := service.List(context.Background(), ListRequest{Kinds: []Kind{KindWorkflowCheckpoint}, ProjectID: "project_1", Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Items) != 1 || first.Items[0].Common().ID != "approval_high_a" || first.NextCursor == "" {
		t.Fatalf("first page = %#v", first)
	}

	// The first item resolving between requests must not shift or duplicate the
	// continuation because the cursor is a source-key position, not an offset.
	source.approvals = source.approvals[0:1:1]
	source.approvals = append(source.approvals, approvalForRun("approval_high_b", statestore.ApprovalWorkflowCheckpoint, "run_high", when.Add(2*time.Second)))
	second, err := service.List(context.Background(), ListRequest{Kinds: []Kind{KindWorkflowCheckpoint}, ProjectID: "project_1", Limit: 1, Cursor: first.NextCursor})
	if err != nil {
		t.Fatal(err)
	}
	if len(second.Items) != 1 || second.Items[0].Common().ID != "approval_high_b" {
		t.Fatalf("second page = %#v", second)
	}
	if _, err := service.List(context.Background(), ListRequest{Kinds: []Kind{KindInputRequired}, ProjectID: "project_1", Limit: 1, Cursor: first.NextCursor}); !errors.Is(err, ErrInvalidCursor) {
		t.Fatalf("cursor/filter mismatch = %v, want ErrInvalidCursor", err)
	}
}

func TestListRemovesResolvedSourcesAndInputDeliveryActionIsDerived(t *testing.T) {
	when := time.Date(2026, 9, 7, 18, 0, 0, 0, time.UTC)
	source := attentionSource{
		projects:  map[string]statestore.ProjectProjection{"project_1": {ProjectID: "project_1", Name: "Darkstar"}},
		works:     map[string]statestore.WorkItemProjection{"work_1": {WorkItemID: "work_1", ProjectID: "project_1", Title: "Queue", Priority: 1}},
		runs:      map[string]statestore.RunProjection{"run_1": {RunID: "run_1", WorkItemID: "work_1"}},
		approvals: []statestore.ApprovalProjection{{ApprovalID: "resolved", RunID: "run_1", Class: statestore.ApprovalWorkflowControl, Status: statestore.ApprovalApproved}},
		inputs: map[statestore.InputRequestStatus][]statestore.InputRequestProjection{
			statestore.InputRequestPending:        {{InputRequestID: "wrong_status", RunID: "run_1", Status: statestore.InputRequestAnswered}},
			statestore.InputRequestAnswerRecorded: {{InputRequestID: "delivery", RunID: "run_1", Status: statestore.InputRequestAnswerRecorded, CreatedAt: when, ResourceVersion: 3}},
		},
	}
	service, _ := New(&source)
	page, err := service.List(context.Background(), ListRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 1 {
		t.Fatalf("items = %#v", page.Items)
	}
	input := page.Items[0].(InputRequired)
	if len(input.AllowedActions) != 1 || input.AllowedActions[0] != InputRequiredRetryDelivery {
		t.Fatalf("actions = %#v", input.AllowedActions)
	}
}

func TestCheckpointUnionRejectsUnknownVariant(t *testing.T) {
	var page Page
	if err := json.Unmarshal([]byte(`{"schemaVersion":1,"items":[{"kind":"future_attention"}]}`), &page); err == nil {
		t.Fatal("unknown variant decoded successfully")
	}
}

func approval(id string, class statestore.ApprovalClass, when time.Time) statestore.ApprovalProjection {
	return approvalForRun(id, class, "run_1", when)
}

func approvalForRun(id string, class statestore.ApprovalClass, runID string, when time.Time) statestore.ApprovalProjection {
	return statestore.ApprovalProjection{ApprovalID: id, RunID: runID, Class: class, Status: statestore.ApprovalPending, CheckpointID: "checkpoint_1", VisitID: "visit_1", NodeID: "build", AttemptID: "attempt_1", CheckpointRevision: 1, CandidateArtifactID: "artifact_1", CandidateArtifactVersion: 2, CandidateDigest: "candidate", CheckpointMode: "approve", ScopeDigest: "scope", PolicyDigest: "policy", ResourceVersion: 1, CreatedAt: when}
}

func providerPermission(id string, status statestore.ProviderPermissionStatus, when time.Time) statestore.ProviderPermissionProjection {
	return statestore.ProviderPermissionProjection{PermissionRequestID: id, RunID: "run_1", AttemptID: "attempt_1", NodeID: "build", ProviderThreadID: "thread_1", ProviderTurnID: "turn_1", ProviderRequestID: "provider_request_1", InteractionKind: "tool", Scope: `{"tool":"deploy"}`, ScopeDigest: "scope", PolicyDigest: "policy", Evidence: `{"summary":"Deploy"}`, Status: status, ResourceVersion: 4, CreatedAt: when}
}

type attentionSource struct {
	approvals   []statestore.ApprovalProjection
	inputs      map[statestore.InputRequestStatus][]statestore.InputRequestProjection
	permissions map[statestore.ProviderPermissionStatus][]statestore.ProviderPermissionProjection
	runs        map[string]statestore.RunProjection
	attempts    map[string]statestore.AttemptProjection
	works       map[string]statestore.WorkItemProjection
	projects    map[string]statestore.ProjectProjection
}

func (source *attentionSource) Approvals(context.Context, statestore.ApprovalStatus) ([]statestore.ApprovalProjection, error) {
	return append([]statestore.ApprovalProjection(nil), source.approvals...), nil
}
func (source *attentionSource) InputRequests(_ context.Context, status statestore.InputRequestStatus) ([]statestore.InputRequestProjection, error) {
	return append([]statestore.InputRequestProjection(nil), source.inputs[status]...), nil
}
func (source *attentionSource) ProviderPermissions(_ context.Context, status statestore.ProviderPermissionStatus) ([]statestore.ProviderPermissionProjection, error) {
	return append([]statestore.ProviderPermissionProjection(nil), source.permissions[status]...), nil
}
func (source *attentionSource) Run(_ context.Context, id string) (statestore.RunProjection, error) {
	value, ok := source.runs[id]
	if !ok {
		return value, statestore.ErrNotFound
	}
	return value, nil
}
func (source *attentionSource) Attempt(_ context.Context, id string) (statestore.AttemptProjection, error) {
	value, ok := source.attempts[id]
	if !ok {
		return value, statestore.ErrNotFound
	}
	return value, nil
}
func (source *attentionSource) WorkItem(_ context.Context, id string) (statestore.WorkItemProjection, error) {
	value, ok := source.works[id]
	if !ok {
		return value, statestore.ErrNotFound
	}
	return value, nil
}
func (source *attentionSource) Project(_ context.Context, id string) (statestore.ProjectProjection, error) {
	value, ok := source.projects[id]
	if !ok {
		return value, statestore.ErrNotFound
	}
	return value, nil
}
