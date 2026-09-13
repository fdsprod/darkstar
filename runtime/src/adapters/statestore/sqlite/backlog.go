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

var _ statestore.BacklogStore = (*Database)(nil)

type backlogSourceJSON struct {
	Version            int                `json:"version"`
	Kind               string             `json:"kind"`
	Namespace          *tracker.Namespace `json:"namespace,omitempty"`
	ConnectionID       string             `json:"connectionId,omitempty"`
	ConnectionRevision string             `json:"connectionRevision,omitempty"`
	Scope              *tracker.Scope     `json:"scope,omitempty"`
}

func encodeBacklogSource(project string, source statestore.BacklogSource) ([]byte, error) {
	value := backlogSourceJSON{Version: 1}
	switch source := source.(type) {
	case statestore.NativeBacklogSource:
		if source.Namespace != (tracker.Namespace{Provider: "built_in", Host: "darkstar.local", TenantID: "local", ScopeID: project}) {
			return nil, backlogFailure(ports.FailureInvalidRequest, "native backlog namespace must match its project")
		}
		value.Kind, value.Namespace = "built_in", &source.Namespace
	case statestore.ExternalBacklogSource:
		namespace := source.Scope.Namespace
		if (namespace.Provider != "linear" && namespace.Provider != "github_issues") || namespace.Host == "" || namespace.TenantID == "" || namespace.ScopeID == "" || source.Scope.ContainerID == "" || strings.TrimSpace(source.ConnectionID) == "" || strings.TrimSpace(source.ConnectionRevision) == "" {
			return nil, backlogFailure(ports.FailureInvalidRequest, "external backlog source requires an exact connection revision and native scope")
		}
		value.Kind, value.ConnectionID, value.ConnectionRevision, value.Scope = "external", source.ConnectionID, source.ConnectionRevision, &source.Scope
	default:
		return nil, backlogFailure(ports.FailureInvalidRequest, "unknown backlog source variant")
	}
	return json.Marshal(value)
}

func decodeBacklogSource(project string, encoded []byte) (statestore.BacklogSource, error) {
	var value backlogSourceJSON
	if strictBacklogJSON(encoded, &value) != nil || value.Version != 1 {
		return nil, backlogFailure(ports.FailureProtocolDrift, "unsupported backlog source encoding")
	}
	var source statestore.BacklogSource
	switch {
	case value.Kind == "built_in" && value.Namespace != nil && value.Scope == nil && value.ConnectionID == "" && value.ConnectionRevision == "":
		source = statestore.NativeBacklogSource{Namespace: *value.Namespace}
	case value.Kind == "external" && value.Scope != nil && value.Namespace == nil:
		source = statestore.ExternalBacklogSource{ConnectionID: value.ConnectionID, ConnectionRevision: value.ConnectionRevision, Scope: *value.Scope}
	default:
		return nil, backlogFailure(ports.FailureProtocolDrift, "contradictory backlog source encoding")
	}
	if _, err := encodeBacklogSource(project, source); err != nil {
		return nil, err
	}
	return source, nil
}

func strictBacklogJSON(encoded []byte, result any) error {
	return trackercontract.DecodeClosedJSON(encoded, result)
}

func scanBacklogBinding(row interface{ Scan(...any) error }) (statestore.BacklogBinding, error) {
	var result statestore.BacklogBinding
	var source, selected string
	if err := row.Scan(&result.ProjectID, &result.Revision, &source, &selected); err != nil {
		return result, backlogNormalize(err)
	}
	var err error
	result.Source, err = decodeBacklogSource(result.ProjectID, []byte(source))
	if err == nil {
		result.SelectedAt, err = parseTime(selected)
	}
	return result, err
}

func (d *Database) BacklogBinding(ctx context.Context, project string) (statestore.BacklogBinding, error) {
	return scanBacklogBinding(d.sql.QueryRowContext(ctx, `SELECT b.project_id,b.revision,b.source_json,b.selected_at FROM backlog_bindings b JOIN backlog_selected_sources s ON s.project_id=b.project_id AND s.binding_revision=b.revision WHERE b.project_id=?`, project))
}

func (d *Database) BacklogBindingHistory(ctx context.Context, project string) ([]statestore.BacklogBinding, error) {
	rows, err := d.sql.QueryContext(ctx, `SELECT project_id,revision,source_json,selected_at FROM backlog_bindings WHERE project_id=? ORDER BY revision`, project)
	if err != nil {
		return nil, err
	}
	defer func() {
		_ = rows.Close()
	}()
	values := []statestore.BacklogBinding{}
	for rows.Next() {
		value, err := scanBacklogBinding(rows)
		if err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	return values, rows.Err()
}

func (d *Database) SelectBacklogSource(ctx context.Context, project string, expected uint64, source statestore.BacklogSource, selected time.Time) (statestore.BacklogBinding, error) {
	encoded, err := encodeBacklogSource(project, source)
	if err != nil || expected == 0 || selected.IsZero() {
		return statestore.BacklogBinding{}, backlogFailure(ports.FailureInvalidRequest, "source selection requires valid scope, expected binding revision and timestamp")
	}
	tx, err := d.sql.BeginTx(ctx, nil)
	if err != nil {
		return statestore.BacklogBinding{}, err
	}
	defer func() {
		_ = tx.Rollback()
	}()
	var current uint64
	if err := tx.QueryRowContext(ctx, `SELECT binding_revision FROM backlog_selected_sources WHERE project_id=?`, project).Scan(&current); err != nil {
		return statestore.BacklogBinding{}, backlogNormalize(err)
	}
	if current != expected {
		return statestore.BacklogBinding{}, backlogFailure(ports.FailureConflict, "selected backlog source changed")
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO backlog_bindings VALUES (?,?,?,?)`, project, current+1, string(encoded), formatTime(selected)); err != nil {
		return statestore.BacklogBinding{}, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE backlog_selected_sources SET binding_revision=? WHERE project_id=? AND binding_revision=?`, current+1, project, expected); err != nil {
		return statestore.BacklogBinding{}, err
	}
	if err := tx.Commit(); err != nil {
		return statestore.BacklogBinding{}, err
	}
	return statestore.BacklogBinding{ProjectID: project, Revision: current + 1, Source: source, SelectedAt: selected}, nil
}

func (d *Database) BacklogRefresh(ctx context.Context, project string, binding uint64) (statestore.BacklogRefreshState, error) {
	var encoded, sourceJSON string
	var revision uint64
	if err := d.sql.QueryRowContext(ctx, `SELECT r.revision,r.state_json,b.source_json FROM backlog_refreshes r JOIN backlog_bindings b ON b.project_id=r.project_id AND b.revision=r.binding_revision WHERE r.project_id=? AND r.binding_revision=?`, project, binding).Scan(&revision, &encoded, &sourceJSON); err != nil {
		return statestore.BacklogRefreshState{}, backlogNormalize(err)
	}
	var envelope backlogCheckpointJSON
	if strictBacklogJSON([]byte(encoded), &envelope) != nil || envelope.Version != "darkstar.backlog-checkpoint/v1" || !validBacklogCheckpoint(envelope.State) || envelope.State.ProjectID != project || envelope.State.BindingRevision != binding || envelope.State.Revision != revision {
		return statestore.BacklogRefreshState{}, backlogFailure(ports.FailureProtocolDrift, "backlog refresh checkpoint is invalid or differs from its index")
	}
	if _, err := backlogSourceNamespace(project, sourceJSON, envelope.State.Pin); err != nil {
		return statestore.BacklogRefreshState{}, backlogFailure(ports.FailureProtocolDrift, "backlog checkpoint pin differs from its source binding")
	}
	return envelope.State, nil
}

func backlogSourceNamespace(project, sourceJSON string, pin tracker.Pin) (tracker.Namespace, error) {
	source, err := decodeBacklogSource(project, []byte(sourceJSON))
	if err != nil {
		return tracker.Namespace{}, err
	}
	var namespace tracker.Namespace
	switch source := source.(type) {
	case statestore.NativeBacklogSource:
		namespace = source.Namespace
	case statestore.ExternalBacklogSource:
		namespace = source.Scope.Namespace
		if pin != (tracker.Pin{}) && (pin.InstallationID != source.ConnectionID || pin.ConfigRevision != source.ConnectionRevision) {
			return tracker.Namespace{}, backlogFailure(ports.FailureInvalidRequest, "refresh pin differs from the selected connection revision")
		}
	default:
		return tracker.Namespace{}, backlogFailure(ports.FailureProtocolDrift, "unsupported selected backlog source")
	}
	if pin != (tracker.Pin{}) && pin.AdapterID != namespace.Provider {
		return tracker.Namespace{}, backlogFailure(ports.FailureInvalidRequest, "refresh adapter differs from the selected source")
	}
	return namespace, nil
}

type backlogCheckpointJSON struct {
	Version string                         `json:"version"`
	State   statestore.BacklogRefreshState `json:"state"`
}

func validBacklogCheckpoint(value statestore.BacklogRefreshState) bool {
	if value.ProjectID == "" || value.BindingRevision == 0 || value.Revision == 0 || value.Generation == 0 || value.Query.Cursor != "" || value.UpdatedAt.IsZero() {
		return false
	}
	switch value.Phase {
	case statestore.BacklogRefreshing:
		return value.Failure == nil && !value.StartedAt.IsZero() && trackercontract.ValidatePin(value.Pin, value.Pin) == nil
	case statestore.BacklogComplete:
		return value.Failure == nil && value.Cursor == "" && !value.StartedAt.IsZero() && !value.LastSuccessAt.IsZero() && trackercontract.ValidatePin(value.Pin, value.Pin) == nil
	case statestore.BacklogFailed:
		return value.Failure != nil && value.Failures > 0 && ((value.Pin == tracker.Pin{} && value.Cursor == "") || trackercontract.ValidatePin(value.Pin, value.Pin) == nil)
	default:
		return false
	}
}

func (d *Database) CommitBacklogRefresh(ctx context.Context, commit statestore.BacklogCommit) error {
	state := commit.State
	if !validBacklogCheckpoint(state) || state.Revision != commit.ExpectedRevision+1 || len(commit.Tickets) > 1000 || len(commit.Observations) > 1000 {
		return backlogFailure(ports.FailureInvalidRequest, "inconsistent backlog checkpoint transaction")
	}
	tx, err := d.sql.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() {
		_ = tx.Rollback()
	}()
	var selected uint64
	var sourceJSON string
	if err := tx.QueryRowContext(ctx, `SELECT s.binding_revision,b.source_json FROM backlog_selected_sources s JOIN backlog_bindings b ON b.project_id=s.project_id AND b.revision=s.binding_revision WHERE s.project_id=?`, state.ProjectID).Scan(&selected, &sourceJSON); err != nil {
		return backlogNormalize(err)
	}
	if selected != state.BindingRevision {
		return backlogFailure(ports.FailureConflict, "source changed while refresh was in progress")
	}
	namespace, err := backlogSourceNamespace(state.ProjectID, sourceJSON, state.Pin)
	if err != nil {
		return err
	}
	var revision uint64
	err = tx.QueryRowContext(ctx, `SELECT revision FROM backlog_refreshes WHERE project_id=? AND binding_revision=?`, state.ProjectID, state.BindingRevision).Scan(&revision)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if revision != commit.ExpectedRevision {
		return backlogFailure(ports.FailureConflict, "another refresh advanced the saved checkpoint")
	}
	for _, value := range commit.Observations {
		if err := validateBacklogObservation(value); err != nil {
			return err
		}
		if value.Ref.Namespace != namespace {
			return backlogFailure(ports.FailureInvalidRequest, "observation belongs to another source namespace")
		}
		ref, _ := json.Marshal(value.Ref)
		if _, err := tx.ExecContext(ctx, `INSERT INTO backlog_observations VALUES (?,?,?,?,?,?,?,?) ON CONFLICT(observation_id) DO NOTHING`, value.ID, value.TicketKey, value.NativeRevision, value.ContentDigest, string(ref), string(value.Ticket), formatTime(value.ObservedAt), value.EvidenceRef); err != nil {
			return err
		}
		var retainedDigest, retainedKey, retainedRevision string
		if err := tx.QueryRowContext(ctx, `SELECT content_digest,ticket_key,native_revision FROM backlog_observations WHERE observation_id=?`, value.ID).Scan(&retainedDigest, &retainedKey, &retainedRevision); err != nil {
			return err
		}
		if retainedDigest != value.ContentDigest || retainedKey != value.TicketKey || retainedRevision != value.NativeRevision {
			return backlogFailure(ports.FailureProtocolDrift, "provider reused an immutable revision for different ticket content")
		}
	}
	for _, value := range commit.Tickets {
		if value.ProjectID != state.ProjectID || value.BindingRevision != state.BindingRevision || value.TicketKey == "" || value.ObservationID == "" || value.CheckedAt.IsZero() {
			return backlogFailure(ports.FailureInvalidRequest, "cache update is outside the pinned source refresh")
		}
		if value.State != statestore.BacklogAvailable && value.State != statestore.BacklogMissing && value.State != statestore.BacklogInaccessible {
			return backlogFailure(ports.FailureInvalidRequest, "unknown source cache state")
		}
		var key, refJSON string
		if err := tx.QueryRowContext(ctx, `SELECT ticket_key,ref_json FROM backlog_observations WHERE observation_id=?`, value.ObservationID).Scan(&key, &refJSON); err != nil {
			return backlogNormalize(err)
		}
		if key != value.TicketKey {
			return backlogFailure(ports.FailureInvalidRequest, "cache ticket reference differs from retained observation")
		}
		var ref tracker.TicketRef
		if strictBacklogJSON([]byte(refJSON), &ref) != nil || ref.Namespace != namespace {
			return backlogFailure(ports.FailureInvalidRequest, "cached observation belongs to another source namespace")
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO backlog_cached_tickets VALUES (?,?,?,?,?,?,?,?,?) ON CONFLICT(project_id,binding_revision,ticket_key) DO UPDATE SET observation_id=excluded.observation_id,state=excluded.state,checked_at=excluded.checked_at,seen_generation=excluded.seen_generation,reason=excluded.reason,evidence_ref=excluded.evidence_ref`, value.ProjectID, value.BindingRevision, value.TicketKey, value.ObservationID, value.State, formatTime(value.CheckedAt), value.SeenGeneration, value.Reason, value.EvidenceRef)
		if err != nil {
			return err
		}
	}
	encoded, err := json.Marshal(backlogCheckpointJSON{Version: "darkstar.backlog-checkpoint/v1", State: state})
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO backlog_refreshes VALUES (?,?,?,?) ON CONFLICT(project_id,binding_revision) DO UPDATE SET revision=excluded.revision,state_json=excluded.state_json`, state.ProjectID, state.BindingRevision, state.Revision, string(encoded))
	if err != nil {
		return err
	}
	return tx.Commit()
}

func validateBacklogObservation(value statestore.BacklogObservation) error {
	ticket, err := trackercontract.DecodeTicket(value.Ticket)
	if err != nil {
		return err
	}
	fresh, ok := ticket.Freshness.(tracker.Fresh)
	if !ok || ticket.Ref != value.Ref || ticket.Revision != value.NativeRevision || ticket.EvidenceRef != value.EvidenceRef || !fresh.ObservedAt.Equal(value.ObservedAt) {
		return backlogFailure(ports.FailureInvalidRequest, "observation differs from its source identity, revision or evidence")
	}
	id, key, digest, err := trackercontract.ObservationIdentity(ticket)
	if err != nil {
		return err
	}
	if value.ID != id || value.TicketKey != key || value.ContentDigest != digest {
		return backlogFailure(ports.FailureInvalidRequest, "observation identity or content digest does not match its immutable ticket")
	}
	return nil
}

const backlogObservationColumns = `o.observation_id,o.ticket_key,o.native_revision,o.content_digest,o.ref_json,o.ticket_json,o.observed_at,o.evidence_ref`

func scanBacklogObservation(row interface{ Scan(...any) error }) (statestore.BacklogObservation, error) {
	var value statestore.BacklogObservation
	var ref, ticket, observed string
	err := row.Scan(&value.ID, &value.TicketKey, &value.NativeRevision, &value.ContentDigest, &ref, &ticket, &observed, &value.EvidenceRef)
	if err != nil {
		return value, backlogNormalize(err)
	}
	value.Ticket = json.RawMessage(ticket)
	if err := strictBacklogJSON([]byte(ref), &value.Ref); err != nil {
		return value, err
	}
	value.ObservedAt, err = parseTime(observed)
	if err == nil {
		err = validateBacklogObservation(value)
	}
	return value, err
}

func (d *Database) BacklogObservation(ctx context.Context, id string) (statestore.BacklogObservation, error) {
	return scanBacklogObservation(d.sql.QueryRowContext(ctx, `SELECT `+backlogObservationColumns+` FROM backlog_observations o WHERE observation_id=?`, id))
}

const backlogCacheColumns = `c.project_id,c.binding_revision,c.ticket_key,c.observation_id,c.state,c.checked_at,c.seen_generation,c.reason,c.evidence_ref,` + backlogObservationColumns

func scanBacklogCache(row interface{ Scan(...any) error }) (statestore.BacklogCachedTicket, error) {
	var value statestore.BacklogCachedTicket
	var checked, ref, ticket, observed string
	o := &value.Observation
	err := row.Scan(&value.ProjectID, &value.BindingRevision, &value.TicketKey, &value.ObservationID, &value.State, &checked, &value.SeenGeneration, &value.Reason, &value.EvidenceRef, &o.ID, &o.TicketKey, &o.NativeRevision, &o.ContentDigest, &ref, &ticket, &observed, &o.EvidenceRef)
	if err != nil {
		return value, backlogNormalize(err)
	}
	if err := strictBacklogJSON([]byte(ref), &o.Ref); err != nil {
		return value, err
	}
	o.Ticket = json.RawMessage(ticket)
	o.ObservedAt, err = parseTime(observed)
	if err == nil {
		value.CheckedAt, err = parseTime(checked)
	}
	if err == nil {
		err = validateBacklogObservation(value.Observation)
	}
	if err == nil && (value.ObservationID != value.Observation.ID || value.TicketKey != value.Observation.TicketKey || value.CheckedAt.IsZero() || (value.State != statestore.BacklogAvailable && value.State != statestore.BacklogMissing && value.State != statestore.BacklogInaccessible)) {
		err = backlogFailure(ports.FailureProtocolDrift, "cached backlog row differs from its retained observation")
	}
	return value, err
}

func (d *Database) BacklogTicket(ctx context.Context, project string, binding uint64, key string) (statestore.BacklogCachedTicket, error) {
	return scanBacklogCache(d.sql.QueryRowContext(ctx, `SELECT `+backlogCacheColumns+` FROM backlog_cached_tickets c JOIN backlog_observations o ON o.observation_id=c.observation_id WHERE c.project_id=? AND c.binding_revision=? AND c.ticket_key=?`, project, binding, key))
}

func (d *Database) BacklogTickets(ctx context.Context, project string, binding uint64, previous bool, after string, limit int) ([]statestore.BacklogCachedTicket, error) {
	if limit < 1 || limit > 1001 {
		return nil, backlogFailure(ports.FailureInvalidRequest, "backlog cache page size is outside limits")
	}
	rows, err := d.sql.QueryContext(ctx, `SELECT `+backlogCacheColumns+` FROM backlog_cached_tickets c JOIN backlog_observations o ON o.observation_id=c.observation_id WHERE c.project_id=? AND (? OR c.binding_revision=?) AND printf('%020d',c.binding_revision)||':'||c.ticket_key>? ORDER BY c.binding_revision,c.ticket_key LIMIT ?`, project, previous, binding, after, limit)
	if err != nil {
		return nil, err
	}
	defer func() {
		_ = rows.Close()
	}()
	values := []statestore.BacklogCachedTicket{}
	for rows.Next() {
		value, err := scanBacklogCache(rows)
		if err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	return values, rows.Err()
}

func backlogFailure(code ports.FailureCode, message string) error {
	return &ports.Failure{Code: code, Message: message}
}

func backlogNormalize(err error) error {
	if errors.Is(err, sql.ErrNoRows) {
		return backlogFailure(ports.FailureNotFound, "backlog record not found")
	}
	return err
}
