package workflow

import (
	"encoding/json"
	"fmt"
	"sort"
)

const (
	ValidationRouteEntryInvalid    ValidationCode = "ROUTE_ENTRY_INVALID"
	ValidationRouteTerminalInvalid ValidationCode = "ROUTE_TERMINAL_INVALID"
	ValidationRoutePathIncomplete  ValidationCode = "ROUTE_PATH_INCOMPLETE"
	ValidationRoutePolicyViolation ValidationCode = "ROUTE_POLICY_VIOLATION"
	ValidationRunInputRequired     ValidationCode = "RUN_INPUT_REQUIRED"
)

// RouteRequest is the explicit route range requested by a caller. Empty fields
// select the workflow defaults; From and Until are the direct core equivalents
// of the CLI --from and --until options.
type RouteRequest struct {
	From  Identifier   `json:"from,omitempty"`
	Until []Identifier `json:"until,omitempty"`
}

// RouteContext contains facts available while freezing a route. Map membership,
// rather than the JSON value, records availability so a present null remains a
// present value. RequiredNodes is the project-policy control set that the route
// is not authorized to bypass.
type RouteContext struct {
	RunInputs       map[Identifier]json.RawMessage                `json:"runInputs,omitempty"`
	AcceptedOutputs map[Identifier]map[Identifier]json.RawMessage `json:"acceptedOutputs,omitempty"`
	RequiredNodes   []Identifier                                  `json:"requiredNodes,omitempty"`
}

// Route is the frozen authorized subgraph. Nodes and ExcludedNodes are separate
// partitions so an excluded node cannot accidentally become executable through
// a contradictory flag combination.
type Route struct {
	Entry             Identifier         `json:"entry"`
	Terminals         []Identifier       `json:"terminals"`
	Nodes             []RouteNode        `json:"nodes"`
	Transitions       []RouteTransition  `json:"transitions"`
	ExcludedNodes     []ExcludedNode     `json:"excludedNodes"`
	InputRequirements []InputRequirement `json:"inputRequirements"`
}

// RouteNode is one executable node and its route-projected join, when needed.
type RouteNode struct {
	ID   Identifier `json:"id"`
	Join *Join      `json:"join,omitempty"`
}

// RouteTransition is one enabled transition retained in the frozen route.
type RouteTransition struct {
	ID   Identifier `json:"id"`
	From Identifier `json:"from"`
	To   Identifier `json:"to"`
}

// ExclusionReason states why an authored node is outside this route.
type ExclusionReason string

const (
	ExclusionBeforeEntry  ExclusionReason = "before_entry"
	ExclusionPastTerminal ExclusionReason = "past_terminal"
	ExclusionNotConnected ExclusionReason = "not_connected"
)

// ExcludedNode is non-executable route evidence. It is never copied into Nodes.
type ExcludedNode struct {
	ID     Identifier      `json:"id"`
	Reason ExclusionReason `json:"reason"`
}

// InputRequirement is a structured waiting result for one required binding that
// neither current context nor an included predecessor can produce.
type InputRequirement struct {
	Code   ValidationCode `json:"code"`
	Node   Identifier     `json:"node"`
	Input  Identifier     `json:"input"`
	Source string         `json:"source"`
}

// CreateRoute resolves defaults, freezes the enabled subgraph at the selected
// terminals, validates authorization and connectivity, and reports missing
// runtime inputs without treating excluded nodes as failed visits.
func CreateRoute(document Document, request RouteRequest, context RouteContext) (Route, ValidationErrors) {
	entry := request.From
	if entry == "" {
		entry = document.Spec.RouteDefaults.Entry
	}
	terminals := append([]Identifier(nil), request.Until...)
	if len(terminals) == 0 {
		terminals = append(terminals, document.Spec.RouteDefaults.Terminals...)
	}
	terminals = uniqueSortedIdentifiers(terminals)

	errors := validateRouteBoundaries(document, entry, terminals)
	if len(errors) != 0 {
		sortValidationErrors(errors)
		return Route{}, errors
	}

	terminalSet := identifierSet(terminals)
	included := forwardRouteNodes(document, entry, terminalSet)
	errors = append(errors, validateReachableTerminals(terminals, included)...)
	transitions := includedRouteTransitions(document, included, terminalSet)
	errors = append(errors, validateRouteClosure(included, terminalSet, transitions)...)
	errors = append(errors, validateRoutePolicy(document, included, context.RequiredNodes)...)
	if len(errors) != 0 {
		sortValidationErrors(errors)
		return Route{}, errors
	}

	route := Route{
		Entry:         entry,
		Terminals:     terminals,
		Transitions:   transitions,
		ExcludedNodes: excludedRouteNodes(document, included, entry, terminalSet),
	}
	route.Nodes = projectedRouteNodes(document, included, transitions)
	route.InputRequirements = routeInputRequirements(document, included, transitions, context)
	return route, nil
}

func validateRouteBoundaries(document Document, entry Identifier, terminals []Identifier) ValidationErrors {
	var errors ValidationErrors
	node, exists := document.Spec.Nodes[entry]
	if !exists || !node.Fields().Entry {
		errors = append(errors, ValidationError{
			Code: ValidationRouteEntryInvalid, Message: "selected entry is absent or not entry-capable",
			Location: "/from", Details: map[string][]string{"nodeIds": {string(entry)}},
		})
	}
	if len(terminals) == 0 {
		errors = append(errors, ValidationError{
			Code: ValidationRouteTerminalInvalid, Message: "at least one selected terminal is required", Location: "/until",
		})
		return errors
	}
	for _, terminal := range terminals {
		node, exists := document.Spec.Nodes[terminal]
		if !exists || !node.Fields().Terminal {
			errors = append(errors, ValidationError{
				Code: ValidationRouteTerminalInvalid, Message: "selected terminal is absent or not terminal-capable",
				Location: "/until", Details: map[string][]string{"nodeIds": {string(terminal)}},
			})
		}
	}
	return errors
}

func forwardRouteNodes(document Document, entry Identifier, terminals map[Identifier]bool) map[Identifier]bool {
	included := make(map[Identifier]bool, len(document.Spec.Nodes))
	stack := []Identifier{entry}
	for len(stack) != 0 {
		last := len(stack) - 1
		current := stack[last]
		stack = stack[:last]
		if included[current] {
			continue
		}
		included[current] = true
		if terminals[current] {
			continue
		}
		for _, transition := range document.Spec.Nodes[current].Fields().Transitions {
			if transitionEnabled(transition) {
				if _, exists := document.Spec.Nodes[transition.Target()]; exists {
					stack = append(stack, transition.Target())
				}
			}
		}
	}
	return included
}

func validateReachableTerminals(terminals []Identifier, included map[Identifier]bool) ValidationErrors {
	var errors ValidationErrors
	for _, terminal := range terminals {
		if !included[terminal] {
			errors = append(errors, ValidationError{
				Code: ValidationRouteTerminalInvalid, Message: "selected terminal is unreachable from the selected entry",
				Location: "/until", Details: map[string][]string{"nodeIds": {string(terminal)}},
			})
		}
	}
	return errors
}

func includedRouteTransitions(document Document, included, terminals map[Identifier]bool) []RouteTransition {
	result := make([]RouteTransition, 0)
	for _, source := range sortedNodeIDs(document.Spec.Nodes) {
		if !included[source] || terminals[source] {
			continue
		}
		for _, transition := range document.Spec.Nodes[source].Fields().Transitions {
			if transitionEnabled(transition) && included[transition.Target()] {
				result = append(result, RouteTransition{ID: transition.ID(), From: source, To: transition.Target()})
			}
		}
	}
	return result
}

func validateRouteClosure(included, terminals map[Identifier]bool, transitions []RouteTransition) ValidationErrors {
	reverse := make(map[Identifier][]Identifier, len(included))
	for _, transition := range transitions {
		reverse[transition.To] = append(reverse[transition.To], transition.From)
	}
	canReachTerminal := make(map[Identifier]bool, len(included))
	stack := make([]Identifier, 0, len(terminals))
	for terminal := range terminals {
		if included[terminal] {
			canReachTerminal[terminal] = true
			stack = append(stack, terminal)
		}
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
	for nodeID := range included {
		if !canReachTerminal[nodeID] {
			stranded = append(stranded, string(nodeID))
		}
	}
	if len(stranded) == 0 {
		return nil
	}
	sort.Strings(stranded)
	return ValidationErrors{{
		Code: ValidationRoutePathIncomplete, Message: "one or more possible enabled branches cannot reach the selected terminal boundary",
		Location: "/", Details: map[string][]string{"nodeIds": stranded},
	}}
}

func validateRoutePolicy(document Document, included map[Identifier]bool, required []Identifier) ValidationErrors {
	missing := make([]string, 0)
	for nodeID := range identifierSet(required) {
		if _, exists := document.Spec.Nodes[nodeID]; !exists || !included[nodeID] {
			missing = append(missing, string(nodeID))
		}
	}
	if len(missing) == 0 {
		return nil
	}
	sort.Strings(missing)
	return ValidationErrors{{
		Code: ValidationRoutePolicyViolation, Message: "selected route bypasses one or more policy-required nodes",
		Location: "/", Details: map[string][]string{"nodeIds": missing},
	}}
}

func projectedRouteNodes(document Document, included map[Identifier]bool, transitions []RouteTransition) []RouteNode {
	incoming := make(map[Identifier][]Identifier, len(included))
	for _, transition := range transitions {
		incoming[transition.To] = append(incoming[transition.To], transition.ID)
	}
	result := make([]RouteNode, 0, len(included))
	for _, nodeID := range sortedNodeIDs(document.Spec.Nodes) {
		if !included[nodeID] {
			continue
		}
		routeNode := RouteNode{ID: nodeID}
		if authored := document.Spec.Nodes[nodeID].Fields().Join; authored != nil && len(incoming[nodeID]) != 0 {
			allowed := identifierSet(incoming[nodeID])
			projected := make([]Identifier, 0, len(incoming[nodeID]))
			for _, transitionID := range authored.From {
				if allowed[transitionID] {
					projected = append(projected, transitionID)
				}
			}
			routeNode.Join = &Join{Mode: authored.Mode, From: projected}
		}
		result = append(result, routeNode)
	}
	return result
}

func excludedRouteNodes(document Document, included map[Identifier]bool, entry Identifier, terminals map[Identifier]bool) []ExcludedNode {
	before := reverseReachable(document, entry)
	past := forwardReachable(document, terminals)
	result := make([]ExcludedNode, 0, len(document.Spec.Nodes)-len(included))
	for _, nodeID := range sortedNodeIDs(document.Spec.Nodes) {
		if included[nodeID] {
			continue
		}
		reason := ExclusionNotConnected
		switch {
		case before[nodeID]:
			reason = ExclusionBeforeEntry
		case past[nodeID]:
			reason = ExclusionPastTerminal
		}
		result = append(result, ExcludedNode{ID: nodeID, Reason: reason})
	}
	return result
}

func reverseReachable(document Document, target Identifier) map[Identifier]bool {
	reverse := make(map[Identifier][]Identifier, len(document.Spec.Nodes))
	for source, node := range document.Spec.Nodes {
		for _, transition := range node.Fields().Transitions {
			if transitionEnabled(transition) {
				reverse[transition.Target()] = append(reverse[transition.Target()], source)
			}
		}
	}
	visited := map[Identifier]bool{target: true}
	stack := []Identifier{target}
	for len(stack) != 0 {
		last := len(stack) - 1
		current := stack[last]
		stack = stack[:last]
		for _, source := range reverse[current] {
			if !visited[source] {
				visited[source] = true
				stack = append(stack, source)
			}
		}
	}
	return visited
}

func forwardReachable(document Document, starts map[Identifier]bool) map[Identifier]bool {
	visited := make(map[Identifier]bool, len(document.Spec.Nodes))
	stack := make([]Identifier, 0, len(starts))
	for start := range starts {
		stack = append(stack, start)
	}
	for len(stack) != 0 {
		last := len(stack) - 1
		current := stack[last]
		stack = stack[:last]
		if visited[current] {
			continue
		}
		visited[current] = true
		node, exists := document.Spec.Nodes[current]
		if !exists {
			continue
		}
		for _, transition := range node.Fields().Transitions {
			if transitionEnabled(transition) {
				stack = append(stack, transition.Target())
			}
		}
	}
	return visited
}

func routeInputRequirements(
	document Document,
	included map[Identifier]bool,
	transitions []RouteTransition,
	context RouteContext,
) []InputRequirement {
	result := make([]InputRequirement, 0)
	predecessors := routePredecessors(transitions)
	for _, nodeID := range sortedNodeIDs(document.Spec.Nodes) {
		if !included[nodeID] {
			continue
		}
		fields := document.Spec.Nodes[nodeID].Fields()
		for _, inputID := range sortedBindingIDs(fields.Inputs) {
			binding, required := fields.Inputs[inputID].(RequiredBinding)
			if !required || routeBindingAvailable(binding.From, nodeID, predecessors, context) {
				continue
			}
			result = append(result, InputRequirement{
				Code: ValidationRunInputRequired, Node: nodeID, Input: inputID, Source: binding.From,
			})
		}
	}
	return result
}

func routeBindingAvailable(
	source string,
	consumer Identifier,
	predecessors map[Identifier]map[Identifier]bool,
	context RouteContext,
) bool {
	parts := splitReference(source)
	if len(parts) == 3 && parts[0] == "run" && parts[1] == "input" {
		_, exists := context.RunInputs[Identifier(parts[2])]
		return exists
	}
	if len(parts) == 4 && parts[0] == "node" && parts[2] == "output" {
		sourceNode, output := Identifier(parts[1]), Identifier(parts[3])
		if outputs, exists := context.AcceptedOutputs[sourceNode]; exists {
			if _, exists := outputs[output]; exists {
				return true
			}
		}
		return predecessors[consumer][sourceNode]
	}
	return false
}

func routePredecessors(transitions []RouteTransition) map[Identifier]map[Identifier]bool {
	reverse := make(map[Identifier][]Identifier)
	for _, transition := range transitions {
		reverse[transition.To] = append(reverse[transition.To], transition.From)
	}
	result := make(map[Identifier]map[Identifier]bool)
	for consumer := range reverse {
		visited := make(map[Identifier]bool)
		stack := append([]Identifier(nil), reverse[consumer]...)
		for len(stack) != 0 {
			last := len(stack) - 1
			current := stack[last]
			stack = stack[:last]
			if visited[current] {
				continue
			}
			visited[current] = true
			stack = append(stack, reverse[current]...)
		}
		delete(visited, consumer)
		result[consumer] = visited
	}
	return result
}

func splitReference(value string) []string {
	parts := make([]string, 0, 4)
	start := 0
	for index := 0; index <= len(value); index++ {
		if index == len(value) || value[index] == '.' {
			parts = append(parts, value[start:index])
			start = index + 1
		}
	}
	return parts
}

func identifierSet(values []Identifier) map[Identifier]bool {
	result := make(map[Identifier]bool, len(values))
	for _, value := range values {
		result[value] = true
	}
	return result
}

func uniqueSortedIdentifiers(values []Identifier) []Identifier {
	set := identifierSet(values)
	result := make([]Identifier, 0, len(set))
	for value := range set {
		result = append(result, value)
	}
	sort.Slice(result, func(i, j int) bool { return result[i] < result[j] })
	return result
}

func (requirement InputRequirement) String() string {
	return fmt.Sprintf("%s: node %q input %q requires %s", requirement.Code, requirement.Node, requirement.Input, requirement.Source)
}
