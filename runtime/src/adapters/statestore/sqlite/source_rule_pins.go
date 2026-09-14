package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"darkstar/src/core/trackerrules"
	"darkstar/src/ports"
	"darkstar/src/ports/statestore"
)

var _ statestore.SourceAdmissionReceipts = (*Database)(nil)

func (d *Database) ReplayTicketAdmission(ctx context.Context, key, digest string) (statestore.TicketAdmission, error) {
	return admissionReplay(ctx, d.sql, key, digest)
}

func (d *Database) TicketAdmissionRulePin(ctx context.Context, admission string) (*statestore.SourceRulePin, error) {
	return readSourceRulePin(ctx, d.sql, admission)
}

func readSourceRulePin(ctx context.Context, query rowQueryer, admission string) (*statestore.SourceRulePin, error) {
	var encoded string
	err := query.QueryRowContext(ctx, `SELECT snapshot_json FROM source_admission_rule_pins WHERE admission_id=?`, admission).Scan(&encoded)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var pin statestore.SourceRulePin
	if err := strictBacklogJSON([]byte(encoded), &pin); err != nil {
		return nil, err
	}
	return &pin, nil
}

func persistSourceRulePin(ctx context.Context, tx *sql.Tx, admission statestore.TicketAdmission, pin *statestore.SourceRulePin) error {
	var previousID string
	err := tx.QueryRowContext(ctx, `SELECT p.admission_id FROM source_admission_rule_pins p JOIN source_ticket_admissions a ON a.admission_id=p.admission_id WHERE a.work_id=? AND a.lineage_revision=? ORDER BY a.approved_at,a.rowid LIMIT 1`, admission.WorkID, admission.LineageRevision).Scan(&previousID)
	if err == nil {
		previous, readErr := readSourceRulePin(ctx, tx, previousID)
		if readErr != nil {
			return readErr
		}
		if pin != nil {
			oldJSON, _ := json.Marshal(previous)
			newJSON, _ := json.Marshal(pin)
			if string(oldJSON) != string(newJSON) {
				return backlogFailure(ports.FailureConflict, "existing source work retains its original mapping revision")
			}
		}
		pin = previous
	} else if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if pin == nil {
		return nil
	}
	if previousID == "" {
		var revision uint64
		if err := tx.QueryRowContext(ctx, `SELECT mapping_revision FROM tracker_mapping_active WHERE project_id=? AND binding_revision=?`, admission.ProjectID, admission.BindingRevision).Scan(&revision); err != nil || revision != pin.Revision {
			return backlogFailure(ports.FailureConflict, "active intake mapping changed before admission")
		}
		var saved string
		if err := tx.QueryRowContext(ctx, `SELECT rules_json FROM tracker_mapping_revisions WHERE project_id=? AND revision=?`, admission.ProjectID, pin.Revision).Scan(&saved); err != nil {
			return err
		}
		stored, err := trackerrules.Decode([]byte(saved))
		if err != nil {
			return err
		}
		canonical, err := trackerrules.Encode(stored)
		if err != nil || string(canonical) != string(pin.Rules) {
			return backlogFailure(ports.FailureConflict, "admission mapping bytes differ from the activated revision")
		}
	}
	rules, err := trackerrules.Decode(pin.Rules)
	if err != nil || rules.ID != pin.RuleSetID || rules.Revision != pin.Revision || rules.Scope.ProjectID != admission.ProjectID || rules.Scope.BindingRevision != admission.BindingRevision {
		return backlogFailure(ports.FailureInvalidRequest, "admission mapping snapshot does not match its source scope")
	}
	matched := false
	for _, rule := range rules.Intake {
		action, ok := rule.Action.(trackerrules.Admit)
		if rule.ID == pin.RuleID && ok && action.Workflow.ID == pin.WorkflowID && action.Workflow.Version == pin.WorkflowVersion && action.Workflow.Digest == pin.WorkflowDigest && action.ReadinessPolicy == pin.ReadinessPolicy && string(action.Mode) == pin.AdmissionMode {
			matched = true
		}
	}
	if !matched {
		return backlogFailure(ports.FailureInvalidRequest, "admission workflow must match the immutable intake rule")
	}
	encoded, err := json.Marshal(pin)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO source_admission_rule_pins(admission_id,snapshot_json) VALUES (?,?)`, admission.ID, string(encoded))
	return err
}

func (d *Database) SourceIntakeCursor(ctx context.Context, identity string) (json.RawMessage, error) {
	var encoded string
	err := d.sql.QueryRowContext(ctx, `SELECT cursor_json FROM source_intake_cursors WHERE identity=?`, identity).Scan(&encoded)
	return json.RawMessage(encoded), backlogNormalize(err)
}

func saveSourceIntakeCursor(ctx context.Context, tx *sql.Tx, change statestore.SourceIntakeCursorMutation) error {
	if change.Identity == "" || !json.Valid(change.Cursor) {
		return backlogFailure(ports.FailureInvalidRequest, "intake cursor requires exact identity and state")
	}
	var previous string
	err := tx.QueryRowContext(ctx, `SELECT cursor_json FROM source_intake_cursors WHERE identity=?`, change.Identity).Scan(&previous)
	if errors.Is(err, sql.ErrNoRows) {
		if change.ExpectedDigest != "" {
			return backlogFailure(ports.FailureConflict, "intake cursor changed before admission")
		}
	} else if err != nil {
		return err
	} else if sourceHash([]byte(previous)) != change.ExpectedDigest {
		return backlogFailure(ports.FailureConflict, "intake cursor changed before admission")
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO source_intake_cursors(identity,cursor_json) VALUES (?,?) ON CONFLICT(identity) DO UPDATE SET cursor_json=excluded.cursor_json`, change.Identity, string(change.Cursor))
	return err
}

func (d *Database) SaveSourceIntakeCursor(ctx context.Context, change statestore.SourceIntakeCursorMutation) error {
	tx, err := d.sql.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() {
		_ = tx.Rollback()
	}()
	if _, err := tx.ExecContext(ctx, `UPDATE global_positions SET last_position=last_position WHERE singleton=1`); err != nil {
		return err
	}
	if err := saveSourceIntakeCursor(ctx, tx, change); err != nil {
		return err
	}
	return tx.Commit()
}
