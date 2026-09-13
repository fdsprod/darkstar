// Package trackercontract provides pure preflight checks for the versioned
// tracker ports. It does not authorize, dispatch, persist, retry, or schedule.
package trackercontract

import (
	"encoding/hex"
	"math"
	"reflect"
	"slices"
	"strings"
	"time"

	"darkstar/src/ports"
	"darkstar/src/ports/tracker"
)

func fail(code ports.FailureCode, reason string) error {
	return &ports.Failure{Code: code, Message: reason}
}

func ValidatePin(expected, observed tracker.Pin) error {
	for _, value := range []string{expected.AdapterID, expected.AdapterVersion, expected.InstallationID, expected.AccountID, expected.BindingRevision, expected.ConfigRevision, expected.ConfigDigest, expected.CapabilitiesDigest} {
		if strings.TrimSpace(value) == "" {
			return fail(ports.FailureInvalidRequest, "exact adapter and configuration pins are required")
		}
	}
	if expected.ContractVersion != tracker.Version || observed != expected {
		return fail(ports.FailureProtocolDrift, "tracker contract, implementation, configuration, or capabilities changed")
	}
	return nil
}

func Require(manifest tracker.Manifest, capability tracker.Capability) error {
	switch support := manifest.Capabilities[capability].(type) {
	case tracker.Known[bool]:
		if support.Value {
			return nil
		}
		return fail(ports.FailureUnsupported, "tracker capability is unavailable: "+string(capability))
	case tracker.Unsupported[bool]:
		return fail(ports.FailureUnsupported, "tracker capability is unsupported: "+string(capability))
	default:
		return fail(ports.FailureProtocolDrift, "tracker capability is unknown: "+string(capability))
	}
}

func ValidateQuery(pin tracker.Pin, manifest tracker.Manifest, query tracker.Query) error {
	if err := ValidatePin(pin, manifest.Pin); err != nil {
		return err
	}
	if err := Require(manifest, tracker.List); err != nil {
		return err
	}
	if query.PageSize < 1 || query.PageSize > manifest.MaxPageSize {
		return fail(ports.FailureInvalidRequest, "page size is outside advertised limits")
	}
	if err := Require(manifest, tracker.Page); err != nil {
		return err
	}
	if query.Text != "" {
		if err := Require(manifest, tracker.Search); err != nil {
			return err
		}
	}
	for _, predicate := range query.Predicates {
		if err := Require(manifest, tracker.Filter); err != nil {
			return err
		}
		if !slices.Contains(manifest.Filters[predicate.FieldID], predicate.Operator) || len(predicate.Values) == 0 {
			return fail(ports.FailureUnsupported, "filter field/operator is not advertised")
		}
		if predicate.Operator != tracker.In && len(predicate.Values) != 1 {
			return fail(ports.FailureInvalidRequest, "filter operator requires exactly one value")
		}
	}
	return nil
}

// ValidatePublicationDestination limits the reviewed-backlog publication workflow.
// Built-in writers remain first-class for native ticket operations outside it.
func ValidatePublicationDestination(destination tracker.Scope) error {
	if destination.Namespace.Provider != "linear" && destination.Namespace.Provider != "github_issues" {
		return fail(ports.FailureUnsupported, "backlog publication requires Linear or GitHub Issues")
	}
	return validateScope(destination)
}

func validateScope(scope tracker.Scope) error {
	if scope.ContainerID == "" {
		return fail(ports.FailureInvalidRequest, "binding container ID is required")
	}
	return validateNamespace(scope.Namespace)
}

func validateNamespace(namespace tracker.Namespace) error {
	if namespace.Provider == "" || namespace.Host == "" || namespace.TenantID == "" || namespace.ScopeID == "" {
		return fail(ports.FailureInvalidRequest, "stable provider, host, tenant, and scope identities are required")
	}
	return nil
}

// ValidateIntent checks observed compatibility only. The daemon must also verify
// the exact approval/rule, artifact hashes, desired digest, and operation journal.
func ValidateIntent(intent tracker.Intent, manifest tracker.Manifest, options tracker.WriteOptions, now time.Time) error {
	if intent.OperationID == "" || intent.DesiredDigest == "" || intent.AuthorizationRef == "" {
		return fail(ports.FailureInvalidRequest, "operation, desired digest, and authorization reference are required")
	}
	if err := validateScope(intent.Destination); err != nil {
		return err
	}
	if err := ValidatePin(intent.Pin, manifest.Pin); err != nil {
		return err
	}
	if intent.Destination != manifest.Scope {
		return fail(ports.FailureConflict, "intent destination differs from adapter scope")
	}
	if err := Require(manifest, tracker.Reconciliation); err != nil {
		return err
	}
	if err := ValidatePin(intent.Pin, options.Pin); err != nil {
		return err
	}
	if options.EvidenceRef == "" || options.ObservedAt.IsZero() || options.ObservedAt.After(now) || !options.ValidUntil.After(now) {
		return fail(ports.FailureConflict, "writer options are stale or lack observation evidence")
	}
	switch effect := intent.Effect.(type) {
	case tracker.CreateTicket:
		if err := Require(manifest, tracker.Create); err != nil {
			return err
		}
		if !reflect.DeepEqual(options.Scope, tracker.CreationScope{IssueTypeID: effect.IssueTypeID}) || !validOrigin(effect.Origin) {
			return fail(ports.FailureInvalidRequest, "creation type and exact canonical story/backlog are required")
		}
		return validateFields(options.Fields, effect.Fields)
	case tracker.EditTicket:
		if err := checkTarget(intent, options, effect.Target, manifest, tracker.Edit); err != nil {
			return err
		}
		return validateFields(options.Fields, effect.Fields)
	case tracker.ReportProgress:
		if err := checkTarget(intent, options, effect.Target, manifest, tracker.Progress); err != nil {
			return err
		}
		if !validProgress(effect.Evidence) || strings.TrimSpace(effect.Body) == "" {
			return fail(ports.FailureInvalidRequest, "progress requires handoff evidence and a report")
		}
		return nil
	case tracker.TakeTransition:
		if err := checkTarget(intent, options, effect.Target, manifest, tracker.Transitions); err != nil {
			return err
		}
		available, ok := options.Transitions.(tracker.Known[[]tracker.Transition])
		if !ok {
			return fail(ports.FailureUnsupported, "available transitions are unknown or unsupported")
		}
		seen := make(map[string]bool)
		for _, transition := range available.Value {
			if seen[transition.Identity.ID] || transition.Identity.ID == "" {
				return fail(ports.FailureProtocolDrift, "transition IDs must be unique and nonempty")
			}
			seen[transition.Identity.ID] = true
		}
		for _, transition := range available.Value {
			if transition.Identity.ID != effect.TransitionID || effect.TransitionID == "" {
				continue
			}
			if transition.ToState.ID == "" {
				return fail(ports.FailureProtocolDrift, "transition has no stable target state")
			}
			for _, guard := range transition.Guards {
				satisfied, known := guard.Satisfied.(tracker.Known[bool])
				if !known || !satisfied.Value || guard.Identity.ID == "" || guard.EvidenceRef == "" {
					return fail(ports.FailureConflict, "transition guard is unsatisfied or unknown")
				}
			}
			return validateFields(tracker.Known[[]tracker.Field]{Value: transition.Fields}, effect.Fields)
		}
		return fail(ports.FailureConflict, "transition ID is not available for this ticket/type/workflow snapshot")
	case tracker.LinkStories:
		return validateRelationship(intent, manifest, options, effect)
	default:
		return fail(ports.FailureUnsupported, "unknown desired effect")
	}
}

func checkTarget(intent tracker.Intent, options tracker.WriteOptions, target tracker.TicketScope, manifest tracker.Manifest, capability tracker.Capability) error {
	if err := Require(manifest, capability); err != nil {
		return err
	}
	if target.Ref.Namespace != intent.Destination.Namespace || target.Ref.ID == "" || target.Revision == "" || !reflect.DeepEqual(options.Scope, target) {
		return fail(ports.FailureConflict, "ticket, revision, type, workflow, status, or sprint observation changed")
	}
	return nil
}

func validStory(story tracker.StoryRef) bool {
	return story.ProjectID != "" && story.FeatureKey != "" && story.StoryKey != ""
}

func validArtifact(artifact tracker.ArtifactRef) bool {
	decoded, err := hex.DecodeString(artifact.SHA256)
	return artifact.ArtifactID != "" && artifact.Version > 0 && err == nil && len(decoded) == 32
}

func validOrigin(origin tracker.CreationOrigin) bool {
	switch value := origin.(type) {
	case tracker.PlannedCreation:
		return validStory(value.Story) && validArtifact(value.Backlog)
	case tracker.DirectCreation:
		return validArtifact(value.Request)
	default:
		return false
	}
}

func validProgress(evidence tracker.ProgressEvidence) bool {
	switch value := evidence.(type) {
	case tracker.MilestoneProgress:
		return validArtifact(value.Handoff)
	case tracker.GeneralProgress:
		if len(value.Artifacts) == 0 {
			return false
		}
		for _, artifact := range value.Artifacts {
			if !validArtifact(artifact) {
				return false
			}
		}
		return true
	default:
		return false
	}
}

func validateRelationship(intent tracker.Intent, manifest tracker.Manifest, options tracker.WriteOptions, effect tracker.LinkStories) error {
	capability := tracker.Dependencies
	switch effect.Relation.Kind {
	case tracker.ChildOf:
		capability = tracker.Hierarchy
	case tracker.DependsOn:
	default:
		return fail(ports.FailureUnsupported, "unknown canonical relationship")
	}
	if !validStory(effect.Story) || !validStory(effect.RelatedStory) || effect.Story == effect.RelatedStory || effect.Story.ProjectID != effect.RelatedStory.ProjectID || effect.Story.FeatureKey != effect.RelatedStory.FeatureKey || !validArtifact(effect.Backlog) || effect.Relation.Target.ID == "" || effect.Relation.Target == effect.Target.Ref {
		return fail(ports.FailureInvalidRequest, "relationship requires distinct canonical stories, tickets, and exact backlog")
	}
	// This contract's publication scope cannot hide cross-destination writes.
	if effect.Relation.Target.Namespace != intent.Destination.Namespace {
		return fail(ports.FailureUnsupported, "cross-destination relationship needs a separately supported contract")
	}
	switch representation := effect.Representation.(type) {
	case tracker.NativeRelation:
	case tracker.ManagedRelation:
		if representation.FallbackRevision == "" || representation.SectionID == "" {
			return fail(ports.FailureInvalidRequest, "visible relationship fallback must be explicitly configured")
		}
		capability = tracker.ManagedLinks
	default:
		return fail(ports.FailureUnsupported, "relationship representation must be native or an explicit visible fallback")
	}
	return checkTarget(intent, options, effect.Target, manifest, capability)
}

func validateFields(observation tracker.Knowledge[[]tracker.Field], values map[string]tracker.FieldValue) error {
	fields, ok := observation.(tracker.Known[[]tracker.Field])
	if !ok {
		return fail(ports.FailureUnsupported, "field requirements are unknown or unsupported")
	}
	definitions := make(map[string]tracker.Field)
	for _, field := range fields.Value {
		switch constraint := field.Constraint.(type) {
		case tracker.TextConstraint, tracker.NumberConstraint:
		case tracker.IDsConstraint:
			switch constraint.Choices.(type) {
			case tracker.AnyID, tracker.AllowedIDs:
			default:
				return fail(ports.FailureProtocolDrift, "ID field constraints are unknown")
			}
		default:
			return fail(ports.FailureProtocolDrift, "unknown field constraint")
		}
		if _, duplicate := definitions[field.Identity.ID]; duplicate || field.Identity.ID == "" {
			return fail(ports.FailureProtocolDrift, "field definitions require unique stable IDs")
		}
		definitions[field.Identity.ID] = field
		if _, present := values[field.Identity.ID]; field.Required && !present {
			return fail(ports.FailureInvalidRequest, "required field is missing: "+field.Identity.ID)
		}
	}
	for id, value := range values {
		field, exists := definitions[id]
		if !exists {
			return fail(ports.FailureUnsupported, "field is not advertised for this operation: "+id)
		}
		valid := false
		switch typed := value.(type) {
		case tracker.TextValue:
			_, textField := field.Constraint.(tracker.TextConstraint)
			valid = textField && (!field.Required || strings.TrimSpace(string(typed)) != "")
		case tracker.NumberValue:
			_, numberField := field.Constraint.(tracker.NumberConstraint)
			valid = numberField && !math.IsNaN(float64(typed)) && !math.IsInf(float64(typed), 0)
		case tracker.IDsValue:
			idsConstraint, idsField := field.Constraint.(tracker.IDsConstraint)
			valid = idsField && (!field.Required || len(typed) > 0)
			for _, selected := range typed {
				if selected == "" {
					valid = false
				}
				switch constraint := idsConstraint.Choices.(type) {
				case tracker.AnyID:
				case tracker.AllowedIDs:
					valid = valid && slices.Contains(constraint.Values, selected)
				default:
					valid = false
				}
			}
		}
		if !valid {
			return fail(ports.FailureInvalidRequest, "field value violates its discovered type or allowed values: "+id)
		}
	}
	return nil
}

// ValidateReceipt prevents an unrelated observation from completing an operation.
// Adapters must additionally verify read-back content against the desired effect.
func ValidateReceipt(intent tracker.Intent, receipt tracker.Receipt) error {
	if err := ValidatePin(intent.Pin, receipt.Pin); err != nil {
		return err
	}
	if receipt.OperationID != intent.OperationID || receipt.DesiredDigest != intent.DesiredDigest || receipt.Destination != intent.Destination || receipt.Target.Namespace != intent.Destination.Namespace || receipt.Target.ID == "" || receipt.ObservedRevision == "" || receipt.ObservedAt.IsZero() || receipt.EvidenceRef == "" {
		return fail(ports.FailureConflict, "receipt does not prove this exact operation and destination")
	}
	var target tracker.TicketRef
	switch effect := intent.Effect.(type) {
	case tracker.CreateTicket:
		return nil
	case tracker.EditTicket:
		target = effect.Target.Ref
	case tracker.ReportProgress:
		target = effect.Target.Ref
	case tracker.TakeTransition:
		target = effect.Target.Ref
	case tracker.LinkStories:
		target = effect.Target.Ref
	default:
		return fail(ports.FailureUnsupported, "unknown receipt effect")
	}
	if receipt.Target != target {
		return fail(ports.FailureConflict, "receipt references another ticket")
	}
	return nil
}

// RetryAfterReconciliation never permits a blind retry after an uncertain write.
// A true result still requires daemon policy, current authorization and preflight.
func RetryAfterReconciliation(intent tracker.Intent, result tracker.EffectResult) bool {
	absent, ok := result.(tracker.NotApplied)
	return ok && absent.EvidenceRef != "" && absent.OperationID == intent.OperationID && absent.DesiredDigest == intent.DesiredDigest && absent.Pin == intent.Pin && absent.Destination == intent.Destination && intent.OperationID != "" && intent.DesiredDigest != "" && ValidatePin(intent.Pin, absent.Pin) == nil
}
