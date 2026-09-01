package workflow_test

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/fdsprod/darkstar/runtime/src/core/workflow"
	"github.com/fdsprod/darkstar/runtime/src/ports/statestore"
)

func TestRoutePatchJSONKeepsOperationsClosedAndStrict(t *testing.T) {
	t.Parallel()
	raw := []byte(`{
  "apiVersion":"darkstar.local/v1alpha1",
  "kind":"RoutePatch",
  "metadata":{"id":"patch_insert_review"},
  "spec":{"runId":"run_1","expectedRouteRevision":0,"reason":"New interface uncertainty.","approvalId":"approval_1","operations":[
    {"op":"disableTransition","transitionId":"direct"},
    {"op":"enableTransition","transitionId":"to_review"},
    {"op":"setTerminals","nodes":["end"]}
  ]}
}`)
	var patch workflow.RoutePatch
	if err := json.Unmarshal(raw, &patch); err != nil {
		t.Fatal(err)
	}
	if _, ok := patch.Spec.Operations[0].(workflow.DisableTransitionOperation); !ok {
		t.Fatalf("first operation has type %T", patch.Spec.Operations[0])
	}
	if _, ok := patch.Spec.Operations[1].(workflow.EnableTransitionOperation); !ok {
		t.Fatalf("second operation has type %T", patch.Spec.Operations[1])
	}
	terminal, ok := patch.Spec.Operations[2].(workflow.SetTerminalsOperation)
	if !ok || !reflect.DeepEqual(terminal.Nodes, []workflow.Identifier{"end"}) {
		t.Fatalf("terminal operation = %#v", patch.Spec.Operations[2])
	}
	encoded, err := json.Marshal(patch)
	if err != nil {
		t.Fatal(err)
	}
	var roundTrip workflow.RoutePatch
	if err := json.Unmarshal(encoded, &roundTrip); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(roundTrip, patch) {
		t.Fatalf("round trip = %#v, want %#v", roundTrip, patch)
	}

	for _, invalid := range []string{
		strings.Replace(string(raw), `"transitionId":"direct"`, `"transitionId":"direct","target":"review"`, 1),
		strings.Replace(string(raw), `"op":"disableTransition"`, `"op":"authorTransition"`, 1),
		strings.Replace(string(raw), `"nodes":["end"]`, `"nodes":["end","end"]`, 1),
	} {
		if err := json.Unmarshal([]byte(invalid), &patch); err == nil {
			t.Fatalf("invalid route patch was accepted: %s", invalid)
		}
	}
}

func TestApprovedRoutePatchInsertsPredeclaredNodeAndAdvancesRevision(t *testing.T) {
	t.Parallel()
	document, current := patchableRouteState(t)
	patch := insertReviewPatch()
	proposal, err := workflow.ProposeRoutePatch(document, current, patch, workflow.RouteContext{}, approvalPolicy())
	if err != nil {
		t.Fatal(err)
	}
	impact := proposal.Impact()
	if !reflect.DeepEqual(impact.AddedNodes, []workflow.Identifier{"review"}) ||
		!reflect.DeepEqual(impact.AddedTransitions, []workflow.Identifier{"review_to_end", "to_review"}) ||
		!reflect.DeepEqual(impact.RemovedTransitions, []workflow.Identifier{"direct"}) {
		t.Fatalf("impact = %#v", impact)
	}
	if proposal.AuthorizationMode() != workflow.RoutePatchRequireApproval || !digest(proposal.ScopeDigest()) || !digest(proposal.ValidationDigest()) {
		t.Fatalf("proposal authorization = %q, scope %q, validation %q", proposal.AuthorizationMode(), proposal.ScopeDigest(), proposal.ValidationDigest())
	}

	authorized, err := workflow.AuthorizeRoutePatch(proposal, approvedRoutePatch(proposal, statestore.ApprovalWorkflowControl, statestore.ApprovalApproved))
	if err != nil {
		t.Fatal(err)
	}
	next, record, err := workflow.ApplyAuthorizedRoutePatch(document, current, authorized, workflow.RouteContext{}, workflow.RoutePatchExecutionState{})
	if err != nil {
		t.Fatal(err)
	}
	if current.Revision() != 0 || next.Revision() != 1 {
		t.Fatalf("revisions current=%d next=%d", current.Revision(), next.Revision())
	}
	if got := transitionIDs(next.Route()); !reflect.DeepEqual(got, []workflow.Identifier{"review_to_end", "to_review"}) {
		t.Fatalf("patched transitions = %v", got)
	}
	if got := routeNodeIDs(next.Route()); !reflect.DeepEqual(got, []workflow.Identifier{"end", "review", "start"}) {
		t.Fatalf("patched nodes = %v", got)
	}
	if record.OldRevision != 0 || record.NewRevision != 1 || record.ApprovalID != "approval_1" || record.Actor.ID != "operator_1" ||
		record.ScopeDigest != proposal.ScopeDigest() || record.ValidationDigest != proposal.ValidationDigest() {
		t.Fatalf("application record = %#v", record)
	}
	if got := next.Snapshot().Overrides; !reflect.DeepEqual(got, []workflow.TransitionOverride{
		{TransitionID: "direct", Enabled: false},
		{TransitionID: "review_to_end", Enabled: true},
		{TransitionID: "to_review", Enabled: true},
	}) {
		t.Fatalf("overrides = %#v", got)
	}
	if _, _, err := workflow.ApplyAuthorizedRoutePatch(document, next, authorized, workflow.RouteContext{}, workflow.RoutePatchExecutionState{}); patchCode(err) != workflow.RoutePatchConflict {
		t.Fatalf("replay error = %#v", err)
	}
}

func TestAutomaticAuthorizationIsLimitedToAutomaticNonExpansion(t *testing.T) {
	t.Parallel()
	document, current := patchableRouteState(t)
	automaticPatch := insertReviewPatch()
	automaticPatch.Spec.ApprovalID = ""
	proposal, err := workflow.ProposeRoutePatch(document, current, automaticPatch, workflow.RouteContext{}, automaticPolicy())
	if err != nil {
		t.Fatal(err)
	}
	if proposal.AuthorizationMode() != workflow.RoutePatchAutomatic {
		t.Fatalf("authorization mode = %q", proposal.AuthorizationMode())
	}
	automatic := workflow.AutomaticRoutePatchAuthorization{
		Actor:       statestore.Actor{Type: statestore.ActorSystem, ID: "route-policy"},
		ScopeDigest: proposal.ScopeDigest(), PolicyDigest: strings.Repeat("b", 64),
	}
	authorized, err := workflow.AuthorizeRoutePatch(proposal, automatic)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := workflow.ApplyAuthorizedRoutePatch(document, current, authorized, workflow.RouteContext{}, workflow.RoutePatchExecutionState{}); err != nil {
		t.Fatal(err)
	}

	linear := linearRouteWorkflow()
	route, routeErrors := workflow.CreateRoute(linear, workflow.RouteRequest{Until: []workflow.Identifier{"middle"}}, workflow.RouteContext{})
	if len(routeErrors) != 0 {
		t.Fatal(routeErrors)
	}
	linearState, err := workflow.NewRouteState("run_1", route)
	if err != nil {
		t.Fatal(err)
	}
	expansion := routePatch(workflow.SetTerminalsOperation{Nodes: []workflow.Identifier{"end"}})
	expansion.Spec.ApprovalID = ""
	expansionProposal, err := workflow.ProposeRoutePatch(linear, linearState, expansion, workflow.RouteContext{}, automaticPolicy())
	if err != nil {
		t.Fatal(err)
	}
	if expansionProposal.AuthorizationMode() != workflow.RoutePatchRequireApproval || !expansionProposal.Impact().ExpandsTerminalBoundary() {
		t.Fatalf("terminal expansion proposal = mode %q, impact %#v", expansionProposal.AuthorizationMode(), expansionProposal.Impact())
	}
	automatic.ScopeDigest = expansionProposal.ScopeDigest()
	if _, err := workflow.AuthorizeRoutePatch(expansionProposal, automatic); patchCode(err) != workflow.RoutePatchAuthorizationRequired {
		t.Fatalf("terminal expansion automatic authorization error = %#v", err)
	}
}

func TestProviderApprovalCannotAuthorizeWorkflowControl(t *testing.T) {
	t.Parallel()
	document, current := patchableRouteState(t)
	proposal, err := workflow.ProposeRoutePatch(document, current, insertReviewPatch(), workflow.RouteContext{}, approvalPolicy())
	if err != nil {
		t.Fatal(err)
	}

	for _, authorization := range []workflow.RoutePatchAuthorization{
		approvedRoutePatch(proposal, statestore.ApprovalProviderPermission, statestore.ApprovalApproved),
		approvedRoutePatch(proposal, statestore.ApprovalWorkflowControl, statestore.ApprovalDenied),
		workflow.ApprovedRoutePatchAuthorization{
			Approval: statestore.ApprovalProjection{
				ApprovalID: "approval_1", RunID: "run_other", Class: statestore.ApprovalWorkflowControl,
				Status: statestore.ApprovalApproved, ScopeDigest: proposal.ScopeDigest(), PolicyDigest: strings.Repeat("a", 64),
			},
			Actor: statestore.Actor{Type: statestore.ActorUser, ID: "operator_1"},
		},
	} {
		if _, err := workflow.AuthorizeRoutePatch(proposal, authorization); patchCode(err) != workflow.RoutePatchAuthorizationInvalid {
			t.Fatalf("authorization %#v error = %#v", authorization, err)
		}
	}
}

func TestPatchApplicationRejectsRunningConsumedAndHistoricalConflictsAtomically(t *testing.T) {
	t.Parallel()
	document, current := patchableRouteState(t)
	proposal, err := workflow.ProposeRoutePatch(document, current, insertReviewPatch(), workflow.RouteContext{}, approvalPolicy())
	if err != nil {
		t.Fatal(err)
	}
	authorized, err := workflow.AuthorizeRoutePatch(proposal, approvedRoutePatch(proposal, statestore.ApprovalWorkflowControl, statestore.ApprovalApproved))
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name  string
		state workflow.RoutePatchExecutionState
		code  string
	}{
		{"running affected attempt", workflow.RoutePatchExecutionState{RunningAttempts: []workflow.Identifier{"review"}}, workflow.RoutePatchAttemptRunning},
		{"consumed disabled transition", workflow.RoutePatchExecutionState{EmittedTransitions: []workflow.Identifier{"direct"}}, workflow.RoutePatchTransitionConsumed},
	} {
		t.Run(test.name, func(t *testing.T) {
			next, record, err := workflow.ApplyAuthorizedRoutePatch(document, current, authorized, workflow.RouteContext{}, test.state)
			if patchCode(err) != test.code || next.RunID() != "" || record.PatchID != "" || current.Revision() != 0 {
				t.Fatalf("next %#v record %#v error %#v", next, record, err)
			}
		})
	}

	linear := linearRouteWorkflow()
	route, routeErrors := workflow.CreateRoute(linear, workflow.RouteRequest{}, workflow.RouteContext{})
	if len(routeErrors) != 0 {
		t.Fatal(routeErrors)
	}
	linearState, err := workflow.NewRouteState("run_1", route)
	if err != nil {
		t.Fatal(err)
	}
	terminalPatch := routePatch(workflow.SetTerminalsOperation{Nodes: []workflow.Identifier{"middle"}})
	terminalProposal, err := workflow.ProposeRoutePatch(linear, linearState, terminalPatch, workflow.RouteContext{}, approvalPolicy())
	if err != nil {
		t.Fatal(err)
	}
	terminalAuthorization, err := workflow.AuthorizeRoutePatch(terminalProposal, approvedRoutePatch(terminalProposal, statestore.ApprovalWorkflowControl, statestore.ApprovalApproved))
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = workflow.ApplyAuthorizedRoutePatch(linear, linearState, terminalAuthorization, workflow.RouteContext{}, workflow.RoutePatchExecutionState{SucceededNodes: []workflow.Identifier{"end"}})
	if patchCode(err) != workflow.RoutePatchHistoryConflict {
		t.Fatalf("history conflict error = %#v", err)
	}
}

func TestApprovalDoesNotBypassCurrentRouteRevalidation(t *testing.T) {
	t.Parallel()
	document, current := patchableRouteState(t)
	proposal, err := workflow.ProposeRoutePatch(document, current, insertReviewPatch(), workflow.RouteContext{}, approvalPolicy())
	if err != nil {
		t.Fatal(err)
	}
	authorized, err := workflow.AuthorizeRoutePatch(proposal, approvedRoutePatch(proposal, statestore.ApprovalWorkflowControl, statestore.ApprovalApproved))
	if err != nil {
		t.Fatal(err)
	}
	changedContext := workflow.RouteContext{RunInputs: map[workflow.Identifier]json.RawMessage{"topic": json.RawMessage(`"available"`)}}
	_, _, err = workflow.ApplyAuthorizedRoutePatch(document, current, authorized, changedContext, workflow.RoutePatchExecutionState{})
	if patchCode(err) != workflow.RoutePatchValidationStale {
		t.Fatalf("changed validation error = %#v", err)
	}

	_, _, err = workflow.ApplyAuthorizedRoutePatch(document, current, authorized, workflow.RouteContext{RequiredNodes: []workflow.Identifier{"missing"}}, workflow.RoutePatchExecutionState{})
	var validation *workflow.RoutePatchRouteValidationError
	if !errors.As(err, &validation) || len(validation.Errors) == 0 || validation.Errors[0].Code != workflow.ValidationRoutePolicyViolation {
		t.Fatalf("policy revalidation error = %#v", err)
	}
}

func TestPatchProposalRejectsUnknownDuplicateAndNoOpOperations(t *testing.T) {
	t.Parallel()
	document, current := patchableRouteState(t)
	for _, test := range []struct {
		name       string
		operations []workflow.RoutePatchOperation
		code       string
	}{
		{"unknown transition", []workflow.RoutePatchOperation{workflow.EnableTransitionOperation{TransitionID: "unknown"}}, workflow.WorkflowReferenceError},
		{"already enabled", []workflow.RoutePatchOperation{workflow.EnableTransitionOperation{TransitionID: "direct"}}, workflow.RoutePatchOperationInvalid},
		{"ambiguous exclusive outcomes", []workflow.RoutePatchOperation{workflow.EnableTransitionOperation{TransitionID: "to_review"}, workflow.EnableTransitionOperation{TransitionID: "review_to_end"}}, workflow.RoutePatchOperationInvalid},
		{"duplicate transition", []workflow.RoutePatchOperation{workflow.DisableTransitionOperation{TransitionID: "direct"}, workflow.EnableTransitionOperation{TransitionID: "direct"}}, workflow.RoutePatchOperationInvalid},
		{"duplicate terminal operation", []workflow.RoutePatchOperation{workflow.SetTerminalsOperation{Nodes: []workflow.Identifier{"end"}}, workflow.SetTerminalsOperation{Nodes: []workflow.Identifier{"end"}}}, workflow.RoutePatchOperationInvalid},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := workflow.ProposeRoutePatch(document, current, routePatch(test.operations...), workflow.RouteContext{}, approvalPolicy())
			if patchCode(err) != test.code || current.Revision() != 0 {
				t.Fatalf("error = %#v", err)
			}
		})
	}
}

func patchableRouteState(t *testing.T) (workflow.Document, workflow.RouteState) {
	t.Helper()
	falseValue := false
	document := workflow.Document{
		APIVersion: workflow.APIVersionV1Alpha1, Kind: workflow.KindWorkflow,
		Metadata: workflow.Metadata{Name: "patchable", Version: "1.0.0"},
		Spec: workflow.Spec{
			RouteDefaults: workflow.RouteDefaults{Entry: "start", Terminals: []workflow.Identifier{"end"}},
			Nodes: map[workflow.Identifier]workflow.Node{
				"start": command(workflow.NodeFields{
					Entry: true, Inputs: map[workflow.Identifier]workflow.Binding{}, Outputs: map[workflow.Identifier]workflow.OutputDeclaration{},
					TransitionMode: workflow.TransitionExclusive,
					Transitions: []workflow.Transition{
						normal("direct", "end"),
						workflow.NormalTransition{Common: workflow.TransitionFields{TransitionID: "to_review", To: "review", EnabledByDefault: &falseValue}},
					},
				}),
				"review": command(workflow.NodeFields{
					Terminal: true,
					Inputs: map[workflow.Identifier]workflow.Binding{
						"topic": workflow.RequiredBinding{From: "run.input.topic", Type: workflow.ValueString},
					},
					Outputs: map[workflow.Identifier]workflow.OutputDeclaration{}, TransitionMode: workflow.TransitionExclusive,
					Transitions: []workflow.Transition{
						workflow.NormalTransition{Common: workflow.TransitionFields{TransitionID: "review_to_end", To: "end", EnabledByDefault: &falseValue}},
					},
				}),
				"end": command(workflow.NodeFields{
					Terminal: true, Inputs: map[workflow.Identifier]workflow.Binding{}, Outputs: map[workflow.Identifier]workflow.OutputDeclaration{},
					TransitionMode: workflow.TransitionExclusive,
					Join:           &workflow.Join{Mode: workflow.JoinOne, From: []workflow.Identifier{"direct", "review_to_end"}},
				}),
			},
		},
	}
	route, routeErrors := workflow.CreateRoute(document, workflow.RouteRequest{}, workflow.RouteContext{})
	if len(routeErrors) != 0 {
		t.Fatal(routeErrors)
	}
	state, err := workflow.NewRouteState("run_1", route)
	if err != nil {
		t.Fatal(err)
	}
	return document, state
}

func insertReviewPatch() workflow.RoutePatch {
	return routePatch(
		workflow.DisableTransitionOperation{TransitionID: "direct"},
		workflow.EnableTransitionOperation{TransitionID: "to_review"},
		workflow.EnableTransitionOperation{TransitionID: "review_to_end"},
	)
}

func routePatch(operations ...workflow.RoutePatchOperation) workflow.RoutePatch {
	return workflow.RoutePatch{
		APIVersion: workflow.APIVersionV1Alpha1, Kind: workflow.KindRoutePatch,
		Metadata: workflow.RoutePatchMetadata{ID: "patch_1"},
		Spec: workflow.RoutePatchSpec{
			RunID: "run_1", ExpectedRouteRevision: 0, Reason: "New uncertainty requires a route change.",
			ApprovalID: "approval_1", Operations: operations,
		},
	}
}

func approvalPolicy() workflow.RoutePatchPolicy {
	return workflow.RoutePatchPolicy{Mode: workflow.RoutePatchRequireApproval, PolicyDigest: strings.Repeat("a", 64)}
}

func automaticPolicy() workflow.RoutePatchPolicy {
	return workflow.RoutePatchPolicy{Mode: workflow.RoutePatchAutomatic, PolicyDigest: strings.Repeat("b", 64)}
}

func approvedRoutePatch(proposal workflow.RoutePatchProposal, class statestore.ApprovalClass, status statestore.ApprovalStatus) workflow.ApprovedRoutePatchAuthorization {
	return workflow.ApprovedRoutePatchAuthorization{
		Approval: statestore.ApprovalProjection{
			ApprovalID: "approval_1", RunID: "run_1", Class: class, Status: status,
			ScopeDigest: proposal.ScopeDigest(), PolicyDigest: strings.Repeat("a", 64),
		},
		Actor: statestore.Actor{Type: statestore.ActorUser, ID: "operator_1"},
	}
}

func patchCode(err error) string {
	var patchError *workflow.RoutePatchError
	if errors.As(err, &patchError) {
		return patchError.Code
	}
	return ""
}

func digest(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, character := range value {
		if !strings.ContainsRune("0123456789abcdef", character) {
			return false
		}
	}
	return true
}

func transitionIDs(route workflow.Route) []workflow.Identifier {
	result := make([]workflow.Identifier, len(route.Transitions))
	for index, transition := range route.Transitions {
		result[index] = transition.ID
	}
	return result
}

func routeNodeIDs(route workflow.Route) []workflow.Identifier {
	result := make([]workflow.Identifier, len(route.Nodes))
	for index, node := range route.Nodes {
		result[index] = node.ID
	}
	return result
}
