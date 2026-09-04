package workflow

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"darkstar/src/ports/statestore"
)

const KindRoutePatch = "RoutePatch"

const (
	RoutePatchConflict              = "ROUTE_PATCH_CONFLICT"
	RoutePatchInvalid               = "ROUTE_PATCH_INVALID"
	RoutePatchOperationInvalid      = "ROUTE_PATCH_OPERATION_INVALID"
	RoutePatchAuthorizationRequired = "ROUTE_PATCH_AUTHORIZATION_REQUIRED"
	RoutePatchAuthorizationInvalid  = "ROUTE_PATCH_AUTHORIZATION_INVALID"
	RoutePatchAttemptRunning        = "ROUTE_PATCH_ATTEMPT_RUNNING"
	RoutePatchTransitionConsumed    = "ROUTE_PATCH_TRANSITION_CONSUMED"
	RoutePatchHistoryConflict       = "ROUTE_PATCH_HISTORY_CONFLICT"
	RoutePatchValidationStale       = "ROUTE_PATCH_VALIDATION_STALE"
)

// RoutePatch is the typed v1alpha1 wire document. Operations form a closed
// union: patches can select only authored transitions and terminal boundaries.
type RoutePatch struct {
	APIVersion string             `json:"apiVersion"`
	Kind       string             `json:"kind"`
	Metadata   RoutePatchMetadata `json:"metadata"`
	Spec       RoutePatchSpec     `json:"spec"`
}

type RoutePatchMetadata struct {
	ID string `json:"id"`
}

type RoutePatchSpec struct {
	RunID                 string                `json:"runId"`
	ExpectedRouteRevision uint64                `json:"expectedRouteRevision"`
	Reason                string                `json:"reason"`
	ApprovalID            string                `json:"approvalId,omitempty"`
	Operations            []RoutePatchOperation `json:"operations"`
}

type RoutePatchOperation interface {
	operationName() string
	isRoutePatchOperation()
}

type EnableTransitionOperation struct {
	TransitionID Identifier `json:"transitionId"`
}

func (EnableTransitionOperation) operationName() string  { return "enableTransition" }
func (EnableTransitionOperation) isRoutePatchOperation() {}
func (operation EnableTransitionOperation) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		Operation    string     `json:"op"`
		TransitionID Identifier `json:"transitionId"`
	}{Operation: operation.operationName(), TransitionID: operation.TransitionID})
}

type DisableTransitionOperation struct {
	TransitionID Identifier `json:"transitionId"`
}

func (DisableTransitionOperation) operationName() string  { return "disableTransition" }
func (DisableTransitionOperation) isRoutePatchOperation() {}
func (operation DisableTransitionOperation) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		Operation    string     `json:"op"`
		TransitionID Identifier `json:"transitionId"`
	}{Operation: operation.operationName(), TransitionID: operation.TransitionID})
}

type SetTerminalsOperation struct {
	Nodes []Identifier `json:"nodes"`
}

func (SetTerminalsOperation) operationName() string  { return "setTerminals" }
func (SetTerminalsOperation) isRoutePatchOperation() {}
func (operation SetTerminalsOperation) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		Operation string       `json:"op"`
		Nodes     []Identifier `json:"nodes"`
	}{Operation: operation.operationName(), Nodes: operation.Nodes})
}

// UnmarshalJSON keeps the operation union closed and rejects unknown fields at
// every level instead of accepting a partially understood control request.
func (patch *RoutePatch) UnmarshalJSON(data []byte) error {
	var wire struct {
		APIVersion string             `json:"apiVersion"`
		Kind       string             `json:"kind"`
		Metadata   RoutePatchMetadata `json:"metadata"`
		Spec       struct {
			RunID                 string            `json:"runId"`
			ExpectedRouteRevision uint64            `json:"expectedRouteRevision"`
			Reason                string            `json:"reason"`
			ApprovalID            string            `json:"approvalId"`
			Operations            []json.RawMessage `json:"operations"`
		} `json:"spec"`
	}
	if err := strictDecode(data, &wire); err != nil {
		return err
	}
	operations := make([]RoutePatchOperation, 0, len(wire.Spec.Operations))
	for index, raw := range wire.Spec.Operations {
		var discriminator struct {
			Operation string `json:"op"`
		}
		if err := json.Unmarshal(raw, &discriminator); err != nil {
			return fmt.Errorf("operations[%d]: %w", index, err)
		}
		switch discriminator.Operation {
		case "enableTransition":
			var value struct {
				Operation    string     `json:"op"`
				TransitionID Identifier `json:"transitionId"`
			}
			if err := strictDecode(raw, &value); err != nil {
				return fmt.Errorf("operations[%d]: %w", index, err)
			}
			operations = append(operations, EnableTransitionOperation{TransitionID: value.TransitionID})
		case "disableTransition":
			var value struct {
				Operation    string     `json:"op"`
				TransitionID Identifier `json:"transitionId"`
			}
			if err := strictDecode(raw, &value); err != nil {
				return fmt.Errorf("operations[%d]: %w", index, err)
			}
			operations = append(operations, DisableTransitionOperation{TransitionID: value.TransitionID})
		case "setTerminals":
			var value struct {
				Operation string       `json:"op"`
				Nodes     []Identifier `json:"nodes"`
			}
			if err := strictDecode(raw, &value); err != nil {
				return fmt.Errorf("operations[%d]: %w", index, err)
			}
			operations = append(operations, SetTerminalsOperation{Nodes: value.Nodes})
		default:
			return fmt.Errorf("operations[%d]: unsupported route patch operation %q", index, discriminator.Operation)
		}
	}
	*patch = RoutePatch{
		APIVersion: wire.APIVersion, Kind: wire.Kind, Metadata: wire.Metadata,
		Spec: RoutePatchSpec{
			RunID: wire.Spec.RunID, ExpectedRouteRevision: wire.Spec.ExpectedRouteRevision,
			Reason: wire.Spec.Reason, ApprovalID: wire.Spec.ApprovalID, Operations: operations,
		},
	}
	return validateRoutePatchDocument(*patch)
}

// TransitionOverride is persisted only when a patch differs from the authored
// enabledByDefault value. It is a projection/cache over the immutable workflow,
// not a second authored transition definition.
type TransitionOverride struct {
	TransitionID Identifier `json:"transitionId"`
	Enabled      bool       `json:"enabled"`
}

// RouteStateSnapshot is the serializable current route projection. The route is
// derived from the workflow plus overrides; Revision is the compare-and-swap
// boundary for patch application.
type RouteStateSnapshot struct {
	RunID     string               `json:"runId"`
	Revision  uint64               `json:"revision"`
	Route     Route                `json:"route"`
	Overrides []TransitionOverride `json:"overrides,omitempty"`
}

// RouteState hides the override map so callers cannot create duplicate or
// contradictory transition state.
type RouteState struct {
	runID     string
	revision  uint64
	route     Route
	overrides map[Identifier]bool
}

// NewRouteState creates revision zero from an already validated frozen route.
func NewRouteState(runID string, route Route) (RouteState, error) {
	return restoreRouteState(RouteStateSnapshot{RunID: runID, Route: route})
}

// RestoreRouteState validates a persisted route projection before using it as
// an authorization or compare-and-swap boundary.
func RestoreRouteState(snapshot RouteStateSnapshot) (RouteState, error) {
	return restoreRouteState(snapshot)
}

func restoreRouteState(snapshot RouteStateSnapshot) (RouteState, error) {
	if strings.TrimSpace(snapshot.RunID) == "" || strings.TrimSpace(snapshot.RunID) != snapshot.RunID {
		return RouteState{}, errors.New("route state requires a run ID without surrounding whitespace")
	}
	if err := validateFrozenRoute(snapshot.Route); err != nil {
		return RouteState{}, err
	}
	overrides := make(map[Identifier]bool, len(snapshot.Overrides))
	for _, override := range snapshot.Overrides {
		if err := validateIdentifier(override.TransitionID); err != nil {
			return RouteState{}, err
		}
		if _, duplicate := overrides[override.TransitionID]; duplicate {
			return RouteState{}, fmt.Errorf("route state repeats transition override %q", override.TransitionID)
		}
		overrides[override.TransitionID] = override.Enabled
	}
	return RouteState{runID: snapshot.RunID, revision: snapshot.Revision, route: cloneRoute(snapshot.Route), overrides: overrides}, nil
}

func (state RouteState) Snapshot() RouteStateSnapshot {
	return RouteStateSnapshot{
		RunID: state.runID, Revision: state.revision, Route: cloneRoute(state.route),
		Overrides: sortedTransitionOverrides(state.overrides),
	}
}

func (state RouteState) RunID() string    { return state.runID }
func (state RouteState) Revision() uint64 { return state.revision }
func (state RouteState) Route() Route     { return cloneRoute(state.route) }

type RoutePatchAuthorizationMode string

const (
	RoutePatchAutomatic       RoutePatchAuthorizationMode = "automatic"
	RoutePatchRequireApproval RoutePatchAuthorizationMode = "require_approval"
)

// RoutePatchPolicy is the frozen policy decision for this proposal. PolicyDigest
// binds the decision to the exact policy snapshot used by the daemon.
type RoutePatchPolicy struct {
	Mode         RoutePatchAuthorizationMode `json:"mode"`
	PolicyDigest string                      `json:"policyDigest"`
}

// RoutePatchImpact is a deterministic diff between the current and proposed
// future route. History is deliberately absent: applying a patch never rewrites
// visit or transition-token evidence.
type RoutePatchImpact struct {
	AddedNodes         []Identifier `json:"addedNodes,omitempty"`
	RemovedNodes       []Identifier `json:"removedNodes,omitempty"`
	AddedTransitions   []Identifier `json:"addedTransitions,omitempty"`
	RemovedTransitions []Identifier `json:"removedTransitions,omitempty"`
	PreviousTerminals  []Identifier `json:"previousTerminals"`
	ProposedTerminals  []Identifier `json:"proposedTerminals"`
}

func (impact RoutePatchImpact) ExpandsTerminalBoundary() bool {
	if len(impact.AddedNodes) == 0 {
		return false
	}
	previous := identifierSet(impact.PreviousTerminals)
	for _, terminal := range impact.ProposedTerminals {
		if !previous[terminal] {
			return true
		}
	}
	return false
}

// RoutePatchProposal is validated but not executable. It can become executable
// only through AuthorizeRoutePatch, which returns the distinct authorized type.
type RoutePatchProposal struct {
	patch              RoutePatch
	candidate          RouteState
	impact             RoutePatchImpact
	affectedNodes      []Identifier
	mode               RoutePatchAuthorizationMode
	policyDigest       string
	scopeDigest        string
	validationDigest   string
	baseTopologyDigest string
}

func (proposal RoutePatchProposal) Patch() RoutePatch { return cloneRoutePatch(proposal.patch) }
func (proposal RoutePatchProposal) Candidate() RouteStateSnapshot {
	return proposal.candidate.Snapshot()
}
func (proposal RoutePatchProposal) Impact() RoutePatchImpact {
	return cloneRoutePatchImpact(proposal.impact)
}
func (proposal RoutePatchProposal) AuthorizationMode() RoutePatchAuthorizationMode {
	return proposal.mode
}
func (proposal RoutePatchProposal) ScopeDigest() string      { return proposal.scopeDigest }
func (proposal RoutePatchProposal) ValidationDigest() string { return proposal.validationDigest }
func (proposal RoutePatchProposal) PolicyDigest() string     { return proposal.policyDigest }

// ProposeRoutePatch applies the ordered operations to a private override copy,
// rederives the complete route, validates it, and returns deterministic impact
// and authorization digests. The current RouteState remains unchanged on every
// error.
func ProposeRoutePatch(
	document Document,
	current RouteState,
	patch RoutePatch,
	context RouteContext,
	policy RoutePatchPolicy,
) (RoutePatchProposal, error) {
	if err := validateRoutePatchDocument(patch); err != nil {
		return RoutePatchProposal{}, err
	}
	if current.runID == "" || patch.Spec.RunID != current.runID || patch.Spec.ExpectedRouteRevision != current.revision {
		return RoutePatchProposal{}, patchError(RoutePatchConflict, "patch run or expected route revision does not match current state", "/spec/expectedRouteRevision")
	}
	if policy.Mode != RoutePatchAutomatic && policy.Mode != RoutePatchRequireApproval {
		return RoutePatchProposal{}, patchError(RoutePatchInvalid, "route patch policy has an invalid authorization mode", "/policy/mode")
	}
	if !digestPattern.MatchString(policy.PolicyDigest) {
		return RoutePatchProposal{}, patchError(RoutePatchInvalid, "route patch policy digest must be 64 lowercase hexadecimal characters", "/policy/policyDigest")
	}

	transitions := authoredTransitions(document)
	overrides := cloneOverrides(current.overrides)
	terminals := append([]Identifier(nil), current.route.Terminals...)
	seenTransitions := make(map[Identifier]bool)
	setTerminalsSeen := false
	for index, operation := range patch.Spec.Operations {
		location := fmt.Sprintf("/spec/operations/%d", index)
		switch value := operation.(type) {
		case EnableTransitionOperation:
			if err := applyTransitionOverride(value.TransitionID, true, transitions, overrides, seenTransitions, location); err != nil {
				return RoutePatchProposal{}, err
			}
		case DisableTransitionOperation:
			if err := applyTransitionOverride(value.TransitionID, false, transitions, overrides, seenTransitions, location); err != nil {
				return RoutePatchProposal{}, err
			}
		case SetTerminalsOperation:
			if setTerminalsSeen {
				return RoutePatchProposal{}, patchError(RoutePatchOperationInvalid, "a patch may set terminals only once", location)
			}
			setTerminalsSeen = true
			terminals = uniqueSortedIdentifiers(value.Nodes)
		default:
			return RoutePatchProposal{}, patchError(RoutePatchOperationInvalid, fmt.Sprintf("unsupported route patch operation %T", operation), location)
		}
	}

	candidateRoute, routeErrors := createRoute(document, RouteRequest{From: current.route.Entry, Until: terminals}, context, overrides)
	if len(routeErrors) != 0 {
		return RoutePatchProposal{}, &RoutePatchRouteValidationError{Errors: routeErrors}
	}
	if err := validatePatchedExclusiveOutcomes(document, candidateRoute, overrides); err != nil {
		return RoutePatchProposal{}, err
	}
	impact := routePatchImpact(current.route, candidateRoute)
	if routePatchImpactEmpty(impact) {
		return RoutePatchProposal{}, patchError(RoutePatchOperationInvalid, "patch has no effect on the future route", "/spec/operations")
	}

	mode := policy.Mode
	if impact.ExpandsTerminalBoundary() || patch.Spec.ApprovalID != "" {
		mode = RoutePatchRequireApproval
	}
	candidate := RouteState{runID: current.runID, revision: current.revision + 1, route: candidateRoute, overrides: overrides}
	validationDigest := digestJSON(struct {
		Candidate RouteStateSnapshot `json:"candidate"`
		Impact    RoutePatchImpact   `json:"impact"`
	}{Candidate: candidate.Snapshot(), Impact: impact})
	scopeDigest := digestJSON(struct {
		RunID                 string                      `json:"runId"`
		PatchID               string                      `json:"patchId"`
		ExpectedRouteRevision uint64                      `json:"expectedRouteRevision"`
		Reason                string                      `json:"reason"`
		Operations            []RoutePatchOperation       `json:"operations"`
		AuthorizationMode     RoutePatchAuthorizationMode `json:"authorizationMode"`
		ValidationDigest      string                      `json:"validationDigest"`
	}{
		RunID: patch.Spec.RunID, PatchID: patch.Metadata.ID, ExpectedRouteRevision: patch.Spec.ExpectedRouteRevision,
		Reason: patch.Spec.Reason, Operations: patch.Spec.Operations, AuthorizationMode: mode, ValidationDigest: validationDigest,
	})
	return RoutePatchProposal{
		patch: cloneRoutePatch(patch), candidate: candidate, impact: impact,
		affectedNodes: affectedPatchNodes(document, current.route, candidateRoute, patch.Spec.Operations),
		mode:          mode, policyDigest: policy.PolicyDigest, scopeDigest: scopeDigest,
		validationDigest: validationDigest, baseTopologyDigest: routeTopologyDigest(current.route),
	}, nil
}

type RoutePatchAuthorization interface {
	isRoutePatchAuthorization()
}

// AutomaticRoutePatchAuthorization is a frozen policy authorization. Only a
// system actor may exercise it, and never for a proposal requiring approval.
type AutomaticRoutePatchAuthorization struct {
	Actor        statestore.Actor `json:"actor"`
	ScopeDigest  string           `json:"scopeDigest"`
	PolicyDigest string           `json:"policyDigest"`
}

func (AutomaticRoutePatchAuthorization) isRoutePatchAuthorization() {}

// ApprovedRoutePatchAuthorization binds a workflow-control approval projection
// and its attributable decision actor to the proposal.
type ApprovedRoutePatchAuthorization struct {
	Approval statestore.ApprovalProjection `json:"approval"`
	Actor    statestore.Actor              `json:"actor"`
}

func (ApprovedRoutePatchAuthorization) isRoutePatchAuthorization() {}

// AuthorizedRoutePatch is the only value accepted by ApplyAuthorizedRoutePatch.
// Its fields are private so a plain proposal cannot be cast into authorization.
type AuthorizedRoutePatch struct {
	proposal   RoutePatchProposal
	actor      statestore.Actor
	approvalID string
}

func AuthorizeRoutePatch(proposal RoutePatchProposal, authorization RoutePatchAuthorization) (AuthorizedRoutePatch, error) {
	if proposal.scopeDigest == "" || proposal.validationDigest == "" {
		return AuthorizedRoutePatch{}, patchError(RoutePatchAuthorizationInvalid, "route patch proposal is incomplete", "/authorization")
	}
	switch value := authorization.(type) {
	case AutomaticRoutePatchAuthorization:
		if proposal.mode != RoutePatchAutomatic {
			return AuthorizedRoutePatch{}, patchError(RoutePatchAuthorizationRequired, "this route patch requires an attributable workflow-control approval", "/authorization")
		}
		if value.Actor.Type != statestore.ActorSystem || strings.TrimSpace(value.Actor.ID) == "" ||
			value.ScopeDigest != proposal.scopeDigest || value.PolicyDigest != proposal.policyDigest {
			return AuthorizedRoutePatch{}, patchError(RoutePatchAuthorizationInvalid, "automatic authorization does not match proposal scope, policy, or actor", "/authorization")
		}
		return AuthorizedRoutePatch{proposal: proposal, actor: value.Actor}, nil
	case ApprovedRoutePatchAuthorization:
		approval := value.Approval
		actorValid := (value.Actor.Type == statestore.ActorUser || value.Actor.Type == statestore.ActorExternal) && strings.TrimSpace(value.Actor.ID) != ""
		if approval.ApprovalID == "" || approval.RunID != proposal.patch.Spec.RunID ||
			approval.Class != statestore.ApprovalWorkflowControl || approval.Status != statestore.ApprovalApproved ||
			approval.ScopeDigest != proposal.scopeDigest || approval.PolicyDigest != proposal.policyDigest || !actorValid {
			return AuthorizedRoutePatch{}, patchError(RoutePatchAuthorizationInvalid, "approval does not match proposal run, class, status, scope, policy, or actor", "/authorization")
		}
		if expected := proposal.patch.Spec.ApprovalID; expected != "" && approval.ApprovalID != expected {
			return AuthorizedRoutePatch{}, patchError(RoutePatchAuthorizationInvalid, "approval ID does not match the route patch", "/spec/approvalId")
		}
		return AuthorizedRoutePatch{proposal: proposal, actor: value.Actor, approvalID: approval.ApprovalID}, nil
	default:
		return AuthorizedRoutePatch{}, patchError(RoutePatchAuthorizationInvalid, fmt.Sprintf("unsupported route patch authorization %T", authorization), "/authorization")
	}
}

// RoutePatchExecutionState is authoritative runtime evidence relevant to a
// future-route mutation. The slices name workflow node/transition IDs; visit and
// attempt records remain untouched by application.
type RoutePatchExecutionState struct {
	RunningAttempts    []Identifier `json:"runningAttempts,omitempty"`
	ActivatedNodes     []Identifier `json:"activatedNodes,omitempty"`
	SucceededNodes     []Identifier `json:"succeededNodes,omitempty"`
	ReachedTerminals   []Identifier `json:"reachedTerminals,omitempty"`
	EmittedTransitions []Identifier `json:"emittedTransitions,omitempty"`
}

// AppliedRoutePatch is the complete audit payload for one atomic revision.
type AppliedRoutePatch struct {
	PatchID          string                `json:"patchId"`
	RunID            string                `json:"runId"`
	OldRevision      uint64                `json:"oldRevision"`
	NewRevision      uint64                `json:"newRevision"`
	Reason           string                `json:"reason"`
	Operations       []RoutePatchOperation `json:"operations"`
	Impact           RoutePatchImpact      `json:"impact"`
	Actor            statestore.Actor      `json:"actor"`
	ApprovalID       string                `json:"approvalId,omitempty"`
	ScopeDigest      string                `json:"scopeDigest"`
	ValidationDigest string                `json:"validationDigest"`
}

// ApplyAuthorizedRoutePatch revalidates after approval, checks current runtime
// evidence, and returns one new route revision plus its audit record. Callers
// persist both in the same transaction; no input value is mutated on failure.
func ApplyAuthorizedRoutePatch(
	document Document,
	current RouteState,
	authorized AuthorizedRoutePatch,
	context RouteContext,
	execution RoutePatchExecutionState,
) (RouteState, AppliedRoutePatch, error) {
	proposal := authorized.proposal
	if proposal.scopeDigest == "" || authorized.actor.ID == "" {
		return RouteState{}, AppliedRoutePatch{}, patchError(RoutePatchAuthorizationRequired, "an authorized route patch is required", "/authorization")
	}
	if current.runID != proposal.patch.Spec.RunID || current.revision != proposal.patch.Spec.ExpectedRouteRevision ||
		routeTopologyDigest(current.route) != proposal.baseTopologyDigest {
		return RouteState{}, AppliedRoutePatch{}, patchError(RoutePatchConflict, "route changed after the patch was proposed", "/spec/expectedRouteRevision")
	}

	revalidated, err := ProposeRoutePatch(document, current, proposal.patch, context, RoutePatchPolicy{Mode: proposal.mode, PolicyDigest: proposal.policyDigest})
	if err != nil {
		return RouteState{}, AppliedRoutePatch{}, err
	}
	if revalidated.scopeDigest != proposal.scopeDigest || revalidated.validationDigest != proposal.validationDigest {
		return RouteState{}, AppliedRoutePatch{}, patchError(RoutePatchValidationStale, "route patch validation changed after authorization", "/authorization/scopeDigest")
	}

	affected := identifierSet(proposal.affectedNodes)
	if conflicts := intersection(execution.RunningAttempts, affected); len(conflicts) != 0 {
		return RouteState{}, AppliedRoutePatch{}, patchErrorWithNodes(RoutePatchAttemptRunning, "an affected node has a running attempt", "/execution/runningAttempts", conflicts)
	}
	disabled := disabledTransitionIDs(proposal.patch.Spec.Operations)
	if conflicts := intersection(execution.EmittedTransitions, disabled); len(conflicts) != 0 {
		return RouteState{}, AppliedRoutePatch{}, patchErrorWithTransitions(RoutePatchTransitionConsumed, "a disabled transition already emitted a token", "/execution/emittedTransitions", conflicts)
	}
	candidateNodes := routeNodeSet(revalidated.candidate.route)
	for location, nodes := range map[string][]Identifier{
		"/execution/activatedNodes":   execution.ActivatedNodes,
		"/execution/succeededNodes":   execution.SucceededNodes,
		"/execution/reachedTerminals": execution.ReachedTerminals,
	} {
		if conflicts := missingFromSet(nodes, candidateNodes); len(conflicts) != 0 {
			return RouteState{}, AppliedRoutePatch{}, patchErrorWithNodes(RoutePatchHistoryConflict, "patch would remove an activated or completed node from the route projection", location, conflicts)
		}
	}

	next := revalidated.candidate
	record := AppliedRoutePatch{
		PatchID: proposal.patch.Metadata.ID, RunID: current.runID, OldRevision: current.revision, NewRevision: next.revision,
		Reason: proposal.patch.Spec.Reason, Operations: cloneRoutePatchOperations(proposal.patch.Spec.Operations),
		Impact: cloneRoutePatchImpact(proposal.impact), Actor: authorized.actor, ApprovalID: authorized.approvalID,
		ScopeDigest: proposal.scopeDigest, ValidationDigest: proposal.validationDigest,
	}
	return next, record, nil
}

type RoutePatchError struct {
	Code          string       `json:"code"`
	Message       string       `json:"message"`
	Location      string       `json:"location"`
	NodeIDs       []Identifier `json:"nodeIds,omitempty"`
	TransitionIDs []Identifier `json:"transitionIds,omitempty"`
}

func (err *RoutePatchError) Error() string {
	return fmt.Sprintf("%s at %s: %s", err.Code, err.Location, err.Message)
}

// RoutePatchRouteValidationError preserves the ordered findings from ordinary
// route validation instead of collapsing them into an authorization error.
type RoutePatchRouteValidationError struct {
	Errors ValidationErrors `json:"errors"`
}

func (err *RoutePatchRouteValidationError) Error() string {
	return fmt.Sprintf("route patch candidate is invalid: %s", err.Errors.Error())
}

func validateRoutePatchDocument(patch RoutePatch) error {
	if patch.APIVersion != APIVersionV1Alpha1 || patch.Kind != KindRoutePatch {
		return patchError(RoutePatchInvalid, "route patch has an unsupported apiVersion or kind", "/")
	}
	if strings.TrimSpace(patch.Metadata.ID) == "" || strings.TrimSpace(patch.Metadata.ID) != patch.Metadata.ID {
		return patchError(RoutePatchInvalid, "route patch metadata requires an ID without surrounding whitespace", "/metadata/id")
	}
	if strings.TrimSpace(patch.Spec.RunID) == "" || strings.TrimSpace(patch.Spec.RunID) != patch.Spec.RunID {
		return patchError(RoutePatchInvalid, "route patch requires a run ID without surrounding whitespace", "/spec/runId")
	}
	if strings.TrimSpace(patch.Spec.Reason) == "" || strings.TrimSpace(patch.Spec.Reason) != patch.Spec.Reason {
		return patchError(RoutePatchInvalid, "route patch requires a reason without surrounding whitespace", "/spec/reason")
	}
	if patch.Spec.ApprovalID != "" && (strings.TrimSpace(patch.Spec.ApprovalID) == "" || strings.TrimSpace(patch.Spec.ApprovalID) != patch.Spec.ApprovalID) {
		return patchError(RoutePatchInvalid, "approval ID cannot contain surrounding whitespace", "/spec/approvalId")
	}
	if len(patch.Spec.Operations) == 0 {
		return patchError(RoutePatchInvalid, "route patch requires at least one operation", "/spec/operations")
	}
	for index, operation := range patch.Spec.Operations {
		location := fmt.Sprintf("/spec/operations/%d", index)
		switch value := operation.(type) {
		case EnableTransitionOperation:
			if err := validateIdentifier(value.TransitionID); err != nil {
				return patchError(RoutePatchInvalid, err.Error(), location+"/transitionId")
			}
		case DisableTransitionOperation:
			if err := validateIdentifier(value.TransitionID); err != nil {
				return patchError(RoutePatchInvalid, err.Error(), location+"/transitionId")
			}
		case SetTerminalsOperation:
			if len(value.Nodes) == 0 {
				return patchError(RoutePatchInvalid, "setTerminals requires at least one node", location+"/nodes")
			}
			if err := validateIdentifierList(value.Nodes, true); err != nil {
				return patchError(RoutePatchInvalid, err.Error(), location+"/nodes")
			}
		default:
			return patchError(RoutePatchInvalid, fmt.Sprintf("unsupported route patch operation %T", operation), location)
		}
	}
	return nil
}

type authoredTransition struct {
	source     Identifier
	transition Transition
}

func authoredTransitions(document Document) map[Identifier]authoredTransition {
	result := make(map[Identifier]authoredTransition)
	for source, node := range document.Spec.Nodes {
		for _, transition := range node.Fields().Transitions {
			result[transition.ID()] = authoredTransition{source: source, transition: transition}
		}
	}
	return result
}

func applyTransitionOverride(
	id Identifier,
	enabled bool,
	authored map[Identifier]authoredTransition,
	overrides map[Identifier]bool,
	seen map[Identifier]bool,
	location string,
) error {
	record, exists := authored[id]
	if !exists {
		return patchErrorWithTransitions(string(WorkflowReferenceError), "route patch references an unknown transition", location+"/transitionId", []Identifier{id})
	}
	if seen[id] {
		return patchErrorWithTransitions(RoutePatchOperationInvalid, "a patch may change each transition only once", location+"/transitionId", []Identifier{id})
	}
	seen[id] = true
	if routeTransitionEnabled(record.transition, overrides) == enabled {
		return patchErrorWithTransitions(RoutePatchOperationInvalid, "transition operation does not change its current state", location+"/transitionId", []Identifier{id})
	}
	if enabled == transitionEnabled(record.transition) {
		delete(overrides, id)
	} else {
		overrides[id] = enabled
	}
	return nil
}

func routePatchImpact(current, candidate Route) RoutePatchImpact {
	currentNodes, candidateNodes := routeNodeSet(current), routeNodeSet(candidate)
	currentTransitions, candidateTransitions := routeTransitionSet(current), routeTransitionSet(candidate)
	return RoutePatchImpact{
		AddedNodes: setDifference(candidateNodes, currentNodes), RemovedNodes: setDifference(currentNodes, candidateNodes),
		AddedTransitions: setDifference(candidateTransitions, currentTransitions), RemovedTransitions: setDifference(currentTransitions, candidateTransitions),
		PreviousTerminals: append([]Identifier(nil), current.Terminals...), ProposedTerminals: append([]Identifier(nil), candidate.Terminals...),
	}
}

func validatePatchedExclusiveOutcomes(document Document, route Route, overrides map[Identifier]bool) error {
	includedTransitions := routeTransitionSet(route)
	for _, nodeID := range sortedNodeIDs(document.Spec.Nodes) {
		node := document.Spec.Nodes[nodeID]
		if node.Fields().TransitionMode != TransitionExclusive {
			continue
		}
		always := make([]Identifier, 0, 2)
		for _, transition := range node.Fields().Transitions {
			if includedTransitions[transition.ID()] && routeTransitionEnabled(transition, overrides) && transitionAlwaysFires(transition) {
				always = append(always, transition.ID())
			}
		}
		if len(always) > 1 {
			return patchErrorWithTransitions(RoutePatchOperationInvalid,
				"patch enables multiple unconditional outcomes on an exclusive node", "/spec/operations", always)
		}
	}
	return nil
}

func routePatchImpactEmpty(impact RoutePatchImpact) bool {
	return len(impact.AddedNodes) == 0 && len(impact.RemovedNodes) == 0 && len(impact.AddedTransitions) == 0 &&
		len(impact.RemovedTransitions) == 0 && sameIdentifierSet(impact.PreviousTerminals, impact.ProposedTerminals)
}

func affectedPatchNodes(document Document, current, candidate Route, operations []RoutePatchOperation) []Identifier {
	affected := make(map[Identifier]bool)
	for _, id := range routePatchImpact(current, candidate).AddedNodes {
		affected[id] = true
	}
	for _, id := range routePatchImpact(current, candidate).RemovedNodes {
		affected[id] = true
	}
	transitions := authoredTransitions(document)
	for _, operation := range operations {
		var id Identifier
		switch value := operation.(type) {
		case EnableTransitionOperation:
			id = value.TransitionID
		case DisableTransitionOperation:
			id = value.TransitionID
		case SetTerminalsOperation:
			for _, terminal := range current.Terminals {
				affected[terminal] = true
			}
			for _, terminal := range candidate.Terminals {
				affected[terminal] = true
			}
		}
		if record, exists := transitions[id]; exists {
			affected[record.source] = true
			affected[record.transition.Target()] = true
		}
	}
	return sortedIdentifierSet(affected)
}

func disabledTransitionIDs(operations []RoutePatchOperation) map[Identifier]bool {
	result := make(map[Identifier]bool)
	for _, operation := range operations {
		if value, ok := operation.(DisableTransitionOperation); ok {
			result[value.TransitionID] = true
		}
	}
	return result
}

func routeNodeSet(route Route) map[Identifier]bool {
	result := make(map[Identifier]bool, len(route.Nodes))
	for _, node := range route.Nodes {
		result[node.ID] = true
	}
	return result
}

func routeTransitionSet(route Route) map[Identifier]bool {
	result := make(map[Identifier]bool, len(route.Transitions))
	for _, transition := range route.Transitions {
		result[transition.ID] = true
	}
	return result
}

func setDifference(left, right map[Identifier]bool) []Identifier {
	result := make([]Identifier, 0)
	for id := range left {
		if !right[id] {
			result = append(result, id)
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i] < result[j] })
	return result
}

func intersection(values []Identifier, set map[Identifier]bool) []Identifier {
	result := make(map[Identifier]bool)
	for _, value := range values {
		if set[value] {
			result[value] = true
		}
	}
	return sortedIdentifierSet(result)
}

func missingFromSet(values []Identifier, set map[Identifier]bool) []Identifier {
	result := make(map[Identifier]bool)
	for _, value := range values {
		if !set[value] {
			result[value] = true
		}
	}
	return sortedIdentifierSet(result)
}

func sortedIdentifierSet(values map[Identifier]bool) []Identifier {
	result := make([]Identifier, 0, len(values))
	for value := range values {
		result = append(result, value)
	}
	sort.Slice(result, func(i, j int) bool { return result[i] < result[j] })
	return result
}

func sortedTransitionOverrides(overrides map[Identifier]bool) []TransitionOverride {
	ids := make([]Identifier, 0, len(overrides))
	for id := range overrides {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	result := make([]TransitionOverride, 0, len(ids))
	for _, id := range ids {
		result = append(result, TransitionOverride{TransitionID: id, Enabled: overrides[id]})
	}
	return result
}

func cloneOverrides(overrides map[Identifier]bool) map[Identifier]bool {
	result := make(map[Identifier]bool, len(overrides))
	for id, enabled := range overrides {
		result[id] = enabled
	}
	return result
}

func cloneRoutePatch(patch RoutePatch) RoutePatch {
	patch.Spec.Operations = cloneRoutePatchOperations(patch.Spec.Operations)
	return patch
}

func cloneRoutePatchOperations(operations []RoutePatchOperation) []RoutePatchOperation {
	result := make([]RoutePatchOperation, len(operations))
	for index, operation := range operations {
		if value, ok := operation.(SetTerminalsOperation); ok {
			value.Nodes = append([]Identifier(nil), value.Nodes...)
			result[index] = value
		} else {
			result[index] = operation
		}
	}
	return result
}

func cloneRoutePatchImpact(impact RoutePatchImpact) RoutePatchImpact {
	impact.AddedNodes = append([]Identifier(nil), impact.AddedNodes...)
	impact.RemovedNodes = append([]Identifier(nil), impact.RemovedNodes...)
	impact.AddedTransitions = append([]Identifier(nil), impact.AddedTransitions...)
	impact.RemovedTransitions = append([]Identifier(nil), impact.RemovedTransitions...)
	impact.PreviousTerminals = append([]Identifier(nil), impact.PreviousTerminals...)
	impact.ProposedTerminals = append([]Identifier(nil), impact.ProposedTerminals...)
	return impact
}

func routeTopologyDigest(route Route) string {
	copy := cloneRoute(route)
	copy.InputRequirements = nil
	return digestJSON(copy)
}

func digestJSON(value any) string {
	encoded, err := json.Marshal(value)
	if err != nil {
		panic("route patch digest contains an unsupported value: " + err.Error())
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:])
}

func patchError(code, message, location string) *RoutePatchError {
	return &RoutePatchError{Code: code, Message: message, Location: location}
}

func patchErrorWithNodes(code, message, location string, nodes []Identifier) *RoutePatchError {
	return &RoutePatchError{Code: code, Message: message, Location: location, NodeIDs: append([]Identifier(nil), nodes...)}
}

func patchErrorWithTransitions(code, message, location string, transitions []Identifier) *RoutePatchError {
	return &RoutePatchError{Code: code, Message: message, Location: location, TransitionIDs: append([]Identifier(nil), transitions...)}
}
