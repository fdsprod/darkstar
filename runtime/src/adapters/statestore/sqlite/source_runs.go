package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"darkstar/src/core/trackercontract"
	"darkstar/src/ports"
	"darkstar/src/ports/statestore"
	"darkstar/src/ports/tracker"
)

func approvedRunSource(ctx context.Context, query rowQueryer, work, observation string, now time.Time) (statestore.RunSourceSnapshot, error) {
	lineage, err := physicalLineage(ctx, query, work)
	if err != nil {
		return statestore.RunSourceSnapshot{}, err
	}
	admission, err := scanAdmission(query.QueryRowContext(ctx, admissionSelect+` WHERE work_id=? AND lineage_revision=? AND observation_id=? ORDER BY approved_at DESC,rowid DESC LIMIT 1`, work, lineage.Revision, observation))
	if err != nil {
		return statestore.RunSourceSnapshot{}, backlogFailure(ports.FailureConflict, "run requires explicit approval of the exact source observation")
	}
	current, pin, err := currentSource(ctx, query, lineage)
	if err != nil {
		return statestore.RunSourceSnapshot{}, err
	}
	if current.ObservationID != observation || current.State != statestore.BacklogAvailable || current.Observation.Ref != lineage.Ref {
		return statestore.RunSourceSnapshot{}, backlogFailure(ports.FailureConflict, "approved source content changed or became inaccessible; assessment is required")
	}
	if err := validateSourceAdmission(ctx, query, lineage.ProjectID, lineage.BindingRevision, current, now, time.Time{}); err != nil {
		return statestore.RunSourceSnapshot{}, err
	}
	if trackercontract.ValidatePin(pin, pin) != nil || pin.BindingRevision != fmt.Sprint(lineage.BindingRevision) || pin.AdapterID != lineage.Ref.Namespace.Provider {
		return statestore.RunSourceSnapshot{}, backlogFailure(ports.FailureProtocolDrift, "approved source pin differs from immutable work lineage")
	}
	ticket, err := trackercontract.DecodeTicket(current.Observation.Ticket)
	if err != nil {
		return statestore.RunSourceSnapshot{}, err
	}
	if archived, ok := ticket.Archived.(tracker.Known[bool]); ok && archived.Value {
		return statestore.RunSourceSnapshot{}, backlogFailure(ports.FailureConflict, "archived source ticket requires action before a new run")
	}
	if placement, ok := ticket.Placement.(tracker.Known[tracker.Scope]); ok {
		binding, err := scanBacklogBinding(query.QueryRowContext(ctx, `SELECT project_id,revision,source_json,selected_at FROM backlog_bindings WHERE project_id=? AND revision=?`, lineage.ProjectID, lineage.BindingRevision))
		if err != nil || placement.Value != bindingScope(binding) {
			return statestore.RunSourceSnapshot{}, backlogFailure(ports.FailureConflict, "source ticket moved outside its approved scope")
		}
	}
	rulePin, err := readSourceRulePin(ctx, query, admission.ID)
	if err != nil {
		return statestore.RunSourceSnapshot{}, err
	}
	return statestore.RunSourceSnapshot{WorkID: work, AdmissionID: admission.ID, ObservationID: observation, LineageRevision: lineage.Revision, BindingRevision: lineage.BindingRevision, Ref: lineage.Ref, Pin: pin, Ticket: current.Observation.Ticket, ApprovedAt: admission.ApprovedAt, RulePin: rulePin}, nil
}

func (d *Database) ApprovedRunSource(ctx context.Context, work, observation string) (statestore.RunSourceSnapshot, error) {
	return approvedRunSource(ctx, d.sql, work, observation, d.now().UTC())
}

func bindingScope(binding statestore.BacklogBinding) tracker.Scope {
	switch source := binding.Source.(type) {
	case statestore.NativeBacklogSource:
		return tracker.Scope{Namespace: source.Namespace, ContainerID: source.Namespace.ScopeID}
	case statestore.ExternalBacklogSource:
		return source.Scope
	default:
		return tracker.Scope{}
	}
}

func sourceWorkSettled(ctx context.Context, query rowQueryer, work, excludingRun string) error {
	var unsettled int
	err := query.QueryRowContext(ctx, `SELECT count(*) FROM run_projection WHERE work_item_id=? AND run_id!=? AND status NOT IN ('completed','cancelled')`, work, excludingRun).Scan(&unsettled)
	if err != nil {
		return err
	}
	if unsettled != 0 {
		return backlogFailure(ports.FailureConflict, "another run retains source execution ownership; settle or cancel it explicitly")
	}
	err = query.QueryRowContext(ctx, `SELECT count(*) FROM attempt_projection a JOIN run_projection r ON r.run_id=a.run_id WHERE r.work_item_id=? AND r.run_id!=? AND a.status NOT IN ('succeeded','failed','cancelled')`, work, excludingRun).Scan(&unsettled)
	if err != nil {
		return err
	}
	if unsettled != 0 {
		return backlogFailure(ports.FailureConflict, "a source execution attempt remains live or requires reconciliation")
	}
	err = query.QueryRowContext(ctx, `SELECT count(*) FROM outbox WHERE state!='committed' AND (aggregate_id=? OR aggregate_id IN (SELECT run_id FROM run_projection WHERE work_item_id=? AND run_id!=?) OR json_extract(request_json,'$.workItemId')=?)`, work, work, excludingRun, work).Scan(&unsettled)
	if err != nil {
		return err
	}
	if unsettled != 0 {
		return backlogFailure(ports.FailureConflict, "old source operations require settlement or reconciliation")
	}
	return nil
}

func (d *Database) prepareTicketExecutionEvent(ctx context.Context, tx *sql.Tx, event statestore.PendingEvent) (*statestore.RunSourceSnapshot, error) {
	switch event.Kind {
	case "run.created", "run.started", "run.retried", "run.resumed", "run.continued":
	default:
		return nil, nil
	}
	// Older migration fixtures and legacy stores can append before this schema
	// version exists. No source-admission capability is available at those versions.
	var exists int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_schema WHERE type='table' AND name='source_work_bindings'`).Scan(&exists); err != nil || exists == 0 {
		return nil, err
	}
	var work string
	if event.Kind == "run.created" {
		var data struct {
			WorkItemID string `json:"workItemId"`
		}
		if err := json.Unmarshal(event.Data, &data); err != nil {
			return nil, err
		}
		work = data.WorkItemID
	} else if err := tx.QueryRowContext(ctx, `SELECT work_item_id FROM run_projection WHERE run_id=?`, event.AggregateID).Scan(&work); err != nil {
		return nil, backlogNormalize(err)
	}
	lineage, err := workLineage(ctx, tx, work)
	if sourceNotFound(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var admissions int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM source_ticket_admissions WHERE work_id=? AND lineage_revision=?`, work, lineage.Revision).Scan(&admissions); err != nil {
		return nil, err
	}
	if lineage.Origin == statestore.SourceLegacyNative && admissions == 0 {
		return nil, nil
	}
	if err := sourceWorkSettled(ctx, tx, work, event.AggregateID); err != nil {
		return nil, err
	}
	if event.Kind != "run.created" {
		// Active inputs stay frozen even if later source observations change.
		return nil, nil
	}
	var metadata struct {
		ObservationID   string `json:"sourceObservationId"`
		AdmissionID     string `json:"sourceAdmissionId"`
		LineageRevision uint64 `json:"sourceLineageRevision"`
		PinDigest       string `json:"sourcePinDigest"`
	}
	if json.Unmarshal(event.Metadata, &metadata) != nil || metadata.ObservationID == "" {
		return nil, backlogFailure(ports.FailureConflict, "source-backed run requires an explicit approved source observation")
	}
	snapshot, err := approvedRunSource(ctx, tx, work, metadata.ObservationID, d.now().UTC())
	if err != nil {
		return nil, err
	}
	if (metadata.AdmissionID != "" && metadata.AdmissionID != snapshot.AdmissionID) || (metadata.LineageRevision != 0 && metadata.LineageRevision != snapshot.LineageRevision) {
		return nil, backlogFailure(ports.FailureConflict, "source approval or lineage changed during run preparation")
	}
	pinJSON, err := json.Marshal(snapshot.Pin)
	if err != nil || metadata.PinDigest != sourceHash(pinJSON) {
		return nil, backlogFailure(ports.FailureConflict, "source configuration changed during run preparation")
	}
	snapshot.RunID, snapshot.CapturedAt = event.AggregateID, d.now().UTC()
	return &snapshot, nil
}

type sourceSnapshotJSON struct {
	Version  string                       `json:"version"`
	Snapshot statestore.RunSourceSnapshot `json:"snapshot"`
}

func persistRunSourceSnapshot(ctx context.Context, tx *sql.Tx, value statestore.RunSourceSnapshot) error {
	encoded, err := json.Marshal(sourceSnapshotJSON{Version: "darkstar.run-source/v1", Snapshot: value})
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO run_source_snapshots VALUES (?,?,?,?,?)`, value.RunID, value.WorkID, value.AdmissionID, value.ObservationID, string(encoded))
	return err
}

func (d *Database) RunSourceSnapshot(ctx context.Context, run string) (statestore.RunSourceSnapshot, error) {
	var encoded, work, admission, observation string
	err := d.sql.QueryRowContext(ctx, `SELECT snapshot_json,work_id,admission_id,observation_id FROM run_source_snapshots WHERE run_id=?`, run).Scan(&encoded, &work, &admission, &observation)
	if err != nil {
		return statestore.RunSourceSnapshot{}, backlogNormalize(err)
	}
	var envelope sourceSnapshotJSON
	if strictBacklogJSON([]byte(encoded), &envelope) != nil || envelope.Version != "darkstar.run-source/v1" {
		return statestore.RunSourceSnapshot{}, backlogFailure(ports.FailureProtocolDrift, "unsupported run source snapshot encoding")
	}
	value := envelope.Snapshot
	ticket, err := trackercontract.DecodeTicket(value.Ticket)
	if err != nil || value.RunID != run || value.WorkID != work || value.AdmissionID != admission || value.ObservationID != observation || ticket.Ref != value.Ref || value.CapturedAt.IsZero() || trackercontract.ValidatePin(value.Pin, value.Pin) != nil {
		return statestore.RunSourceSnapshot{}, backlogFailure(ports.FailureProtocolDrift, "run source snapshot identity or content is invalid")
	}
	id, key, digest, err := trackercontract.ObservationIdentity(ticket)
	if err != nil || id != value.ObservationID {
		return statestore.RunSourceSnapshot{}, backlogFailure(ports.FailureProtocolDrift, "run source snapshot differs from its immutable observation")
	}
	retained, err := d.BacklogObservation(ctx, value.ObservationID)
	if err != nil || retained.ContentDigest != digest || retained.TicketKey != key || string(retained.Ticket) != string(value.Ticket) {
		return statestore.RunSourceSnapshot{}, backlogFailure(ports.FailureProtocolDrift, "run source content differs from retained source evidence")
	}
	approved, err := scanAdmission(d.sql.QueryRowContext(ctx, admissionSelect+` WHERE admission_id=?`, value.AdmissionID))
	if err != nil || approved.WorkID != value.WorkID || approved.ObservationID != value.ObservationID || approved.LineageRevision != value.LineageRevision || approved.BindingRevision != value.BindingRevision || !approved.ApprovedAt.Equal(value.ApprovedAt) || value.CapturedAt.Before(value.ApprovedAt) {
		return statestore.RunSourceSnapshot{}, backlogFailure(ports.FailureProtocolDrift, "run source snapshot differs from its exact approval")
	}
	lineage, err := scanLineage(d.sql.QueryRowContext(ctx, lineageSelect+` WHERE l.work_id=? AND l.revision=?`, value.WorkID, value.LineageRevision))
	if err != nil || lineage.ProjectID != approved.ProjectID || lineage.BindingRevision != value.BindingRevision || lineage.Ref != value.Ref || lineage.TicketKey != key {
		return statestore.RunSourceSnapshot{}, backlogFailure(ports.FailureProtocolDrift, "run source snapshot differs from its historical lineage")
	}
	var sourceJSON string
	if err := d.sql.QueryRowContext(ctx, `SELECT source_json FROM backlog_bindings WHERE project_id=? AND revision=?`, lineage.ProjectID, value.BindingRevision).Scan(&sourceJSON); err != nil {
		return statestore.RunSourceSnapshot{}, backlogFailure(ports.FailureProtocolDrift, "run source binding history is unavailable")
	}
	namespace, err := backlogSourceNamespace(lineage.ProjectID, sourceJSON, value.Pin)
	if err != nil || namespace != value.Ref.Namespace || value.Pin.BindingRevision != fmt.Sprint(value.BindingRevision) {
		return statestore.RunSourceSnapshot{}, backlogFailure(ports.FailureProtocolDrift, "run source pin differs from its historical binding")
	}
	var metadataJSON string
	if err := d.sql.QueryRowContext(ctx, `SELECT metadata_json FROM events WHERE aggregate_id=? AND kind='run.created'`, run).Scan(&metadataJSON); err != nil {
		return statestore.RunSourceSnapshot{}, backlogFailure(ports.FailureProtocolDrift, "run source creation evidence is unavailable")
	}
	var metadata struct {
		PinDigest string `json:"sourcePinDigest"`
	}
	pinJSON, pinErr := json.Marshal(value.Pin)
	if json.Unmarshal([]byte(metadataJSON), &metadata) != nil || pinErr != nil || metadata.PinDigest == "" || metadata.PinDigest != sourceHash(pinJSON) {
		return statestore.RunSourceSnapshot{}, backlogFailure(ports.FailureProtocolDrift, "run source pin differs from its frozen creation evidence")
	}
	return value, nil
}

func (d *Database) RebindSourceTicket(ctx context.Context, request statestore.SourceRebindMutation) (statestore.TicketAdmission, error) {
	if !validAdmission(request.IdempotencyKey, request.RequestDigest, request.AdmissionID, request.Actor, request.ApprovedAt) || request.ExpectedLineageRevision == 0 {
		return statestore.TicketAdmission{}, backlogFailure(ports.FailureInvalidRequest, "rebind requires exact lineage and approved request identity")
	}
	tx, err := d.sql.BeginTx(ctx, nil)
	if err != nil {
		return statestore.TicketAdmission{}, err
	}
	defer func() {
		_ = tx.Rollback()
	}()
	if _, err := tx.ExecContext(ctx, `UPDATE global_positions SET last_position=last_position WHERE singleton=1`); err != nil {
		return statestore.TicketAdmission{}, err
	}
	if existing, err := admissionReplay(ctx, tx, request.IdempotencyKey, request.RequestDigest); err == nil {
		return existing, nil
	} else if !sourceNotFound(err) {
		return statestore.TicketAdmission{}, err
	}
	lineage, err := workLineage(ctx, tx, request.WorkID)
	if err != nil {
		return statestore.TicketAdmission{}, err
	}
	if lineage.Revision != request.ExpectedLineageRevision {
		return statestore.TicketAdmission{}, backlogFailure(ports.FailureConflict, "work source lineage changed")
	}
	if err := ensureSourceWorkUsable(ctx, tx, request.WorkID); err != nil {
		return statestore.TicketAdmission{}, err
	}
	if err := sourceWorkSettled(ctx, tx, request.WorkID, ""); err != nil {
		return statestore.TicketAdmission{}, err
	}
	target, err := selectedObservation(ctx, tx, lineage.ProjectID, request.BindingRevision, request.ObservationID)
	if err != nil {
		return statestore.TicketAdmission{}, err
	}
	if err := validateSourceAdmission(ctx, tx, lineage.ProjectID, request.BindingRevision, target, request.ApprovedAt, request.ObservedNotBefore); err != nil {
		return statestore.TicketAdmission{}, err
	}
	if target.Observation.Ref.Namespace.Provider == "built_in" {
		var nativeOwner string
		err := tx.QueryRowContext(ctx, `SELECT work_id FROM native_work_mappings WHERE ticket_id=? AND project_id=? AND work_id!=? AND mapping_kind='native'`, target.Observation.Ref.ID, lineage.ProjectID, request.WorkID).Scan(&nativeOwner)
		if err == nil {
			return statestore.TicketAdmission{}, backlogFailure(ports.FailureConflict, "target native ticket already has a local execution record")
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return statestore.TicketAdmission{}, err
		}
	}
	var competing string
	err = tx.QueryRowContext(ctx, `SELECT work_id FROM source_work_bindings WHERE project_id=? AND ticket_key=? AND work_id!=?`, lineage.ProjectID, target.TicketKey, request.WorkID).Scan(&competing)
	if err == nil {
		return statestore.TicketAdmission{}, backlogFailure(ports.FailureConflict, "target ticket already belongs to another local execution record")
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return statestore.TicketAdmission{}, err
	}
	if _, err := physicalLineage(ctx, tx, request.WorkID); sourceNotFound(err) {
		if err := insertLineage(ctx, tx, lineage); err != nil {
			return statestore.TicketAdmission{}, err
		}
	} else if err != nil {
		return statestore.TicketAdmission{}, err
	}
	lineage.Revision++
	lineage.BindingRevision, lineage.Ref, lineage.TicketKey = request.BindingRevision, target.Observation.Ref, target.TicketKey
	lineage.Origin, lineage.CreatedAt = statestore.SourceAdmitted, request.ApprovedAt
	if err := insertLineage(ctx, tx, lineage); err != nil {
		return statestore.TicketAdmission{}, err
	}
	value := statestore.TicketAdmission{ID: request.AdmissionID, WorkID: request.WorkID, ProjectID: lineage.ProjectID, ObservationID: request.ObservationID, LineageRevision: lineage.Revision, BindingRevision: lineage.BindingRevision, ApprovedAt: request.ApprovedAt, Actor: request.Actor}
	if err := insertAdmission(ctx, tx, value, request.IdempotencyKey, request.RequestDigest); err != nil {
		return value, err
	}
	return value, tx.Commit()
}
