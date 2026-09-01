package workflow

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
)

const (
	RunLoopLimitExhausted  = "RUN_LOOP_LIMIT_EXHAUSTED"
	RunInputRequired       = "RUN_INPUT_REQUIRED"
	RunOutputInvalid       = "RUN_OUTPUT_INVALID"
	WorkflowReferenceError = "WF_REFERENCE_MISSING"
	WorkflowRecursionError = "WF_SUBWORKFLOW_RECURSION"
)

// WorkflowIdentity is the immutable identity of one installed workflow.
type WorkflowIdentity struct {
	Name    string `json:"name"`
	Version string `json:"version"`
	Digest  string `json:"digest"`
}

// FrameOrigin is a closed root/child union. A root cannot accidentally carry a
// parent visit and a child cannot exist without one.
type FrameOrigin interface {
	isFrameOrigin()
}

type RootFrameOrigin struct {
	RunID string `json:"runId"`
}

func (RootFrameOrigin) isFrameOrigin() {}

type ChildFrameOrigin struct {
	ParentFrameID string `json:"parentFrameId"`
	ParentVisitID string `json:"parentVisitId"`
}

func (ChildFrameOrigin) isFrameOrigin() {}

// TransitionTokenKey is the replay identity for one fired transition. Join
// epochs keep repeated loop activations distinct without sharing tokens.
type TransitionTokenKey struct {
	SourceVisitID string     `json:"sourceVisitId"`
	TransitionID  Identifier `json:"transitionId"`
	JoinEpoch     uint64     `json:"joinEpoch"`
}

// TransitionToken is the durable fact produced by firing one transition.
// Traversal is zero for normal transitions and the post-consumption count for
// bounded transitions.
type TransitionTokenKind string

const (
	TransitionTokenNormal  TransitionTokenKind = "normal"
	TransitionTokenBounded TransitionTokenKind = "bounded"
)

type TransitionToken struct {
	Key           TransitionTokenKey  `json:"key"`
	Source        Identifier          `json:"source"`
	Target        Identifier          `json:"target"`
	Kind          TransitionTokenKind `json:"kind"`
	Traversal     uint16              `json:"traversal,omitempty"`
	MaxTraversals uint16              `json:"maxTraversals,omitempty"`
}

// FrameSnapshot is a complete replay snapshot of one workflow invocation.
// Tokens are the sole authority for bounded counts.
type FrameSnapshot struct {
	ID       string                         `json:"id"`
	Workflow WorkflowIdentity               `json:"workflow"`
	Origin   FrameOrigin                    `json:"-"`
	Route    Route                          `json:"route"`
	Inputs   map[Identifier]json.RawMessage `json:"inputs"`
	Tokens   []TransitionToken              `json:"tokens"`
	Ancestry []WorkflowIdentity             `json:"ancestry,omitempty"`
}

type frameOriginKind string

const (
	frameOriginRoot  frameOriginKind = "root"
	frameOriginChild frameOriginKind = "child"
)

type frameSnapshotWire struct {
	ID            string                         `json:"id"`
	Workflow      WorkflowIdentity               `json:"workflow"`
	Origin        frameOriginKind                `json:"origin"`
	RunID         string                         `json:"runId,omitempty"`
	ParentFrameID string                         `json:"parentFrameId,omitempty"`
	ParentVisitID string                         `json:"parentVisitId,omitempty"`
	Route         Route                          `json:"route"`
	Inputs        map[Identifier]json.RawMessage `json:"inputs"`
	Tokens        []TransitionToken              `json:"tokens"`
	Ancestry      []WorkflowIdentity             `json:"ancestry,omitempty"`
}

// MarshalJSON preserves the closed origin union with an explicit discriminator.
func (snapshot FrameSnapshot) MarshalJSON() ([]byte, error) {
	wire := frameSnapshotWire{
		ID: snapshot.ID, Workflow: snapshot.Workflow, Route: snapshot.Route,
		Inputs: snapshot.Inputs, Tokens: snapshot.Tokens, Ancestry: snapshot.Ancestry,
	}
	switch origin := snapshot.Origin.(type) {
	case RootFrameOrigin:
		wire.Origin, wire.RunID = frameOriginRoot, origin.RunID
	case ChildFrameOrigin:
		wire.Origin, wire.ParentFrameID, wire.ParentVisitID = frameOriginChild, origin.ParentFrameID, origin.ParentVisitID
	default:
		return nil, fmt.Errorf("unsupported frame origin %T", snapshot.Origin)
	}
	return json.Marshal(wire)
}

// UnmarshalJSON rejects contradictory root/child origin fields instead of
// allowing a partially populated frame into replay state.
func (snapshot *FrameSnapshot) UnmarshalJSON(data []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var wire frameSnapshotWire
	if err := decoder.Decode(&wire); err != nil {
		return err
	}
	switch wire.Origin {
	case frameOriginRoot:
		if wire.RunID == "" || wire.ParentFrameID != "" || wire.ParentVisitID != "" {
			return errors.New("root frame snapshot has contradictory origin fields")
		}
		snapshot.Origin = RootFrameOrigin{RunID: wire.RunID}
	case frameOriginChild:
		if wire.RunID != "" || wire.ParentFrameID == "" || wire.ParentVisitID == "" {
			return errors.New("child frame snapshot has contradictory origin fields")
		}
		snapshot.Origin = ChildFrameOrigin{ParentFrameID: wire.ParentFrameID, ParentVisitID: wire.ParentVisitID}
	default:
		return fmt.Errorf("invalid frame origin %q", wire.Origin)
	}
	snapshot.ID, snapshot.Workflow, snapshot.Route = wire.ID, wire.Workflow, wire.Route
	snapshot.Inputs, snapshot.Tokens = wire.Inputs, wire.Tokens
	snapshot.Ancestry = wire.Ancestry
	return nil
}

// Frame owns all mutable control-flow state for one workflow invocation.
// Children are separate frames, so bounded budgets and tokens never leak
// between a parent and a reusable sub-workflow or between sibling calls.
type Frame struct {
	mu         sync.Mutex
	id         string
	workflow   WorkflowIdentity
	origin     FrameOrigin
	route      Route
	inputs     map[Identifier]json.RawMessage
	tokens     map[TransitionTokenKey]TransitionToken
	traversals map[Identifier]uint16 // cache derived from tokens; updated atomically in FireTransition
	ancestry   []WorkflowIdentity
}

// FrameError is a deterministic workflow execution failure.
type FrameError struct {
	Code         string
	Message      string
	Location     string
	TransitionID Identifier
}

func (e *FrameError) Error() string { return e.Message }

// NewRootFrame creates the root execution frame for a frozen route.
func NewRootFrame(id, runID string, definition LoadedDefinition, route Route, inputs map[Identifier]json.RawMessage) (*Frame, error) {
	if strings.TrimSpace(runID) == "" {
		return nil, errors.New("root frame requires a run ID")
	}
	return newFrame(id, workflowIdentity(definition), RootFrameOrigin{RunID: runID}, route, inputs, nil)
}

// StartSubworkflow creates an isolated child frame after verifying the exact
// pinned workflow identity and copying declared parent inputs by value.
func (parent *Frame) StartSubworkflow(id, parentVisitID string, node SubworkflowNode, child LoadedDefinition, route Route, parentInputs map[Identifier]json.RawMessage) (*Frame, error) {
	if parent == nil {
		return nil, errors.New("sub-workflow requires a parent frame")
	}
	if strings.TrimSpace(parentVisitID) == "" {
		return nil, errors.New("sub-workflow requires a parent visit ID")
	}
	identity := workflowIdentity(child)
	if err := validatePinnedReference(node.Call.Workflow, identity); err != nil {
		return nil, err
	}
	if route.Entry != node.Call.Entry || !sameIdentifierSet(route.Terminals, node.Call.Terminals) {
		return nil, &FrameError{
			Code: WorkflowReferenceError, Message: "sub-workflow route does not match its pinned entry and terminals",
			Location: "/call",
		}
	}

	parent.mu.Lock()
	parentID := parent.id
	ancestry := append(append([]WorkflowIdentity(nil), parent.ancestry...), parent.workflow)
	parent.mu.Unlock()
	for _, ancestor := range ancestry {
		if sameWorkflow(ancestor, identity) {
			return nil, &FrameError{
				Code: WorkflowRecursionError, Message: fmt.Sprintf("recursive sub-workflow %q", identity.Name),
				Location: "/call/workflow",
			}
		}
	}

	childInputs := make(map[Identifier]json.RawMessage, len(node.Call.Inputs))
	for childName, parentName := range node.Call.Inputs {
		value, exists := parentInputs[parentName]
		if !exists {
			return nil, &FrameError{
				Code: RunInputRequired, Message: fmt.Sprintf("sub-workflow input %q is missing", childName),
				Location: fmt.Sprintf("/call/inputs/%s", childName),
			}
		}
		declaration, exists := child.Document.Spec.Inputs[childName]
		if !exists {
			return nil, &FrameError{
				Code: WorkflowReferenceError, Message: fmt.Sprintf("sub-workflow input %q is not declared by the child", childName),
				Location: fmt.Sprintf("/call/inputs/%s", childName),
			}
		}
		if !rawMessageHasType(value, declaration.Type) {
			return nil, &FrameError{
				Code: RunInputRequired, Message: fmt.Sprintf("sub-workflow input %q has incompatible type", childName),
				Location: fmt.Sprintf("/call/inputs/%s", childName),
			}
		}
		childInputs[childName] = cloneRawMessage(value)
	}
	for childName := range child.Document.Spec.Inputs {
		if _, exists := childInputs[childName]; !exists {
			return nil, &FrameError{
				Code: RunInputRequired, Message: fmt.Sprintf("sub-workflow input %q has no parent mapping", childName),
				Location: "/call/inputs",
			}
		}
	}

	return newFrame(id, identity, ChildFrameOrigin{ParentFrameID: parentID, ParentVisitID: parentVisitID}, route, childInputs, ancestry)
}

func newFrame(id string, identity WorkflowIdentity, origin FrameOrigin, route Route, inputs map[Identifier]json.RawMessage, ancestry []WorkflowIdentity) (*Frame, error) {
	if strings.TrimSpace(id) == "" {
		return nil, errors.New("execution frame requires an ID")
	}
	if identity.Name == "" || identity.Version == "" || !digestPattern.MatchString(identity.Digest) {
		return nil, errors.New("execution frame requires an installed workflow identity")
	}
	if err := validateFrozenRoute(route); err != nil {
		return nil, err
	}
	if err := validateFrameOrigin(origin); err != nil {
		return nil, err
	}
	if err := validateFrameAncestry(origin, identity, ancestry); err != nil {
		return nil, err
	}
	copiedInputs, err := cloneRawMessages(inputs)
	if err != nil {
		return nil, err
	}
	return &Frame{
		id: id, workflow: identity, origin: origin, route: cloneRoute(route), inputs: copiedInputs,
		tokens: make(map[TransitionTokenKey]TransitionToken), traversals: make(map[Identifier]uint16),
		ancestry: append([]WorkflowIdentity(nil), ancestry...),
	}, nil
}

// FireTransition records one transition exactly once. A replay returns the
// original token without consuming another bounded traversal.
func (frame *Frame) FireTransition(sourceVisitID string, sourceNodeID Identifier, joinEpoch uint64, transition Transition) (TransitionToken, bool, error) {
	if frame == nil || transition == nil {
		return TransitionToken{}, false, errors.New("transition firing requires a frame and transition")
	}
	if strings.TrimSpace(sourceVisitID) == "" {
		return TransitionToken{}, false, errors.New("transition firing requires a source visit ID")
	}
	routeTransition, exists := findRouteTransition(frame.route.Transitions, transition.ID())
	if !exists || routeTransition.From != sourceNodeID || routeTransition.To != transition.Target() {
		return TransitionToken{}, false, errors.New("transition is not enabled for the source node in this execution frame")
	}
	key := TransitionTokenKey{SourceVisitID: sourceVisitID, TransitionID: transition.ID(), JoinEpoch: joinEpoch}
	frame.mu.Lock()
	defer frame.mu.Unlock()
	if token, exists := frame.tokens[key]; exists {
		if token.Source != sourceNodeID || token.Target != transition.Target() || !tokenMatchesTransition(token, transition) {
			return TransitionToken{}, false, errors.New("replayed transition token conflicts with its committed route")
		}
		return token, true, nil
	}

	token := TransitionToken{Key: key, Source: sourceNodeID, Target: transition.Target()}
	switch value := transition.(type) {
	case NormalTransition:
		token.Kind = TransitionTokenNormal
	case BoundedTransition:
		used := frame.traversals[value.ID()]
		if used >= value.MaxTraversals {
			return TransitionToken{}, false, &FrameError{
				Code:     RunLoopLimitExhausted,
				Message:  fmt.Sprintf("transition %q exhausted its traversal budget", value.ID()),
				Location: "/transitions", TransitionID: value.ID(),
			}
		}
		token.Kind, token.MaxTraversals = TransitionTokenBounded, value.MaxTraversals
		token.Traversal = used + 1
		frame.traversals[value.ID()] = token.Traversal
	default:
		return TransitionToken{}, false, fmt.Errorf("unsupported transition type %T", transition)
	}
	frame.tokens[key] = token
	return token, false, nil
}

// Snapshot returns a deep copy suitable for durable persistence or replay.
func (frame *Frame) Snapshot() FrameSnapshot {
	frame.mu.Lock()
	defer frame.mu.Unlock()
	tokens := make([]TransitionToken, 0, len(frame.tokens))
	for _, token := range frame.tokens {
		tokens = append(tokens, token)
	}
	sort.Slice(tokens, func(i, j int) bool {
		if tokens[i].Key.SourceVisitID != tokens[j].Key.SourceVisitID {
			return tokens[i].Key.SourceVisitID < tokens[j].Key.SourceVisitID
		}
		if tokens[i].Key.TransitionID != tokens[j].Key.TransitionID {
			return tokens[i].Key.TransitionID < tokens[j].Key.TransitionID
		}
		return tokens[i].Key.JoinEpoch < tokens[j].Key.JoinEpoch
	})
	return FrameSnapshot{
		ID: frame.id, Workflow: frame.workflow, Origin: frame.origin, Route: cloneRoute(frame.route),
		Inputs: mustCloneRawMessages(frame.inputs),
		Tokens: tokens, Ancestry: append([]WorkflowIdentity(nil), frame.ancestry...),
	}
}

// TraversalCount returns the frame-local count derived from committed tokens.
func (frame *Frame) TraversalCount(transitionID Identifier) uint16 {
	frame.mu.Lock()
	defer frame.mu.Unlock()
	return frame.traversals[transitionID]
}

// RestoreFrame validates and restores a previously captured frame snapshot.
func RestoreFrame(snapshot FrameSnapshot) (*Frame, error) {
	if err := validateFrameOrigin(snapshot.Origin); err != nil {
		return nil, err
	}
	inputs, err := cloneRawMessages(snapshot.Inputs)
	if err != nil {
		return nil, err
	}
	frame := &Frame{
		id: snapshot.ID, workflow: snapshot.Workflow, origin: snapshot.Origin, route: cloneRoute(snapshot.Route), inputs: inputs,
		tokens:     make(map[TransitionTokenKey]TransitionToken, len(snapshot.Tokens)),
		traversals: make(map[Identifier]uint16), ancestry: append([]WorkflowIdentity(nil), snapshot.Ancestry...),
	}
	if frame.id == "" || frame.workflow.Name == "" || frame.workflow.Version == "" || !digestPattern.MatchString(frame.workflow.Digest) {
		return nil, errors.New("execution frame snapshot has invalid identity")
	}
	if err := validateFrozenRoute(frame.route); err != nil {
		return nil, err
	}
	if err := validateFrameAncestry(frame.origin, frame.workflow, frame.ancestry); err != nil {
		return nil, err
	}
	derived := make(map[Identifier]uint16)
	seenTraversals := make(map[Identifier]map[uint16]bool)
	limits := make(map[Identifier]uint16)
	for _, token := range snapshot.Tokens {
		if token.Key.SourceVisitID == "" || token.Key.TransitionID == "" || token.Source == "" || token.Target == "" {
			return nil, errors.New("execution frame snapshot has an invalid transition token")
		}
		routeTransition, exists := findRouteTransition(frame.route.Transitions, token.Key.TransitionID)
		if !exists || routeTransition.From != token.Source || routeTransition.To != token.Target {
			return nil, errors.New("execution frame snapshot token is outside its frozen route")
		}
		switch token.Kind {
		case TransitionTokenNormal:
			if token.Traversal != 0 || token.MaxTraversals != 0 {
				return nil, errors.New("normal transition token has bounded traversal fields")
			}
		case TransitionTokenBounded:
			if token.Traversal == 0 || token.MaxTraversals == 0 || token.Traversal > token.MaxTraversals {
				return nil, errors.New("bounded transition token has invalid traversal fields")
			}
			if seenTraversals[token.Key.TransitionID] == nil {
				seenTraversals[token.Key.TransitionID] = make(map[uint16]bool)
			}
			if seenTraversals[token.Key.TransitionID][token.Traversal] {
				return nil, errors.New("execution frame snapshot reuses a bounded traversal ordinal")
			}
			seenTraversals[token.Key.TransitionID][token.Traversal] = true
			if limit := limits[token.Key.TransitionID]; limit != 0 && limit != token.MaxTraversals {
				return nil, errors.New("execution frame snapshot changes a bounded traversal limit")
			}
			limits[token.Key.TransitionID] = token.MaxTraversals
		default:
			return nil, errors.New("transition token has an invalid kind")
		}
		if _, duplicate := frame.tokens[token.Key]; duplicate {
			return nil, errors.New("execution frame snapshot has a duplicate transition token")
		}
		frame.tokens[token.Key] = token
		if token.Traversal > derived[token.Key.TransitionID] {
			derived[token.Key.TransitionID] = token.Traversal
		}
	}
	for transitionID, count := range derived {
		for ordinal := uint16(1); ordinal <= count; ordinal++ {
			if !seenTraversals[transitionID][ordinal] {
				return nil, errors.New("execution frame snapshot has a gap in bounded traversal ordinals")
			}
		}
		frame.traversals[transitionID] = count
	}
	return frame, nil
}

// MapSubworkflowOutputs maps completed child outputs to the parent candidate.
// Every value is copied so later child mutations cannot change parent state.
func MapSubworkflowOutputs(node SubworkflowNode, child Document, outputs map[Identifier]map[Identifier]json.RawMessage) (map[Identifier]json.RawMessage, error) {
	candidate := make(map[Identifier]json.RawMessage, len(node.Call.Outputs))
	for parentName, source := range node.Call.Outputs {
		childNode, childOutput, ok := parseNodeOutputSource(source)
		if !ok {
			return nil, &FrameError{Code: RunOutputInvalid, Message: fmt.Sprintf("child output source %q is invalid", source), Location: fmt.Sprintf("/call/outputs/%s", parentName)}
		}
		declaredNode, exists := child.Spec.Nodes[childNode]
		if !exists {
			return nil, &FrameError{Code: WorkflowReferenceError, Message: fmt.Sprintf("child node %q does not exist", childNode), Location: fmt.Sprintf("/call/outputs/%s", parentName)}
		}
		declaration, exists := declaredNode.Fields().Outputs[childOutput]
		if !exists {
			return nil, &FrameError{Code: WorkflowReferenceError, Message: fmt.Sprintf("child output %q does not exist", source), Location: fmt.Sprintf("/call/outputs/%s", parentName)}
		}
		value, exists := outputs[childNode][childOutput]
		if !exists || !rawMessageHasType(value, declaration.Type) {
			return nil, &FrameError{Code: RunOutputInvalid, Message: fmt.Sprintf("child output %q is missing or has incompatible type", source), Location: fmt.Sprintf("/call/outputs/%s", parentName)}
		}
		parentDeclaration, exists := node.Common.Outputs[parentName]
		if !exists || parentDeclaration.Type != declaration.Type {
			return nil, &FrameError{Code: RunOutputInvalid, Message: fmt.Sprintf("parent output %q is incompatible with %q", parentName, source), Location: fmt.Sprintf("/call/outputs/%s", parentName)}
		}
		candidate[parentName] = cloneRawMessage(value)
	}
	return candidate, nil
}

func workflowIdentity(definition LoadedDefinition) WorkflowIdentity {
	return WorkflowIdentity{Name: definition.Document.Metadata.Name, Version: definition.Document.Metadata.Version, Digest: definition.Digest}
}

func validatePinnedReference(reference WorkflowReference, identity WorkflowIdentity) error {
	if reference.Name != identity.Name || reference.Version != identity.Version || (reference.Digest != "" && reference.Digest != identity.Digest) {
		return &FrameError{Code: WorkflowReferenceError, Message: fmt.Sprintf("sub-workflow %q does not match its pinned identity", reference.Name), Location: "/call/workflow"}
	}
	return nil
}

func validateFrameOrigin(origin FrameOrigin) error {
	switch value := origin.(type) {
	case RootFrameOrigin:
		if strings.TrimSpace(value.RunID) == "" {
			return errors.New("root frame origin requires a run ID")
		}
	case ChildFrameOrigin:
		if strings.TrimSpace(value.ParentFrameID) == "" || strings.TrimSpace(value.ParentVisitID) == "" {
			return errors.New("child frame origin requires parent frame and visit IDs")
		}
	default:
		return errors.New("execution frame snapshot requires a valid origin")
	}
	return nil
}

func validateFrameAncestry(origin FrameOrigin, current WorkflowIdentity, ancestry []WorkflowIdentity) error {
	if _, root := origin.(RootFrameOrigin); root && len(ancestry) != 0 {
		return errors.New("root frame cannot carry sub-workflow ancestry")
	}
	if _, child := origin.(ChildFrameOrigin); child && len(ancestry) == 0 {
		return errors.New("child frame requires sub-workflow ancestry")
	}
	seen := make(map[WorkflowIdentity]bool, len(ancestry)+1)
	seen[current] = true
	for _, identity := range ancestry {
		if identity.Name == "" || identity.Version == "" || !digestPattern.MatchString(identity.Digest) || seen[identity] {
			return errors.New("execution frame ancestry is invalid or recursive")
		}
		seen[identity] = true
	}
	return nil
}

func sameWorkflow(left, right WorkflowIdentity) bool {
	return left.Name == right.Name && left.Version == right.Version && left.Digest == right.Digest
}

func cloneRoute(route Route) Route {
	result := Route{
		Entry: route.Entry, Terminals: append([]Identifier(nil), route.Terminals...),
		Transitions:       append([]RouteTransition(nil), route.Transitions...),
		ExcludedNodes:     append([]ExcludedNode(nil), route.ExcludedNodes...),
		InputRequirements: append([]InputRequirement(nil), route.InputRequirements...),
		Nodes:             make([]RouteNode, len(route.Nodes)),
	}
	for index, node := range route.Nodes {
		result.Nodes[index] = RouteNode{ID: node.ID}
		if node.Join != nil {
			result.Nodes[index].Join = &Join{Mode: node.Join.Mode, From: append([]Identifier(nil), node.Join.From...)}
		}
	}
	return result
}

func validateFrozenRoute(route Route) error {
	if route.Entry == "" || len(route.Terminals) == 0 {
		return errors.New("execution frame requires a frozen route")
	}
	seen := make(map[Identifier]bool, len(route.Transitions))
	for _, transition := range route.Transitions {
		if transition.ID == "" || transition.From == "" || transition.To == "" || seen[transition.ID] {
			return errors.New("execution frame route has an invalid or duplicate transition")
		}
		seen[transition.ID] = true
	}
	return nil
}

func findRouteTransition(transitions []RouteTransition, id Identifier) (RouteTransition, bool) {
	for _, transition := range transitions {
		if transition.ID == id {
			return transition, true
		}
	}
	return RouteTransition{}, false
}

func sameIdentifierSet(left, right []Identifier) bool {
	if len(left) != len(right) {
		return false
	}
	a, b := append([]Identifier(nil), left...), append([]Identifier(nil), right...)
	sort.Slice(a, func(i, j int) bool { return a[i] < a[j] })
	sort.Slice(b, func(i, j int) bool { return b[i] < b[j] })
	return slicesEqual(a, b)
}

func slicesEqual(left, right []Identifier) bool {
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func cloneRawMessages(values map[Identifier]json.RawMessage) (map[Identifier]json.RawMessage, error) {
	result := make(map[Identifier]json.RawMessage, len(values))
	for name, value := range values {
		if !json.Valid(value) {
			return nil, fmt.Errorf("input %q is not valid JSON", name)
		}
		result[name] = cloneRawMessage(value)
	}
	return result, nil
}

func mustCloneRawMessages(values map[Identifier]json.RawMessage) map[Identifier]json.RawMessage {
	result, _ := cloneRawMessages(values)
	return result
}

func cloneRawMessage(value json.RawMessage) json.RawMessage {
	return append(json.RawMessage(nil), value...)
}

func tokenMatchesTransition(token TransitionToken, transition Transition) bool {
	switch value := transition.(type) {
	case NormalTransition:
		return token.Kind == TransitionTokenNormal && token.Traversal == 0 && token.MaxTraversals == 0
	case BoundedTransition:
		return token.Kind == TransitionTokenBounded && token.Traversal > 0 && token.MaxTraversals == value.MaxTraversals
	default:
		return false
	}
}

func rawMessageHasType(value json.RawMessage, want ValueType) bool {
	if !json.Valid(value) {
		return false
	}
	decoder := json.NewDecoder(bytes.NewReader(value))
	decoder.UseNumber()
	var decoded any
	if err := decoder.Decode(&decoded); err != nil {
		return false
	}
	switch want {
	case ValueNull:
		return decoded == nil
	case ValueBoolean:
		_, ok := decoded.(bool)
		return ok
	case ValueInteger:
		number, ok := decoded.(json.Number)
		if !ok {
			return false
		}
		_, err := number.Int64()
		return err == nil
	case ValueNumber:
		_, ok := decoded.(json.Number)
		return ok
	case ValueString:
		_, ok := decoded.(string)
		return ok
	case ValueArray:
		_, ok := decoded.([]any)
		return ok
	case ValueObject:
		_, ok := decoded.(map[string]any)
		return ok
	default:
		return false
	}
}

func parseNodeOutputSource(source string) (Identifier, Identifier, bool) {
	parts := strings.Split(source, ".")
	if len(parts) != 4 || parts[0] != "node" || parts[2] != "output" || parts[1] == "" || parts[3] == "" {
		return "", "", false
	}
	return Identifier(parts[1]), Identifier(parts[3]), true
}
