package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"reflect"
	"strconv"
	"strings"

	"darkstar/src/ports"
	"darkstar/src/ports/statestore"
	"darkstar/src/ports/tracker"
)

const nativeSelect = `SELECT ticket_id, project_id, title, description, business_state, priority, revision, assignees_json, labels_json, relationships_json, evidence_json, created_at, updated_at FROM native_tickets`

func scanNativeTicket(row interface{ Scan(...any) error }) (statestore.NativeTicket, error) {
	var value statestore.NativeTicket
	var assignees, labels, relations, evidence, created, updated string
	err := row.Scan(&value.ID, &value.ProjectID, &value.Title, &value.Description, &value.State, &value.Priority, &value.Revision, &assignees, &labels, &relations, &evidence, &created, &updated)
	if err != nil {
		return value, err
	}
	if err = json.Unmarshal([]byte(assignees), &value.Assignees); err != nil {
		return value, err
	}
	if err = json.Unmarshal([]byte(labels), &value.Labels); err != nil {
		return value, err
	}
	if err = json.Unmarshal([]byte(relations), &value.Relationships); err != nil {
		return value, err
	}
	if err = json.Unmarshal([]byte(evidence), &value.Evidence); err != nil {
		return value, err
	}
	value.CreatedAt, err = parseTime(created)
	if err == nil {
		value.UpdatedAt, err = parseTime(updated)
	}
	return value, err
}

func (d *Database) NativeTicket(ctx context.Context, projectID, id string) (statestore.NativeTicket, error) {
	value, err := scanNativeTicket(d.sql.QueryRowContext(ctx, nativeSelect+` WHERE project_id=? AND ticket_id=?`, projectID, id))
	if errors.Is(err, sql.ErrNoRows) {
		return value, &ports.Failure{Code: ports.FailureNotFound, Message: "native ticket not found in project"}
	}
	return value, err
}

func (d *Database) NativeTickets(ctx context.Context, projectID string) ([]statestore.NativeTicket, error) {
	var found string
	if err := d.sql.QueryRowContext(ctx, `SELECT project_id FROM native_namespaces WHERE project_id=?`, projectID).Scan(&found); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, &ports.Failure{Code: ports.FailureNotFound, Message: "native project namespace not found"}
		}
		return nil, err
	}
	rows, err := d.sql.QueryContext(ctx, nativeSelect+` WHERE project_id=? ORDER BY ticket_id`, projectID)
	if err != nil {
		return nil, err
	}
	defer func() {
		_ = rows.Close()
	}()
	values := make([]statestore.NativeTicket, 0)
	for rows.Next() {
		value, err := scanNativeTicket(rows)
		if err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	return values, rows.Err()
}

func (d *Database) NativeTicketHistory(ctx context.Context, projectID, id string) ([]statestore.NativeTicketHistory, error) {
	if _, err := d.NativeTicket(ctx, projectID, id); err != nil {
		return nil, err
	}
	rows, err := d.sql.QueryContext(ctx, `SELECT revision,kind,evidence_ref,snapshot_json,request_json,recorded_at FROM native_ticket_history WHERE ticket_id=? ORDER BY revision`, id)
	if err != nil {
		return nil, err
	}
	defer func() {
		_ = rows.Close()
	}()
	values := make([]statestore.NativeTicketHistory, 0)
	for rows.Next() {
		var value statestore.NativeTicketHistory
		var snapshot, request, recorded string
		if err := rows.Scan(&value.Revision, &value.Kind, &value.EvidenceRef, &snapshot, &request, &recorded); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(snapshot), &value.Snapshot); err != nil {
			return nil, err
		}
		value.Request = json.RawMessage(request)
		value.RecordedAt, err = parseTime(recorded)
		if err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	return values, rows.Err()
}

func nativeOperation(ctx context.Context, query rowQueryer, id, fingerprint string) (tracker.Receipt, error) {
	var storedFingerprint, encoded string
	var receipt tracker.Receipt
	err := query.QueryRowContext(ctx, `SELECT fingerprint,receipt_json FROM native_ticket_operations WHERE operation_id=?`, id).Scan(&storedFingerprint, &encoded)
	if errors.Is(err, sql.ErrNoRows) {
		return receipt, &ports.Failure{Code: ports.FailureNotFound, Message: "native operation has not committed"}
	}
	if err != nil {
		return receipt, err
	}
	if storedFingerprint != fingerprint {
		return receipt, &ports.Failure{Code: ports.FailureConflict, Message: "operation ID was already used for a different intent"}
	}
	err = json.Unmarshal([]byte(encoded), &receipt)
	return receipt, err
}

func (d *Database) NativeOperation(ctx context.Context, id, fingerprint string) (tracker.Receipt, error) {
	return nativeOperation(ctx, d.sql, id, fingerprint)
}

func (d *Database) MutateNativeTicket(ctx context.Context, request statestore.NativeTicketMutation) (tracker.Receipt, error) {
	tx, err := d.sql.BeginTx(ctx, nil)
	if err != nil {
		return tracker.Receipt{}, err
	}
	defer func() {
		_ = tx.Rollback()
	}()
	previous, err := nativeOperation(ctx, tx, request.Receipt.OperationID, request.OperationFingerprint)
	if err == nil {
		return previous, nil
	}
	var failure *ports.Failure
	if !errors.As(err, &failure) || failure.Code != ports.FailureNotFound {
		return tracker.Receipt{}, err
	}
	v := request.Ticket
	if v.Revision != request.ExpectedRevision+1 || request.OperationFingerprint == "" || request.Receipt.OperationID == "" || request.Receipt.Target.ID != v.ID || request.Receipt.Target.Namespace.ScopeID != v.ProjectID || request.Receipt.ObservedRevision != strconv.FormatUint(v.Revision, 10) || !json.Valid(request.Request) || request.Kind == "" || strings.TrimSpace(v.Title) == "" || v.Priority < 0 {
		return tracker.Receipt{}, &ports.Failure{Code: ports.FailureInvalidRequest, Message: "inconsistent native mutation"}
	}
	switch v.State {
	case statestore.NativeOpen, statestore.NativeActive, statestore.NativeCompleted, statestore.NativeCancelled:
	default:
		return tracker.Receipt{}, &ports.Failure{Code: ports.FailureInvalidRequest, Message: "invalid native business state"}
	}
	assignees, _ := json.Marshal(v.Assignees)
	labels, _ := json.Marshal(v.Labels)
	relations, _ := json.Marshal(v.Relationships)
	evidence, _ := json.Marshal(v.Evidence)
	if request.ExpectedRevision == 0 {
		_, err = tx.ExecContext(ctx, `INSERT INTO native_tickets VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)`, v.ID, v.ProjectID, v.Title, v.Description, v.State, v.Priority, v.Revision, string(assignees), string(labels), string(relations), string(evidence), formatTime(v.CreatedAt), formatTime(v.UpdatedAt))
	} else {
		var result sql.Result
		result, err = tx.ExecContext(ctx, `UPDATE native_tickets SET title=?,description=?,business_state=?,priority=?,revision=?,assignees_json=?,labels_json=?,relationships_json=?,evidence_json=?,updated_at=? WHERE ticket_id=? AND project_id=? AND revision=?`, v.Title, v.Description, v.State, v.Priority, v.Revision, string(assignees), string(labels), string(relations), string(evidence), formatTime(v.UpdatedAt), v.ID, v.ProjectID, request.ExpectedRevision)
		if err == nil {
			count, countErr := result.RowsAffected()
			if countErr != nil {
				return tracker.Receipt{}, countErr
			}
			if count != 1 {
				return tracker.Receipt{}, &ports.Failure{Code: ports.FailureConflict, Message: "native ticket revision changed"}
			}
		}
	}
	if err != nil {
		return tracker.Receipt{}, err
	}
	// Read back within the transaction before recording any success receipt.
	observed, err := scanNativeTicket(tx.QueryRowContext(ctx, nativeSelect+` WHERE ticket_id=? AND project_id=?`, v.ID, v.ProjectID))
	if err != nil {
		return tracker.Receipt{}, err
	}
	if !reflect.DeepEqual(observed, v) {
		return tracker.Receipt{}, &ports.Failure{Code: ports.FailureConflict, Message: "native read-back differs from requested effect"}
	}
	snapshot, err := json.Marshal(observed)
	if err != nil {
		return tracker.Receipt{}, err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO native_ticket_history VALUES (?,?,?,?,?,?,?)`, v.ID, v.Revision, request.Kind, request.Receipt.EvidenceRef, string(snapshot), string(request.Request), formatTime(v.UpdatedAt))
	if err != nil {
		return tracker.Receipt{}, err
	}
	receiptJSON, err := json.Marshal(request.Receipt)
	if err != nil {
		return tracker.Receipt{}, err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO native_ticket_operations VALUES (?,?,?)`, request.Receipt.OperationID, request.OperationFingerprint, string(receiptJSON))
	if err != nil {
		return tracker.Receipt{}, err
	}
	if err := tx.Commit(); err != nil {
		return tracker.Receipt{}, &ports.Failure{Code: ports.FailureUncertain, Message: "native commit outcome requires reconciliation"}
	}
	return request.Receipt, nil
}
