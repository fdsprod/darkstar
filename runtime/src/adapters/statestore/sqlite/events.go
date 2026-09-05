package sqlite

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"darkstar/src/core/projection"
	"darkstar/src/ports/statestore"
)

const currentStateProjection = "current_state"

var (
	eventIDPattern   = regexp.MustCompile(`^event_[0-9A-HJKMNP-TV-Z]{26}$`)
	idPayloadPattern = regexp.MustCompile(`^[0-9A-HJKMNP-TV-Z]{26}$`)
	kindPattern      = regexp.MustCompile(`^[a-z][a-z0-9_]*(\.[a-z][a-z0-9_]*)+$`)
	aggregateTypes   = map[statestore.AggregateType]string{
		statestore.AggregateProject: "project_", statestore.AggregateWork: "work_",
		statestore.AggregateStory: "story_", statestore.AggregatePoint: "point_",
		statestore.AggregateRun: "run_", statestore.AggregateVisit: "visit_",
		statestore.AggregateAttempt: "attempt_", statestore.AggregateArtifact: "artifact_",
		statestore.AggregateApproval: "approval_", statestore.AggregateOperation: "operation_",
		statestore.AggregateAssessment: "assessment_",
		statestore.AggregateInput:      "input_",
		statestore.AggregatePermission: "permission_",
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

// IdempotencyConflictError reports reuse of an aggregate command identity for
// a different transition. An exact retry returns the original committed event.
type IdempotencyConflictError struct {
	AggregateID string
	CommandID   string
}

func (e *IdempotencyConflictError) Error() string {
	return fmt.Sprintf("aggregate %s command %s conflicts with its committed transition", e.AggregateID, e.CommandID)
}

// NotFoundError reports a missing current-state projection.
type NotFoundError struct {
	Kind string
	ID   string
}

func (e *NotFoundError) Error() string { return fmt.Sprintf("%s %s not found", e.Kind, e.ID) }

// Unwrap preserves the adapter-independent missing-state classification.
func (e *NotFoundError) Unwrap() error { return statestore.ErrNotFound }

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
		existing, duplicate, readErr := readEventByCommand(ctx, tx, item.AggregateID, item.CommandID)
		if readErr != nil {
			return nil, readErr
		}
		if duplicate {
			if !sameTransitionCommand(existing, item) {
				return nil, &IdempotencyConflictError{AggregateID: item.AggregateID, CommandID: item.CommandID}
			}
			committed = append(committed, existing)
			continue
		}
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
	defer func() {
		_ = rows.Close()
	}()
	return scanEvents(rows)
}

// EventBounds returns the inclusive range retained by the authoritative event
// log. The MVP never compacts events, but exposing the range keeps replay
// behavior explicit when retention is introduced later.
func (d *Database) EventBounds(ctx context.Context) (statestore.EventBounds, error) {
	var oldest, latest uint64
	if err := d.sql.QueryRowContext(ctx, `SELECT COALESCE(MIN(global_position), 0), COALESCE(MAX(global_position), 0) FROM events`).Scan(&oldest, &latest); err != nil {
		return statestore.EventBounds{}, fmt.Errorf("query event bounds: %w", err)
	}
	return statestore.EventBounds{Oldest: oldest, Latest: latest}, nil
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

// Node returns the current state of one workflow node visit.
func (d *Database) Node(ctx context.Context, id string) (statestore.NodeProjection, error) {
	value, err := readNodeProjection(ctx, d.sql, id)
	if errors.Is(err, sql.ErrNoRows) {
		return statestore.NodeProjection{}, &NotFoundError{Kind: "node visit", ID: id}
	}
	if err != nil {
		return statestore.NodeProjection{}, fmt.Errorf("read node projection: %w", err)
	}
	return value, nil
}

// NodesForRun returns node visits in stable creation order.
func (d *Database) NodesForRun(ctx context.Context, runID string) ([]statestore.NodeProjection, error) {
	rows, err := d.sql.QueryContext(ctx, nodeSelect+` WHERE run_id = ? ORDER BY created_at, visit_id`, runID)
	if err != nil {
		return nil, fmt.Errorf("query node projections: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var values []statestore.NodeProjection
	for rows.Next() {
		value, scanErr := scanNodeProjection(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		values = append(values, value)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate node projections: %w", err)
	}
	return values, nil
}

// Attempt returns the current provider-attempt projection.
func (d *Database) Attempt(ctx context.Context, id string) (statestore.AttemptProjection, error) {
	value, err := readAttemptProjection(ctx, d.sql, id)
	if errors.Is(err, sql.ErrNoRows) {
		return statestore.AttemptProjection{}, &NotFoundError{Kind: "attempt", ID: id}
	}
	if err != nil {
		return statestore.AttemptProjection{}, fmt.Errorf("read attempt projection: %w", err)
	}
	return value, nil
}

// AttemptsForRun returns attempts in stable creation order.
func (d *Database) AttemptsForRun(ctx context.Context, runID string) ([]statestore.AttemptProjection, error) {
	return d.queryAttempts(ctx, ` WHERE run_id = ? ORDER BY created_at, attempt_id`, runID)
}

// ActiveAttempts returns every non-terminal attempt that startup must resume.
func (d *Database) ActiveAttempts(ctx context.Context) ([]statestore.AttemptProjection, error) {
	return d.queryAttempts(ctx, ` WHERE status IN ('created', 'starting', 'running') ORDER BY created_at, attempt_id`)
}

func (d *Database) queryAttempts(ctx context.Context, suffix string, args ...any) ([]statestore.AttemptProjection, error) {
	rows, err := d.sql.QueryContext(ctx, attemptSelect+suffix, args...)
	if err != nil {
		return nil, fmt.Errorf("query attempt projections: %w", err)
	}
	defer func() {
		_ = rows.Close()
	}()
	var values []statestore.AttemptProjection
	for rows.Next() {
		value, err := scanAttemptProjection(rows)
		if err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate attempt projections: %w", err)
	}
	return values, nil
}

// RunEvidence reads the run projection, every correlated event, and command
// evidence from one SQLite snapshot so an export cannot mix revisions.
func (d *Database) RunEvidence(ctx context.Context, id string) (evidence statestore.RunEvidence, err error) {
	tx, err := d.sql.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return statestore.RunEvidence{}, fmt.Errorf("begin run evidence snapshot: %w", err)
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()

	evidence.Run, err = readRunProjection(ctx, tx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return statestore.RunEvidence{}, &NotFoundError{Kind: "run", ID: id}
	}
	if err != nil {
		return statestore.RunEvidence{}, fmt.Errorf("read run projection: %w", err)
	}
	rows, err := tx.QueryContext(ctx, eventSelect+` WHERE correlation_id = ? OR aggregate_id = ? ORDER BY global_position`, id, id)
	if err != nil {
		return statestore.RunEvidence{}, fmt.Errorf("query run events: %w", err)
	}
	evidence.Events, err = scanEvents(rows)
	closeErr := rows.Close()
	if err != nil {
		return statestore.RunEvidence{}, err
	}
	if closeErr != nil {
		return statestore.RunEvidence{}, fmt.Errorf("close run events: %w", closeErr)
	}
	evidence.Commands, err = readRunCommands(ctx, tx, id)
	if err != nil {
		return statestore.RunEvidence{}, err
	}
	if err = tx.Commit(); err != nil {
		return statestore.RunEvidence{}, fmt.Errorf("commit run evidence snapshot: %w", err)
	}
	return evidence, nil
}

func readRunCommands(ctx context.Context, query *sql.Tx, runID string) ([]statestore.CommandEvidence, error) {
	rows, err := query.QueryContext(ctx, `SELECT scope, idempotency_key, request_digest, status, response_status,
		response_json, first_event_position, last_event_position, created_at, completed_at
		FROM commands WHERE scope = ? OR idempotency_key IN
		(SELECT command_id FROM events WHERE correlation_id = ? OR aggregate_id = ?)
		ORDER BY created_at, scope, idempotency_key`, runID, runID, runID)
	if err != nil {
		return nil, fmt.Errorf("query run commands: %w", err)
	}
	defer func() {
		_ = rows.Close()
	}()

	commands := make([]statestore.CommandEvidence, 0)
	for rows.Next() {
		var command statestore.CommandEvidence
		var responseStatus, firstPosition, lastPosition sql.NullInt64
		var responseJSON, completedAt sql.NullString
		var createdAt string
		if err := rows.Scan(&command.Scope, &command.IdempotencyKey, &command.RequestDigest, &command.Status,
			&responseStatus, &responseJSON, &firstPosition, &lastPosition, &createdAt, &completedAt); err != nil {
			return nil, fmt.Errorf("scan run command: %w", err)
		}
		command.CreatedAt, err = parseTime(createdAt)
		if err != nil {
			return nil, fmt.Errorf("command %s created_at: %w", command.IdempotencyKey, err)
		}
		if responseStatus.Valid {
			value := int(responseStatus.Int64)
			command.ResponseStatus = &value
		}
		if responseJSON.Valid {
			command.Response = json.RawMessage(responseJSON.String)
		}
		if firstPosition.Valid {
			value := uint64(firstPosition.Int64)
			command.FirstEventPosition = &value
		}
		if lastPosition.Valid {
			value := uint64(lastPosition.Int64)
			command.LastEventPosition = &value
		}
		if completedAt.Valid {
			value, parseErr := parseTime(completedAt.String)
			if parseErr != nil {
				return nil, fmt.Errorf("command %s completed_at: %w", command.IdempotencyKey, parseErr)
			}
			command.CompletedAt = &value
		}
		commands = append(commands, command)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate run commands: %w", err)
	}
	return commands, nil
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

// ReadinessAssessment returns one durable readiness-control projection.
func (d *Database) ReadinessAssessment(ctx context.Context, id string) (statestore.ReadinessAssessmentProjection, error) {
	value, err := readReadinessAssessmentProjection(ctx, d.sql, id)
	if errors.Is(err, sql.ErrNoRows) {
		return statestore.ReadinessAssessmentProjection{}, &NotFoundError{Kind: "readiness assessment", ID: id}
	}
	if err != nil {
		return statestore.ReadinessAssessmentProjection{}, fmt.Errorf("read readiness assessment projection: %w", err)
	}
	return value, nil
}

// LatestReadinessAssessmentForRun returns the newest assessment using stable
// creation time and identity ordering.
func (d *Database) LatestReadinessAssessmentForRun(ctx context.Context, runID string) (statestore.ReadinessAssessmentProjection, error) {
	value, err := scanReadinessAssessmentProjection(d.sql.QueryRowContext(ctx, readinessAssessmentSelect+` WHERE run_id = ? ORDER BY last_global_position DESC, assessment_id DESC LIMIT 1`, runID))
	if errors.Is(err, sql.ErrNoRows) {
		return statestore.ReadinessAssessmentProjection{}, &NotFoundError{Kind: "readiness assessment for run", ID: runID}
	}
	if err != nil {
		return statestore.ReadinessAssessmentProjection{}, fmt.Errorf("read latest readiness assessment: %w", err)
	}
	return value, nil
}

func (d *Database) InputRequest(ctx context.Context, id string) (statestore.InputRequestProjection, error) {
	value, err := readInputRequestProjection(ctx, d.sql, id)
	if errors.Is(err, sql.ErrNoRows) {
		return statestore.InputRequestProjection{}, &NotFoundError{Kind: "input request", ID: id}
	}
	if err != nil {
		return statestore.InputRequestProjection{}, fmt.Errorf("read input request projection: %w", err)
	}
	return value, nil
}

// InputRequests returns the global user-attention queue in deterministic order.
func (d *Database) InputRequests(ctx context.Context, status statestore.InputRequestStatus) ([]statestore.InputRequestProjection, error) {
	return queryInputRequests(ctx, d.sql, ` WHERE status = ? ORDER BY created_at, input_request_id`, status)
}

func (d *Database) InputRequestsForRun(ctx context.Context, runID string) ([]statestore.InputRequestProjection, error) {
	return queryInputRequests(ctx, d.sql, ` WHERE run_id = ? ORDER BY created_at, input_request_id`, runID)
}

func (d *Database) InputRequestsForAttempt(ctx context.Context, attemptID string) ([]statestore.InputRequestProjection, error) {
	return queryInputRequests(ctx, d.sql, ` WHERE attempt_id = ? ORDER BY created_at, input_request_id`, attemptID)
}

func (d *Database) ProviderPermission(ctx context.Context, id string) (statestore.ProviderPermissionProjection, error) {
	value, err := readProviderPermissionProjection(ctx, d.sql, id)
	if errors.Is(err, sql.ErrNoRows) {
		return statestore.ProviderPermissionProjection{}, fmt.Errorf("%w: provider permission %s", statestore.ErrNotFound, id)
	}
	return value, err
}

func (d *Database) ProviderPermissions(ctx context.Context, status statestore.ProviderPermissionStatus) ([]statestore.ProviderPermissionProjection, error) {
	return queryProviderPermissions(ctx, d.sql, ` WHERE status=? ORDER BY created_at, permission_request_id`, status)
}

func (d *Database) ProviderPermissionsForAttempt(ctx context.Context, attemptID string) ([]statestore.ProviderPermissionProjection, error) {
	return queryProviderPermissions(ctx, d.sql, ` WHERE attempt_id=? ORDER BY created_at, permission_request_id`, attemptID)
}

// ApprovalsForCheckpoint returns immutable candidate requests in revision order.
func (d *Database) ApprovalsForCheckpoint(ctx context.Context, checkpointID string) ([]statestore.ApprovalProjection, error) {
	rows, err := d.sql.QueryContext(ctx, approvalSelect+` WHERE checkpoint_id = ? ORDER BY checkpoint_revision`, checkpointID)
	if err != nil {
		return nil, fmt.Errorf("query artifact checkpoint approvals: %w", err)
	}
	defer func() { _ = rows.Close() }()
	values := make([]statestore.ApprovalProjection, 0)
	for rows.Next() {
		value, scanErr := scanApprovalProjection(rows)
		if scanErr != nil {
			return nil, fmt.Errorf("scan artifact checkpoint approval: %w", scanErr)
		}
		values = append(values, value)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate artifact checkpoint approvals: %w", err)
	}
	return values, nil
}

// CheckpointApprovals returns full artifact-checkpoint rounds in attention
// order. Empty runID selects all runs; status is always explicit.
func (d *Database) CheckpointApprovals(ctx context.Context, runID string, status statestore.ApprovalStatus) ([]statestore.ApprovalProjection, error) {
	query := approvalSelect + ` WHERE class = ? AND status = ?`
	args := []any{statestore.ApprovalWorkflowCheckpoint, status}
	if runID != "" {
		query += ` AND run_id = ?`
		args = append(args, runID)
	}
	query += ` ORDER BY created_at, approval_id`
	rows, err := d.sql.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("query checkpoint approvals: %w", err)
	}
	defer func() { _ = rows.Close() }()
	values := make([]statestore.ApprovalProjection, 0)
	for rows.Next() {
		value, scanErr := scanApprovalProjection(rows)
		if scanErr != nil {
			return nil, fmt.Errorf("scan checkpoint approval: %w", scanErr)
		}
		values = append(values, value)
	}
	return values, rows.Err()
}

// EventByCommand returns the immutable event owned by one aggregate-scoped
// command identity. It supports exact decision replay after a request resolves.
func (d *Database) EventByCommand(ctx context.Context, aggregateID, commandID string) (statestore.Event, error) {
	rows, err := d.sql.QueryContext(ctx, eventSelect+` WHERE aggregate_id = ? AND command_id = ?`, aggregateID, commandID)
	if err != nil {
		return statestore.Event{}, fmt.Errorf("query aggregate command: %w", err)
	}
	events, scanErr := scanEvents(rows)
	closeErr := rows.Close()
	if scanErr != nil {
		return statestore.Event{}, scanErr
	}
	if closeErr != nil {
		return statestore.Event{}, fmt.Errorf("close aggregate command query: %w", closeErr)
	}
	if len(events) == 0 {
		return statestore.Event{}, &NotFoundError{Kind: "aggregate command", ID: aggregateID + "/" + commandID}
	}
	return events[0], nil
}

// EventsForAggregate returns one complete aggregate stream in revision order.
// Review conversations use the immutable events as their sole turn history.
func (d *Database) EventsForAggregate(ctx context.Context, aggregateID string) ([]statestore.Event, error) {
	rows, err := d.sql.QueryContext(ctx, eventSelect+` WHERE aggregate_id = ? ORDER BY aggregate_revision`, aggregateID)
	if err != nil {
		return nil, fmt.Errorf("query aggregate events: %w", err)
	}
	events, scanErr := scanEvents(rows)
	closeErr := rows.Close()
	if scanErr != nil {
		return nil, scanErr
	}
	if closeErr != nil {
		return nil, fmt.Errorf("close aggregate events: %w", closeErr)
	}
	if len(events) == 0 {
		return nil, &NotFoundError{Kind: "aggregate events", ID: aggregateID}
	}
	return events, nil
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
	if _, err = tx.ExecContext(ctx, `DELETE FROM point_dependencies; DELETE FROM attempt_projection; DELETE FROM point_projection; DELETE FROM story_projection; DELETE FROM work_item_projection; DELETE FROM project_projection; DELETE FROM run_projection; DELETE FROM node_projection; DELETE FROM approval_projection; DELETE FROM readiness_assessment_projection; DELETE FROM input_request_projection; DELETE FROM provider_permission_projection; DELETE FROM projection_checkpoints`); err != nil {
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

func readEventByCommand(ctx context.Context, tx *sql.Tx, aggregateID, commandID string) (statestore.Event, bool, error) {
	rows, err := tx.QueryContext(ctx, eventSelect+` WHERE aggregate_id = ? AND command_id = ?`, aggregateID, commandID)
	if err != nil {
		return statestore.Event{}, false, fmt.Errorf("read aggregate command %s/%s: %w", aggregateID, commandID, err)
	}
	events, scanErr := scanEvents(rows)
	closeErr := rows.Close()
	if scanErr != nil {
		return statestore.Event{}, false, scanErr
	}
	if closeErr != nil {
		return statestore.Event{}, false, fmt.Errorf("close aggregate command query: %w", closeErr)
	}
	if len(events) == 0 {
		return statestore.Event{}, false, nil
	}
	return events[0], true, nil
}

func sameTransitionCommand(committed statestore.Event, pending statestore.PendingEvent) bool {
	return committed.SchemaVersion == pending.SchemaVersion &&
		committed.AggregateType == pending.AggregateType &&
		committed.AggregateID == pending.AggregateID &&
		committed.AggregateRevision == pending.ExpectedRevision+1 &&
		committed.Kind == pending.Kind && committed.CorrelationID == pending.CorrelationID &&
		sameOptionalString(committed.CausationID, pending.CausationID) && committed.CommandID == pending.CommandID &&
		committed.Actor == pending.Actor && bytes.Equal(committed.Data, pending.Data) && bytes.Equal(committed.Metadata, pending.Metadata)
}

func sameOptionalString(left, right *string) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
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
	case statestore.AggregateProject:
		current, err := readProjectProjection(ctx, tx, event.AggregateID)
		var existing *statestore.ProjectProjection
		if err == nil {
			existing = &current
		} else if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		next, applies, err := projection.ReduceProject(existing, event)
		if err != nil || !applies {
			return err
		}
		return writeProjectProjection(ctx, tx, next)
	case statestore.AggregateWork:
		current, err := readWorkItemProjection(ctx, tx, event.AggregateID)
		var existing *statestore.WorkItemProjection
		if err == nil {
			existing = &current
		} else if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		next, applies, err := projection.ReduceWorkItem(existing, event)
		if err != nil || !applies {
			return err
		}
		return writeWorkItemProjection(ctx, tx, next)
	case statestore.AggregateStory:
		current, err := readStoryProjection(ctx, tx, event.AggregateID)
		var existing *statestore.StoryProjection
		if err == nil {
			existing = &current
		} else if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		next, applies, err := projection.ReduceStory(existing, event)
		if err != nil || !applies {
			return err
		}
		return writeStoryProjection(ctx, tx, next)
	case statestore.AggregatePoint:
		current, err := readPointProjection(ctx, tx, event.AggregateID)
		var existing *statestore.PointProjection
		if err == nil {
			existing = &current
		} else if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		next, applies, err := projection.ReducePoint(existing, event)
		if err != nil || !applies {
			return err
		}
		return writePointProjection(ctx, tx, next)
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
	case statestore.AggregateVisit:
		current, err := readNodeProjection(ctx, tx, event.AggregateID)
		var existing *statestore.NodeProjection
		if err == nil {
			existing = &current
		} else if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		next, applies, err := projection.ReduceNode(existing, event)
		if err != nil || !applies {
			return err
		}
		return writeNodeProjection(ctx, tx, next)
	case statestore.AggregateAttempt:
		current, err := readAttemptProjection(ctx, tx, event.AggregateID)
		var existing *statestore.AttemptProjection
		if err == nil {
			existing = &current
		} else if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		next, applies, err := projection.ReduceAttempt(existing, event)
		if err != nil || !applies {
			return err
		}
		return writeAttemptProjection(ctx, tx, next)
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
	case statestore.AggregateAssessment:
		current, err := readReadinessAssessmentProjection(ctx, tx, event.AggregateID)
		var existing *statestore.ReadinessAssessmentProjection
		if err == nil {
			existing = &current
		} else if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		next, applies, err := projection.ReduceReadinessAssessment(existing, event)
		if err != nil || !applies {
			return err
		}
		return writeReadinessAssessmentProjection(ctx, tx, next)
	case statestore.AggregateInput:
		current, err := readInputRequestProjection(ctx, tx, event.AggregateID)
		var existing *statestore.InputRequestProjection
		if err == nil {
			existing = &current
		} else if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		next, applies, err := projection.ReduceInputRequest(existing, event)
		if err != nil || !applies {
			return err
		}
		return writeInputRequestProjection(ctx, tx, next)
	case statestore.AggregatePermission:
		current, err := readProviderPermissionProjection(ctx, tx, event.AggregateID)
		var existing *statestore.ProviderPermissionProjection
		if err == nil {
			existing = &current
		} else if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		next, applies, err := projection.ReduceProviderPermission(existing, event)
		if err != nil || !applies {
			return err
		}
		return writeProviderPermissionProjection(ctx, tx, next)
	default:
		return nil
	}
}

const nodeSelect = `SELECT visit_id, run_id, node_id, status, resource_version,
	last_global_position, created_at, updated_at FROM node_projection`

type nodeRowScanner interface{ Scan(...any) error }

func scanNodeProjection(row nodeRowScanner) (statestore.NodeProjection, error) {
	var value statestore.NodeProjection
	var createdAt, updatedAt string
	err := row.Scan(&value.VisitID, &value.RunID, &value.NodeID, &value.Status, &value.ResourceVersion,
		&value.LastGlobalPosition, &createdAt, &updatedAt)
	if err != nil {
		return statestore.NodeProjection{}, err
	}
	value.CreatedAt, err = parseTime(createdAt)
	if err == nil {
		value.UpdatedAt, err = parseTime(updatedAt)
	}
	return value, err
}

func readNodeProjection(ctx context.Context, query rowQueryer, id string) (statestore.NodeProjection, error) {
	return scanNodeProjection(query.QueryRowContext(ctx, nodeSelect+` WHERE visit_id = ?`, id))
}

type rowQueryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

const runSelect = `SELECT run_id, work_item_id, workflow_id, workflow_version,
	workflow_digest, route_digest, route_snapshot_json, priority, status,
	resource_version, last_global_position, created_at, updated_at FROM run_projection`

type runRowScanner interface{ Scan(...any) error }

func scanRunProjection(row runRowScanner) (statestore.RunProjection, error) {
	var result statestore.RunProjection
	var createdAt, updatedAt string
	err := row.Scan(&result.RunID, &result.WorkItemID, &result.WorkflowID, &result.WorkflowVersion,
		&result.WorkflowDigest, &result.RouteDigest, &result.RouteSnapshot, &result.Priority, &result.Status,
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

func readRunProjection(ctx context.Context, query rowQueryer, id string) (statestore.RunProjection, error) {
	return scanRunProjection(query.QueryRowContext(ctx, runSelect+` WHERE run_id = ?`, id))
}

const approvalSelect = `SELECT approval_id, run_id, class, status, checkpoint_id, visit_id, node_id, attempt_id,
	checkpoint_revision, candidate_artifact_id, candidate_artifact_version, candidate_digest, checkpoint_mode,
	max_revisions, scope_digest, policy_digest, decision_action, decision_action_key, decision_comment,
	decided_by_type, decided_by_id, decided_at, resource_version, last_global_position, created_at, updated_at
	FROM approval_projection`

const readinessAssessmentSelect = `SELECT assessment_id, run_id, node_id, disposition, assessment_digest,
	policy_digest, submission_json, route_context_json, status, decision_id, decision_choice, decision_remedy_code,
	decision_reason, decision_effect_status, decided_by_type, decided_by_id, decided_at,
	resource_version, last_global_position, created_at, updated_at FROM readiness_assessment_projection`

const inputRequestSelect = `SELECT input_request_id, run_id, attempt_id, node_id, provider_thread_id,
	provider_request_id, scope_digest, request_json, status, answer_json, answer_action_key, answered_by_type,
	answered_by_id, answer_recorded_at, receipt_provider_request_id, delivered_at,
	resource_version, last_global_position, created_at, updated_at FROM input_request_projection`

func readInputRequestProjection(ctx context.Context, query rowQueryer, id string) (statestore.InputRequestProjection, error) {
	return scanInputRequestProjection(query.QueryRowContext(ctx, inputRequestSelect+` WHERE input_request_id = ?`, id))
}

func scanInputRequestProjection(row rowScanner) (statestore.InputRequestProjection, error) {
	var value statestore.InputRequestProjection
	var answer, actionKey, actorType, actorID, answerAt, receiptID, deliveredAt sql.NullString
	var createdAt, updatedAt string
	err := row.Scan(&value.InputRequestID, &value.RunID, &value.AttemptID, &value.NodeID, &value.ProviderThreadID,
		&value.ProviderRequestID, &value.ScopeDigest, &value.Request, &value.Status, &answer, &actionKey, &actorType,
		&actorID, &answerAt, &receiptID, &deliveredAt, &value.ResourceVersion, &value.LastGlobalPosition, &createdAt, &updatedAt)
	if err != nil {
		return statestore.InputRequestProjection{}, err
	}
	if answer.Valid {
		when, parseErr := parseTime(answerAt.String)
		if parseErr != nil {
			return statestore.InputRequestProjection{}, parseErr
		}
		value.Answer = &statestore.InputAnswerProjection{Answer: statestore.JSONSnapshot(answer.String), ActionKey: actionKey.String,
			Actor: statestore.Actor{Type: statestore.ActorType(actorType.String), ID: actorID.String}, RecordedAt: when}
	}
	if receiptID.Valid {
		when, parseErr := parseTime(deliveredAt.String)
		if parseErr != nil {
			return statestore.InputRequestProjection{}, parseErr
		}
		value.Receipt = &statestore.InputReceiptProjection{ProviderRequestID: receiptID.String, DeliveredAt: when}
	}
	value.CreatedAt, err = parseTime(createdAt)
	if err == nil {
		value.UpdatedAt, err = parseTime(updatedAt)
	}
	return value, err
}

type rowsQueryer interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

func queryInputRequests(ctx context.Context, query rowsQueryer, suffix string, args ...any) ([]statestore.InputRequestProjection, error) {
	rows, err := query.QueryContext(ctx, inputRequestSelect+suffix, args...)
	if err != nil {
		return nil, fmt.Errorf("query input requests: %w", err)
	}
	defer func() { _ = rows.Close() }()
	values := make([]statestore.InputRequestProjection, 0)
	for rows.Next() {
		value, scanErr := scanInputRequestProjection(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		values = append(values, value)
	}
	return values, rows.Err()
}

const providerPermissionSelect = `SELECT permission_request_id,run_id,attempt_id,node_id,provider_thread_id,provider_turn_id,provider_request_id,
	interaction_kind,scope_json,scope_digest,policy_digest,evidence_json,status,decision,decision_action_key,decided_by_type,decided_by_id,
	decision_recorded_at,receipt_provider_request_id,delivered_at,resource_version,last_global_position,created_at,updated_at
	FROM provider_permission_projection`

func readProviderPermissionProjection(ctx context.Context, query rowQueryer, id string) (statestore.ProviderPermissionProjection, error) {
	return scanProviderPermissionProjection(query.QueryRowContext(ctx, providerPermissionSelect+` WHERE permission_request_id=?`, id))
}

func scanProviderPermissionProjection(row rowScanner) (statestore.ProviderPermissionProjection, error) {
	var value statestore.ProviderPermissionProjection
	var decision, key, actorType, actorID, decisionAt, receiptID, deliveredAt sql.NullString
	var createdAt, updatedAt string
	err := row.Scan(&value.PermissionRequestID, &value.RunID, &value.AttemptID, &value.NodeID, &value.ProviderThreadID, &value.ProviderTurnID, &value.ProviderRequestID,
		&value.InteractionKind, &value.Scope, &value.ScopeDigest, &value.PolicyDigest, &value.Evidence, &value.Status, &decision, &key, &actorType, &actorID, &decisionAt, &receiptID, &deliveredAt,
		&value.ResourceVersion, &value.LastGlobalPosition, &createdAt, &updatedAt)
	if err != nil {
		return value, err
	}
	if decision.Valid {
		when, parseErr := parseTime(decisionAt.String)
		if parseErr != nil {
			return value, parseErr
		}
		value.Decision = &statestore.ProviderPermissionDecisionProjection{Decision: decision.String, ActionKey: key.String, Actor: statestore.Actor{Type: statestore.ActorType(actorType.String), ID: actorID.String}, RecordedAt: when}
	}
	if receiptID.Valid {
		when, parseErr := parseTime(deliveredAt.String)
		if parseErr != nil {
			return value, parseErr
		}
		value.Receipt = &statestore.ProviderPermissionReceiptProjection{ProviderRequestID: receiptID.String, DeliveredAt: when}
	}
	value.CreatedAt, err = parseTime(createdAt)
	if err == nil {
		value.UpdatedAt, err = parseTime(updatedAt)
	}
	return value, err
}

func queryProviderPermissions(ctx context.Context, query rowsQueryer, suffix string, args ...any) ([]statestore.ProviderPermissionProjection, error) {
	rows, err := query.QueryContext(ctx, providerPermissionSelect+suffix, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	values := make([]statestore.ProviderPermissionProjection, 0)
	for rows.Next() {
		value, scanErr := scanProviderPermissionProjection(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		values = append(values, value)
	}
	return values, rows.Err()
}

func readReadinessAssessmentProjection(ctx context.Context, query rowQueryer, id string) (statestore.ReadinessAssessmentProjection, error) {
	return scanReadinessAssessmentProjection(query.QueryRowContext(ctx, readinessAssessmentSelect+` WHERE assessment_id = ?`, id))
}

func scanReadinessAssessmentProjection(row rowScanner) (statestore.ReadinessAssessmentProjection, error) {
	var value statestore.ReadinessAssessmentProjection
	var createdAt, updatedAt string
	var decisionID, choice, remedyCode, reason, effectStatus, actorType, actorID, decidedAt sql.NullString
	err := row.Scan(&value.AssessmentID, &value.RunID, &value.NodeID, &value.Disposition, &value.AssessmentDigest,
		&value.PolicyDigest, &value.Submission, &value.RouteContext, &value.Status, &decisionID, &choice, &remedyCode,
		&reason, &effectStatus, &actorType, &actorID, &decidedAt,
		&value.ResourceVersion, &value.LastGlobalPosition, &createdAt, &updatedAt)
	if err != nil {
		return statestore.ReadinessAssessmentProjection{}, err
	}
	if decisionID.Valid {
		when, parseErr := parseTime(decidedAt.String)
		if parseErr != nil {
			return statestore.ReadinessAssessmentProjection{}, parseErr
		}
		value.Decision = &statestore.ReadinessDecisionProjection{
			DecisionID: decisionID.String, Choice: choice.String, RemedyCode: remedyCode.String, Reason: reason.String,
			EffectStatus: statestore.ReadinessEffectStatus(effectStatus.String),
			Actor:        statestore.Actor{Type: statestore.ActorType(actorType.String), ID: actorID.String}, DecidedAt: when,
		}
	}
	value.CreatedAt, err = parseTime(createdAt)
	if err == nil {
		value.UpdatedAt, err = parseTime(updatedAt)
	}
	return value, err
}

func readApprovalProjection(ctx context.Context, query rowQueryer, id string) (statestore.ApprovalProjection, error) {
	return scanApprovalProjection(query.QueryRowContext(ctx, approvalSelect+` WHERE approval_id = ?`, id))
}

func scanApprovalProjection(row rowScanner) (statestore.ApprovalProjection, error) {
	var result statestore.ApprovalProjection
	var createdAt, updatedAt string
	var maxRevisions sql.NullInt64
	var decisionAction, decisionActionKey, decisionComment, decidedByType, decidedByID, decidedAt sql.NullString
	err := row.Scan(
		&result.ApprovalID, &result.RunID, &result.Class, &result.Status, &result.CheckpointID, &result.VisitID,
		&result.NodeID, &result.AttemptID, &result.CheckpointRevision, &result.CandidateArtifactID,
		&result.CandidateArtifactVersion, &result.CandidateDigest, &result.CheckpointMode, &maxRevisions,
		&result.ScopeDigest, &result.PolicyDigest, &decisionAction, &decisionActionKey, &decisionComment,
		&decidedByType, &decidedByID, &decidedAt, &result.ResourceVersion, &result.LastGlobalPosition, &createdAt, &updatedAt)
	if err != nil {
		return statestore.ApprovalProjection{}, err
	}
	if maxRevisions.Valid {
		value := uint64(maxRevisions.Int64)
		result.MaxRevisions = &value
	}
	if decisionAction.Valid {
		decisionTime, parseErr := parseTime(decidedAt.String)
		if parseErr != nil {
			return statestore.ApprovalProjection{}, parseErr
		}
		result.Decision = &statestore.ApprovalDecisionProjection{
			Action: decisionAction.String, ActionKey: decisionActionKey.String, Comment: decisionComment.String,
			Actor: statestore.Actor{Type: statestore.ActorType(decidedByType.String), ID: decidedByID.String}, DecidedAt: decisionTime,
		}
	}
	result.CreatedAt, err = parseTime(createdAt)
	if err != nil {
		return statestore.ApprovalProjection{}, err
	}
	result.UpdatedAt, err = parseTime(updatedAt)
	return result, err
}

const attemptSelect = `SELECT attempt_id, run_id, visit_id, node_id, point_id, point_revision, priority, scenario, provider, status,
	provider_thread_id, provider_turn_id, process_owner_id, last_sequence, log_reference,
	resource_version, last_global_position, created_at, updated_at FROM attempt_projection`

type attemptRowScanner interface{ Scan(...any) error }

func scanAttemptProjection(row attemptRowScanner) (statestore.AttemptProjection, error) {
	var value statestore.AttemptProjection
	var createdAt, updatedAt string
	var pointID sql.NullString
	var pointRevision sql.NullInt64
	err := row.Scan(&value.AttemptID, &value.RunID, &value.VisitID, &value.NodeID, &pointID, &pointRevision, &value.Priority, &value.Scenario, &value.Provider, &value.Status,
		&value.ProviderThreadID, &value.ProviderTurnID, &value.ProcessOwnerID, &value.LastSequence, &value.LogReference,
		&value.ResourceVersion, &value.LastGlobalPosition, &createdAt, &updatedAt)
	if err != nil {
		return statestore.AttemptProjection{}, err
	}
	if pointID.Valid {
		value.PointID = pointID.String
		value.PointRevision = uint64(pointRevision.Int64)
	}
	value.CreatedAt, err = parseTime(createdAt)
	if err == nil {
		value.UpdatedAt, err = parseTime(updatedAt)
	}
	return value, err
}

func readAttemptProjection(ctx context.Context, query rowQueryer, id string) (statestore.AttemptProjection, error) {
	return scanAttemptProjection(query.QueryRowContext(ctx, attemptSelect+` WHERE attempt_id = ?`, id))
}

func writeRunProjection(ctx context.Context, tx *sql.Tx, value statestore.RunProjection) error {
	routeSnapshot := value.RouteSnapshot
	if len(routeSnapshot) == 0 {
		routeSnapshot = statestore.JSONSnapshot(`{}`)
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO run_projection VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(run_id) DO UPDATE SET workflow_digest=excluded.workflow_digest,
		route_digest=excluded.route_digest, route_snapshot_json=excluded.route_snapshot_json,
		status=excluded.status, resource_version=excluded.resource_version,
		last_global_position=excluded.last_global_position, updated_at=excluded.updated_at`,
		value.RunID, value.WorkItemID, value.WorkflowID, value.WorkflowVersion, value.WorkflowDigest,
		value.RouteDigest, string(routeSnapshot), value.Priority, value.Status,
		value.ResourceVersion, value.LastGlobalPosition, formatTime(value.CreatedAt), formatTime(value.UpdatedAt))
	if err != nil {
		return fmt.Errorf("write run projection %s: %w", value.RunID, err)
	}
	return nil
}

func writeNodeProjection(ctx context.Context, tx *sql.Tx, value statestore.NodeProjection) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO node_projection VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(visit_id) DO UPDATE SET status=excluded.status, resource_version=excluded.resource_version,
		last_global_position=excluded.last_global_position, updated_at=excluded.updated_at`,
		value.VisitID, value.RunID, value.NodeID, value.Status, value.ResourceVersion,
		value.LastGlobalPosition, formatTime(value.CreatedAt), formatTime(value.UpdatedAt))
	if err != nil {
		return fmt.Errorf("write node projection %s: %w", value.VisitID, err)
	}
	return nil
}

func writeApprovalProjection(ctx context.Context, tx *sql.Tx, value statestore.ApprovalProjection) error {
	var maxRevisions, decisionAction, decisionActionKey, decisionComment, decidedByType, decidedByID, decidedAt any
	if value.MaxRevisions != nil {
		maxRevisions = *value.MaxRevisions
	}
	if value.Decision != nil {
		decisionAction, decisionActionKey, decisionComment = value.Decision.Action, value.Decision.ActionKey, value.Decision.Comment
		decidedByType, decidedByID, decidedAt = value.Decision.Actor.Type, value.Decision.Actor.ID, formatTime(value.Decision.DecidedAt)
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO approval_projection(
		approval_id, run_id, class, status, checkpoint_id, visit_id, node_id, attempt_id,
		checkpoint_revision, candidate_artifact_id, candidate_artifact_version, candidate_digest, checkpoint_mode,
		max_revisions, scope_digest, policy_digest, decision_action, decision_action_key, decision_comment,
		decided_by_type, decided_by_id, decided_at, resource_version, last_global_position, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(approval_id) DO UPDATE SET status=excluded.status, decision_action=excluded.decision_action,
		decision_action_key=excluded.decision_action_key, decision_comment=excluded.decision_comment,
		decided_by_type=excluded.decided_by_type, decided_by_id=excluded.decided_by_id, decided_at=excluded.decided_at,
		resource_version=excluded.resource_version, last_global_position=excluded.last_global_position, updated_at=excluded.updated_at`,
		value.ApprovalID, value.RunID, value.Class, value.Status, value.CheckpointID, value.VisitID, value.NodeID,
		value.AttemptID, value.CheckpointRevision, value.CandidateArtifactID, value.CandidateArtifactVersion,
		value.CandidateDigest, value.CheckpointMode, maxRevisions, value.ScopeDigest, value.PolicyDigest,
		decisionAction, decisionActionKey, decisionComment, decidedByType, decidedByID, decidedAt,
		value.ResourceVersion, value.LastGlobalPosition, formatTime(value.CreatedAt), formatTime(value.UpdatedAt))
	if err != nil {
		return fmt.Errorf("write approval projection %s: %w", value.ApprovalID, err)
	}
	return nil
}

func writeReadinessAssessmentProjection(ctx context.Context, tx *sql.Tx, value statestore.ReadinessAssessmentProjection) error {
	var decisionID, choice, remedyCode, reason, effectStatus, actorType, actorID, decidedAt any
	if value.Decision != nil {
		decisionID, choice, reason = value.Decision.DecisionID, value.Decision.Choice, value.Decision.Reason
		effectStatus, actorType, actorID, decidedAt = value.Decision.EffectStatus, value.Decision.Actor.Type, value.Decision.Actor.ID, formatTime(value.Decision.DecidedAt)
		if value.Decision.RemedyCode != "" {
			remedyCode = value.Decision.RemedyCode
		}
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO readiness_assessment_projection(
		assessment_id, run_id, node_id, disposition, assessment_digest, policy_digest, submission_json, route_context_json,
		status, decision_id, decision_choice, decision_remedy_code, decision_reason, decision_effect_status,
		decided_by_type, decided_by_id, decided_at, resource_version, last_global_position, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(assessment_id) DO UPDATE SET status=excluded.status, decision_id=excluded.decision_id,
		decision_choice=excluded.decision_choice, decision_remedy_code=excluded.decision_remedy_code,
		decision_reason=excluded.decision_reason, decision_effect_status=excluded.decision_effect_status,
		decided_by_type=excluded.decided_by_type, decided_by_id=excluded.decided_by_id, decided_at=excluded.decided_at,
		resource_version=excluded.resource_version, last_global_position=excluded.last_global_position, updated_at=excluded.updated_at`,
		value.AssessmentID, value.RunID, value.NodeID, value.Disposition, value.AssessmentDigest, value.PolicyDigest,
		string(value.Submission), string(value.RouteContext), value.Status, decisionID, choice, remedyCode, reason, effectStatus,
		actorType, actorID, decidedAt, value.ResourceVersion, value.LastGlobalPosition, formatTime(value.CreatedAt), formatTime(value.UpdatedAt))
	if err != nil {
		return fmt.Errorf("write readiness assessment projection %s: %w", value.AssessmentID, err)
	}
	return nil
}

func writeInputRequestProjection(ctx context.Context, tx *sql.Tx, value statestore.InputRequestProjection) error {
	var answer, actionKey, actorType, actorID, answerAt, receiptID, deliveredAt any
	if value.Answer != nil {
		answer, actionKey, actorType, actorID, answerAt = string(value.Answer.Answer), value.Answer.ActionKey,
			value.Answer.Actor.Type, value.Answer.Actor.ID, formatTime(value.Answer.RecordedAt)
	}
	if value.Receipt != nil {
		receiptID, deliveredAt = value.Receipt.ProviderRequestID, formatTime(value.Receipt.DeliveredAt)
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO input_request_projection(
		input_request_id, run_id, attempt_id, node_id, provider_thread_id, provider_request_id, scope_digest,
		request_json, status, answer_json, answer_action_key, answered_by_type, answered_by_id, answer_recorded_at,
		receipt_provider_request_id, delivered_at, resource_version, last_global_position, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(input_request_id) DO UPDATE SET status=excluded.status, answer_json=excluded.answer_json,
		answer_action_key=excluded.answer_action_key, answered_by_type=excluded.answered_by_type,
		answered_by_id=excluded.answered_by_id, answer_recorded_at=excluded.answer_recorded_at,
		receipt_provider_request_id=excluded.receipt_provider_request_id, delivered_at=excluded.delivered_at,
		resource_version=excluded.resource_version, last_global_position=excluded.last_global_position, updated_at=excluded.updated_at`,
		value.InputRequestID, value.RunID, value.AttemptID, value.NodeID, value.ProviderThreadID, value.ProviderRequestID,
		value.ScopeDigest, string(value.Request), value.Status, answer, actionKey, actorType, actorID, answerAt,
		receiptID, deliveredAt, value.ResourceVersion, value.LastGlobalPosition, formatTime(value.CreatedAt), formatTime(value.UpdatedAt))
	if err != nil {
		return fmt.Errorf("write input request projection %s: %w", value.InputRequestID, err)
	}
	return nil
}

func writeProviderPermissionProjection(ctx context.Context, tx *sql.Tx, value statestore.ProviderPermissionProjection) error {
	var decision, key, actorType, actorID, decisionAt, receiptID, deliveredAt any
	if value.Decision != nil {
		decision, key, actorType, actorID, decisionAt = value.Decision.Decision, value.Decision.ActionKey, value.Decision.Actor.Type, value.Decision.Actor.ID, formatTime(value.Decision.RecordedAt)
	}
	if value.Receipt != nil {
		receiptID, deliveredAt = value.Receipt.ProviderRequestID, formatTime(value.Receipt.DeliveredAt)
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO provider_permission_projection(permission_request_id,run_id,attempt_id,node_id,provider_thread_id,provider_turn_id,provider_request_id,interaction_kind,scope_json,scope_digest,policy_digest,evidence_json,status,decision,decision_action_key,decided_by_type,decided_by_id,decision_recorded_at,receipt_provider_request_id,delivered_at,resource_version,last_global_position,created_at,updated_at)
	VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT(permission_request_id) DO UPDATE SET status=excluded.status,decision=excluded.decision,decision_action_key=excluded.decision_action_key,decided_by_type=excluded.decided_by_type,decided_by_id=excluded.decided_by_id,decision_recorded_at=excluded.decision_recorded_at,receipt_provider_request_id=excluded.receipt_provider_request_id,delivered_at=excluded.delivered_at,resource_version=excluded.resource_version,last_global_position=excluded.last_global_position,updated_at=excluded.updated_at`,
		value.PermissionRequestID, value.RunID, value.AttemptID, value.NodeID, value.ProviderThreadID, value.ProviderTurnID, value.ProviderRequestID, value.InteractionKind, string(value.Scope), value.ScopeDigest, value.PolicyDigest, string(value.Evidence), value.Status, decision, key, actorType, actorID, decisionAt, receiptID, deliveredAt, value.ResourceVersion, value.LastGlobalPosition, formatTime(value.CreatedAt), formatTime(value.UpdatedAt))
	return err
}

func writeAttemptProjection(ctx context.Context, tx *sql.Tx, value statestore.AttemptProjection) error {
	var pointID, pointRevision any
	if value.PointID != "" {
		pointID, pointRevision = value.PointID, value.PointRevision
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO attempt_projection(
		attempt_id, run_id, visit_id, node_id, point_id, point_revision, priority, scenario, provider, status,
		provider_thread_id, provider_turn_id, process_owner_id, last_sequence, log_reference,
		resource_version, last_global_position, created_at, updated_at
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(attempt_id) DO UPDATE SET status=excluded.status,
		provider_thread_id=excluded.provider_thread_id, provider_turn_id=excluded.provider_turn_id,
		process_owner_id=excluded.process_owner_id, last_sequence=excluded.last_sequence,
		resource_version=excluded.resource_version, last_global_position=excluded.last_global_position,
		updated_at=excluded.updated_at`,
		value.AttemptID, value.RunID, value.VisitID, value.NodeID, pointID, pointRevision, value.Priority,
		value.Scenario, value.Provider, value.Status,
		value.ProviderThreadID, value.ProviderTurnID, value.ProcessOwnerID, value.LastSequence, value.LogReference,
		value.ResourceVersion, value.LastGlobalPosition, formatTime(value.CreatedAt), formatTime(value.UpdatedAt))
	if err != nil {
		return fmt.Errorf("write attempt projection %s: %w", value.AttemptID, err)
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
