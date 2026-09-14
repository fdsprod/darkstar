package trackerrules

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"slices"
	"time"

	"darkstar/src/core/trackercontract"
	"darkstar/src/ports"
	"darkstar/src/ports/tracker"
)

func Group(mapping DisplayMapping, state tracker.Knowledge[tracker.NamedID]) GroupResult {
	result := GroupResult{GroupID: mapping.UnknownGroup.ID, GroupName: mapping.UnknownGroup.Name, Unmapped: true}
	switch observed := state.(type) {
	case tracker.Known[tracker.NamedID]:
		result.StateID = observed.Value.ID
		result.StateName = observed.Value.Name
		result.Reason = "unmapped_state"
		for _, group := range mapping.Groups {
			if slices.Contains(group.StateIDs, observed.Value.ID) {
				result.GroupID = group.ID
				result.GroupName = group.Name
				result.Unmapped = false
				result.Reason = ""
				return result
			}
		}
	case tracker.Unknown[tracker.NamedID]:
		result.Reason = "unknown: " + observed.Reason
	case tracker.Unsupported[tracker.NamedID]:
		result.Reason = "unsupported: " + observed.Reason
	default:
		result.Reason = "state_not_observed"
	}
	return result
}

func validateObservation(scope RuleScope, observation Observation) error {
	if observation.ProjectID != scope.ProjectID || observation.BindingRevision != scope.BindingRevision {
		return fail(ports.FailureConflict, "observation belongs to another project or source binding revision")
	}
	if err := trackercontract.ValidatePin(scope.Pin, observation.Pin); err != nil {
		return err
	}
	if observation.Ticket.Ref.Namespace != scope.Source.Namespace || !validID(observation.Ticket.Ref.ID) {
		return fail(ports.FailureConflict, "observation ticket identity differs from source namespace")
	}
	placement, known := observation.Ticket.Placement.(tracker.Known[tracker.Scope])
	if !known || placement.Value != scope.Source {
		return fail(ports.FailureConflict, "ticket placement is unknown or moved outside rule scope")
	}
	fresh, known := observation.Ticket.Freshness.(tracker.Fresh)
	if !known || fresh.ObservedAt.IsZero() || fresh.Revision != observation.Ticket.Revision || !validID(fresh.Revision) || !validID(observation.Ticket.EvidenceRef) {
		return fail(ports.FailureConflict, "rules require a fresh revision-bound ticket observation with evidence")
	}
	return nil
}

func singleValues(knowledge tracker.Knowledge[tracker.NamedID]) ([]string, error) {
	switch value := knowledge.(type) {
	case tracker.Known[tracker.NamedID]:
		return []string{value.Value.ID}, nil
	case tracker.Unsupported[tracker.NamedID]:
		return nil, fail(ports.FailureUnsupported, "predicate observation is unsupported")
	default:
		return nil, fail(ports.FailureConflict, "predicate observation is unknown")
	}
}

func listValues(knowledge tracker.Knowledge[[]tracker.NamedID]) ([]string, error) {
	switch value := knowledge.(type) {
	case tracker.Known[[]tracker.NamedID]:
		ids := make([]string, 0, len(value.Value))
		for _, item := range value.Value {
			ids = append(ids, item.ID)
		}
		return ids, nil
	case tracker.Unsupported[[]tracker.NamedID]:
		return nil, fail(ports.FailureUnsupported, "predicate list observation is unsupported")
	default:
		return nil, fail(ports.FailureConflict, "predicate list observation is unknown")
	}
}

func fieldValues(observation Observation, field string) ([]string, error) {
	switch field {
	case "state":
		return singleValues(observation.Ticket.BusinessState)
	case "state_reason":
		return singleValues(observation.Ticket.BusinessStateReason)
	case "issue_type":
		return singleValues(observation.Ticket.IssueType)
	case "priority":
		return singleValues(observation.Ticket.Priority)
	case "assignee":
		return listValues(observation.Ticket.Assignees)
	case "label":
		return listValues(observation.Ticket.Labels)
	case "workflow":
		return singleValues(observation.WorkflowID)
	default:
		return nil, fail(ports.FailureUnsupported, "unknown predicate field")
	}
}

func matches(conditions Conditions, observation Observation) (bool, error) {
	for _, predicate := range conditions.Fields {
		values, err := fieldValues(observation, predicate.FieldID)
		if err != nil {
			return false, err
		}
		if !intersects(predicate.Values, values) {
			return false, nil
		}
	}
	if len(conditions.SprintIDs) != 0 {
		values, err := listValues(observation.Ticket.Sprint)
		if err != nil {
			return false, err
		}
		if !intersects(conditions.SprintIDs, values) {
			return false, nil
		}
	}
	return true, nil
}

func PreviewIntake(rules RuleSet, discovery Discovery, observation Observation) (IntakePreview, error) {
	preview := IntakePreview{RuleSetID: rules.ID, RuleRevision: rules.Revision, Group: Group(rules.Display, observation.Ticket.BusinessState)}
	if err := Validate(rules, discovery); err != nil {
		return preview, err
	}
	if err := validateObservation(rules.Scope, observation); err != nil {
		return preview, err
	}
	if archived, known := observation.Ticket.Archived.(tracker.Known[bool]); known && archived.Value {
		return preview, nil
	}
	for _, rule := range rules.Intake {
		matched, err := matches(rule.When, observation)
		if err != nil {
			return preview, err
		}
		if !matched {
			continue
		}
		if preview.Matched {
			return IntakePreview{}, fail(ports.FailureConflict, "multiple intake rules match this observation")
		}
		preview.Matched = true
		preview.RuleID = rule.ID
		preview.Action = rule.Action
	}
	return preview, nil
}

func digest(prefix string, value any) string {
	encoded, _ := json.Marshal(value)
	hash := sha256.Sum256(encoded)
	return prefix + hex.EncodeToString(hash[:])
}

func AdmissionIdentity(projectID string, ref tracker.TicketRef) string {
	return digest("tracker_admission_", struct {
		ProjectID string
		Ref       tracker.TicketRef
	}{ProjectID: projectID, Ref: ref})
}

// EvaluateAdmission is pure. The daemon must persist the returned cursor and
// decision atomically, and retain the selected rule bytes with admitted work.
// Manual decisions are proposals; neither mode bypasses readiness or approvals.
func EvaluateAdmission(rules RuleSet, discovery Discovery, event Event, cursor Cursor, previousSettled bool, ownedOperationIDs []string) (AdmissionDecision, error) {
	return evaluateAdmission(rules, discovery, event, cursor, previousSettled, ownedOperationIDs, false)
}

// ConfirmManualAdmission is called only after the daemon has verified the
// explicit human admission command. Persist its cursor with that admission in
// one transaction; merely presenting a manual proposal never consumes an episode.
func ConfirmManualAdmission(rules RuleSet, discovery Discovery, event Event, cursor Cursor, previousSettled bool, ownedOperationIDs []string) (AdmissionDecision, error) {
	return evaluateAdmission(rules, discovery, event, cursor, previousSettled, ownedOperationIDs, true)
}

func evaluateAdmission(rules RuleSet, discovery Discovery, event Event, cursor Cursor, previousSettled bool, ownedOperationIDs []string, confirmManual bool) (AdmissionDecision, error) {
	preview, err := PreviewIntake(rules, discovery, event.Observation)
	if err != nil {
		return AdmissionDecision{}, err
	}
	identity := AdmissionIdentity(event.Observation.ProjectID, event.Observation.Ticket.Ref)
	if !validID(event.ID) || (cursor.Identity != "" && cursor.Identity != identity) || len(cursor.SeenEventIDs) != len(cursor.SeenRevisions) || !uniqueIDs(cursor.SeenEventIDs) {
		return AdmissionDecision{}, fail(ports.FailureInvalidRequest, "event and admission cursor must identify the exact project and stable ticket")
	}
	cursor.Identity = identity
	decision := AdmissionDecision{Preview: preview, Cursor: cursor}
	if index := slices.Index(cursor.SeenEventIDs, event.ID); index >= 0 {
		if cursor.SeenRevisions[index] != event.Observation.Ticket.Revision {
			return AdmissionDecision{}, fail(ports.FailureConflict, "event identity was reused with another ticket revision")
		}
		decision.State = "duplicate"
		return decision, nil
	}
	if slices.Contains(cursor.SeenRevisions, event.Observation.Ticket.Revision) {
		decision.State = "duplicate"
		return decision, nil
	}
	fresh := event.Observation.Ticket.Freshness.(tracker.Fresh)
	if !cursor.LastObservedAt.IsZero() && !fresh.ObservedAt.After(cursor.LastObservedAt) {
		decision.State = "out_of_order"
		return decision, nil
	}
	if len(cursor.SeenEventIDs) >= 4096 {
		return AdmissionDecision{}, fail(ports.FailureResourceExhausted, "admission observation window is full; explicit cursor archival is required")
	}
	if event.OriginOperationID != "" && !slices.Contains(ownedOperationIDs, event.OriginOperationID) {
		return AdmissionDecision{}, fail(ports.FailureConflict, "outbound echo does not reference a retained owned operation")
	}
	admit, eligible := preview.Action.(Admit)
	if confirmManual && (!eligible || admit.Mode != Manual) {
		return AdmissionDecision{}, fail(ports.FailureConflict, "manual confirmation requires a matching manual admission rule")
	}
	if eligible && !cursor.Eligible && !previousSettled {
		decision.State = "previous_execution_active"
		return decision, nil
	}
	if eligible && !cursor.Eligible && admit.Mode == Manual && !confirmManual && event.OriginOperationID == "" {
		if cursor.Admissions >= admit.Repair.MaxAdmissions {
			decision.State = "repair_limit"
			return decision, nil
		}
		decision.State = "manual"
		decision.AdmissionID = episodeIdentity(identity, cursor.Admissions+1)
		return decision, nil
	}
	cursor.SeenEventIDs = append(slices.Clone(cursor.SeenEventIDs), event.ID)
	cursor.SeenRevisions = append(slices.Clone(cursor.SeenRevisions), event.Observation.Ticket.Revision)
	cursor.LastObservedAt = fresh.ObservedAt
	if event.OriginOperationID != "" {
		decision.State = "outbound_echo"
		decision.Cursor = cursor
		return decision, nil
	}
	switch {
	case !eligible:
		decision.State = "ineligible"
	case cursor.Eligible:
		decision.State = "already_eligible"
	case cursor.Admissions >= admit.Repair.MaxAdmissions:
		decision.State = "repair_limit"
	default:
		cursor.Admissions++
		decision.State = string(admit.Mode)
		decision.AdmissionID = episodeIdentity(identity, cursor.Admissions)
		if confirmManual {
			decision.State = "manual_confirmed"
		}
	}
	cursor.Eligible = eligible
	decision.Cursor = cursor
	return decision, nil
}

func episodeIdentity(identity string, episode uint32) string {
	return digest("tracker_episode_", struct {
		Identity string
		Episode  uint32
	}{Identity: identity, Episode: episode})
}

// PreviewOutbound verifies a declared milestone and observed writer preflight.
// It returns a desired effect only; durable authorization and dispatch remain
// with the daemon and its operation journal.
func PreviewOutbound(ctx context.Context, rules RuleSet, discovery Discovery, observation Observation, milestone MilestoneObservation, options tracker.WriteOptions, now time.Time, validator EvidenceValidator) (OutboundPreview, error) {
	preview := OutboundPreview{RuleSetID: rules.ID, RuleRevision: rules.Revision}
	if err := Validate(rules, discovery); err != nil {
		return preview, err
	}
	if err := validateObservation(rules.Scope, observation); err != nil {
		return preview, err
	}
	if !validID(milestone.EventID) || !validID(milestone.RunID) || !validWorkflow(milestone.Workflow) {
		return preview, fail(ports.FailureInvalidRequest, "milestone requires an exact event, run and workflow pin")
	}
	for _, rule := range rules.Outbound {
		if rule.Milestone.ID != milestone.MilestoneID || rule.Milestone.Workflow != milestone.Workflow {
			continue
		}
		matched, err := matches(rule.When, observation)
		if err != nil {
			return preview, err
		}
		if !matched {
			continue
		}
		if preview.Matched {
			return OutboundPreview{}, fail(ports.FailureConflict, "multiple outbound rules match")
		}
		if err := validateEvidence(ctx, rule.Milestone, milestone.Evidence, validator); err != nil {
			return preview, err
		}
		preview.Matched = true
		preview.RuleID = rule.ID
		if _, noop := rule.Action.(Noop); noop {
			continue
		}
		target, known := options.Scope.(tracker.TicketScope)
		state, stateKnown := observation.Ticket.BusinessState.(tracker.Known[tracker.NamedID])
		if !known || !stateKnown || target.Ref != observation.Ticket.Ref || target.Revision != observation.Ticket.Revision || target.StateID != state.Value.ID {
			return preview, fail(ports.FailureConflict, "writer options differ from current ticket observation")
		}
		if observedType, ok := observation.Ticket.IssueType.(tracker.Known[tracker.NamedID]); ok && target.IssueTypeID != observedType.Value.ID {
			return preview, fail(ports.FailureConflict, "writer issue type differs from observation")
		}
		if !sameSprint(target.Sprint, observation.Ticket.Sprint) {
			return preview, fail(ports.FailureConflict, "writer sprint differs from observation")
		}
		switch action := rule.Action.(type) {
		case Report:
			artifacts := make([]tracker.ArtifactRef, 0, len(milestone.Evidence))
			for _, evidence := range milestone.Evidence {
				artifacts = append(artifacts, evidence.Artifact)
			}
			preview.Effect = tracker.ReportProgress{Target: target, Evidence: tracker.GeneralProgress{Artifacts: artifacts}, Body: action.Body}
		case Transition:
			preview.Effect = tracker.TakeTransition{Target: target, TransitionID: action.TransitionID, Fields: action.Fields}
		default:
			return preview, fail(ports.FailureUnsupported, "unsupported outbound action")
		}
		preview.OperationID = digest("tracker_milestone_", struct {
			ProjectID string
			RuleSetID string
			Revision  uint64
			RuleID    string
			RunID     string
			EventID   string
			Ref       tracker.TicketRef
		}{rules.Scope.ProjectID, rules.ID, rules.Revision, rule.ID, milestone.RunID, milestone.EventID, observation.Ticket.Ref})
		intent := tracker.Intent{OperationID: preview.OperationID, DesiredDigest: digest("", preview.Effect), AuthorizationRef: "preview:" + preview.OperationID, Pin: rules.Scope.Pin, Destination: rules.Scope.Source, Effect: preview.Effect}
		if err := trackercontract.ValidateIntent(intent, discovery.Manifest, options, now); err != nil {
			return OutboundPreview{}, err
		}
	}
	return preview, nil
}

func sameSprint(left, right tracker.Knowledge[[]tracker.NamedID]) bool {
	leftIDs, leftErr := listValues(left)
	rightIDs, rightErr := listValues(right)
	if leftErr == nil && rightErr == nil {
		slices.Sort(leftIDs)
		slices.Sort(rightIDs)
		return slices.Equal(leftIDs, rightIDs)
	}
	_, leftUnsupported := left.(tracker.Unsupported[[]tracker.NamedID])
	_, rightUnsupported := right.(tracker.Unsupported[[]tracker.NamedID])
	return leftUnsupported && rightUnsupported
}

func validateEvidence(ctx context.Context, contract MilestoneContract, evidence []Evidence, validator EvidenceValidator) error {
	if validator == nil || len(evidence) == 0 {
		return fail(ports.FailureInvalidRequest, "daemon evidence validation is required")
	}
	seen := make(map[string]bool)
	for _, item := range evidence {
		hash, err := hex.DecodeString(item.Artifact.SHA256)
		if !slices.Contains(contract.EvidenceTypes, item.Type) || seen[item.Type] || !validID(item.Authority) || !validID(item.Revision) || !validID(item.Artifact.ArtifactID) || item.Artifact.Version == 0 || err != nil || len(hash) != 32 {
			return fail(ports.FailureInvalidRequest, "milestone evidence must exactly satisfy declared types with retained artifact bytes and authority")
		}
		if err := validator.ValidateMilestoneEvidence(ctx, contract, item); err != nil {
			return err
		}
		seen[item.Type] = true
	}
	for _, required := range contract.EvidenceTypes {
		if !seen[required] {
			return fail(ports.FailureInvalidRequest, "milestone evidence contract is incomplete")
		}
	}
	return nil
}
