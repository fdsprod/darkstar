package trackerrules

import (
	"encoding/hex"
	"math"
	"reflect"
	"slices"
	"strings"

	"darkstar/src/core/trackercontract"
	"darkstar/src/ports"
	"darkstar/src/ports/tracker"
)

func fail(code ports.FailureCode, message string) error {
	return &ports.Failure{Code: code, Message: message}
}

func validID(value string) bool {
	return value != "" && value == strings.TrimSpace(value)
}

func uniqueIDs(values []string) bool {
	seen := make(map[string]bool)
	for _, value := range values {
		if !validID(value) || seen[value] {
			return false
		}
		seen[value] = true
	}
	return true
}

func validWorkflow(pin WorkflowPin) bool {
	digest, err := hex.DecodeString(pin.Digest)
	return validID(pin.ID) && validID(pin.Version) && err == nil && len(digest) == 32
}

func validMilestone(milestone MilestoneContract) bool {
	if !validID(milestone.ID) || !validWorkflow(milestone.Workflow) || len(milestone.EvidenceTypes) == 0 || !uniqueIDs(milestone.EvidenceTypes) {
		return false
	}
	switch milestone.ID {
	case "run_completed", "run.completed", "completed", "succeeded":
		return false
	}
	return true
}

func validateConditions(conditions Conditions) error {
	seen := make(map[string]bool)
	for _, field := range conditions.Fields {
		switch field.FieldID {
		case "state", "state_reason", "issue_type", "priority", "assignee", "label", "workflow":
		default:
			return fail(ports.FailureUnsupported, "unsupported predicate field: "+field.FieldID)
		}
		if seen[field.FieldID] || len(field.Values) == 0 || !uniqueIDs(field.Values) {
			return fail(ports.FailureInvalidRequest, "predicate fields and values must be unique stable IDs")
		}
		seen[field.FieldID] = true
	}
	if !uniqueIDs(conditions.SprintIDs) {
		return fail(ports.FailureInvalidRequest, "sprint IDs must be unique stable IDs")
	}
	return nil
}

// ValidateShape rejects contradictions before consulting provider capabilities.
func ValidateShape(rules RuleSet) error {
	if rules.Version != Version {
		return fail(ports.FailureProtocolDrift, "unsupported tracker rule schema version")
	}
	if !validID(rules.ID) || rules.Revision == 0 || !validID(rules.Scope.ProjectID) || rules.Scope.BindingRevision == 0 {
		return fail(ports.FailureInvalidRequest, "rule identity, revision, project and source binding revision are required")
	}
	if err := trackercontract.ValidatePin(rules.Scope.Pin, rules.Scope.Pin); err != nil {
		return err
	}
	namespace := rules.Scope.Source.Namespace
	for _, id := range []string{namespace.Provider, namespace.Host, namespace.TenantID, namespace.ScopeID, rules.Scope.Source.ContainerID} {
		if !validID(id) {
			return fail(ports.FailureInvalidRequest, "source scope requires complete stable identity")
		}
	}
	seen := make(map[string]bool)
	for _, rule := range rules.Intake {
		if !validID(rule.ID) || seen[rule.ID] {
			return fail(ports.FailureInvalidRequest, "rule IDs must be unique and nonempty")
		}
		seen[rule.ID] = true
		if err := validateConditions(rule.When); err != nil {
			return err
		}
		switch action := rule.Action.(type) {
		case Noop:
		case Admit:
			if !validWorkflow(action.Workflow) || !validID(action.ReadinessPolicy) || (action.Mode != Manual && action.Mode != Automatic) || action.Repair.MaxAdmissions < 1 || action.Repair.MaxAdmissions > 100 {
				return fail(ports.FailureInvalidRequest, "admission requires a pinned workflow, readiness policy, admission mode and bounded repair policy (1-100 admissions)")
			}
		default:
			return fail(ports.FailureUnsupported, "unsupported intake action")
		}
	}
	for _, rule := range rules.Outbound {
		if !validID(rule.ID) || seen[rule.ID] {
			return fail(ports.FailureInvalidRequest, "rule IDs must be unique and nonempty")
		}
		seen[rule.ID] = true
		if err := validateConditions(rule.When); err != nil {
			return err
		}
		if !validMilestone(rule.Milestone) {
			return fail(ports.FailureInvalidRequest, "outbound rules require a typed workflow milestone with an evidence contract")
		}
		switch action := rule.Action.(type) {
		case Noop:
		case Report:
			if strings.TrimSpace(action.Body) == "" {
				return fail(ports.FailureInvalidRequest, "report body is required")
			}
		case Transition:
			if !validID(action.TransitionID) {
				return fail(ports.FailureInvalidRequest, "transition stable operation ID is required")
			}
			for id, value := range action.Fields {
				if !validID(id) || value == nil {
					return fail(ports.FailureInvalidRequest, "transition field values must be explicit")
				}
				switch typed := value.(type) {
				case tracker.TextValue:
				case tracker.NumberValue:
					if math.IsNaN(float64(typed)) || math.IsInf(float64(typed), 0) {
						return fail(ports.FailureInvalidRequest, "transition number must be finite")
					}
				case tracker.IDsValue:
					if !uniqueIDs(typed) {
						return fail(ports.FailureInvalidRequest, "transition IDs must be unique")
					}
				default:
					return fail(ports.FailureUnsupported, "unsupported transition field value")
				}
			}
		default:
			return fail(ports.FailureUnsupported, "unsupported outbound action")
		}
	}
	if err := ValidateDisplay(rules.Display); err != nil {
		return err
	}
	for index, rule := range rules.Intake {
		for _, other := range rules.Intake[index+1:] {
			if conditionsOverlap(rule.When, other.When) {
				return fail(ports.FailureConflict, "ambiguous intake rules: "+rule.ID+" and "+other.ID)
			}
		}
	}
	for index, rule := range rules.Outbound {
		for _, other := range rules.Outbound[index+1:] {
			if rule.Milestone.ID == other.Milestone.ID && rule.Milestone.Workflow == other.Milestone.Workflow && conditionsOverlap(rule.When, other.When) {
				return fail(ports.FailureConflict, "ambiguous outbound milestone rules: "+rule.ID+" and "+other.ID)
			}
		}
	}
	return nil
}

func ValidateDisplay(display DisplayMapping) error {
	if !validID(display.UnknownGroup.ID) || strings.TrimSpace(display.UnknownGroup.Name) == "" || len(display.UnknownGroup.StateIDs) != 0 {
		return fail(ports.FailureInvalidRequest, "display requires an explicit unknown group without mapped state IDs")
	}
	groups := map[string]bool{display.UnknownGroup.ID: true}
	states := make(map[string]bool)
	for _, group := range display.Groups {
		if !validID(group.ID) || groups[group.ID] || strings.TrimSpace(group.Name) == "" || len(group.StateIDs) == 0 || !uniqueIDs(group.StateIDs) {
			return fail(ports.FailureInvalidRequest, "display groups require unique IDs, names and nonempty state mappings")
		}
		groups[group.ID] = true
		for _, state := range group.StateIDs {
			if states[state] {
				return fail(ports.FailureConflict, "one business state cannot appear in multiple display groups")
			}
			states[state] = true
		}
	}
	return nil
}

func conditionsOverlap(left, right Conditions) bool {
	for _, a := range left.Fields {
		// Multivalued labels/assignees can satisfy disjoint value predicates.
		if a.FieldID == "label" || a.FieldID == "assignee" {
			continue
		}
		for _, b := range right.Fields {
			if a.FieldID == b.FieldID && !intersects(a.Values, b.Values) {
				return false
			}
		}
	}
	return true
}

func intersects(left, right []string) bool {
	for _, value := range left {
		if slices.Contains(right, value) {
			return true
		}
	}
	return false
}

func Validate(rules RuleSet, discovery Discovery) error {
	if err := ValidateShape(rules); err != nil {
		return err
	}
	if err := trackercontract.ValidatePin(rules.Scope.Pin, discovery.Manifest.Pin); err != nil {
		return err
	}
	if rules.Scope.Source != discovery.Manifest.Scope {
		return fail(ports.FailureConflict, "rule source differs from discovered scope")
	}
	if err := trackercontract.Require(discovery.Manifest, tracker.Fetch); err != nil {
		return err
	}
	fields := make(map[string][]tracker.NamedID)
	for _, field := range discovery.Fields {
		if _, exists := fields[field.ID]; exists || !validID(field.ID) || !uniqueNamedIDs(field.Values) {
			return fail(ports.FailureProtocolDrift, "discovery field/value IDs must be unique")
		}
		fields[field.ID] = field.Values
	}
	if !uniqueNamedIDs(discovery.Sprints) {
		return fail(ports.FailureProtocolDrift, "discovery sprint IDs must be unique")
	}
	transitions := make(map[string]tracker.Transition)
	for _, transition := range discovery.Transitions {
		if _, exists := transitions[transition.Identity.ID]; exists || !validID(transition.Identity.ID) || !validID(transition.ToState.ID) {
			return fail(ports.FailureProtocolDrift, "discovery transition IDs must be unique and name a target state")
		}
		transitions[transition.Identity.ID] = transition
	}
	for _, rule := range rules.Intake {
		if err := validateDiscoveredConditions(rule.When, fields, discovery.Sprints); err != nil {
			return err
		}
		if action, ok := rule.Action.(Admit); ok {
			if !slices.Contains(discovery.Workflows, action.Workflow) || !slices.Contains(discovery.ReadinessPolicies, action.ReadinessPolicy) {
				return fail(ports.FailureUnsupported, "admission workflow or readiness policy is not available at the exact pin")
			}
		}
	}
	for _, rule := range rules.Outbound {
		if err := validateDiscoveredConditions(rule.When, fields, discovery.Sprints); err != nil {
			return err
		}
		declared := false
		for _, milestone := range discovery.Milestones {
			if reflect.DeepEqual(milestone, rule.Milestone) {
				declared = true
			}
		}
		if !declared {
			return fail(ports.FailureUnsupported, "milestone is not declared by the pinned workflow with this evidence contract")
		}
		switch action := rule.Action.(type) {
		case Noop:
		case Report:
			if err := trackercontract.Require(discovery.Manifest, tracker.Progress); err != nil {
				return err
			}
		case Transition:
			if err := trackercontract.Require(discovery.Manifest, tracker.Transitions); err != nil {
				return err
			}
			transition, exists := transitions[action.TransitionID]
			if !exists {
				return fail(ports.FailureUnsupported, "transition operation ID was not discovered")
			}
			if err := validateTransitionFields(transition.Fields, action.Fields); err != nil {
				return err
			}
		}
	}
	for _, group := range rules.Display.Groups {
		for _, state := range group.StateIDs {
			if !namedContains(fields["state"], state) {
				return fail(ports.FailureUnsupported, "display state ID was not discovered")
			}
		}
	}
	return nil
}

func uniqueNamedIDs(values []tracker.NamedID) bool {
	ids := make([]string, 0, len(values))
	for _, value := range values {
		ids = append(ids, value.ID)
	}
	return uniqueIDs(ids)
}

func namedContains(values []tracker.NamedID, id string) bool {
	for _, value := range values {
		if value.ID == id {
			return true
		}
	}
	return false
}

func validateDiscoveredConditions(conditions Conditions, fields map[string][]tracker.NamedID, sprints []tracker.NamedID) error {
	for _, predicate := range conditions.Fields {
		for _, value := range predicate.Values {
			if !namedContains(fields[predicate.FieldID], value) {
				return fail(ports.FailureUnsupported, "predicate field/value was not discovered: "+predicate.FieldID)
			}
		}
	}
	for _, sprint := range conditions.SprintIDs {
		if !namedContains(sprints, sprint) {
			return fail(ports.FailureUnsupported, "sprint predicate was not discovered")
		}
	}
	return nil
}

func validateTransitionFields(definitions []tracker.Field, values map[string]tracker.FieldValue) error {
	seen := make(map[string]bool)
	for _, definition := range definitions {
		id := definition.Identity.ID
		if !validID(id) || seen[id] {
			return fail(ports.FailureProtocolDrift, "transition field definitions contain duplicate or empty IDs")
		}
		seen[id] = true
		value, exists := values[id]
		if !exists {
			if definition.Required {
				return fail(ports.FailureInvalidRequest, "required transition field is missing: "+id)
			}
			continue
		}
		switch constraint := definition.Constraint.(type) {
		case tracker.TextConstraint:
			if _, ok := value.(tracker.TextValue); !ok {
				return fail(ports.FailureInvalidRequest, "transition field requires text")
			}
		case tracker.NumberConstraint:
			if _, ok := value.(tracker.NumberValue); !ok {
				return fail(ports.FailureInvalidRequest, "transition field requires a number")
			}
		case tracker.IDsConstraint:
			ids, ok := value.(tracker.IDsValue)
			if !ok {
				return fail(ports.FailureInvalidRequest, "transition field requires IDs")
			}
			switch choices := constraint.Choices.(type) {
			case tracker.AnyID:
			case tracker.AllowedIDs:
				for _, id := range ids {
					if !slices.Contains(choices.Values, id) {
						return fail(ports.FailureInvalidRequest, "transition field ID is not allowed")
					}
				}
			default:
				return fail(ports.FailureUnsupported, "transition field choices are unknown")
			}
		default:
			return fail(ports.FailureUnsupported, "transition field constraint is unknown")
		}
	}
	for id := range values {
		if !seen[id] {
			return fail(ports.FailureUnsupported, "transition field was not discovered: "+id)
		}
	}
	return nil
}
