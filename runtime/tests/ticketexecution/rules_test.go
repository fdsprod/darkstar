package ticketexecution_test

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"darkstar/src/adapters/statestore/sqlite"
	"darkstar/src/adapters/tracker/builtin"
	"darkstar/src/core/backlog"
	"darkstar/src/core/ticketexecution"
	"darkstar/src/core/ticketmanagement"
	"darkstar/src/core/trackerrules"
	"darkstar/src/core/workmanagement"
	"darkstar/src/ports/statestore"
	"darkstar/src/ports/tracker"
)

type ruleFixture struct {
	db                         *sqlite.Database
	adapter                    *builtin.Adapter
	rules                      trackerrules.RuleSet
	discovery                  trackerrules.Discovery
	project, work, observation string
}

func (f *ruleFixture) Resolve(ctx context.Context, binding statestore.BacklogBinding) (backlog.ResolvedSource, error) {
	return backlog.ResolvedSource{Source: f.adapter, Browser: f.adapter, Config: f.adapter.ConfigPin()}, nil
}

func (f *ruleFixture) ResolveRules(ctx context.Context, project, observation string) (trackerrules.RuleSet, trackerrules.Discovery, error) {
	return f.rules, f.discovery, nil
}

func (f *ruleFixture) DiscoverRules(ctx context.Context, project, observation string) (trackerrules.Discovery, error) {
	return f.discovery, nil
}

func newRuleFixture(t *testing.T, mode trackerrules.AdmissionMode) *ruleFixture {
	t.Helper()
	ctx := context.Background()
	db, err := sqlite.Open(ctx, filepath.Join(t.TempDir(), "rules.db"), sqlite.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = db.Close()
	})
	management, _ := workmanagement.New(db)
	project, err := management.RegisterProject(ctx, workmanagement.ProjectRegistration{Name: "Rules", Source: t.TempDir()}, "rules-project")
	if err != nil {
		t.Fatal(err)
	}
	work, err := management.CreateWork(ctx, workmanagement.CreateWorkRequest{ProjectID: project.ProjectID, Title: "Mapped ticket"}, "rules-native-work")
	if err != nil {
		t.Fatal(err)
	}
	adapter, err := builtin.NewForBinding(db, project.ProjectID, "1")
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := adapter.Discover(ctx, adapter.ConfigPin())
	if err != nil {
		t.Fatal(err)
	}
	f := &ruleFixture{db: db, adapter: adapter, project: project.ProjectID, work: work.WorkItemID}
	cache, _ := backlog.New(db, f, backlog.Options{})
	if _, err := cache.Refresh(ctx, f.project, 1, tracker.Query{PageSize: 10}); err != nil {
		t.Fatal(err)
	}
	view, err := cache.View(ctx, f.project, backlog.ViewRequest{})
	if err != nil || len(view.Tickets) != 1 {
		t.Fatalf("view: %#v %v", view, err)
	}
	f.observation = view.Tickets[0].ObservationID
	workflow := trackerrules.WorkflowPin{ID: "implementation", Version: "1.0.0", Digest: strings.Repeat("a", 64)}
	f.discovery = trackerrules.Discovery{Manifest: manifest, Fields: []trackerrules.FieldDefinition{{ID: "state", Values: []tracker.NamedID{{ID: "open", Name: "Open"}}}}, Workflows: []trackerrules.WorkflowPin{workflow}, ReadinessPolicies: []string{"require_approval"}}
	f.rules = trackerrules.RuleSet{Version: trackerrules.Version, ID: "delivery", Revision: 1, Scope: trackerrules.RuleScope{ProjectID: f.project, BindingRevision: 1, Pin: manifest.Pin, Source: manifest.Scope}, Intake: []trackerrules.IntakeRule{{ID: "ready", When: trackerrules.Conditions{Fields: []trackerrules.Predicate{{FieldID: "state", Values: []string{"open"}}}}, Action: trackerrules.Admit{Workflow: workflow, ReadinessPolicy: "require_approval", Mode: mode, Repair: trackerrules.RepairPolicy{MaxAdmissions: 1}}}}, Display: trackerrules.DisplayMapping{Groups: []trackerrules.DisplayGroup{{ID: "backlog", Name: "Backlog", StateIDs: []string{"open"}}}, UnknownGroup: trackerrules.DisplayGroup{ID: "unknown", Name: "Unmapped"}}}
	f.save(t, 0)
	return f
}

func (f *ruleFixture) save(t *testing.T, previous uint64) {
	t.Helper()
	encoded, err := trackerrules.Encode(f.rules)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.db.SaveTrackerMapping(context.Background(), statestore.TrackerMappingRevision{ProjectID: f.project, Revision: f.rules.Revision, BindingRevision: 1, RulesJSON: encoded, CreatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	if err := f.db.ActivateTrackerMapping(context.Background(), f.project, f.rules.Revision, previous, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
}

func TestAdmittedRuleSnapshotSurvivesMappingActivationAndApproval(t *testing.T) {
	f := newRuleFixture(t, trackerrules.Manual)
	ctx := context.Background()
	service, _ := ticketexecution.New(f.db, f, ticketexecution.Options{Rules: f})
	first, err := service.Admit(ctx, ticketexecution.AdmissionRequest{ProjectID: f.project, BindingRevision: 1, ObservationID: f.observation}, "mapped-manual-admission")
	if err != nil {
		t.Fatal(err)
	}
	f.rules.Revision = 2
	f.rules.Display.Groups[0].Name = "Renamed"
	f.save(t, 1)
	if _, err := service.Approve(ctx, first.Work.WorkItemID, f.observation, "mapped-later-approval"); err != nil {
		t.Fatal(err)
	}
	snapshot, err := f.db.ApprovedRunSource(ctx, first.Work.WorkItemID, f.observation)
	if err != nil || snapshot.RulePin == nil || snapshot.RulePin.Revision != 1 || snapshot.RulePin.WorkflowVersion != "1.0.0" {
		t.Fatalf("mapping changed admitted work: %#v %v", snapshot.RulePin, err)
	}
	if _, err := f.db.SQL().ExecContext(ctx, `UPDATE source_admission_rule_pins SET snapshot_json='{}'`); err == nil {
		t.Fatal("immutable admission pin was overwritten")
	}
}

func TestAutomaticIntakeIsDurableAndCannotInventExecution(t *testing.T) {
	f := newRuleFixture(t, trackerrules.Automatic)
	ctx := context.Background()
	service, _ := ticketexecution.New(f.db, f, ticketexecution.Options{Rules: f})
	first, err := service.EvaluateIntake(ctx, f.project, f.observation)
	if err != nil || first.State != "automatic" || first.Admission == nil {
		t.Fatalf("automatic intake: %#v %v", first, err)
	}
	restarted, _ := ticketexecution.New(f.db, f, ticketexecution.Options{Rules: f})
	second, err := restarted.EvaluateIntake(ctx, f.project, f.observation)
	if err != nil || second.State != "duplicate" || second.Admission != nil {
		t.Fatalf("replayed intake: %#v %v", second, err)
	}
	runs, err := f.db.Runs(ctx)
	if err != nil || len(runs) != 0 {
		t.Fatalf("intake fabricated run state: %#v %v", runs, err)
	}
	var admissions int
	if err := f.db.SQL().QueryRowContext(ctx, `SELECT count(*) FROM source_ticket_admissions`).Scan(&admissions); err != nil || admissions != 1 {
		t.Fatalf("duplicate admission: %d %v", admissions, err)
	}
	var cursor string
	if err := f.db.SQL().QueryRowContext(ctx, `SELECT cursor_json FROM source_intake_cursors`).Scan(&cursor); err != nil {
		t.Fatal(err)
	}
	var retained trackerrules.Cursor
	if err := json.Unmarshal([]byte(cursor), &retained); err != nil || retained.Admissions != 1 {
		t.Fatalf("cursor: %#v %v", retained, err)
	}
	snapshot, err := f.db.ApprovedRunSource(ctx, first.Admission.Work.WorkItemID, f.observation)
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.db.AdmitSourceTicket(ctx, statestore.SourceAdmissionMutation{ProjectID: f.project, WorkID: first.Admission.Work.WorkItemID, ExistingWorkID: first.Admission.Work.WorkItemID, BindingRevision: 1, ObservationID: f.observation, IdempotencyKey: "racing-rule-admission", RequestDigest: strings.Repeat("b", 64), AdmissionID: "racing-rule-admission", Actor: "tracker-intake", ApprovedAt: time.Now().UTC(), RulePin: snapshot.RulePin, IntakeCursor: &statestore.SourceIntakeCursorMutation{Identity: retained.Identity, ExpectedDigest: "", Cursor: json.RawMessage(cursor)}})
	if err == nil {
		t.Fatal("stale cursor admitted duplicate work")
	}
	if err := f.db.SQL().QueryRowContext(ctx, `SELECT count(*) FROM source_ticket_admissions`).Scan(&admissions); err != nil || admissions != 1 {
		t.Fatalf("cursor conflict did not roll back admission: %d %v", admissions, err)
	}
}

func TestManualAdmissionReplaysBeforeLatestMappingAndSourceResolution(t *testing.T) {
	f := newRuleFixture(t, trackerrules.Manual)
	ctx := context.Background()
	service, _ := ticketexecution.New(f.db, f, ticketexecution.Options{Rules: f})
	request := ticketexecution.AdmissionRequest{ProjectID: f.project, BindingRevision: 1, ObservationID: f.observation}
	first, err := service.Admit(ctx, request, "manual-replay-original")
	if err != nil {
		t.Fatal(err)
	}
	f.rules.Revision = 2
	f.rules.Intake[0].Action = trackerrules.Noop{}
	f.save(t, 1)
	second, err := service.Admit(ctx, request, "manual-replay-original")
	if err != nil || second.Admission.ID != first.Admission.ID {
		t.Fatalf("mapping edit reinterpreted a completed admission: %#v %v", second, err)
	}
	source := statestore.ExternalBacklogSource{ConnectionID: "new-source", ConnectionRevision: "1", Scope: tracker.Scope{Namespace: tracker.Namespace{Provider: "linear", Host: "linear.app", TenantID: "workspace", ScopeID: "workspace"}, ContainerID: "team"}}
	if _, err := f.db.SelectBacklogSource(ctx, f.project, 1, source, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	restarted, _ := ticketexecution.New(f.db, f, ticketexecution.Options{Rules: f})
	third, err := restarted.Admit(ctx, request, "manual-replay-original")
	if err != nil || third.Admission.ID != first.Admission.ID {
		t.Fatalf("source switch lost completed admission receipt: %#v %v", third, err)
	}
	changed := request
	changed.ObservationID = "another-observation"
	if _, err := restarted.Admit(ctx, changed, "manual-replay-original"); err == nil {
		t.Fatal("completed admission key accepted different request bytes")
	}
	var count int
	if err := f.db.SQL().QueryRowContext(ctx, `SELECT count(*) FROM source_ticket_admissions`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("replay created another admission: %d %v", count, err)
	}
}

func TestFirstApprovalEnforcesActiveIntakeAndLaterApprovalRetainsOriginalRule(t *testing.T) {
	f := newRuleFixture(t, trackerrules.Manual)
	ctx := context.Background()
	service, _ := ticketexecution.New(f.db, f, ticketexecution.Options{Rules: f})
	originalAction := f.rules.Intake[0].Action
	f.rules.Revision = 2
	f.rules.Intake[0].Action = trackerrules.Noop{}
	f.save(t, 1)
	if _, err := service.Approve(ctx, f.work, f.observation, "first-approval-noop"); err == nil {
		t.Fatal("first source approval bypassed the activated observe-only intake rule")
	}
	var count int
	if err := f.db.SQL().QueryRowContext(ctx, `SELECT count(*) FROM source_ticket_admissions`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("rejected approval persisted authority: %d %v", count, err)
	}
	f.rules.Revision = 3
	f.rules.Intake[0].Action = originalAction
	f.save(t, 2)
	first, err := service.Approve(ctx, f.work, f.observation, "first-approval-mapped")
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := f.db.ApprovedRunSource(ctx, f.work, f.observation)
	if err != nil || snapshot.RulePin == nil || snapshot.RulePin.Revision != 3 {
		t.Fatalf("first approval omitted activated rule pin: %#v %v", snapshot.RulePin, err)
	}
	identity := trackerrules.AdmissionIdentity(f.project, snapshot.Ref)
	encoded, err := f.db.SourceIntakeCursor(ctx, identity)
	var cursor trackerrules.Cursor
	if err != nil || json.Unmarshal(encoded, &cursor) != nil || cursor.Admissions != 1 {
		t.Fatalf("first approval did not atomically consume admission episode: %#v %v", cursor, err)
	}
	f.rules.Revision = 4
	f.rules.Intake[0].Action = trackerrules.Noop{}
	f.save(t, 3)
	replayed, err := service.Approve(ctx, f.work, f.observation, "first-approval-mapped")
	if err != nil || replayed.Admission.ID != first.Admission.ID {
		t.Fatalf("approval replay changed authority: %#v %v", replayed, err)
	}
	if _, err := service.Approve(ctx, f.work, f.observation, "later-approval-retained"); err != nil {
		t.Fatal("later approval lost already-pinned human capability:", err)
	}
	snapshot, err = f.db.ApprovedRunSource(ctx, f.work, f.observation)
	if err != nil || snapshot.RulePin == nil || snapshot.RulePin.Revision != 3 {
		t.Fatalf("later approval changed original mapping: %#v %v", snapshot.RulePin, err)
	}
	if _, err := service.Approve(ctx, f.work, "another-observation", "first-approval-mapped"); err == nil {
		t.Fatal("approval replay accepted a different source observation")
	}
}

func (f *ruleFixture) move(t *testing.T, state, key string) {
	t.Helper()
	ctx := context.Background()
	manager, err := ticketmanagement.New(f.db, f.db, func(context.Context, string) (ticketmanagement.Binding, error) {
		return ticketmanagement.Binding{Source: f.adapter, Browser: f.adapter, Writer: f.adapter, Config: f.adapter.ConfigPin()}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	current, err := manager.Detail(ctx, f.project, f.work)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Transition(ctx, f.project, f.work, ticketmanagement.TransitionRequest{SchemaVersion: 1, Revision: current.Ticket.Revision, TransitionID: "set-state:" + state}, key); err != nil {
		t.Fatal(err)
	}
	cache, err := backlog.New(f.db, f, backlog.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := cache.Refresh(ctx, f.project, 1, tracker.Query{PageSize: 10}); err != nil {
		t.Fatal(err)
	}
	view, err := cache.View(ctx, f.project, backlog.ViewRequest{})
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range view.Tickets {
		if entry.Ticket.Ref.ID == f.work {
			f.observation = entry.ObservationID
			return
		}
	}
	t.Fatal("updated ticket observation missing")
}

func TestRepairUsesRetainedRulesWhileNewTicketsUseActiveMapping(t *testing.T) {
	for _, mode := range []trackerrules.AdmissionMode{trackerrules.Automatic, trackerrules.Manual} {
		t.Run(string(mode), func(t *testing.T) {
			f := newRuleFixture(t, mode)
			ctx := context.Background()
			f.discovery.Fields[0].Values = append(f.discovery.Fields[0].Values, tracker.NamedID{ID: "active", Name: "Active"})
			action := f.rules.Intake[0].Action.(trackerrules.Admit)
			action.Repair.MaxAdmissions = 2
			f.rules.Intake[0].Action = action
			f.rules.Revision = 2
			f.save(t, 1)
			service, _ := ticketexecution.New(f.db, f, ticketexecution.Options{Rules: f})
			var first ticketexecution.Result
			if mode == trackerrules.Automatic {
				result, err := service.EvaluateIntake(ctx, f.project, f.observation)
				if err != nil || result.Admission == nil {
					t.Fatalf("first automatic intake: %#v %v", result, err)
				}
				first = *result.Admission
			} else {
				var err error
				first, err = service.Admit(ctx, ticketexecution.AdmissionRequest{ProjectID: f.project, BindingRevision: 1, ObservationID: f.observation}, "manual-initial-episode")
				if err != nil {
					t.Fatal(err)
				}
			}
			f.rules.Revision = 3
			f.rules.Intake[0].Action = trackerrules.Noop{}
			f.save(t, 2)
			f.move(t, "active", "repair-leave-eligible")
			left, err := service.EvaluateIntake(ctx, f.project, f.observation)
			if err != nil || left.State != "ineligible" {
				t.Fatalf("leaving eligibility: %#v %v", left, err)
			}
			f.move(t, "open", "repair-reenter-eligible")
			restarted, _ := ticketexecution.New(f.db, f, ticketexecution.Options{Rules: f})
			var repaired ticketexecution.Result
			if mode == trackerrules.Automatic {
				result, err := restarted.EvaluateIntake(ctx, f.project, f.observation)
				if err != nil || result.State != "automatic" || result.Admission == nil {
					t.Fatalf("repair ignored original automatic rule: %#v %v", result, err)
				}
				repaired = *result.Admission
			} else {
				repaired, err = restarted.Admit(ctx, ticketexecution.AdmissionRequest{ProjectID: f.project, BindingRevision: 1, ObservationID: f.observation}, "manual-repair-episode")
				if err != nil {
					t.Fatal("repair ignored original manual rule:", err)
				}
			}
			if repaired.Work.WorkItemID != first.Work.WorkItemID || repaired.Admission.ID == first.Admission.ID {
				t.Fatal("repair did not retain work identity and create one new admission episode")
			}
			snapshot, err := f.db.ApprovedRunSource(ctx, repaired.Work.WorkItemID, f.observation)
			if err != nil || snapshot.RulePin == nil || snapshot.RulePin.Revision != 2 {
				t.Fatalf("repair changed original rules: %#v %v", snapshot.RulePin, err)
			}
			management, _ := workmanagement.New(f.db)
			newWork, err := management.CreateWork(ctx, workmanagement.CreateWorkRequest{ProjectID: f.project, Title: "Future ticket"}, "new-ticket-after-mapping")
			if err != nil {
				t.Fatal(err)
			}
			cache, _ := backlog.New(f.db, f, backlog.Options{})
			if _, err := cache.Refresh(ctx, f.project, 1, tracker.Query{PageSize: 10}); err != nil {
				t.Fatal(err)
			}
			view, err := cache.View(ctx, f.project, backlog.ViewRequest{})
			if err != nil {
				t.Fatal(err)
			}
			for _, entry := range view.Tickets {
				if entry.Ticket.Ref.ID == newWork.WorkItemID {
					result, err := restarted.EvaluateIntake(ctx, f.project, entry.ObservationID)
					if err != nil || result.State != "ineligible" || result.Admission != nil {
						t.Fatalf("new ticket ignored active observe-only mapping: %#v %v", result, err)
					}
				}
			}
			// A retained rule does not retain obsolete provider capabilities.
			f.discovery.Fields[0].Values = []tracker.NamedID{{ID: "active", Name: "Active"}}
			if _, err := restarted.EvaluateIntake(ctx, f.project, f.observation); err == nil {
				t.Fatal("retained rule bypassed provider status drift validation")
			}
		})
	}
}
