package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"darkstar/src/core/trackercontract"
	"darkstar/src/ports"
	"darkstar/src/ports/statestore"
	"darkstar/src/ports/tracker"
)

var _ statestore.TicketExecutionStore = (*Database)(nil)

const lineageSelect = `SELECT l.work_id,l.revision,l.project_id,l.binding_revision,l.ticket_key,l.ref_json,l.origin,l.created_at FROM source_work_lineages l`
const admissionSelect = `SELECT admission_id,work_id,project_id,observation_id,lineage_revision,binding_revision,approved_at,actor FROM source_ticket_admissions`

func scanLineage(row interface{ Scan(...any) error }) (statestore.SourceLineage, error) {
	var value statestore.SourceLineage
	var ref, created string
	if err := row.Scan(&value.WorkID, &value.Revision, &value.ProjectID, &value.BindingRevision, &value.TicketKey, &ref, &value.Origin, &created); err != nil {
		return value, backlogNormalize(err)
	}
	if err := strictBacklogJSON([]byte(ref), &value.Ref); err != nil {
		return value, err
	}
	var err error
	value.CreatedAt, err = parseTime(created)
	return value, err
}

func physicalLineage(ctx context.Context, query rowQueryer, work string) (statestore.SourceLineage, error) {
	return scanLineage(query.QueryRowContext(ctx, lineageSelect+` JOIN source_work_bindings b ON b.work_id=l.work_id AND b.lineage_revision=l.revision WHERE l.work_id=?`, work))
}

func workLineage(ctx context.Context, query rowQueryer, work string) (statestore.SourceLineage, error) {
	value, err := physicalLineage(ctx, query, work)
	if err == nil || !sourceNotFound(err) {
		return value, err
	}
	// Legacy native mappings are immutable migration evidence. Projecting their
	// source identity avoids inventing approvals or rewriting old events.
	var project, ticket, created string
	err = query.QueryRowContext(ctx, `SELECT m.project_id,m.ticket_id,n.created_at FROM native_work_mappings m JOIN native_tickets n ON n.ticket_id=m.ticket_id WHERE m.work_id=? AND m.mapping_kind='native'`, work).Scan(&project, &ticket, &created)
	if err != nil {
		return value, backlogNormalize(err)
	}
	ref := tracker.TicketRef{Namespace: tracker.Namespace{Provider: "built_in", Host: "darkstar.local", TenantID: "local", ScopeID: project}, ID: ticket}
	encoded, _ := json.Marshal(ref)
	createdAt, err := parseTime(created)
	if err != nil {
		return value, err
	}
	var legacy int
	if err := query.QueryRowContext(ctx, `SELECT count(*) FROM source_legacy_work WHERE work_id=?`, work).Scan(&legacy); err != nil {
		return value, err
	}
	origin := statestore.SourceNative
	if legacy != 0 {
		origin = statestore.SourceLegacyNative
	}
	return statestore.SourceLineage{WorkID: work, ProjectID: project, TicketKey: sourceHash(encoded), Revision: 1, BindingRevision: 1, Ref: ref, Origin: origin, CreatedAt: createdAt}, nil
}

func (d *Database) WorkTicketLineage(ctx context.Context, work string) (statestore.SourceLineage, error) {
	return workLineage(ctx, d.sql, work)
}

func (d *Database) WorkTicketLineages(ctx context.Context, work string) ([]statestore.SourceLineage, error) {
	rows, err := d.sql.QueryContext(ctx, lineageSelect+` WHERE l.work_id=? ORDER BY revision`, work)
	if err != nil {
		return nil, err
	}
	values := []statestore.SourceLineage{}
	for rows.Next() {
		value, err := scanLineage(rows)
		if err != nil {
			_ = rows.Close()
			return nil, err
		}
		values = append(values, value)
	}
	readErr := rows.Err()
	_ = rows.Close()
	if readErr != nil {
		return nil, readErr
	}
	if len(values) == 0 {
		legacy, err := workLineage(ctx, d.sql, work)
		if err == nil {
			values = append(values, legacy)
		} else if !sourceNotFound(err) {
			return nil, err
		}
	}
	return values, nil
}

func scanAdmission(row interface{ Scan(...any) error }) (statestore.TicketAdmission, error) {
	var value statestore.TicketAdmission
	var approved string
	if err := row.Scan(&value.ID, &value.WorkID, &value.ProjectID, &value.ObservationID, &value.LineageRevision, &value.BindingRevision, &approved, &value.Actor); err != nil {
		return value, backlogNormalize(err)
	}
	var err error
	value.ApprovedAt, err = parseTime(approved)
	return value, err
}

func (d *Database) LatestTicketAdmission(ctx context.Context, work string) (statestore.TicketAdmission, error) {
	return scanAdmission(d.sql.QueryRowContext(ctx, admissionSelect+` WHERE work_id=? AND lineage_revision=(SELECT lineage_revision FROM source_work_bindings WHERE work_id=?) ORDER BY approved_at DESC,rowid DESC LIMIT 1`, work, work))
}

func admissionReplay(ctx context.Context, query rowQueryer, key, digest string) (statestore.TicketAdmission, error) {
	var saved string
	if err := query.QueryRowContext(ctx, `SELECT request_digest FROM source_ticket_admissions WHERE request_key=?`, key).Scan(&saved); err != nil {
		return statestore.TicketAdmission{}, backlogNormalize(err)
	}
	if saved != digest {
		return statestore.TicketAdmission{}, backlogFailure(ports.FailureConflict, "source approval idempotency key was used for another request")
	}
	return scanAdmission(query.QueryRowContext(ctx, admissionSelect+` WHERE request_key=?`, key))
}

func insertLineage(ctx context.Context, tx *sql.Tx, value statestore.SourceLineage) error {
	ref, _ := json.Marshal(value.Ref)
	_, err := tx.ExecContext(ctx, `INSERT INTO source_work_lineages VALUES (?,?,?,?,?,?,?,?)`, value.WorkID, value.Revision, value.ProjectID, value.BindingRevision, value.TicketKey, string(ref), value.Origin, formatTime(value.CreatedAt))
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO source_work_bindings VALUES (?,?,?,?) ON CONFLICT(work_id) DO UPDATE SET lineage_revision=excluded.lineage_revision,project_id=excluded.project_id,ticket_key=excluded.ticket_key`, value.WorkID, value.Revision, value.ProjectID, value.TicketKey)
	return err
}

func insertAdmission(ctx context.Context, tx *sql.Tx, value statestore.TicketAdmission, key, digest string) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO source_ticket_admissions VALUES (?,?,?,?,?,?,?,?,?,?)`, value.ID, key, digest, value.WorkID, value.LineageRevision, value.ProjectID, value.BindingRevision, value.ObservationID, formatTime(value.ApprovedAt), value.Actor)
	return err
}

func validAdmission(key, digest, id, actor string, at time.Time) bool {
	return len(key) >= 8 && len(key) <= 128 && strings.TrimSpace(key) == key && len(digest) == 64 && id != "" && strings.TrimSpace(actor) != "" && !at.IsZero()
}

func selectedObservation(ctx context.Context, tx *sql.Tx, project string, binding uint64, observation string) (statestore.BacklogCachedTicket, error) {
	var selected uint64
	if err := tx.QueryRowContext(ctx, `SELECT binding_revision FROM backlog_selected_sources WHERE project_id=?`, project).Scan(&selected); err != nil {
		return statestore.BacklogCachedTicket{}, backlogNormalize(err)
	}
	if selected != binding {
		return statestore.BacklogCachedTicket{}, backlogFailure(ports.FailureConflict, "selected source changed before approval")
	}
	cached, err := scanBacklogCache(tx.QueryRowContext(ctx, `SELECT `+backlogCacheColumns+` FROM backlog_cached_tickets c JOIN backlog_observations o ON o.observation_id=c.observation_id WHERE c.project_id=? AND c.binding_revision=? AND o.ticket_key=(SELECT ticket_key FROM backlog_observations WHERE observation_id=?)`, project, binding, observation))
	if err != nil {
		return cached, err
	}
	if cached.ObservationID != observation || cached.State != statestore.BacklogAvailable {
		return cached, backlogFailure(ports.FailureConflict, "approval must identify the current accessible source observation")
	}
	lineage := statestore.SourceLineage{ProjectID: project, BindingRevision: binding, TicketKey: cached.TicketKey, Ref: cached.Observation.Ref}
	current, _, err := currentSource(ctx, tx, lineage)
	if err != nil {
		return cached, err
	}
	if current.State != statestore.BacklogAvailable {
		return current, backlogFailure(ports.FailureConflict, "source access changed before approval")
	}
	return current, nil
}

func (d *Database) AdmitSourceTicket(ctx context.Context, request statestore.SourceAdmissionMutation) (statestore.TicketAdmission, error) {
	if !validAdmission(request.IdempotencyKey, request.RequestDigest, request.AdmissionID, request.Actor, request.ApprovedAt) || request.ProjectID == "" || request.BindingRevision == 0 || request.ObservationID == "" {
		return statestore.TicketAdmission{}, backlogFailure(ports.FailureInvalidRequest, "source admission requires exact binding, observation and request identity")
	}
	tx, err := d.sql.BeginTx(ctx, nil)
	if err != nil {
		return statestore.TicketAdmission{}, err
	}
	defer func() {
		_ = tx.Rollback()
	}()
	// Reserve writer ownership before reads, including across daemon connections.
	if _, err := tx.ExecContext(ctx, `UPDATE global_positions SET last_position=last_position WHERE singleton=1`); err != nil {
		return statestore.TicketAdmission{}, err
	}
	if previous, err := admissionReplay(ctx, tx, request.IdempotencyKey, request.RequestDigest); err == nil {
		return previous, nil
	} else if !sourceNotFound(err) {
		return statestore.TicketAdmission{}, err
	}
	var projectStatus string
	if err := tx.QueryRowContext(ctx, `SELECT status FROM project_projection WHERE project_id=?`, request.ProjectID).Scan(&projectStatus); err != nil {
		return statestore.TicketAdmission{}, backlogNormalize(err)
	}
	if projectStatus != "active" {
		return statestore.TicketAdmission{}, backlogFailure(ports.FailureConflict, "archived project cannot admit execution")
	}
	var cached statestore.BacklogCachedTicket
	var lineage statestore.SourceLineage
	if request.ExistingWorkID != "" {
		lineage, err = workLineage(ctx, tx, request.ExistingWorkID)
		if err == nil && (lineage.ProjectID != request.ProjectID || lineage.BindingRevision != request.BindingRevision) {
			err = backlogFailure(ports.FailureConflict, "approval cannot retarget existing work lineage")
		}
		if err == nil {
			cached, _, err = currentSource(ctx, tx, lineage)
		}
		if err == nil && (cached.ObservationID != request.ObservationID || cached.State != statestore.BacklogAvailable) {
			err = backlogFailure(ports.FailureConflict, "approval must identify the current accessible pinned observation")
		}
	} else {
		cached, err = selectedObservation(ctx, tx, request.ProjectID, request.BindingRevision, request.ObservationID)
	}
	if err != nil {
		return statestore.TicketAdmission{}, err
	}
	if err := validateSourceAdmission(ctx, tx, request.ProjectID, request.BindingRevision, cached, request.ApprovedAt, request.ObservedNotBefore); err != nil {
		return statestore.TicketAdmission{}, err
	}
	ticket, err := trackercontract.DecodeTicket(cached.Observation.Ticket)
	if err != nil {
		return statestore.TicketAdmission{}, err
	}
	if request.ExistingWorkID == "" {
		lineage, err = scanLineage(tx.QueryRowContext(ctx, lineageSelect+` JOIN source_work_bindings b ON b.work_id=l.work_id AND b.lineage_revision=l.revision WHERE b.project_id=? AND b.ticket_key=?`, request.ProjectID, cached.TicketKey))
		if err != nil && !sourceNotFound(err) {
			return statestore.TicketAdmission{}, err
		}
		if sourceNotFound(err) {
			workID := request.WorkID
			if ticket.Ref.Namespace.Provider == "built_in" {
				var legacy string
				err := tx.QueryRowContext(ctx, `SELECT work_id FROM native_work_mappings WHERE project_id=? AND ticket_id=? AND mapping_kind='native'`, request.ProjectID, ticket.Ref.ID).Scan(&legacy)
				if err == nil {
					workID = legacy
				} else if !errors.Is(err, sql.ErrNoRows) {
					return statestore.TicketAdmission{}, err
				}
			}
			lineage = statestore.SourceLineage{WorkID: workID, ProjectID: request.ProjectID, TicketKey: cached.TicketKey, Revision: 1, BindingRevision: request.BindingRevision, Ref: ticket.Ref, Origin: statestore.SourceAdmitted, CreatedAt: request.ApprovedAt}
		}
	}
	if lineage.Ref != ticket.Ref || lineage.BindingRevision != request.BindingRevision {
		return statestore.TicketAdmission{}, backlogFailure(ports.FailureConflict, "existing local work belongs to another source ticket")
	}
	if existing, err := physicalLineage(ctx, tx, lineage.WorkID); err == nil {
		if existing.Ref != lineage.Ref || existing.BindingRevision != lineage.BindingRevision {
			return statestore.TicketAdmission{}, backlogFailure(ports.FailureConflict, "historical ticket work was rebound; explicit lineage assessment is required")
		}
		lineage = existing
	} else if !sourceNotFound(err) {
		return statestore.TicketAdmission{}, err
	}
	if _, err := physicalLineage(ctx, tx, lineage.WorkID); sourceNotFound(err) {
		if _, err := readWorkItemProjection(ctx, tx, lineage.WorkID); sourceNotFound(err) {
			if request.NewWork.AggregateID != lineage.WorkID || request.NewWork.Kind != "work.created" || request.NewWork.AggregateType != statestore.AggregateWork {
				return statestore.TicketAdmission{}, backlogFailure(ports.FailureInvalidRequest, "new source work requires its exact creation event")
			}
			var data struct{ ProjectID, Title, Details string }
			if json.Unmarshal(request.NewWork.Data, &data) != nil || data.ProjectID != request.ProjectID || data.Title != ticket.Title || data.Details != ticket.Description {
				return statestore.TicketAdmission{}, backlogFailure(ports.FailureInvalidRequest, "new local work must retain the approved source content")
			}
			request.NewWork.Metadata, _ = json.Marshal(map[string]any{"sourceAdmissionId": request.AdmissionID})
			if _, err := d.appendInTransaction(ctx, tx, request.NewWork); err != nil {
				return statestore.TicketAdmission{}, err
			}
		} else if err != nil {
			return statestore.TicketAdmission{}, err
		}
		lineage.Origin = statestore.SourceAdmitted
		if err := insertLineage(ctx, tx, lineage); err != nil {
			return statestore.TicketAdmission{}, err
		}
	} else if err != nil {
		return statestore.TicketAdmission{}, err
	}
	if err := ensureSourceWorkUsable(ctx, tx, lineage.WorkID); err != nil {
		return statestore.TicketAdmission{}, err
	}
	value := statestore.TicketAdmission{ID: request.AdmissionID, WorkID: lineage.WorkID, ProjectID: request.ProjectID, ObservationID: request.ObservationID, LineageRevision: lineage.Revision, BindingRevision: lineage.BindingRevision, ApprovedAt: request.ApprovedAt, Actor: request.Actor}
	if err := insertAdmission(ctx, tx, value, request.IdempotencyKey, request.RequestDigest); err != nil {
		return value, err
	}
	if err := tx.Commit(); err != nil {
		return value, err
	}
	return value, nil
}

func validateSourceAdmission(ctx context.Context, query rowQueryer, project string, revision uint64, cached statestore.BacklogCachedTicket, now, cutoff time.Time) error {
	if cutoff.IsZero() {
		cutoff = now.Add(-5 * time.Minute)
	}
	if cached.State != statestore.BacklogAvailable || cached.CheckedAt.Before(cutoff) || cached.CheckedAt.After(now.Add(time.Minute)) {
		return backlogFailure(ports.FailureConflict, "source must be checked recently before approving execution")
	}
	ticket, err := trackercontract.DecodeTicket(cached.Observation.Ticket)
	if err != nil {
		return err
	}
	if archived, ok := ticket.Archived.(tracker.Known[bool]); ok && archived.Value {
		return backlogFailure(ports.FailureConflict, "archived source ticket cannot acquire new execution authority")
	}
	if state, ok := ticket.BusinessState.(tracker.Known[tracker.NamedID]); ok && ticket.Ref.Namespace.Provider == "built_in" && state.Value.ID == "cancelled" {
		return backlogFailure(ports.FailureConflict, "cancelled native ticket requires action before execution")
	}
	binding, err := scanBacklogBinding(query.QueryRowContext(ctx, `SELECT project_id,revision,source_json,selected_at FROM backlog_bindings WHERE project_id=? AND revision=?`, project, revision))
	if err != nil {
		return err
	}
	if placement, ok := ticket.Placement.(tracker.Known[tracker.Scope]); ok && placement.Value != bindingScope(binding) {
		return backlogFailure(ports.FailureConflict, "source ticket moved outside its pinned scope")
	}
	return nil
}

func ensureSourceWorkUsable(ctx context.Context, query rowQueryer, work string) error {
	value, err := readWorkItemProjection(ctx, query, work)
	if err != nil {
		return err
	}
	if value.Deletion != statestore.WorkRetained {
		return backlogFailure(ports.FailureConflict, "deleted or deleting work cannot acquire source execution authority")
	}
	return nil
}

func sourceNotFound(err error) bool {
	var value *ports.Failure
	return errors.Is(err, statestore.ErrNotFound) || errors.Is(err, sql.ErrNoRows) || errors.As(err, &value) && value.Code == ports.FailureNotFound
}
