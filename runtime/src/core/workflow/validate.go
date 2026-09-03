package workflow

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// ValidationCode is a stable machine-readable workflow validation result.
type ValidationCode string

const (
	ValidationSchemaInvalid        ValidationCode = "WF_SCHEMA_INVALID"
	ValidationReferenceMissing     ValidationCode = "WF_REFERENCE_MISSING"
	ValidationBindingIncompatible  ValidationCode = "WF_BINDING_INCOMPATIBLE"
	ValidationReasoningEdgeInvalid ValidationCode = "WF_REASONING_EDGE_INVALID"
	ValidationGateInvalid          ValidationCode = "WF_GATE_INVALID"
	ValidationUnreachableNode      ValidationCode = "WF_UNREACHABLE_NODE"
	ValidationUnboundedCycle       ValidationCode = "WF_UNBOUNDED_CYCLE"
	ValidationJoinInvalid          ValidationCode = "WF_JOIN_INVALID"
	ValidationDefaultRouteInvalid  ValidationCode = "WF_DEFAULT_ROUTE_INVALID"
	ValidationCapabilityMissing    ValidationCode = "CAPABILITY_REQUIRED_MISSING"
	ValidationReadinessInvalid     ValidationCode = "WF_READINESS_INVALID"
)

// ValidationError is one deterministic semantic error. Detail values are
// sorted identifier lists so callers never observe map iteration order.
type ValidationError struct {
	Code     ValidationCode      `json:"code"`
	Message  string              `json:"message"`
	Location string              `json:"location,omitempty"`
	Details  map[string][]string `json:"details,omitempty"`
}

func (e ValidationError) Error() string {
	if e.Location == "" {
		return fmt.Sprintf("%s: %s", e.Code, e.Message)
	}
	return fmt.Sprintf("%s at %s: %s", e.Code, e.Location, e.Message)
}

// ValidationErrors is the complete, stable-order result of static validation.
type ValidationErrors []ValidationError

func (errors ValidationErrors) Error() string {
	if len(errors) == 0 {
		return ""
	}
	if len(errors) == 1 {
		return errors[0].Error()
	}
	return fmt.Sprintf("%s (and %d more validation errors)", errors[0].Error(), len(errors)-1)
}

// CapabilityKind distinguishes workflow skill and tool requirements.
type CapabilityKind string

const (
	CapabilitySkill CapabilityKind = "skill"
	CapabilityTool  CapabilityKind = "tool"
)

// CapabilityReference is one available capability in an immutable validation
// snapshot. Kind is part of the identity, so a tool cannot satisfy a skill.
type CapabilityReference struct {
	Kind CapabilityKind
	Name string
}

// CapabilityAvailability is the narrow view the workflow validator needs from
// the future capability registry.
type CapabilityAvailability interface {
	HasCapability(kind CapabilityKind, name string) bool
}

// CapabilitySnapshot is an immutable-by-convention availability set suitable
// for validation and tests.
type CapabilitySnapshot struct {
	available map[CapabilityReference]struct{}
}

func NewCapabilitySnapshot(references ...CapabilityReference) CapabilitySnapshot {
	available := make(map[CapabilityReference]struct{}, len(references))
	for _, reference := range references {
		available[reference] = struct{}{}
	}
	return CapabilitySnapshot{available: available}
}

func (snapshot CapabilitySnapshot) HasCapability(kind CapabilityKind, name string) bool {
	_, exists := snapshot.available[CapabilityReference{Kind: kind, Name: name}]
	return exists
}

// StaticValidator performs document-only checks and, when availability is
// supplied, verifies every explicitly required skill and tool.
type StaticValidator struct {
	capabilities CapabilityAvailability
}

func NewStaticValidator(capabilities CapabilityAvailability) StaticValidator {
	return StaticValidator{capabilities: capabilities}
}

// Validate performs environment-independent semantic validation.
func Validate(document Document) ValidationErrors {
	return StaticValidator{}.Validate(document)
}

// Validate performs all static checks configured for this validator.
func (validator StaticValidator) Validate(document Document) ValidationErrors {
	state := validationState{
		document:     document,
		nodes:        document.Spec.Nodes,
		transitions:  make(map[Identifier]transitionRecord),
		incoming:     make(map[Identifier][]transitionRecord, len(document.Spec.Nodes)),
		adjacency:    make(map[Identifier][]Identifier, len(document.Spec.Nodes)),
		normalGraph:  make(map[Identifier][]Identifier, len(document.Spec.Nodes)),
		capabilities: validator.capabilities,
	}
	state.validate()
	sortValidationErrors(state.errors)
	return state.errors
}

type transitionRecord struct {
	source     Identifier
	target     Identifier
	transition Transition
	index      int
}

type validationState struct {
	document     Document
	nodes        map[Identifier]Node
	transitions  map[Identifier]transitionRecord
	incoming     map[Identifier][]transitionRecord
	adjacency    map[Identifier][]Identifier
	normalGraph  map[Identifier][]Identifier
	capabilities CapabilityAvailability
	errors       ValidationErrors
}

func (state *validationState) validate() {
	nodeIDs := sortedNodeIDs(state.nodes)
	for _, nodeID := range nodeIDs {
		state.incoming[nodeID] = nil
		state.adjacency[nodeID] = nil
		state.normalGraph[nodeID] = nil
	}
	state.indexTransitions(nodeIDs)
	state.validateNodes(nodeIDs)
	state.validateJoins(nodeIDs)
	state.validateReachability(nodeIDs)
	state.validateCycles(nodeIDs)
	state.validateDefaultRoute()
}

func (state *validationState) indexTransitions(nodeIDs []Identifier) {
	for _, nodeID := range nodeIDs {
		fields := state.nodes[nodeID].Fields()
		for index, transition := range fields.Transitions {
			record := transitionRecord{source: nodeID, target: transition.Target(), transition: transition, index: index}
			location := transitionLocation(nodeID, index)
			if previous, exists := state.transitions[transition.ID()]; exists {
				state.add(ValidationSchemaInvalid,
					fmt.Sprintf("transition id %q is also declared by node %q", transition.ID(), previous.source),
					location+"/id", map[string][]string{"transitionIds": {string(transition.ID())}})
			} else {
				state.transitions[transition.ID()] = record
			}
			if _, exists := state.nodes[transition.Target()]; !exists {
				state.add(ValidationReferenceMissing,
					fmt.Sprintf("transition %q targets unknown node %q", transition.ID(), transition.Target()),
					location+"/to", nil)
				continue
			}
			state.incoming[transition.Target()] = append(state.incoming[transition.Target()], record)
			state.adjacency[nodeID] = append(state.adjacency[nodeID], transition.Target())
			if _, bounded := transition.(BoundedTransition); !bounded {
				state.normalGraph[nodeID] = append(state.normalGraph[nodeID], transition.Target())
			}
		}
	}
}

func (state *validationState) validateNodes(nodeIDs []Identifier) {
	for _, nodeID := range nodeIDs {
		node := state.nodes[nodeID]
		fields := node.Fields()
		state.validateBindings(nodeID, fields)
		state.validateReadiness(nodeID, fields)
		state.validateApproval(nodeID, node, fields)
		state.validatePointExecution(nodeID, node, fields)
		state.validateValidators(nodeID, fields)
		state.validatePredicates(nodeID, node, fields)
		state.validateSubworkflow(nodeID, node, fields)
		state.validateCapabilities(nodeID, node)
	}
}

func (state *validationState) validatePointExecution(nodeID Identifier, node Node, fields NodeFields) {
	pointNode, ok := node.(PointExecutionNode)
	if !ok {
		return
	}
	location := fmt.Sprintf("/spec/nodes/%s/points/planInput", nodeID)
	binding, exists := fields.Inputs[pointNode.Executor.PlanInput]
	if !exists {
		state.add(ValidationReferenceMissing, fmt.Sprintf("point executor references unknown input %q", pointNode.Executor.PlanInput), location, nil)
		return
	}
	if binding.ValueType() != ValueObject {
		state.add(ValidationBindingIncompatible, fmt.Sprintf("point execution plan input %q must be an object", pointNode.Executor.PlanInput), location, nil)
	}
}

func (state *validationState) validateApproval(nodeID Identifier, node Node, fields NodeFields) {
	approval, ok := node.(ApprovalNode)
	if !ok {
		return
	}
	external, ok := approval.Executor.(ExternalApproval)
	if !ok {
		return
	}
	location := fmt.Sprintf("/spec/nodes/%s/approval/evidenceOutput", nodeID)
	declaration, exists := fields.Outputs[external.EvidenceOutput]
	if !exists {
		state.add(ValidationReferenceMissing, fmt.Sprintf("external evidence references unknown output %q", external.EvidenceOutput), location, nil)
		return
	}
	if declaration.Type != ValueObject || declaration.Schema == "" {
		state.add(ValidationBindingIncompatible,
			fmt.Sprintf("external evidence output %q must be an object with a schema", external.EvidenceOutput), location, nil)
	}
}

func (state *validationState) validateReadiness(nodeID Identifier, fields NodeFields) {
	if fields.Readiness == nil {
		return
	}
	for index, remedy := range fields.Readiness.Remedies {
		location := fmt.Sprintf("/spec/nodes/%s/readiness/remedies/%d/target", nodeID, index)
		if _, exists := state.nodes[remedy.Target]; !exists {
			state.add(ValidationReferenceMissing, fmt.Sprintf("readiness remedy targets unknown node %q", remedy.Target), location, nil)
			continue
		}
		if remedy.Target != nodeID && !state.nodeReaches(remedy.Target, nodeID) {
			state.add(ValidationReadinessInvalid,
				fmt.Sprintf("readiness remedy target %q is not upstream of node %q", remedy.Target, nodeID), location, nil)
		}
	}
}

func (state *validationState) nodeReaches(start, target Identifier) bool {
	stack := []Identifier{start}
	visited := make(map[Identifier]bool)
	for len(stack) != 0 {
		last := len(stack) - 1
		current := stack[last]
		stack = stack[:last]
		if current == target {
			return true
		}
		if visited[current] {
			continue
		}
		visited[current] = true
		stack = append(stack, state.adjacency[current]...)
	}
	return false
}

func (state *validationState) validateBindings(nodeID Identifier, fields NodeFields) {
	for _, inputID := range sortedBindingIDs(fields.Inputs) {
		binding := fields.Inputs[inputID]
		location := fmt.Sprintf("/spec/nodes/%s/inputs/%s", nodeID, inputID)
		sourceType, exists := state.bindingSourceType(binding.Source())
		if !exists {
			state.add(ValidationReferenceMissing,
				fmt.Sprintf("unknown binding source %q", binding.Source()), location+"/from", nil)
		} else if bindingPointer(binding) == "" && !typesCompatible(sourceType, binding.ValueType()) {
			state.add(ValidationBindingIncompatible,
				fmt.Sprintf("binding %q expects %s from %s", inputID, binding.ValueType(), sourceType), location, nil)
		}
		optional, ok := binding.(OptionalBinding)
		if ok && optional.Default != nil {
			actual, valid := rawValueType(optional.Default)
			if !valid || !typesCompatible(actual, optional.Type) {
				state.add(ValidationBindingIncompatible,
					fmt.Sprintf("default for %q has type %s, want %s", inputID, actual, optional.Type), location+"/default", nil)
			}
		}
	}
}

func (state *validationState) validateValidators(nodeID Identifier, fields NodeFields) {
	for index, validator := range fields.Validators {
		schemaValidator, ok := validator.(SchemaValidator)
		if !ok {
			continue
		}
		if _, exists := fields.Outputs[schemaValidator.Output]; !exists {
			state.add(ValidationReferenceMissing,
				fmt.Sprintf("validator references unknown output %q", schemaValidator.Output),
				fmt.Sprintf("/spec/nodes/%s/validators/%d/output", nodeID, index), nil)
		}
	}
}

func (state *validationState) validatePredicates(nodeID Identifier, node Node, fields NodeFields) {
	for index, transition := range fields.Transitions {
		when := transitionFields(transition).When
		if when == nil {
			continue
		}
		location := transitionLocation(nodeID, index) + "/when"
		state.checkPredicate(nodeID, when, fields, location, true)
		if node.Type() == NodeReasoning && predicateHasPrefix(when, "output.") {
			state.add(ValidationReasoningEdgeInvalid,
				fmt.Sprintf("reasoning node %q cannot branch on its own output", nodeID), location, nil)
		}
	}

	if gate, ok := node.(GateNode); ok {
		passed, passedOK := fields.Outputs["passed"]
		evidence, evidenceOK := fields.Outputs["gate_evidence"]
		if !passedOK || passed.Type != ValueBoolean || !evidenceOK || evidence.Type != ValueObject {
			state.add(ValidationGateInvalid,
				fmt.Sprintf("gate %q must declare passed:boolean and gate_evidence:object outputs", nodeID),
				fmt.Sprintf("/spec/nodes/%s/outputs", nodeID), nil)
		}
		location := fmt.Sprintf("/spec/nodes/%s/gate/condition", nodeID)
		state.checkPredicate(nodeID, gate.Executor.Condition, fields, location, false)
		if predicateHasPrefix(gate.Executor.Condition, "output.") {
			state.add(ValidationGateInvalid, fmt.Sprintf("gate %q condition cannot reference output", nodeID), location, nil)
		}
	}

	if checkpoint, ok := fields.Checkpoint.(ApproveOnChangeCheckpoint); ok {
		location := fmt.Sprintf("/spec/nodes/%s/checkpoint/when", nodeID)
		state.checkPredicate(nodeID, checkpoint.When, fields, location, true)
		if node.Type() == NodeReasoning && predicateHasPrefix(checkpoint.When, "output.") {
			state.add(ValidationReasoningEdgeInvalid,
				fmt.Sprintf("reasoning node %q cannot gate a checkpoint directly on its own output", nodeID), location, nil)
		}
	}
}

func (state *validationState) validateSubworkflow(nodeID Identifier, node Node, fields NodeFields) {
	callNode, ok := node.(SubworkflowNode)
	if !ok {
		return
	}
	for childInput, parentInput := range callNode.Call.Inputs {
		if _, exists := fields.Inputs[parentInput]; !exists {
			state.add(ValidationReferenceMissing,
				fmt.Sprintf("sub-workflow input %q maps unknown parent input %q", childInput, parentInput),
				fmt.Sprintf("/spec/nodes/%s/call/inputs/%s", nodeID, childInput), nil)
		}
	}
	for parentOutput := range callNode.Call.Outputs {
		if _, exists := fields.Outputs[parentOutput]; !exists {
			state.add(ValidationReferenceMissing,
				fmt.Sprintf("sub-workflow maps unknown parent output %q", parentOutput),
				fmt.Sprintf("/spec/nodes/%s/call/outputs/%s", nodeID, parentOutput), nil)
		}
	}
}

func (state *validationState) validateCapabilities(nodeID Identifier, node Node) {
	if state.capabilities == nil {
		return
	}
	reasoning, ok := node.(ReasoningNode)
	if !ok {
		return
	}
	for index, skill := range reasoning.Executor.Skills {
		if !state.capabilities.HasCapability(CapabilitySkill, skill) {
			state.add(ValidationCapabilityMissing, fmt.Sprintf("required skill %q is unavailable", skill),
				fmt.Sprintf("/spec/nodes/%s/reasoning/skills/%d", nodeID, index),
				map[string][]string{"capabilities": {skill}})
		}
	}
	for index, tool := range reasoning.Executor.Tools {
		if !state.capabilities.HasCapability(CapabilityTool, tool) {
			state.add(ValidationCapabilityMissing, fmt.Sprintf("required tool %q is unavailable", tool),
				fmt.Sprintf("/spec/nodes/%s/reasoning/tools/%d", nodeID, index),
				map[string][]string{"capabilities": {tool}})
		}
	}
}

func (state *validationState) validateJoins(nodeIDs []Identifier) {
	for _, nodeID := range nodeIDs {
		fields := state.nodes[nodeID].Fields()
		incoming := state.incoming[nodeID]
		location := fmt.Sprintf("/spec/nodes/%s/join", nodeID)
		if len(incoming) <= 1 {
			if fields.Join != nil {
				state.add(ValidationJoinInvalid, fmt.Sprintf("node %q has a redundant join", nodeID), location, nil)
			}
			continue
		}
		authored := make([]string, 0, len(incoming))
		for _, record := range incoming {
			authored = append(authored, string(record.transition.ID()))
		}
		if fields.Join == nil || !sameIdentifiers(fields.Join.From, authored) {
			state.add(ValidationJoinInvalid,
				fmt.Sprintf("node %q must join all authored incoming transitions", nodeID), location,
				map[string][]string{"transitionIds": authored})
			continue
		}
		state.validateJoinSatisfiability(nodeID, fields.Join, incoming, location)
	}
}

func (state *validationState) validateJoinSatisfiability(nodeID Identifier, join *Join, incoming []transitionRecord, location string) {
	for _, forkID := range sortedNodeIDs(state.nodes) {
		forkFields := state.nodes[forkID].Fields()
		if len(forkFields.Transitions) < 2 {
			continue
		}
		type branchCoverage struct {
			transition Transition
			incoming   map[Identifier]bool
		}
		coverage := make([]branchCoverage, 0, len(forkFields.Transitions))
		for _, branch := range forkFields.Transitions {
			covered := make(map[Identifier]bool)
			for _, producer := range incoming {
				if state.branchReachesIncoming(branch, producer, nodeID) {
					covered[producer.transition.ID()] = true
				}
			}
			coverage = append(coverage, branchCoverage{transition: branch, incoming: covered})
		}

		for left := 0; left < len(coverage); left++ {
			for right := left + 1; right < len(coverage); right++ {
				producers := distinctCoveredIncoming(coverage[left].incoming, coverage[right].incoming)
				if len(producers) < 2 {
					continue
				}
				if join.Mode == JoinAll && forkFields.TransitionMode == TransitionExclusive {
					state.add(ValidationJoinInvalid,
						fmt.Sprintf("all join %q requires mutually exclusive branches from %q", nodeID, forkID),
						location, map[string][]string{"transitionIds": producers})
					return
				}
				if join.Mode == JoinOne && forkFields.TransitionMode == TransitionFanout &&
					transitionAlwaysFires(coverage[left].transition) && transitionAlwaysFires(coverage[right].transition) {
					state.add(ValidationJoinInvalid,
						fmt.Sprintf("one join %q can receive multiple branches from fanout node %q", nodeID, forkID),
						location, map[string][]string{"transitionIds": producers})
					return
				}
			}
		}
	}
}

func (state *validationState) branchReachesIncoming(branch Transition, incoming transitionRecord, joinNode Identifier) bool {
	if branch.ID() == incoming.transition.ID() {
		return true
	}
	start := branch.Target()
	if start == joinNode {
		return false
	}
	stack := []Identifier{start}
	visited := make(map[Identifier]bool)
	for len(stack) != 0 {
		last := len(stack) - 1
		current := stack[last]
		stack = stack[:last]
		if current == incoming.source {
			return true
		}
		if current == joinNode || visited[current] {
			continue
		}
		visited[current] = true
		stack = append(stack, state.adjacency[current]...)
	}
	return false
}

func (state *validationState) validateReachability(nodeIDs []Identifier) {
	stack := make([]Identifier, 0)
	for _, nodeID := range nodeIDs {
		if state.nodes[nodeID].Fields().Entry {
			stack = append(stack, nodeID)
		}
	}
	reachable := make(map[Identifier]bool, len(nodeIDs))
	for len(stack) != 0 {
		last := len(stack) - 1
		current := stack[last]
		stack = stack[:last]
		if reachable[current] {
			continue
		}
		reachable[current] = true
		stack = append(stack, state.adjacency[current]...)
	}
	for _, nodeID := range nodeIDs {
		if !reachable[nodeID] {
			state.add(ValidationUnreachableNode, fmt.Sprintf("node %q is unreachable", nodeID),
				fmt.Sprintf("/spec/nodes/%s", nodeID), nil)
		}
	}
}

func (state *validationState) validateCycles(nodeIDs []Identifier) {
	colors := make(map[Identifier]uint8, len(nodeIDs))
	var visit func(Identifier) bool
	visit = func(nodeID Identifier) bool {
		colors[nodeID] = 1
		targets := append([]Identifier(nil), state.normalGraph[nodeID]...)
		sort.Slice(targets, func(i, j int) bool { return targets[i] < targets[j] })
		for _, target := range targets {
			if colors[target] == 1 || (colors[target] == 0 && visit(target)) {
				return true
			}
		}
		colors[nodeID] = 2
		return false
	}
	for _, nodeID := range nodeIDs {
		if colors[nodeID] == 0 && visit(nodeID) {
			state.add(ValidationUnboundedCycle, "normal transitions contain a directed cycle", "/spec/nodes", nil)
			return
		}
	}
}

func (state *validationState) validateDefaultRoute() {
	entry := state.document.Spec.RouteDefaults.Entry
	entryNode, exists := state.nodes[entry]
	if !exists || !entryNode.Fields().Entry {
		state.add(ValidationDefaultRouteInvalid, "default entry is absent or not entry-capable", "/spec/routeDefaults/entry", nil)
		return
	}
	terminalSet := make(map[Identifier]bool, len(state.document.Spec.RouteDefaults.Terminals))
	for _, terminal := range state.document.Spec.RouteDefaults.Terminals {
		node, exists := state.nodes[terminal]
		if !exists || !node.Fields().Terminal {
			state.add(ValidationDefaultRouteInvalid, "default terminal is absent or not terminal-capable", "/spec/routeDefaults/terminals",
				map[string][]string{"nodeIds": {string(terminal)}})
			continue
		}
		terminalSet[terminal] = true
	}
	if len(terminalSet) == 0 {
		return
	}

	reachable := map[Identifier]bool{}
	stack := []Identifier{entry}
	for len(stack) != 0 {
		last := len(stack) - 1
		current := stack[last]
		stack = stack[:last]
		if reachable[current] {
			continue
		}
		reachable[current] = true
		if terminalSet[current] {
			continue
		}
		for _, transition := range state.nodes[current].Fields().Transitions {
			if transitionEnabled(transition) {
				if _, exists := state.nodes[transition.Target()]; exists {
					stack = append(stack, transition.Target())
				}
			}
		}
	}
	missing := make([]string, 0)
	for terminal := range terminalSet {
		if !reachable[terminal] {
			missing = append(missing, string(terminal))
		}
	}
	if len(missing) != 0 {
		state.add(ValidationDefaultRouteInvalid, "one or more default terminals are unreachable", "/spec/routeDefaults/terminals",
			map[string][]string{"nodeIds": missing})
		return
	}

	reverse := make(map[Identifier][]Identifier, len(reachable))
	for source := range reachable {
		if terminalSet[source] {
			continue
		}
		for _, transition := range state.nodes[source].Fields().Transitions {
			if transitionEnabled(transition) && reachable[transition.Target()] {
				reverse[transition.Target()] = append(reverse[transition.Target()], source)
			}
		}
	}
	canReachTerminal := make(map[Identifier]bool, len(reachable))
	stack = stack[:0]
	for terminal := range terminalSet {
		canReachTerminal[terminal] = true
		stack = append(stack, terminal)
	}
	for len(stack) != 0 {
		last := len(stack) - 1
		current := stack[last]
		stack = stack[:last]
		for _, source := range reverse[current] {
			if !canReachTerminal[source] {
				canReachTerminal[source] = true
				stack = append(stack, source)
			}
		}
	}
	stranded := make([]string, 0)
	for nodeID := range reachable {
		if !canReachTerminal[nodeID] {
			stranded = append(stranded, string(nodeID))
		}
	}
	if len(stranded) != 0 {
		state.add(ValidationDefaultRouteInvalid, "enabled branch cannot reach a default terminal", "/spec/routeDefaults",
			map[string][]string{"nodeIds": stranded})
	}
}

func (state *validationState) bindingSourceType(source string) (ValueType, bool) {
	parts := strings.Split(source, ".")
	if len(parts) == 3 && parts[0] == "run" && parts[1] == "input" {
		declaration, exists := state.document.Spec.Inputs[Identifier(parts[2])]
		return declaration.Type, exists
	}
	if len(parts) == 4 && parts[0] == "node" && parts[2] == "output" {
		node, exists := state.nodes[Identifier(parts[1])]
		if !exists {
			return "", false
		}
		declaration, exists := node.Fields().Outputs[Identifier(parts[3])]
		return declaration.Type, exists
	}
	return "", false
}

func (state *validationState) checkPredicate(nodeID Identifier, predicate Predicate, fields NodeFields, location string, allowOutput bool) {
	var visit func(Predicate, string)
	visit = func(current Predicate, currentLocation string) {
		switch value := current.(type) {
		case ConstantPredicate:
			return
		case ComparisonPredicate:
			leftType, leftKnown := state.checkOperand(nodeID, value.Args[0], fields, currentLocation+"/args/0", allowOutput)
			rightType, rightKnown := state.checkOperand(nodeID, value.Args[1], fields, currentLocation+"/args/1", allowOutput)
			if leftKnown && rightKnown && !validComparison(value.Operator, leftType, rightType) {
				state.add(ValidationBindingIncompatible,
					fmt.Sprintf("operator %q cannot compare %s and %s", value.Operator, leftType, rightType), currentLocation, nil)
			}
		case PresentPredicate:
			state.checkReference(nodeID, value.Reference.Ref, fields, currentLocation+"/arg/ref", allowOutput)
		case LogicalPredicate:
			for index, arg := range value.Args {
				visit(arg, fmt.Sprintf("%s/args/%d", currentLocation, index))
			}
		case NotPredicate:
			visit(value.Arg, currentLocation+"/arg")
		}
	}
	visit(predicate, location)
}

func (state *validationState) checkOperand(nodeID Identifier, operand Operand, fields NodeFields, location string, allowOutput bool) (ValueType, bool) {
	switch value := operand.(type) {
	case ReferenceOperand:
		return state.checkReference(nodeID, value.Ref, fields, location+"/ref", allowOutput)
	case LiteralOperand:
		return rawValueType(value.Literal)
	default:
		return "", false
	}
}

func (state *validationState) checkReference(nodeID Identifier, reference string, fields NodeFields, location string, allowOutput bool) (ValueType, bool) {
	parts := strings.Split(reference, ".")
	var valueType ValueType
	var exists bool
	var rootLength int
	switch {
	case len(parts) >= 2 && parts[0] == "output":
		rootLength = 2
		if allowOutput {
			declaration, found := fields.Outputs[Identifier(parts[1])]
			valueType, exists = declaration.Type, found
		}
	case len(parts) >= 2 && parts[0] == "input":
		rootLength = 2
		binding, found := fields.Inputs[Identifier(parts[1])]
		if found {
			valueType, exists = binding.ValueType(), true
		}
	case len(parts) >= 3 && parts[0] == "run" && parts[1] == "input":
		rootLength = 3
		declaration, found := state.document.Spec.Inputs[Identifier(parts[2])]
		valueType, exists = declaration.Type, found
	}
	if !exists {
		state.add(ValidationReferenceMissing, fmt.Sprintf("unknown predicate reference %q", reference), location, nil)
		return "", false
	}
	if len(parts) > rootLength {
		return "", false
	}
	return valueType, true
}

func (state *validationState) add(code ValidationCode, message, location string, details map[string][]string) {
	copyDetails := make(map[string][]string, len(details))
	for key, values := range details {
		copyValues := append([]string(nil), values...)
		sort.Strings(copyValues)
		copyDetails[key] = copyValues
	}
	if len(copyDetails) == 0 {
		copyDetails = nil
	}
	state.errors = append(state.errors, ValidationError{Code: code, Message: message, Location: location, Details: copyDetails})
}

func sortValidationErrors(errors ValidationErrors) {
	sort.SliceStable(errors, func(i, j int) bool {
		if errors[i].Location != errors[j].Location {
			return errors[i].Location < errors[j].Location
		}
		if errors[i].Code != errors[j].Code {
			return errors[i].Code < errors[j].Code
		}
		left, _ := json.Marshal(errors[i].Details)
		right, _ := json.Marshal(errors[j].Details)
		return bytes.Compare(left, right) < 0
	})
}

func sortedNodeIDs(nodes map[Identifier]Node) []Identifier {
	result := make([]Identifier, 0, len(nodes))
	for id := range nodes {
		result = append(result, id)
	}
	sort.Slice(result, func(i, j int) bool { return result[i] < result[j] })
	return result
}

func sortedBindingIDs(bindings map[Identifier]Binding) []Identifier {
	result := make([]Identifier, 0, len(bindings))
	for id := range bindings {
		result = append(result, id)
	}
	sort.Slice(result, func(i, j int) bool { return result[i] < result[j] })
	return result
}

func transitionLocation(nodeID Identifier, index int) string {
	return fmt.Sprintf("/spec/nodes/%s/transitions/%d", nodeID, index)
}

func transitionFields(transition Transition) TransitionFields {
	switch value := transition.(type) {
	case NormalTransition:
		return value.Common
	case BoundedTransition:
		return value.Common
	default:
		return TransitionFields{}
	}
}

func bindingPointer(binding Binding) string {
	switch value := binding.(type) {
	case RequiredBinding:
		return value.Pointer
	case OptionalBinding:
		return value.Pointer
	default:
		return ""
	}
}

func typesCompatible(source, target ValueType) bool {
	return source == target || source == ValueInteger && target == ValueNumber
}

func rawValueType(raw json.RawMessage) (ValueType, bool) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return "", false
	}
	switch value := value.(type) {
	case nil:
		return ValueNull, true
	case bool:
		return ValueBoolean, true
	case string:
		return ValueString, true
	case []any:
		return ValueArray, true
	case map[string]any:
		return ValueObject, true
	case json.Number:
		if _, err := value.Int64(); err == nil {
			return ValueInteger, true
		}
		if _, err := value.Float64(); err == nil {
			return ValueNumber, true
		}
	}
	return "", false
}

func validComparison(operator ComparisonOperator, left, right ValueType) bool {
	compatible := typesCompatible(left, right) || typesCompatible(right, left)
	if operator == CompareEqual || operator == CompareNotEqual {
		return compatible
	}
	if left == ValueString && right == ValueString {
		return true
	}
	return (left == ValueInteger || left == ValueNumber) && (right == ValueInteger || right == ValueNumber)
}

func predicateHasPrefix(predicate Predicate, prefix string) bool {
	switch value := predicate.(type) {
	case ComparisonPredicate:
		for _, operand := range value.Args {
			if reference, ok := operand.(ReferenceOperand); ok && strings.HasPrefix(reference.Ref, prefix) {
				return true
			}
		}
	case PresentPredicate:
		return strings.HasPrefix(value.Reference.Ref, prefix)
	case LogicalPredicate:
		for _, arg := range value.Args {
			if predicateHasPrefix(arg, prefix) {
				return true
			}
		}
	case NotPredicate:
		return predicateHasPrefix(value.Arg, prefix)
	}
	return false
}

func sameIdentifiers(declared []Identifier, authored []string) bool {
	if len(declared) != len(authored) {
		return false
	}
	set := make(map[string]struct{}, len(declared))
	for _, id := range declared {
		set[string(id)] = struct{}{}
	}
	for _, id := range authored {
		if _, exists := set[id]; !exists {
			return false
		}
	}
	return true
}

func distinctCoveredIncoming(left, right map[Identifier]bool) []string {
	result := make([]string, 0, len(left)+len(right))
	for id := range left {
		if !right[id] {
			result = append(result, string(id))
		}
	}
	for id := range right {
		if !left[id] {
			result = append(result, string(id))
		}
	}
	return result
}

func transitionAlwaysFires(transition Transition) bool {
	when := transitionFields(transition).When
	if when == nil {
		return true
	}
	constant, ok := when.(ConstantPredicate)
	return ok && constant.Value
}

func transitionEnabled(transition Transition) bool {
	enabled := transitionFields(transition).EnabledByDefault
	return enabled == nil || *enabled
}
