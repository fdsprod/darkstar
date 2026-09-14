package runexecution_test

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"darkstar/src/adapters/statestore/sqlite"
	"darkstar/src/core/trackercontract"
	"darkstar/src/core/trackerrules"
	"darkstar/src/ports/statestore"
	"darkstar/src/ports/tracker"
)

func TestRuleAdmissionPinsRunWorkflowAndAttributesAutomaticPreparation(t *testing.T) {
	ctx := context.Background()
	db, err := sqlite.Open(ctx, filepath.Join(t.TempDir(), "rule-run.db"), sqlite.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = db.Close()
	}()
	project, work, observation, state := seedAdmittedSource(t, db)
	service, _, request := sourceRunService(t, db, work, observation.ID)
	defer func() {
		_ = service.Close()
	}()
	ticket, err := trackercontract.DecodeTicket(observation.Ticket)
	if err != nil {
		t.Fatal(err)
	}
	workflow := sourceRunPlanner().definition.Version
	workflowPin := trackerrules.WorkflowPin{ID: workflow.Name, Version: workflow.Version, Digest: workflow.Digest}
	rules := trackerrules.RuleSet{Version: trackerrules.Version, ID: "test-rules", Revision: 1, Scope: trackerrules.RuleScope{ProjectID: project, BindingRevision: state.BindingRevision, Pin: state.Pin, Source: ticket.Placement.(tracker.Known[tracker.Scope]).Value}, Intake: []trackerrules.IntakeRule{{ID: "admit", When: trackerrules.Conditions{Fields: []trackerrules.Predicate{{FieldID: "state", Values: []string{"open"}}}}, Action: trackerrules.Admit{Workflow: workflowPin, Mode: trackerrules.Automatic, ReadinessPolicy: "require_approval", Repair: trackerrules.RepairPolicy{MaxAdmissions: 1}}}}, Display: trackerrules.DisplayMapping{UnknownGroup: trackerrules.DisplayGroup{ID: "unmapped", Name: "Unmapped"}}}
	encoded, err := trackerrules.Encode(rules)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.SaveTrackerMapping(ctx, statestore.TrackerMappingRevision{ProjectID: project, Revision: 1, BindingRevision: state.BindingRevision, RulesJSON: encoded, CreatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if err := db.ActivateTrackerMapping(ctx, project, 1, 0, time.Now()); err != nil {
		t.Fatal(err)
	}
	pin := &statestore.SourceRulePin{RuleSetID: rules.ID, Revision: 1, RuleID: "admit", Rules: encoded, WorkflowID: workflowPin.ID, WorkflowVersion: workflowPin.Version, WorkflowDigest: workflowPin.Digest, ReadinessPolicy: "require_approval", AdmissionMode: "automatic"}
	if _, err := db.AdmitSourceTicket(ctx, statestore.SourceAdmissionMutation{ProjectID: project, WorkID: work, ExistingWorkID: work, ObservationID: observation.ID, BindingRevision: state.BindingRevision, IdempotencyKey: "automatic-rule-admission", RequestDigest: strings.Repeat("a", 64), AdmissionID: "rule-admission", Actor: "tracker-intake", ApprovedAt: time.Now().UTC(), RulePin: pin}); err != nil {
		t.Fatal(err)
	}
	wrong := request
	wrong.WorkflowVersion = "2.0.0"
	if _, err := service.Prepare(ctx, wrong, "override-pinned-workflow"); err == nil {
		t.Fatal("request replaced the pinned workflow")
	}
	ready, err := service.PrepareIntake(ctx, work, observation.ID, "automatic-rule-prepare")
	if err != nil {
		t.Fatal(err)
	}
	if ready.Status != statestore.RunReady {
		t.Fatalf("automatic preparation bypassed readiness: %s", ready.Status)
	}
	snapshot, err := db.RunSourceSnapshot(ctx, ready.RunID)
	if err != nil || snapshot.RulePin == nil || snapshot.RulePin.WorkflowDigest != workflowPin.Digest {
		t.Fatalf("rule was not frozen with run: %#v %v", snapshot.RulePin, err)
	}
	var actorType, actorID string
	if err := db.SQL().QueryRowContext(ctx, `SELECT json_extract(actor_json,'$.type'),json_extract(actor_json,'$.id') FROM events WHERE aggregate_id=? AND kind='run.created'`, ready.RunID).Scan(&actorType, &actorID); err != nil {
		t.Fatal(err)
	}
	if actorType != "system" || actorID != "tracker-intake" {
		t.Fatalf("automatic preparation impersonated a human: %s/%s", actorType, actorID)
	}
	attempts, err := db.AttemptsForRun(ctx, ready.RunID)
	if err != nil || len(attempts) != 0 {
		t.Fatalf("preparation fabricated an attempt: %#v %v", attempts, err)
	}
}
