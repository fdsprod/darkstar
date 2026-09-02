package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"darkstar/src/ports/statestore"
)

type workQueryer interface {
	rowQueryer
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

const projectSelect = `SELECT project_id, name, source_hash, status, resource_version,
	last_global_position, created_at, updated_at FROM project_projection`

func scanProjectProjection(row interface{ Scan(...any) error }) (statestore.ProjectProjection, error) {
	var value statestore.ProjectProjection
	var createdAt, updatedAt string
	err := row.Scan(&value.ProjectID, &value.Name, &value.SourceHash, &value.Status, &value.ResourceVersion,
		&value.LastGlobalPosition, &createdAt, &updatedAt)
	if err != nil {
		return statestore.ProjectProjection{}, err
	}
	value.CreatedAt, err = parseTime(createdAt)
	if err == nil {
		value.UpdatedAt, err = parseTime(updatedAt)
	}
	return value, err
}

func readProjectProjection(ctx context.Context, query rowQueryer, id string) (statestore.ProjectProjection, error) {
	return scanProjectProjection(query.QueryRowContext(ctx, projectSelect+` WHERE project_id = ?`, id))
}

func (d *Database) Project(ctx context.Context, id string) (statestore.ProjectProjection, error) {
	value, err := readProjectProjection(ctx, d.sql, id)
	return value, projectionReadError("project", id, err)
}

func (d *Database) Projects(ctx context.Context) ([]statestore.ProjectProjection, error) {
	rows, err := d.sql.QueryContext(ctx, projectSelect+` ORDER BY created_at, project_id`)
	if err != nil {
		return nil, fmt.Errorf("query projects: %w", err)
	}
	defer func() { _ = rows.Close() }()
	values := make([]statestore.ProjectProjection, 0)
	for rows.Next() {
		value, scanErr := scanProjectProjection(rows)
		if scanErr != nil {
			return nil, fmt.Errorf("scan project: %w", scanErr)
		}
		values = append(values, value)
	}
	return values, rows.Err()
}

const workItemSelect = `SELECT work_item_id, project_id, title, source_hash, priority, status,
	resource_version, last_global_position, created_at, updated_at FROM work_item_projection`

func scanWorkItemProjection(row interface{ Scan(...any) error }) (statestore.WorkItemProjection, error) {
	var value statestore.WorkItemProjection
	var createdAt, updatedAt string
	err := row.Scan(&value.WorkItemID, &value.ProjectID, &value.Title, &value.SourceHash, &value.Priority, &value.Status,
		&value.ResourceVersion, &value.LastGlobalPosition, &createdAt, &updatedAt)
	if err != nil {
		return statestore.WorkItemProjection{}, err
	}
	value.CreatedAt, err = parseTime(createdAt)
	if err == nil {
		value.UpdatedAt, err = parseTime(updatedAt)
	}
	return value, err
}

func readWorkItemProjection(ctx context.Context, query rowQueryer, id string) (statestore.WorkItemProjection, error) {
	return scanWorkItemProjection(query.QueryRowContext(ctx, workItemSelect+` WHERE work_item_id = ?`, id))
}

func (d *Database) WorkItem(ctx context.Context, id string) (statestore.WorkItemProjection, error) {
	value, err := readWorkItemProjection(ctx, d.sql, id)
	return value, projectionReadError("work item", id, err)
}

func (d *Database) WorkItems(ctx context.Context) ([]statestore.WorkItemProjection, error) {
	return queryWorkItems(ctx, d.sql, "", nil)
}

func (d *Database) WorkItemsForProject(ctx context.Context, projectID string) ([]statestore.WorkItemProjection, error) {
	return queryWorkItems(ctx, d.sql, ` WHERE project_id = ?`, []any{projectID})
}

func queryWorkItems(ctx context.Context, query workQueryer, where string, arguments []any) ([]statestore.WorkItemProjection, error) {
	rows, err := query.QueryContext(ctx, workItemSelect+where+` ORDER BY priority DESC, created_at, work_item_id`, arguments...)
	if err != nil {
		return nil, fmt.Errorf("query work items: %w", err)
	}
	defer func() { _ = rows.Close() }()
	values := make([]statestore.WorkItemProjection, 0)
	for rows.Next() {
		value, scanErr := scanWorkItemProjection(rows)
		if scanErr != nil {
			return nil, fmt.Errorf("scan work item: %w", scanErr)
		}
		values = append(values, value)
	}
	return values, rows.Err()
}

func (d *Database) RunsForWorkItem(ctx context.Context, workItemID string) ([]statestore.RunProjection, error) {
	return queryRuns(ctx, d.sql, ` WHERE work_item_id = ?`, []any{workItemID})
}

func (d *Database) Runs(ctx context.Context) ([]statestore.RunProjection, error) {
	return queryRuns(ctx, d.sql, "", nil)
}

func queryRuns(ctx context.Context, query workQueryer, where string, arguments []any) ([]statestore.RunProjection, error) {
	rows, err := query.QueryContext(ctx, runSelect+where+` ORDER BY priority DESC, created_at, run_id`, arguments...)
	if err != nil {
		return nil, fmt.Errorf("query runs: %w", err)
	}
	defer func() { _ = rows.Close() }()
	values := make([]statestore.RunProjection, 0)
	for rows.Next() {
		value, scanErr := scanRunProjection(rows)
		if scanErr != nil {
			return nil, fmt.Errorf("scan run: %w", scanErr)
		}
		values = append(values, value)
	}
	return values, rows.Err()
}

const storySelect = `SELECT story_id, work_item_id, title, source_hash, priority, position, status,
	resource_version, last_global_position, created_at, updated_at FROM story_projection`

func scanStoryProjection(row interface{ Scan(...any) error }) (statestore.StoryProjection, error) {
	var value statestore.StoryProjection
	var createdAt, updatedAt string
	err := row.Scan(&value.StoryID, &value.WorkItemID, &value.Title, &value.SourceHash, &value.Priority, &value.Position,
		&value.Status, &value.ResourceVersion, &value.LastGlobalPosition, &createdAt, &updatedAt)
	if err != nil {
		return statestore.StoryProjection{}, err
	}
	value.CreatedAt, err = parseTime(createdAt)
	if err == nil {
		value.UpdatedAt, err = parseTime(updatedAt)
	}
	return value, err
}

func readStoryProjection(ctx context.Context, query rowQueryer, id string) (statestore.StoryProjection, error) {
	return scanStoryProjection(query.QueryRowContext(ctx, storySelect+` WHERE story_id = ?`, id))
}

func (d *Database) Story(ctx context.Context, id string) (statestore.StoryProjection, error) {
	value, err := readStoryProjection(ctx, d.sql, id)
	return value, projectionReadError("story", id, err)
}

func (d *Database) StoriesForWorkItem(ctx context.Context, workItemID string) ([]statestore.StoryProjection, error) {
	rows, err := d.sql.QueryContext(ctx, storySelect+` WHERE work_item_id = ? ORDER BY position, priority DESC, story_id`, workItemID)
	if err != nil {
		return nil, fmt.Errorf("query work-item stories: %w", err)
	}
	defer func() { _ = rows.Close() }()
	values := make([]statestore.StoryProjection, 0)
	for rows.Next() {
		value, scanErr := scanStoryProjection(rows)
		if scanErr != nil {
			return nil, fmt.Errorf("scan story: %w", scanErr)
		}
		values = append(values, value)
	}
	return values, rows.Err()
}

const pointSelect = `SELECT point_id, story_id, revision, title, source_hash, priority, position, status,
	resource_version, last_global_position, created_at, updated_at FROM point_projection`

func scanPointProjection(row interface{ Scan(...any) error }) (statestore.PointProjection, error) {
	var value statestore.PointProjection
	var createdAt, updatedAt string
	err := row.Scan(&value.PointID, &value.StoryID, &value.Revision, &value.Title, &value.SourceHash, &value.Priority,
		&value.Position, &value.Status, &value.ResourceVersion, &value.LastGlobalPosition, &createdAt, &updatedAt)
	if err != nil {
		return statestore.PointProjection{}, err
	}
	value.CreatedAt, err = parseTime(createdAt)
	if err == nil {
		value.UpdatedAt, err = parseTime(updatedAt)
	}
	return value, err
}

func readPointProjection(ctx context.Context, query workQueryer, id string) (statestore.PointProjection, error) {
	value, err := scanPointProjection(query.QueryRowContext(ctx, pointSelect+` WHERE point_id = ?`, id))
	if err != nil {
		return statestore.PointProjection{}, err
	}
	value.Dependencies, err = readPointDependencies(ctx, query, id)
	return value, err
}

func readPointDependencies(ctx context.Context, query workQueryer, pointID string) ([]string, error) {
	rows, err := query.QueryContext(ctx, `SELECT depends_on_point_id FROM point_dependencies WHERE point_id = ? ORDER BY depends_on_point_id`, pointID)
	if err != nil {
		return nil, fmt.Errorf("query point dependencies: %w", err)
	}
	defer func() { _ = rows.Close() }()
	values := make([]string, 0)
	for rows.Next() {
		var dependency string
		if err := rows.Scan(&dependency); err != nil {
			return nil, fmt.Errorf("scan point dependency: %w", err)
		}
		values = append(values, dependency)
	}
	return values, rows.Err()
}

func (d *Database) Point(ctx context.Context, id string) (statestore.PointProjection, error) {
	value, err := readPointProjection(ctx, d.sql, id)
	return value, projectionReadError("point", id, err)
}

func (d *Database) PointsForStory(ctx context.Context, storyID string) ([]statestore.PointProjection, error) {
	rows, err := d.sql.QueryContext(ctx, pointSelect+` WHERE story_id = ? ORDER BY position, priority DESC, point_id`, storyID)
	if err != nil {
		return nil, fmt.Errorf("query story points: %w", err)
	}
	values := make([]statestore.PointProjection, 0)
	for rows.Next() {
		value, scanErr := scanPointProjection(rows)
		if scanErr != nil {
			_ = rows.Close()
			return nil, fmt.Errorf("scan point: %w", scanErr)
		}
		values = append(values, value)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, fmt.Errorf("close point rows: %w", err)
	}
	for index := range values {
		values[index].Dependencies, err = readPointDependencies(ctx, d.sql, values[index].PointID)
		if err != nil {
			return nil, err
		}
	}
	return values, nil
}

func (d *Database) AttemptsForPoint(ctx context.Context, pointID string, revision uint64) ([]statestore.AttemptProjection, error) {
	rows, err := d.sql.QueryContext(ctx, attemptSelect+` WHERE point_id = ? AND point_revision = ? ORDER BY created_at, attempt_id`, pointID, revision)
	if err != nil {
		return nil, fmt.Errorf("query point attempts: %w", err)
	}
	defer func() { _ = rows.Close() }()
	values := make([]statestore.AttemptProjection, 0)
	for rows.Next() {
		value, scanErr := scanAttemptProjection(rows)
		if scanErr != nil {
			return nil, fmt.Errorf("scan point attempt: %w", scanErr)
		}
		values = append(values, value)
	}
	return values, rows.Err()
}

func writeProjectProjection(ctx context.Context, tx *sql.Tx, value statestore.ProjectProjection) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO project_projection VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(project_id) DO UPDATE SET status=excluded.status, resource_version=excluded.resource_version,
		last_global_position=excluded.last_global_position, updated_at=excluded.updated_at`, value.ProjectID, value.Name,
		value.SourceHash, value.Status, value.ResourceVersion, value.LastGlobalPosition, formatTime(value.CreatedAt), formatTime(value.UpdatedAt))
	return projectionWriteError("project", value.ProjectID, err)
}

func writeWorkItemProjection(ctx context.Context, tx *sql.Tx, value statestore.WorkItemProjection) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO work_item_projection VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(work_item_id) DO UPDATE SET status=excluded.status, resource_version=excluded.resource_version,
		last_global_position=excluded.last_global_position, updated_at=excluded.updated_at`, value.WorkItemID, value.ProjectID,
		value.Title, value.SourceHash, value.Priority, value.Status, value.ResourceVersion, value.LastGlobalPosition,
		formatTime(value.CreatedAt), formatTime(value.UpdatedAt))
	return projectionWriteError("work item", value.WorkItemID, err)
}

func writeStoryProjection(ctx context.Context, tx *sql.Tx, value statestore.StoryProjection) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO story_projection VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(story_id) DO UPDATE SET status=excluded.status, resource_version=excluded.resource_version,
		last_global_position=excluded.last_global_position, updated_at=excluded.updated_at`, value.StoryID, value.WorkItemID,
		value.Title, value.SourceHash, value.Priority, value.Position, value.Status, value.ResourceVersion,
		value.LastGlobalPosition, formatTime(value.CreatedAt), formatTime(value.UpdatedAt))
	return projectionWriteError("story", value.StoryID, err)
}

func writePointProjection(ctx context.Context, tx *sql.Tx, value statestore.PointProjection) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO point_projection VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(point_id) DO UPDATE SET revision=excluded.revision, title=excluded.title,
		source_hash=excluded.source_hash, priority=excluded.priority, position=excluded.position, status=excluded.status,
		resource_version=excluded.resource_version, last_global_position=excluded.last_global_position, updated_at=excluded.updated_at`,
		value.PointID, value.StoryID, value.Revision, value.Title, value.SourceHash, value.Priority, value.Position, value.Status,
		value.ResourceVersion, value.LastGlobalPosition, formatTime(value.CreatedAt), formatTime(value.UpdatedAt))
	if err != nil {
		return projectionWriteError("point", value.PointID, err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM point_dependencies WHERE point_id = ?`, value.PointID); err != nil {
		return fmt.Errorf("clear point %s dependencies: %w", value.PointID, err)
	}
	for _, dependency := range value.Dependencies {
		var dependencyStoryID string
		if err := tx.QueryRowContext(ctx, `SELECT story_id FROM point_projection WHERE point_id = ?`, dependency).Scan(&dependencyStoryID); err != nil {
			return fmt.Errorf("read point %s dependency %s: %w", value.PointID, dependency, err)
		}
		if dependencyStoryID != value.StoryID {
			return fmt.Errorf("point %s dependency %s belongs to story %s, not %s", value.PointID, dependency, dependencyStoryID, value.StoryID)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO point_dependencies(point_id, depends_on_point_id, source_revision) VALUES (?, ?, ?)`, value.PointID, dependency, value.Revision); err != nil {
			return fmt.Errorf("write point %s dependency %s: %w", value.PointID, dependency, err)
		}
	}
	return nil
}

func projectionReadError(kind, id string, err error) error {
	if errors.Is(err, sql.ErrNoRows) {
		return &NotFoundError{Kind: kind, ID: id}
	}
	if err != nil {
		return fmt.Errorf("read %s projection: %w", kind, err)
	}
	return nil
}

func projectionWriteError(kind, id string, err error) error {
	if err != nil {
		return fmt.Errorf("write %s projection %s: %w", kind, id, err)
	}
	return nil
}
