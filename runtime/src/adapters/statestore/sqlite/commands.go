package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/fdsprod/darkstar/runtime/src/ports/statestore"
)

// CommandConflictError reports reuse of an idempotency key for different input.
type CommandConflictError struct{ Scope, IdempotencyKey string }

func (e *CommandConflictError) Error() string {
	return fmt.Sprintf("command %s/%s conflicts with existing request", e.Scope, e.IdempotencyKey)
}

// BeginCommand records a pending command or returns the existing exact command.
func (d *Database) BeginCommand(ctx context.Context, request statestore.BeginCommandRequest) (statestore.CommandEvidence, bool, error) {
	if strings.TrimSpace(request.Scope) == "" || strings.TrimSpace(request.IdempotencyKey) == "" ||
		strings.TrimSpace(request.RequestDigest) == "" || request.CreatedAt.IsZero() {
		return statestore.CommandEvidence{}, false, errors.New("command scope, idempotency key, request digest, and created time are required")
	}
	result, err := d.sql.ExecContext(ctx, `INSERT INTO commands(scope, idempotency_key, request_digest, status, created_at)
		VALUES (?, ?, ?, 'pending', ?) ON CONFLICT(scope, idempotency_key) DO NOTHING`,
		request.Scope, request.IdempotencyKey, request.RequestDigest, formatTime(request.CreatedAt.UTC().Round(0)))
	if err != nil {
		return statestore.CommandEvidence{}, false, fmt.Errorf("begin command: %w", err)
	}
	inserted, err := result.RowsAffected()
	if err != nil {
		return statestore.CommandEvidence{}, false, fmt.Errorf("inspect begun command: %w", err)
	}
	existing, err := d.readCommand(ctx, request.Scope, request.IdempotencyKey)
	if err != nil {
		return statestore.CommandEvidence{}, false, err
	}
	if existing.RequestDigest != request.RequestDigest {
		return statestore.CommandEvidence{}, true, &CommandConflictError{Scope: request.Scope, IdempotencyKey: request.IdempotencyKey}
	}
	return existing, inserted == 0, nil
}

// CompleteCommand atomically closes a pending command with replay evidence.
func (d *Database) CompleteCommand(ctx context.Context, request statestore.CompleteCommandRequest) (statestore.CommandEvidence, error) {
	if request.ResponseStatus < 100 || !json.Valid(request.Response) || request.CompletedAt.IsZero() {
		return statestore.CommandEvidence{}, errors.New("completed command requires an HTTP status, valid JSON response, and completion time")
	}
	if (request.FirstEventPosition == nil) != (request.LastEventPosition == nil) ||
		(request.FirstEventPosition != nil && *request.FirstEventPosition > *request.LastEventPosition) {
		return statestore.CommandEvidence{}, errors.New("command event positions must form an ordered pair")
	}
	result, err := d.sql.ExecContext(ctx, `UPDATE commands SET status='completed', response_status=?, response_json=?,
		first_event_position=?, last_event_position=?, completed_at=?
		WHERE scope=? AND idempotency_key=? AND status='pending'`, request.ResponseStatus, string(request.Response),
		request.FirstEventPosition, request.LastEventPosition, formatTime(request.CompletedAt.UTC().Round(0)),
		request.Scope, request.IdempotencyKey)
	if err != nil {
		return statestore.CommandEvidence{}, fmt.Errorf("complete command: %w", err)
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return statestore.CommandEvidence{}, fmt.Errorf("inspect completed command: %w", err)
	}
	if changed != 1 {
		return statestore.CommandEvidence{}, errors.New("command is missing or already completed")
	}
	return d.readCommand(ctx, request.Scope, request.IdempotencyKey)
}

func (d *Database) readCommand(ctx context.Context, scope, key string) (statestore.CommandEvidence, error) {
	var value statestore.CommandEvidence
	var responseStatus, firstPosition, lastPosition sql.NullInt64
	var responseJSON, completedAt sql.NullString
	var createdAt string
	err := d.sql.QueryRowContext(ctx, `SELECT scope, idempotency_key, request_digest, status, response_status,
		response_json, first_event_position, last_event_position, created_at, completed_at
		FROM commands WHERE scope=? AND idempotency_key=?`, scope, key).Scan(
		&value.Scope, &value.IdempotencyKey, &value.RequestDigest, &value.Status, &responseStatus,
		&responseJSON, &firstPosition, &lastPosition, &createdAt, &completedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return statestore.CommandEvidence{}, &NotFoundError{Kind: "command", ID: scope + "/" + key}
	}
	if err != nil {
		return statestore.CommandEvidence{}, fmt.Errorf("read command: %w", err)
	}
	value.CreatedAt, err = parseTime(createdAt)
	if err != nil {
		return statestore.CommandEvidence{}, err
	}
	if responseStatus.Valid {
		v := int(responseStatus.Int64)
		value.ResponseStatus = &v
	}
	if responseJSON.Valid {
		value.Response = json.RawMessage(responseJSON.String)
	}
	if firstPosition.Valid {
		v := uint64(firstPosition.Int64)
		value.FirstEventPosition = &v
	}
	if lastPosition.Valid {
		v := uint64(lastPosition.Int64)
		value.LastEventPosition = &v
	}
	if completedAt.Valid {
		v, parseErr := parseTime(completedAt.String)
		if parseErr != nil {
			return statestore.CommandEvidence{}, parseErr
		}
		value.CompletedAt = &v
	}
	return value, nil
}
