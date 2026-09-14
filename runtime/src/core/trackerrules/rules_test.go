package trackerrules

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"darkstar/src/ports/tracker"
)

func ruleFixture(provider string) (RuleSet, Discovery, Observation) {
	namespace := tracker.Namespace{Provider: provider, Host: "fixture.local", TenantID: "tenant-1", ScopeID: "namespace-1"}
	source := tracker.Scope{Namespace: namespace, ContainerID: "team-1"}
	pin := tracker.Pin{AdapterConfigPin: tracker.AdapterConfigPin{ContractVersion: tracker.Version, AdapterID: provider, AdapterVersion: "1", InstallationID: "installation-1", AccountID: "account-1", BindingRevision: "1", ConfigRevision: "1", ConfigDigest: strings.Repeat("a", 64)}, CapabilitiesDigest: strings.Repeat("b", 64)}
	workflow := WorkflowPin{ID: "implementation", Version: "1.0.0", Digest: strings.Repeat("c", 64)}
	rules := RuleSet{
		Version:  Version,
		ID:       "implementation-intake",
		Revision: 1,
		Scope:    RuleScope{ProjectID: "project-1", BindingRevision: 1, Pin: pin, Source: source},
		Intake:   []IntakeRule{{ID: "ready-for-dev", When: Conditions{Fields: []Predicate{{FieldID: "state", Values: []string{"ready"}}}}, Action: Admit{Workflow: workflow, ReadinessPolicy: "require_approval", Mode: Automatic, Repair: RepairPolicy{MaxAdmissions: 2}}}},
		Display:  DisplayMapping{Groups: []DisplayGroup{{ID: "todo", Name: "To do", StateIDs: []string{"backlog", "ready"}}}, UnknownGroup: DisplayGroup{ID: "unmapped", Name: "Unmapped"}},
	}
	now := time.Date(2026, 9, 13, 12, 0, 0, 0, time.UTC)
	discovery := Discovery{
		Manifest:          tracker.Manifest{Pin: pin, Scope: source, Capabilities: map[tracker.Capability]tracker.Knowledge[bool]{tracker.Fetch: tracker.Known[bool]{Value: true}, tracker.Transitions: tracker.Known[bool]{Value: true}, tracker.Progress: tracker.Known[bool]{Value: true}, tracker.Reconciliation: tracker.Known[bool]{Value: true}}, ObservedAt: now, EvidenceRef: "provider-discovery"},
		Fields:            []FieldDefinition{{ID: "state", Values: []tracker.NamedID{{ID: "backlog", Name: "Backlog"}, {ID: "ready", Name: "Ready for Dev"}, {ID: "testing", Name: "Ready for Test"}}}, {ID: "issue_type", Values: []tracker.NamedID{{ID: "story", Name: "Story"}}}, {ID: "workflow", Values: []tracker.NamedID{{ID: "story-workflow", Name: "Story Workflow"}}}},
		Workflows:         []WorkflowPin{workflow},
		ReadinessPolicies: []string{"require_approval"},
		Transitions:       []tracker.Transition{{Identity: tracker.NamedID{ID: "to-testing", Name: "Ready for Test"}, ToState: tracker.NamedID{ID: "testing"}}},
	}
	observation := Observation{ProjectID: rules.Scope.ProjectID, BindingRevision: 1, Pin: pin, WorkflowID: tracker.Known[tracker.NamedID]{Value: tracker.NamedID{ID: "story-workflow"}}, Ticket: tracker.Ticket{
		Ref:      tracker.TicketRef{Namespace: namespace, ID: "immutable-ticket-1"},
		Revision: "revision-1", Key: "TICKET-1", Title: "Fixture ticket",
		BusinessState: tracker.Known[tracker.NamedID]{Value: tracker.NamedID{ID: "ready", Name: "Ready for Dev"}},
		IssueType:     tracker.Known[tracker.NamedID]{Value: tracker.NamedID{ID: "story"}},
		Placement:     tracker.Known[tracker.Scope]{Value: source},
		Archived:      tracker.Known[bool]{Value: false},
		Sprint:        tracker.Unsupported[[]tracker.NamedID]{Reason: "sprints unavailable"},
		Freshness:     tracker.Fresh{ObservedAt: now, Revision: "revision-1"}, EvidenceRef: "retained-observation-1",
	}}
	if provider == "jira_fixture" || provider == "linear" {
		discovery.Sprints = []tracker.NamedID{{ID: "sprint-1", Name: "Current sprint"}}
		observation.Ticket.Sprint = tracker.Known[[]tracker.NamedID]{Value: discovery.Sprints}
	}
	return rules, discovery, observation
}

func nextObservation(observation Observation, revision, state string, elapsed time.Duration) Observation {
	observation.Ticket.Revision = revision
	observation.Ticket.BusinessState = tracker.Known[tracker.NamedID]{Value: tracker.NamedID{ID: state, Name: state}}
	previous := observation.Ticket.Freshness.(tracker.Fresh)
	observation.Ticket.Freshness = tracker.Fresh{ObservedAt: previous.ObservedAt.Add(elapsed), Revision: revision}
	return observation
}

func TestProviderRulesUseStableIDsAndIndependentSprint(t *testing.T) {
	for _, provider := range []string{"built_in", "linear", "github_issues", "jira_fixture"} {
		t.Run(provider, func(t *testing.T) {
			rules, discovery, observation := ruleFixture(provider)
			preview, err := PreviewIntake(rules, discovery, observation)
			if err != nil || !preview.Matched {
				t.Fatalf("expected eligibility without a universal sprint requirement: %#v, %v", preview, err)
			}
			observation.Ticket.BusinessState = tracker.Known[tracker.NamedID]{Value: tracker.NamedID{ID: "ready", Name: "Renamed by team"}}
			preview, err = PreviewIntake(rules, discovery, observation)
			if err != nil || !preview.Matched || preview.Group.StateName != "Renamed by team" {
				t.Fatalf("stable state identity must survive rename: %#v, %v", preview, err)
			}
			rules.Intake[0].When.SprintIDs = []string{"sprint-1"}
			preview, err = PreviewIntake(rules, discovery, observation)
			if provider == "github_issues" || provider == "built_in" {
				if err == nil {
					t.Fatal("unsupported sprint condition was silently accepted")
				}
				return
			}
			if err != nil || !preview.Matched {
				t.Fatalf("discovered sprint should be eligible: %#v, %v", preview, err)
			}
			observation.Ticket.Sprint = tracker.Known[[]tracker.NamedID]{Value: []tracker.NamedID{}}
			preview, err = PreviewIntake(rules, discovery, observation)
			if err != nil || preview.Matched {
				t.Fatalf("observed no sprint must fail separate sprint condition: %#v, %v", preview, err)
			}
		})
	}
}

func TestRejectsUnsupportedAmbiguousAndDriftedRules(t *testing.T) {
	cases := []struct {
		name   string
		change func(*RuleSet, *Discovery, *Observation)
	}{
		{name: "display name is not identity", change: func(r *RuleSet, d *Discovery, o *Observation) {
			r.Intake[0].When.Fields[0].Values = []string{"Ready for Dev"}
		}},
		{name: "ambiguous catchall", change: func(r *RuleSet, d *Discovery, o *Observation) {
			r.Intake = append(r.Intake, IntakeRule{ID: "catch-all", Action: Noop{}})
		}},
		{name: "workflow bytes changed", change: func(r *RuleSet, d *Discovery, o *Observation) {
			d.Workflows[0].Digest = strings.Repeat("d", 64)
		}},
		{name: "policy unavailable", change: func(r *RuleSet, d *Discovery, o *Observation) {
			d.ReadinessPolicies = nil
		}},
		{name: "wrong project", change: func(r *RuleSet, d *Discovery, o *Observation) {
			o.ProjectID = "other-project"
		}},
		{name: "binding changed", change: func(r *RuleSet, d *Discovery, o *Observation) {
			o.BindingRevision = 2
		}},
		{name: "ticket moved", change: func(r *RuleSet, d *Discovery, o *Observation) {
			scope := r.Scope.Source
			scope.ContainerID = "other-team"
			o.Ticket.Placement = tracker.Known[tracker.Scope]{Value: scope}
		}},
		{name: "stale source", change: func(r *RuleSet, d *Discovery, o *Observation) {
			o.Ticket.Freshness = tracker.Stale{Reason: "refresh failed"}
		}},
		{name: "unknown state", change: func(r *RuleSet, d *Discovery, o *Observation) {
			o.Ticket.BusinessState = tracker.Unknown[tracker.NamedID]{Reason: "provider omitted state"}
		}},
		{name: "unsupported condition", change: func(r *RuleSet, d *Discovery, o *Observation) {
			r.Intake[0].When.Fields[0].FieldID = "execute_expression"
		}},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			rules, discovery, observation := ruleFixture("linear")
			test.change(&rules, &discovery, &observation)
			if _, err := PreviewIntake(rules, discovery, observation); err == nil {
				t.Fatal("invalid rule or source observation was accepted")
			}
		})
	}
}

func TestDisplayIsManyToOneWithoutReverseExecutionMeaning(t *testing.T) {
	rules, _, _ := ruleFixture("jira_fixture")
	for _, state := range []string{"ready", "backlog"} {
		group := Group(rules.Display, tracker.Known[tracker.NamedID]{Value: tracker.NamedID{ID: state, Name: state}})
		if group.GroupID != "todo" || group.StateID != state || group.Unmapped {
			t.Fatalf("group must retain distinct business-state identity: %#v", group)
		}
	}
	group := Group(rules.Display, tracker.Known[tracker.NamedID]{Value: tracker.NamedID{ID: "external-done", Name: "Externally Accepted"}})
	if !group.Unmapped || group.GroupID != "unmapped" || group.StateName != "Externally Accepted" {
		t.Fatalf("unmapped business state must remain visible: %#v", group)
	}
	rules.Display.Groups = append(rules.Display.Groups, DisplayGroup{ID: "also-todo", Name: "Duplicate", StateIDs: []string{"ready"}})
	if err := ValidateDisplay(rules.Display); err == nil {
		t.Fatal("ambiguous display mapping was accepted")
	}
}

func TestStrictCodecRejectsUnknownAndContradictoryVariants(t *testing.T) {
	rules, _, _ := ruleFixture("linear")
	encoded, err := Encode(rules)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := Decode(encoded)
	if err != nil || !reflect.DeepEqual(decoded, rules) {
		t.Fatalf("rule roundtrip failed: %v", err)
	}
	bad := []string{
		strings.Replace(string(encoded), `"kind":"admit"`, `"kind":"run"`, 1),
		strings.Replace(string(encoded), `"kind":"admit"`, `"kind":"admit","body":"mixed sibling"`, 1),
		strings.Replace(string(encoded), `"kind":"admit"`, `"kind":"admit","kind":"noop"`, 1),
		strings.Replace(string(encoded), Version, "darkstar.tracker-rules/v99", 1),
		string(encoded) + `{}`,
	}
	for _, value := range bad {
		if _, err := Decode([]byte(value)); err == nil {
			t.Errorf("invalid closed rule encoding accepted: %s", value)
		}
	}
}

func TestManualProposalDoesNotConsumeAdmissionAndConfirmationIsBounded(t *testing.T) {
	rules, discovery, observation := ruleFixture("built_in")
	action := rules.Intake[0].Action.(Admit)
	action.Mode = Manual
	action.Repair.MaxAdmissions = 1
	rules.Intake[0].Action = action
	event := Event{ID: "event-1", Observation: observation}
	proposal, err := EvaluateAdmission(rules, discovery, event, Cursor{}, true, nil)
	if err != nil || proposal.State != "manual" || proposal.Cursor.Admissions != 0 || proposal.Cursor.Eligible || len(proposal.Cursor.SeenEventIDs) != 0 {
		t.Fatalf("manual proposal consumed an episode: %#v, %v", proposal, err)
	}
	repeated, err := EvaluateAdmission(rules, discovery, event, proposal.Cursor, true, nil)
	if err != nil || repeated.AdmissionID != proposal.AdmissionID || repeated.State != "manual" {
		t.Fatalf("manual proposal identity is not stable: %#v, %v", repeated, err)
	}
	confirmed, err := ConfirmManualAdmission(rules, discovery, event, proposal.Cursor, true, nil)
	if err != nil || confirmed.State != "manual_confirmed" || confirmed.Cursor.Admissions != 1 || !confirmed.Cursor.Eligible || confirmed.AdmissionID != proposal.AdmissionID {
		t.Fatalf("manual approval failed: %#v, %v", confirmed, err)
	}
	duplicate, err := ConfirmManualAdmission(rules, discovery, event, confirmed.Cursor, true, nil)
	if err != nil || duplicate.State != "duplicate" || duplicate.Cursor.Admissions != 1 {
		t.Fatalf("duplicate confirmation consumed an episode: %#v, %v", duplicate, err)
	}
	backlog := nextObservation(observation, "revision-2", "backlog", time.Minute)
	left, err := EvaluateAdmission(rules, discovery, Event{ID: "event-2", Observation: backlog}, confirmed.Cursor, true, nil)
	if err != nil || left.Cursor.Eligible {
		t.Fatalf("backward source move was not observed: %#v, %v", left, err)
	}
	reopened := nextObservation(backlog, "revision-3", "ready", time.Minute)
	limited, err := ConfirmManualAdmission(rules, discovery, Event{ID: "event-3", Observation: reopened}, left.Cursor, true, nil)
	if err != nil || limited.State != "repair_limit" || limited.Cursor.Admissions != 1 {
		t.Fatalf("manual reopen bypassed repair bound: %#v, %v", limited, err)
	}
}

func TestAdmissionRepeatReopenEchoAndInitialActiveExecution(t *testing.T) {
	rules, discovery, observation := ruleFixture("linear")
	event := Event{ID: "event-1", Observation: observation}
	blocked, err := EvaluateAdmission(rules, discovery, event, Cursor{}, false, nil)
	if err != nil || blocked.State != "previous_execution_active" || blocked.Cursor.Admissions != 0 {
		t.Fatalf("initial cursor bypassed active execution: %#v, %v", blocked, err)
	}
	first, err := EvaluateAdmission(rules, discovery, event, Cursor{}, true, nil)
	if err != nil || first.State != "automatic" || first.Cursor.Admissions != 1 {
		t.Fatalf("initial admission failed: %#v, %v", first, err)
	}
	duplicate, err := EvaluateAdmission(rules, discovery, Event{ID: "poll-event", Observation: observation}, first.Cursor, true, nil)
	if err != nil || duplicate.State != "duplicate" {
		t.Fatalf("same revision from poll created duplicate intake: %#v, %v", duplicate, err)
	}
	echoObservation := nextObservation(observation, "revision-2", "backlog", time.Minute)
	echo, err := EvaluateAdmission(rules, discovery, Event{ID: "echo", Observation: echoObservation, OriginOperationID: "owned-operation"}, first.Cursor, true, []string{"owned-operation"})
	if err != nil || echo.State != "outbound_echo" || !echo.Cursor.Eligible || echo.Cursor.Admissions != 1 {
		t.Fatalf("outbound echo manufactured reopen: %#v, %v", echo, err)
	}
	if _, err := EvaluateAdmission(rules, discovery, Event{ID: "forged-echo", Observation: echoObservation, OriginOperationID: "unowned"}, first.Cursor, true, nil); err == nil {
		t.Fatal("unproven outbound echo was accepted")
	}
	backlog := nextObservation(echoObservation, "revision-3", "backlog", time.Minute)
	left, err := EvaluateAdmission(rules, discovery, Event{ID: "left", Observation: backlog}, echo.Cursor, true, nil)
	if err != nil || left.Cursor.Eligible {
		t.Fatalf("backward state move failed: %#v, %v", left, err)
	}
	reopened := nextObservation(backlog, "revision-4", "ready", time.Minute)
	second, err := EvaluateAdmission(rules, discovery, Event{ID: "reopened", Observation: reopened}, left.Cursor, true, nil)
	if err != nil || second.State != "automatic" || second.Cursor.Admissions != 2 || second.AdmissionID == first.AdmissionID {
		t.Fatalf("bounded repair failed: %#v, %v", second, err)
	}
	old, err := EvaluateAdmission(rules, discovery, Event{ID: "out-of-order", Observation: nextObservation(observation, "old-revision", "backlog", time.Second)}, second.Cursor, true, nil)
	if err != nil || old.State != "out_of_order" || !old.Cursor.Eligible {
		t.Fatalf("out-of-order observation reset eligibility: %#v, %v", old, err)
	}
	changed := observation
	changed.Ticket.Ref.ID = "other-ticket"
	if _, err := EvaluateAdmission(rules, discovery, Event{ID: "wrong-ticket", Observation: changed}, second.Cursor, true, nil); err == nil {
		t.Fatal("cursor was reused across stable ticket identities")
	}
	if AdmissionIdentity(observation.ProjectID, observation.Ticket.Ref) != first.Cursor.Identity {
		t.Fatal("admission identity changed unexpectedly")
	}
}

type fixtureEvidenceValidator struct {
	err   error
	calls int
}

func (validator *fixtureEvidenceValidator) ValidateMilestoneEvidence(ctx context.Context, contract MilestoneContract, evidence Evidence) error {
	validator.calls++
	return validator.err
}

func milestoneFixture() (RuleSet, Discovery, Observation, MilestoneObservation, tracker.WriteOptions, time.Time) {
	rules, discovery, observation := ruleFixture("jira_fixture")
	workflow := rules.Intake[0].Action.(Admit).Workflow
	contract := MilestoneContract{ID: "implementation.validated_handoff", Workflow: workflow, EvidenceTypes: []string{"validated_implementation", "approved_pr"}}
	discovery.Milestones = []MilestoneContract{contract}
	discovery.Transitions[0].Fields = []tracker.Field{{Identity: tracker.NamedID{ID: "resolution"}, Required: true, Constraint: tracker.IDsConstraint{Choices: tracker.AllowedIDs{Values: []string{"implemented"}}}}}
	discovery.Transitions[0].Guards = []tracker.Guard{{Identity: tracker.NamedID{ID: "in-selected-sprint"}, Satisfied: tracker.Known[bool]{Value: true}, EvidenceRef: "guard-evidence"}}
	rules.Outbound = []OutboundRule{{ID: "handoff-to-test", Milestone: contract, Action: Transition{TransitionID: "to-testing", Fields: map[string]tracker.FieldValue{"resolution": tracker.IDsValue{"implemented"}}}}}
	stamp := observation.Ticket.Freshness.(tracker.Fresh).ObservedAt
	options := tracker.WriteOptions{Pin: rules.Scope.Pin, Scope: tracker.TicketScope{Ref: observation.Ticket.Ref, Revision: observation.Ticket.Revision, WorkflowRevision: "story-workflow-r1", IssueTypeID: "story", StateID: "ready", Sprint: observation.Ticket.Sprint}, Transitions: tracker.Known[[]tracker.Transition]{Value: discovery.Transitions}, ObservedAt: stamp, ValidUntil: stamp.Add(time.Hour), EvidenceRef: "inspected-options"}
	milestone := MilestoneObservation{EventID: "milestone-event-1", RunID: "run-1", Workflow: workflow, MilestoneID: contract.ID, Evidence: []Evidence{{Type: "validated_implementation", Authority: "daemon-validator", Revision: "validation-1", Artifact: tracker.ArtifactRef{ArtifactID: "validation", Version: 1, SHA256: strings.Repeat("d", 64)}}, {Type: "approved_pr", Authority: "github-pr-review", Revision: "review-1", Artifact: tracker.ArtifactRef{ArtifactID: "pr-review", Version: 1, SHA256: strings.Repeat("e", 64)}}}}
	return rules, discovery, observation, milestone, options, stamp.Add(time.Minute)
}

func TestJiraTypedMilestoneRequiresRealEvidenceAndSupportedTransition(t *testing.T) {
	rules, discovery, observation, milestone, options, now := milestoneFixture()
	validator := &fixtureEvidenceValidator{}
	preview, err := PreviewOutbound(context.Background(), rules, discovery, observation, milestone, options, now, validator)
	if err != nil || !preview.Matched || validator.calls != 2 {
		t.Fatalf("validated handoff failed: %#v, %v", preview, err)
	}
	effect, ok := preview.Effect.(tracker.TakeTransition)
	if !ok || effect.TransitionID != "to-testing" || effect.Target.Ref != observation.Ticket.Ref {
		t.Fatalf("expected exact configured operation: %#v", preview.Effect)
	}
	repeated, err := PreviewOutbound(context.Background(), rules, discovery, observation, milestone, options, now, validator)
	if err != nil || repeated.OperationID != preview.OperationID {
		t.Fatalf("milestone operation identity must be stable: %#v, %v", repeated, err)
	}
	encoded, err := Encode(rules)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := Decode(encoded)
	if err != nil || !reflect.DeepEqual(decoded, rules) {
		t.Fatalf("typed transition field codec failed: %v", err)
	}
	badCases := []struct {
		name   string
		change func(*RuleSet, *Discovery, *MilestoneObservation, *tracker.WriteOptions, *fixtureEvidenceValidator)
	}{
		{name: "model claim rejected", change: func(r *RuleSet, d *Discovery, m *MilestoneObservation, o *tracker.WriteOptions, v *fixtureEvidenceValidator) {
			v.err = errors.New("artifact bytes and authority could not be verified")
		}},
		{name: "PR creation is not approval", change: func(r *RuleSet, d *Discovery, m *MilestoneObservation, o *tracker.WriteOptions, v *fixtureEvidenceValidator) {
			m.Evidence[1].Type = "created_pr"
		}},
		{name: "required evidence missing", change: func(r *RuleSet, d *Discovery, m *MilestoneObservation, o *tracker.WriteOptions, v *fixtureEvidenceValidator) {
			m.Evidence = m.Evidence[:1]
		}},
		{name: "required transition field missing", change: func(r *RuleSet, d *Discovery, m *MilestoneObservation, o *tracker.WriteOptions, v *fixtureEvidenceValidator) {
			r.Outbound[0].Action = Transition{TransitionID: "to-testing"}
		}},
		{name: "unsupported transition has no implicit fallback", change: func(r *RuleSet, d *Discovery, m *MilestoneObservation, o *tracker.WriteOptions, v *fixtureEvidenceValidator) {
			r.Outbound[0].Action = Transition{TransitionID: "close:completed"}
		}},
		{name: "unknown guard blocks", change: func(r *RuleSet, d *Discovery, m *MilestoneObservation, o *tracker.WriteOptions, v *fixtureEvidenceValidator) {
			transitions := o.Transitions.(tracker.Known[[]tracker.Transition]).Value
			transitions[0].Guards[0].Satisfied = tracker.Unknown[bool]{Reason: "permission changed"}
		}},
		{name: "writer options expired", change: func(r *RuleSet, d *Discovery, m *MilestoneObservation, o *tracker.WriteOptions, v *fixtureEvidenceValidator) {
			o.ValidUntil = o.ObservedAt
		}},
		{name: "generic completion cannot declare handoff", change: func(r *RuleSet, d *Discovery, m *MilestoneObservation, o *tracker.WriteOptions, v *fixtureEvidenceValidator) {
			r.Outbound[0].Milestone.ID = "run.completed"
			d.Milestones[0].ID = "run.completed"
		}},
	}
	for _, test := range badCases {
		t.Run(test.name, func(t *testing.T) {
			r, d, observation, m, options, now := milestoneFixture()
			v := &fixtureEvidenceValidator{}
			test.change(&r, &d, &m, &options, v)
			if _, err := PreviewOutbound(context.Background(), r, d, observation, m, options, now, v); err == nil {
				t.Fatal("unsupported or unproven milestone effect was accepted")
			}
		})
	}
}

func TestGenericCompletionHasNoEffectAndExplicitReportDoesNotTransition(t *testing.T) {
	rules, discovery, observation, milestone, options, now := milestoneFixture()
	validator := &fixtureEvidenceValidator{}
	milestone.MilestoneID = "run.completed"
	preview, err := PreviewOutbound(context.Background(), rules, discovery, observation, milestone, options, now, validator)
	if err != nil || preview.Matched || preview.Effect != nil || validator.calls != 0 {
		t.Fatalf("generic completion acquired business-state authority: %#v, %v", preview, err)
	}
	milestone.MilestoneID = rules.Outbound[0].Milestone.ID
	rules.Outbound[0].Action = Report{Body: "Implementation handoff validated; awaiting external acceptance."}
	preview, err = PreviewOutbound(context.Background(), rules, discovery, observation, milestone, options, now, validator)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := preview.Effect.(tracker.ReportProgress); !ok {
		t.Fatalf("report-only mapping implied a transition: %#v", preview.Effect)
	}
	rules.Outbound[0].Action = Noop{}
	preview, err = PreviewOutbound(context.Background(), rules, discovery, observation, milestone, options, now, validator)
	if err != nil || !preview.Matched || preview.Effect != nil || preview.OperationID != "" {
		t.Fatalf("explicit no-op produced an effect: %#v, %v", preview, err)
	}
}

func TestRulesDoNotMutateRetainedInput(t *testing.T) {
	rules, discovery, observation := ruleFixture("linear")
	cursor := Cursor{}
	beforeRules, _ := Encode(rules)
	beforeCursor, _ := json.Marshal(cursor)
	if _, err := EvaluateAdmission(rules, discovery, Event{ID: "event", Observation: observation}, cursor, true, nil); err != nil {
		t.Fatal(err)
	}
	afterRules, _ := Encode(rules)
	afterCursor, _ := json.Marshal(cursor)
	if string(beforeRules) != string(afterRules) || string(beforeCursor) != string(afterCursor) {
		t.Fatal("pure evaluation modified retained configuration or prior cursor")
	}
}

func TestJiraJSONFixtureIsExecutable(t *testing.T) {
	encoded, err := os.ReadFile("testdata/jira-workflow.json")
	if err != nil {
		t.Fatal(err)
	}
	rules, err := Decode(encoded)
	if err != nil {
		t.Fatal(err)
	}
	_, discovery, observation, milestone, options, now := milestoneFixture()
	preview, err := PreviewIntake(rules, discovery, observation)
	if err != nil || !preview.Matched {
		t.Fatalf("Jira type/workflow/sprint fixture did not match: %#v, %v", preview, err)
	}
	if action := preview.Action.(Admit); action.Mode != Manual || action.Repair.MaxAdmissions != 2 {
		t.Fatalf("unexpected fixture admission policy: %#v", action)
	}
	outbound, err := PreviewOutbound(context.Background(), rules, discovery, observation, milestone, options, now, &fixtureEvidenceValidator{})
	if err != nil || !outbound.Matched {
		t.Fatalf("Jira JSON handoff fixture did not validate: %#v, %v", outbound, err)
	}
	observation.WorkflowID = tracker.Known[tracker.NamedID]{Value: tracker.NamedID{ID: "other-workflow"}}
	preview, err = PreviewIntake(rules, discovery, observation)
	if err != nil || preview.Matched {
		t.Fatalf("source workflow identity was ignored: %#v, %v", preview, err)
	}
}
