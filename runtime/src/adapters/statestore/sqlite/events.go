package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/fdsprod/darkstar/runtime/src/core/projection"
	"github.com/fdsprod/darkstar/runtime/src/ports/statestore"
)

const currentStateProjection = "current_state"

var (
	eventIDPattern   = regexp.MustCompile(`^event_[0-9A-HJKMNP-TV-Z]{26}$`)
	idPayloadPattern = regexp.MustCompile(`^[0-9A-HJKMNP-TV-Z]{26}$`)
	kindPattern      = regexp.MustCompile(`^[a-z][a-z0-9_]*(\.[a-z][a-z0-9_]*)+$`)
	aggregateTypes   = map[statestore.AggregateType]string{
		statestore.AggregateProject: "project_", statestore.AggregateWork: "work_",
		statestore.AggregateRun: "run_", statestore.AggregateVisit: "visit_",
		statestore.AggregateAttempt: "attempt_", statestore.AggregateArtifact: "artifact_",
		statestore.AggregateApproval: "approval_", statestore.AggregateOperation: "operation_",
	}
)

// RevisionConflictError reports an optimistic-concurrency mismatch.
type RevisionConflictError struct {
	AggregateID string
	Expected    uint64
	Actual      uint64
}

func (e *RevisionConflictError) Error() string {
	return fmt.Sprintf("aggregate %s revision conflict: expected %d, actual %d", e.AggregateID, e.Expected, e.Actual)
}

// NotFoundError reports a missing current-state projection.
type NotFoundError struct {
	Kind string
	ID   string
}

func (e *NotFoundError) Error() string { return fmt.Sprintf("%s %s not found", e.Kind, e.ID) }

// Append assigns consecutive global positions and aggregate revisions, appends
// immutable events, and updates all affected projections in one transaction.
func (d *Database) Append(ctx context.Context, pending ...statestore.PendingEvent) (committed []statestore.Event, err error) {
	if len(pending) == 0 {
		return nil, errors.New("append requires at least one event")
	}
	for index := range pending {
		if err := validatePendingEvent(pending[index]); err != nil {
			return nil, fmt.Errorf("event %d: %w", index, err)
		}
	}

	tx, err := d.sql.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin event transaction: %w", err)
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()

	var lastPosition uint64
	if err = tx.QueryRowContext(ctx, `SELECT last_position FROM global_positions WHERE singleton = 1`).Scan(&lastPosition); err != nil {
		return nil, fmt.Errorf("read global position: %w", err)
	}
	recordedAt := d.now().UTC().Round(0)
	committed = make([]statestore.Event, 0, len(pending))
	for _, item := range pending {
		currentRevision, exists, readErr := readAggregateRevision(ctx, tx, item.AggregateID, item.AggregateType)
		if readErr != nil {
			err = readErr
			return nil, err
		}
		if currentRevision != item.ExpectedRevision {
			err = &RevisionConflictError{AggregateID: item.AggregateID, Expected: item.ExpectedRevision, Actual: currentRevision}
			return nil, err
		}
		if !exists {
			if item.ExpectedRevision != 0 {
				err = &RevisionConflictError{AggregateID: item.AggregateID, Expected: item.ExpectedRevision, Actual: 0}
				return nil, err
			}
			if _, err = tx.ExecContext(ctx, `INSERT INTO aggregates(aggregate_id, aggregate_type, revision, created_at, updated_at) VALUES (?, ?, 0, ?, ?)`,
				item.AggregateID, item.AggregateType, formatTime(recordedAt), formatTime(recordedAt)); err != nil {
				return nil, fmt.Errorf("create aggregate %s: %w", item.AggregateID, err)
			}
		}

		lastPosition++
		event := statestore.Event{
			SchemaVersion: item.SchemaVersion, ID: item.ID, GlobalPosition: lastPosition,
			AggregateType: item.AggregateType, AggregateID: item.AggregateID,
			AggregateRevision: item.ExpectedRevision + 1, Kind: item.Kind,
			OccurredAt: item.OccurredAt.UTC().Round(0), RecordedAt: recordedAt,
			CorrelationID: item.CorrelationID, CausationID: item.CausationID,
			CommandID: item.CommandID, Actor: item.Actor,
			Data: cloneJSON(item.Data), Metadata: cloneJSON(item.Metadata),
		}
		if err = insertEvent(ctx, tx, event); err != nil {
			return nil, err
		}
		if _, err = tx.ExecContext(ctx, `UPDATE aggregates SET revision = ?, updated_at = ? WHERE aggregate_id = ?`,
			event.AggregateRevision, formatTime(recordedAt), event.AggregateID); err != nil {
			return nil, fmt.Errorf("advance aggregate %s: %w", event.AggregateID, err)
		}
		if err = applyProjection(ctx, tx, event); err != nil {
			return nil, fmt.Errorf("apply event %s to projections: %w", event.ID, err)
		}
		committed = append(committed, event)
	}

	if _, err = tx.ExecContext(ctx, `UPDATE global_positions SET last_position = ? WHERE singleton = 1`, lastPosition); err != nil {
		return nil, fmt.Errorf("advance global position: %w", err)
	}
	if err = writeProjectionCheckpoint(ctx, tx, lastPosition, recordedAt); err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit event transaction: %w", err)
	}
	return committed, nil
}

// EventsAfter returns committed events strictly after a global position.
func (d *Database) EventsAfter(ctx context.Context, position uint64, limit int) ([]statestore.Event, error) {
	if limit < 1 || limit > 1000 {
		return nil, fmt.Errorf("event limit must be between 1 and 1000: %d", limit)
	}
	rows, err := d.sql.QueryContext(ctx, eventSelect+` WHERE global_position > ? ORDER BY global_position LIMIT ?`, position, limit)
	if err != nil {
		return nil, fmt.Errorf("query events: %w", err)
	}
	defer rows.Close()
	return scanEvents(rows)
}

// Run returns the current run projection.
func (d *Database) Run(ctx context.Context, id string) (statestore.RunProjection, error) {
	projection, err := readRunProjection(ctx, d.sql, id)
	if errors.Is(err, sql.ErrNoRows) {
		return statestore.RunProjection{}, &NotFoundError{Kind: "run", ID: id}
	}
	if err != nil {
		return statestore.RunProjection{}, fmt.Errorf("read run projection: %w", err)
	}
	return projection, nil
}

// Approval returns the current approval projection.
func (d *Database) Approval(ctx context.Context, id string) (statestore.ApprovalProjection, error) {
	projection, err := readApprovalProjection(ctx, d.sql, id)
	if errors.Is(err, sql.ErrNoRows) {
		return statestore.ApprovalProjection{}, &NotFoundError{Kind: "approval", ID: id}
	}
	if err != nil {
		return statestore.ApprovalProjection{}, fmt.Errorf("read approval projection: %w", err)
	}
	return projection, nil
}

// RebuildProjections atomically replaces current-state projections by replaying
// the authoritative event log from global position zero.
func (d *Database) RebuildProjections(ctx context.Context) (err error) {
	tx, err := d.sql.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin projection rebuild: %w", err)
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()

	rows, err := tx.QueryContext(ctx, eventSelect+` ORDER BY global_position`)
	if err != nil {
		return fmt.Errorf("query replay events: %w", err)
	}
	events, err := scanEvents(rows)
	closeErr := rows.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return fmt.Errorf("close replay events: %w", closeErr)
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM run_projection; DELETE FROM approval_projection; DELETE FROM projection_checkpoints`); err != nil {
		return fmt.Errorf("clear projections: %w", err)
	}
	for _, event := range events {
		if err = applyProjection(ctx, tx, event); err != nil {
			return fmt.Errorf("replay event %s at position %d: %w", event.ID, event.GlobalPosition, err)
		}
	}
	var lastPosition uint64
	if len(events) > 0 {
		lastPosition = events[len(events)-1].GlobalPosition
	}
	if err = writeProjectionCheckpoint(ctx, tx, lastPosition, d.now().UTC().Round(0)); err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return fmt.Errorf("commit projection rebuild: %w", err)
	}
	return nil
}

func validatePendingEvent(event statestore.PendingEvent) error {
	if event.SchemaVersion != 1 {
		return fmt.Errorf("unsupported schema version %d", event.SchemaVersion)
	}
	if !eventIDPattern.MatchString(event.ID) {
		return fmt.Errorf("invalid event ID %q", event.ID)
	}
	prefix, ok := aggregateTypes[event.AggregateType]
	if !ok || !strings.HasPrefix(event.AggregateID, prefix) || !idPayloadPattern.MatchString(strings.TrimPrefix(event.AggregateID, prefix)) {
		return fmt.Errorf("aggregate ID %q does not match type %q", event.AggregateID, event.AggregateType)
	}
	if !kindPattern.MatchString(event.Kind) {
		return fmt.Errorf("invalid event kind %q", event.Kind)
	}
	if event.OccurredAt.IsZero() {
		return errors.New("occurred time is required")
	}
	if event.CorrelationID == "" || event.CommandID == "" || event.Actor.ID == "" {
		return errors.New("correlation ID, command ID, and actor ID are required")
	}
	if len(event.CorrelationID) > 128 || len(event.CommandID) > 128 || len(event.Actor.ID) > 128 || (event.CausationID != nil && len(*event.CausationID) > 128) {
		return errors.New("correlation ID, causation ID, command ID, and actor ID must be at most 128 bytes")
	}
	switch event.Actor.Type {
	case statestore.ActorUser, statestore.ActorSystem, statestore.ActorProvider, statestore.ActorExternal:
	default:
		return fmt.Errorf("invalid actor type %q", event.Actor.Type)
	}
	if !jsonObject(event.Data) {
		return errors.New("event data must be a JSON object")
	}
	if !jsonObject(event.Metadata) {
		return errors.New("event metadata must be a JSON object")
	}
	return nil
}

func readAggregateRevision(ctx context.Context, tx *sql.Tx, id string, wantType statestore.AggregateType) (uint64, bool, error) {
	var revision uint64
	var gotType statestore.AggregateType
	err := tx.QueryRowContext(ctx, `SELECT aggregate_type, revision FROM aggregates WHERE aggregate_id = ?`, id).Scan(&gotType, &revision)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, fmt.Errorf("read aggregate %s: %w", id, err)
	}
	if gotType != wantType {
		return 0, true, fmt.Errorf("aggregate %s has type %s, not %s", id, gotType, wantType)
	}
	return revision, true, nil
}

func insertEvent(ctx context.Context, tx *sql.Tx, event statestore.Event) error {
	actor, err := json.Marshal(event.Actor)
	if err != nil {
		return fmt.Errorf("encode actor: %w", err)
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO events(
		global_position, event_id, schema_version, aggregate_id, aggregate_revision, kind,
		occurred_at, recorded_at, correlation_id, causation_id, command_id, actor_json, data_json, metadata_json
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		event.GlobalPosition, event.ID, event.SchemaVersion, event.AggregateID, event.AggregateRevision,
		event.Kind, formatTime(event.OccurredAt), formatTime(event.RecordedAt), event.CorrelationID,
		event.CausationID, event.CommandID, string(actor), string(event.Data), string(event.Metadata))
	if err != nil {
		return fmt.Errorf("insert event %s: %w", event.ID, err)
	}
	return nil
}

func applyProjection(ctx context.Context, tx *sql.Tx, event statestore.Event) error {
	if event.SchemaVersion != 1 {
		return &projection.UnsupportedSchemaVersionError{EventID: event.ID, Version: event.SchemaVersion}
	}
	switch event.AggregateType {
	case statestore.AggregateRun:
		current, err := readRunProjection(ctx, tx, event.AggregateID)
		var existing *statestore.RunProjection
		if err == nil {
			existing = &current
		} else if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		next, applies, err := projection.ReduceRun(existing, event)
		if err != nil || !applies {
			return err
		}
		return writeRunProjection(ctx, tx, next)
	case statestore.AggregateApproval:
		current, err := readApprovalProjection(ctx, tx, event.AggregateID)
		var existing *statestore.ApprovalProjection
		if err == nil {
			existing = &current
		} else if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		next, applies, err := projection.ReduceApproval(existing, event)
		if err != nil || !applies {
			return err
		}
		return writeApprovalProjection(ctx, tx, next)
	default:
		return nil
	}
}

type rowQueryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func readRunProjection(ctx context.Context, query rowQueryer, id string) (statestore.RunProjection, error) {
	var result statestore.RunProjection
	var createdAt, updatedAt string
	err := query.QueryRowContext(ctx, `SELECT run_id, work_item_id, workflow_id, workflow_version, status,
		resource_version, last_global_position, created_at, updated_at FROM run_projection WHERE run_id = ?`, id).Scan(
		&result.RunID, &result.WorkItemID, &result.WorkflowID, &result.WorkflowVersion, &result.Status,
		&result.ResourceVersion, &result.LastGlobalPosition, &createdAt, &updatedAt)
	if err != nil {
		return statestore.RunProjection{}, err
	}
	result.CreatedAt, err = parseTime(createdAt)
	if err != nil {
		return statestore.RunProjection{}, err
	}
	result.UpdatedAt, err = parseTime(updatedAt)
	return result, err
}

func readApprovalProjection(ctx context.Context, query rowQueryer, id string) (statestore.ApprovalProjection, error) {
	var result statestore.ApprovalProjection
	var createdAt, updatedAt string
	err := query.QueryRowContext(ctx, `SELECT approval_id, run_id, class, status, scope_digest, policy_digest,
		resource_version, last_global_position, created_at, updated_at FROM approval_projection WHERE approval_id = ?`, id).Scan(
		&result.ApprovalID, &result.RunID, &result.Class, &result.Status, &result.ScopeDigest, &result.PolicyDigest,
		&result.ResourceVersion, &result.LastGlobalPosition, &createdAt, &updatedAt)
	if err != nil {
		return statestore.ApprovalProjection{}, err
	}
	result.CreatedAt, err = parseTime(createdAt)
	if err != nil {
		return statestore.ApprovalProjection{}, err
	}
	result.UpdatedAt, err = parseTime(updatedAt)
	return result, err
}

func writeRunProjection(ctx context.Context, tx *sql.Tx, value statestore.RunProjection) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO run_projection VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(run_id) DO UPDATE SET status=excluded.status, resource_version=excluded.resource_version,
		last_global_position=excluded.last_global_position, updated_at=excluded.updated_at`,
		value.RunID, value.WorkItemID, value.WorkflowID, value.WorkflowVersion, value.Status,
		value.ResourceVersion, value.LastGlobalPosition, formatTime(value.CreatedAt), formatTime(value.UpdatedAt))
	if err != nil {
		return fmt.Errorf("write run projection %s: %w", value.RunID, err)
	}
	return nil
}

func writeApprovalProjection(ctx context.Context, tx *sql.Tx, value statestore.ApprovalProjection) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO approval_projection VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(approval_id) DO UPDATE SET status=excluded.status, resource_version=excluded.resource_version,
		last_global_position=excluded.last_global_position, updated_at=excluded.updated_at`,
		value.ApprovalID, value.RunID, value.Class, value.Status, value.ScopeDigest, value.PolicyDigest,
		value.ResourceVersion, value.LastGlobalPosition, formatTime(value.CreatedAt), formatTime(value.UpdatedAt))
	if err != nil {
		return fmt.Errorf("write approval projection %s: %w", value.ApprovalID, err)
	}
	return nil
}

func writeProjectionCheckpoint(ctx context.Context, tx *sql.Tx, position uint64, updatedAt time.Time) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO projection_checkpoints VALUES (?, ?, ?, ?)
		ON CONFLICT(projection_name) DO UPDATE SET last_global_position=excluded.last_global_position,
		reducer_version=excluded.reducer_version, updated_at=excluded.updated_at`,
		currentStateProjection, position, projection.ReducerVersion, formatTime(updatedAt))
	if err != nil {
		return fmt.Errorf("write projection checkpoint: %w", err)
	}
	return nil
}

const eventSelect = `SELECT global_position, event_id, schema_version, aggregate_id, aggregate_revision, kind,
	occurred_at, recorded_at, correlation_id, causation_id, command_id, actor_json, data_json, metadata_json FROM events`

type eventRows interface {
	Next() bool
	Scan(...any) error
	Err() error
}

func scanEvents(rows eventRows) ([]statestore.Event, error) {
	var events []statestore.Event
	for rows.Next() {
		var event statestore.Event
		var occurredAt, recordedAt, actorJSON string
		var dataJSON, metadataJSON string
		if err := rows.Scan(&event.GlobalPosition, &event.ID, &event.SchemaVersion, &event.AggregateID,
			&event.AggregateRevision, &event.Kind, &occurredAt, &recordedAt, &event.CorrelationID,
			&event.CausationID, &event.CommandID, &actorJSON, &dataJSON, &metadataJSON); err != nil {
			return nil, fmt.Errorf("scan event: %w", err)
		}
		prefix := strings.IndexByte(event.AggregateID, '_')
		if prefix <= 0 {
			return nil, fmt.Errorf("event %s has invalid aggregate ID %q", event.ID, event.AggregateID)
		}
		event.AggregateType = statestore.AggregateType(event.AggregateID[:prefix])
		var err error
		event.OccurredAt, err = parseTime(occurredAt)
		if err != nil {
			return nil, fmt.Errorf("event %s occurred_at: %w", event.ID, err)
		}
		event.RecordedAt, err = parseTime(recordedAt)
		if err != nil {
			return nil, fmt.Errorf("event %s recorded_at: %w", event.ID, err)
		}
		if err := json.Unmarshal([]byte(actorJSON), &event.Actor); err != nil {
			return nil, fmt.Errorf("event %s actor: %w", event.ID, err)
		}
		event.Data = json.RawMessage(dataJSON)
		event.Metadata = json.RawMessage(metadataJSON)
		events = append(events, event)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate events: %w", err)
	}
	return events, nil
}

func jsonObject(value json.RawMessage) bool {
	var object map[string]json.RawMessage
	return json.Unmarshal(value, &object) == nil && object != nil
}

func cloneJSON(value json.RawMessage) json.RawMessage { return append(json.RawMessage(nil), value...) }

func formatTime(value time.Time) string { return value.UTC().Format(time.RFC3339Nano) }

func parseTime(value string) (time.Time, error) {
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return time.Time{}, fmt.Errorf("parse timestamp %q: %w", value, err)
	}
	return parsed.UTC(), nil
}

var _ statestore.Store = (*Database)(nil)
