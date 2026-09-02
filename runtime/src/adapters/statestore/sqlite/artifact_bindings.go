package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/fdsprod/darkstar/runtime/src/ports/artifactbinding"
	"github.com/fdsprod/darkstar/runtime/src/ports/artifactregistry"
)

var _ artifactbinding.Store = (*Database)(nil)

const bindingVersionSelect = `SELECT binding_id, version, idempotency_key, state,
	artifact_id, artifact_version, target_kind, target_id, created_at
	FROM artifact_binding_versions`

// Bind creates or reactivates a logical binding with an exact artifact version.
func (d *Database) Bind(ctx context.Context, request artifactbinding.BindRequest) (artifactbinding.Version, bool, error) {
	normalized, err := normalizeBindRequest(request)
	if err != nil {
		return artifactbinding.Version{}, false, err
	}
	tx, err := d.sql.BeginTx(ctx, nil)
	if err != nil {
		return artifactbinding.Version{}, false, fmt.Errorf("begin artifact bind: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	existing, existingKey, err := scanBindingVersion(tx.QueryRowContext(ctx,
		bindingVersionSelect+` WHERE binding_id = ? AND idempotency_key = ?`,
		normalized.BindingID, normalized.IdempotencyKey))
	if err == nil {
		if !sameBind(existing, existingKey, normalized) {
			return artifactbinding.Version{}, false, fmt.Errorf("%w: binding %s key %s", artifactbinding.ErrConflict, normalized.BindingID, normalized.IdempotencyKey)
		}
		if err := tx.Commit(); err != nil {
			return artifactbinding.Version{}, false, fmt.Errorf("commit repeated artifact bind: %w", err)
		}
		return existing, false, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return artifactbinding.Version{}, false, fmt.Errorf("read artifact bind: %w", err)
	}

	latest, _, err := scanBindingVersion(tx.QueryRowContext(ctx,
		bindingVersionSelect+` WHERE binding_id = ? ORDER BY version DESC LIMIT 1`, normalized.BindingID))
	nextVersion := uint64(1)
	if err == nil {
		if latest.State != artifactbinding.StateUnbound {
			return artifactbinding.Version{}, false, fmt.Errorf("%w: binding %s is already bound", artifactbinding.ErrStateConflict, normalized.BindingID)
		}
		if latest.Target != normalized.Target || latest.Artifact.ArtifactID != normalized.Artifact.ArtifactID {
			return artifactbinding.Version{}, false, fmt.Errorf("%w: binding identity cannot change", artifactbinding.ErrConflict)
		}
		if normalized.Artifact.Version < latest.Artifact.Version {
			return artifactbinding.Version{}, false, fmt.Errorf("%w: artifact version cannot move backwards", artifactbinding.ErrConflict)
		}
		nextVersion = latest.Version + 1
	} else if !errors.Is(err, sql.ErrNoRows) {
		return artifactbinding.Version{}, false, fmt.Errorf("read latest artifact binding: %w", err)
	}

	created, err := insertBindingVersion(ctx, tx, normalized.BindingID, nextVersion,
		normalized.IdempotencyKey, artifactbinding.StateBound, normalized.Artifact,
		normalized.Target, normalized.CreatedAt)
	if err != nil {
		return artifactbinding.Version{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return artifactbinding.Version{}, false, fmt.Errorf("commit artifact bind: %w", err)
	}
	return created, true, nil
}

// Unbind appends a deactivation snapshot while retaining all prior bindings.
func (d *Database) Unbind(ctx context.Context, request artifactbinding.UnbindRequest) (artifactbinding.Version, bool, error) {
	normalized, err := normalizeUnbindRequest(request)
	if err != nil {
		return artifactbinding.Version{}, false, err
	}
	tx, err := d.sql.BeginTx(ctx, nil)
	if err != nil {
		return artifactbinding.Version{}, false, fmt.Errorf("begin artifact unbind: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	existing, existingKey, err := scanBindingVersion(tx.QueryRowContext(ctx,
		bindingVersionSelect+` WHERE binding_id = ? AND idempotency_key = ?`,
		normalized.BindingID, normalized.IdempotencyKey))
	if err == nil {
		if existingKey != normalized.IdempotencyKey || existing.State != artifactbinding.StateUnbound {
			return artifactbinding.Version{}, false, fmt.Errorf("%w: binding %s key %s", artifactbinding.ErrConflict, normalized.BindingID, normalized.IdempotencyKey)
		}
		if err := tx.Commit(); err != nil {
			return artifactbinding.Version{}, false, fmt.Errorf("commit repeated artifact unbind: %w", err)
		}
		return existing, false, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return artifactbinding.Version{}, false, fmt.Errorf("read artifact unbind: %w", err)
	}

	latest, _, err := scanBindingVersion(tx.QueryRowContext(ctx,
		bindingVersionSelect+` WHERE binding_id = ? ORDER BY version DESC LIMIT 1`, normalized.BindingID))
	if errors.Is(err, sql.ErrNoRows) {
		return artifactbinding.Version{}, false, fmt.Errorf("%w: binding %s", artifactbinding.ErrNotFound, normalized.BindingID)
	}
	if err != nil {
		return artifactbinding.Version{}, false, fmt.Errorf("read latest artifact binding: %w", err)
	}
	if latest.State != artifactbinding.StateBound {
		return artifactbinding.Version{}, false, fmt.Errorf("%w: binding %s is already unbound", artifactbinding.ErrStateConflict, normalized.BindingID)
	}
	created, err := insertBindingVersion(ctx, tx, normalized.BindingID, latest.Version+1,
		normalized.IdempotencyKey, artifactbinding.StateUnbound, latest.Artifact,
		latest.Target, normalized.CreatedAt)
	if err != nil {
		return artifactbinding.Version{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return artifactbinding.Version{}, false, fmt.Errorf("commit artifact unbind: %w", err)
	}
	return created, true, nil
}

// BindingVersion returns one exact immutable transition snapshot.
func (d *Database) BindingVersion(ctx context.Context, bindingID string, version uint64) (artifactbinding.Version, error) {
	if !strings.HasPrefix(bindingID, "binding_") || version == 0 {
		return artifactbinding.Version{}, errors.New("binding ID and exact version are required")
	}
	value, _, err := scanBindingVersion(d.sql.QueryRowContext(ctx,
		bindingVersionSelect+` WHERE binding_id = ? AND version = ?`, bindingID, version))
	return classifyBindingRead(value, err, bindingID)
}

// LatestBinding returns the greatest version of one logical binding.
func (d *Database) LatestBinding(ctx context.Context, bindingID string) (artifactbinding.Version, error) {
	if !strings.HasPrefix(bindingID, "binding_") {
		return artifactbinding.Version{}, errors.New("binding ID is required")
	}
	value, _, err := scanBindingVersion(d.sql.QueryRowContext(ctx,
		bindingVersionSelect+` WHERE binding_id = ? ORDER BY version DESC LIMIT 1`, bindingID))
	return classifyBindingRead(value, err, bindingID)
}

// BindingVersions lists a logical binding's complete history.
func (d *Database) BindingVersions(ctx context.Context, bindingID string) ([]artifactbinding.Version, error) {
	if !strings.HasPrefix(bindingID, "binding_") {
		return nil, errors.New("binding ID is required")
	}
	return d.queryBindingVersions(ctx, bindingVersionSelect+` WHERE binding_id = ? ORDER BY version`, bindingID)
}

// ActiveBindings returns the latest bound snapshot for every binding at target.
func (d *Database) ActiveBindings(ctx context.Context, target artifactbinding.Target) ([]artifactbinding.Version, error) {
	if err := validateBindingTarget(target); err != nil {
		return nil, err
	}
	return d.queryBindingVersions(ctx, bindingVersionSelect+` current
		WHERE target_kind = ? AND target_id = ? AND state = 'bound'
		AND version = (SELECT MAX(candidate.version) FROM artifact_binding_versions candidate
			WHERE candidate.binding_id = current.binding_id)
		ORDER BY binding_id`, target.Kind, target.ID)
}

func insertBindingVersion(ctx context.Context, tx *sql.Tx, bindingID string, version uint64,
	idempotencyKey string, state artifactbinding.State, artifact artifactregistry.VersionRef,
	target artifactbinding.Target, createdAt time.Time) (artifactbinding.Version, error) {
	_, err := tx.ExecContext(ctx, `INSERT INTO artifact_binding_versions(
		binding_id, version, idempotency_key, state, artifact_id, artifact_version,
		target_kind, target_id, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		bindingID, version, idempotencyKey, state, artifact.ArtifactID, artifact.Version,
		target.Kind, target.ID, formatTime(createdAt))
	if err != nil {
		return artifactbinding.Version{}, fmt.Errorf("insert artifact binding version: %w", err)
	}
	value, _, err := scanBindingVersion(tx.QueryRowContext(ctx,
		bindingVersionSelect+` WHERE binding_id = ? AND version = ?`, bindingID, version))
	if err != nil {
		return artifactbinding.Version{}, fmt.Errorf("read created artifact binding: %w", err)
	}
	return value, nil
}

func normalizeBindRequest(request artifactbinding.BindRequest) (artifactbinding.BindRequest, error) {
	if !strings.HasPrefix(request.BindingID, "binding_") || strings.TrimSpace(request.IdempotencyKey) == "" {
		return request, errors.New("binding ID and idempotency key are required")
	}
	if err := validateVersionRef(request.Artifact); err != nil {
		return request, err
	}
	if err := validateBindingTarget(request.Target); err != nil {
		return request, err
	}
	if request.CreatedAt.IsZero() {
		return request, errors.New("artifact binding creation time is required")
	}
	request.CreatedAt = request.CreatedAt.UTC()
	return request, nil
}

func normalizeUnbindRequest(request artifactbinding.UnbindRequest) (artifactbinding.UnbindRequest, error) {
	if !strings.HasPrefix(request.BindingID, "binding_") || strings.TrimSpace(request.IdempotencyKey) == "" {
		return request, errors.New("binding ID and idempotency key are required")
	}
	if request.CreatedAt.IsZero() {
		return request, errors.New("artifact unbinding creation time is required")
	}
	request.CreatedAt = request.CreatedAt.UTC()
	return request, nil
}

func validateBindingTarget(target artifactbinding.Target) error {
	if strings.TrimSpace(target.ID) == "" {
		return errors.New("artifact binding target ID is required")
	}
	switch target.Kind {
	case artifactbinding.TargetProject, artifactbinding.TargetWork, artifactbinding.TargetRun,
		artifactbinding.TargetNode, artifactbinding.TargetCheckpoint, artifactbinding.TargetDecision,
		artifactbinding.TargetStory, artifactbinding.TargetImplementationPoint:
		return nil
	default:
		return fmt.Errorf("invalid artifact binding target kind %q", target.Kind)
	}
}

func sameBind(value artifactbinding.Version, key string, request artifactbinding.BindRequest) bool {
	return key == request.IdempotencyKey && value.State == artifactbinding.StateBound &&
		value.BindingID == request.BindingID && value.Artifact == request.Artifact &&
		value.Target == request.Target
}

func classifyBindingRead(value artifactbinding.Version, err error, bindingID string) (artifactbinding.Version, error) {
	if errors.Is(err, sql.ErrNoRows) {
		return artifactbinding.Version{}, fmt.Errorf("%w: binding %s", artifactbinding.ErrNotFound, bindingID)
	}
	if err != nil {
		return artifactbinding.Version{}, fmt.Errorf("read artifact binding: %w", err)
	}
	return value, nil
}

func (d *Database) queryBindingVersions(ctx context.Context, statement string, args ...any) ([]artifactbinding.Version, error) {
	rows, err := d.sql.QueryContext(ctx, statement, args...)
	if err != nil {
		return nil, fmt.Errorf("list artifact binding versions: %w", err)
	}
	defer func() { _ = rows.Close() }()
	result := make([]artifactbinding.Version, 0)
	for rows.Next() {
		value, _, err := scanBindingVersion(rows)
		if err != nil {
			return nil, fmt.Errorf("scan artifact binding version: %w", err)
		}
		result = append(result, value)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate artifact binding versions: %w", err)
	}
	return result, nil
}

func scanBindingVersion(row rowScanner) (artifactbinding.Version, string, error) {
	var value artifactbinding.Version
	var idempotencyKey, createdAt string
	if err := row.Scan(&value.BindingID, &value.Version, &idempotencyKey, &value.State,
		&value.Artifact.ArtifactID, &value.Artifact.Version, &value.Target.Kind,
		&value.Target.ID, &createdAt); err != nil {
		return artifactbinding.Version{}, "", err
	}
	parsed, err := parseTime(createdAt)
	if err != nil {
		return artifactbinding.Version{}, "", err
	}
	value.CreatedAt = parsed
	return value, idempotencyKey, nil
}
