package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/fdsprod/darkstar/runtime/src/ports/artifactlineage"
	"github.com/fdsprod/darkstar/runtime/src/ports/artifactregistry"
)

var _ artifactlineage.Store = (*Database)(nil)

const dependencySelect = `SELECT source_artifact_id, source_artifact_version,
	dependent_artifact_id, dependent_artifact_version, impact, created_at
	FROM artifact_dependencies`

// AddDependency records one immutable exact-version edge. Repeating the same
// edge and impact is idempotent; changing the impact conflicts.
func (d *Database) AddDependency(ctx context.Context, request artifactlineage.AddRequest) (artifactlineage.Dependency, bool, error) {
	normalized, err := normalizeDependencyRequest(request)
	if err != nil {
		return artifactlineage.Dependency{}, false, err
	}

	tx, err := d.sql.BeginTx(ctx, nil)
	if err != nil {
		return artifactlineage.Dependency{}, false, fmt.Errorf("begin artifact dependency: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	existing, err := scanDependency(tx.QueryRowContext(ctx, dependencySelect+` WHERE
		source_artifact_id = ? AND source_artifact_version = ? AND
		dependent_artifact_id = ? AND dependent_artifact_version = ?`,
		normalized.Source.ArtifactID, normalized.Source.Version,
		normalized.Dependent.ArtifactID, normalized.Dependent.Version))
	if err == nil {
		if existing.Impact != normalized.Impact {
			return artifactlineage.Dependency{}, false, fmt.Errorf("%w: exact edge already has impact %s", artifactlineage.ErrDependencyConflict, existing.Impact)
		}
		if err := tx.Commit(); err != nil {
			return artifactlineage.Dependency{}, false, fmt.Errorf("commit repeated artifact dependency: %w", err)
		}
		return existing, false, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return artifactlineage.Dependency{}, false, fmt.Errorf("read artifact dependency: %w", err)
	}
	if err := requireArtifactVersion(ctx, tx, normalized.Source); err != nil {
		return artifactlineage.Dependency{}, false, err
	}
	if err := requireArtifactVersion(ctx, tx, normalized.Dependent); err != nil {
		return artifactlineage.Dependency{}, false, err
	}

	var cycle int
	err = tx.QueryRowContext(ctx, `WITH RECURSIVE descendants(artifact_id, version) AS (
		SELECT dependent_artifact_id, dependent_artifact_version FROM artifact_dependencies
		WHERE source_artifact_id = ? AND source_artifact_version = ?
		UNION
		SELECT edge.dependent_artifact_id, edge.dependent_artifact_version
		FROM artifact_dependencies edge
		JOIN descendants parent ON edge.source_artifact_id = parent.artifact_id
			AND edge.source_artifact_version = parent.version
	)
	SELECT EXISTS(SELECT 1 FROM descendants WHERE artifact_id = ? AND version = ?)`,
		normalized.Dependent.ArtifactID, normalized.Dependent.Version,
		normalized.Source.ArtifactID, normalized.Source.Version).Scan(&cycle)
	if err != nil {
		return artifactlineage.Dependency{}, false, fmt.Errorf("check artifact dependency cycle: %w", err)
	}
	if cycle != 0 {
		return artifactlineage.Dependency{}, false, fmt.Errorf("%w: %s/%d -> %s/%d", artifactlineage.ErrDependencyCycle,
			normalized.Source.ArtifactID, normalized.Source.Version, normalized.Dependent.ArtifactID, normalized.Dependent.Version)
	}

	_, err = tx.ExecContext(ctx, `INSERT INTO artifact_dependencies(
		source_artifact_id, source_artifact_version, dependent_artifact_id,
		dependent_artifact_version, impact, created_at) VALUES (?, ?, ?, ?, ?, ?)`,
		normalized.Source.ArtifactID, normalized.Source.Version, normalized.Dependent.ArtifactID,
		normalized.Dependent.Version, normalized.Impact, formatTime(normalized.CreatedAt))
	if err != nil {
		return artifactlineage.Dependency{}, false, fmt.Errorf("insert artifact dependency: %w", err)
	}
	created := artifactlineage.Dependency(normalized)
	if err := tx.Commit(); err != nil {
		return artifactlineage.Dependency{}, false, fmt.Errorf("commit artifact dependency: %w", err)
	}
	return created, true, nil
}

// Dependencies lists the direct inputs of one exact dependent version.
func (d *Database) Dependencies(ctx context.Context, dependent artifactregistry.VersionRef) ([]artifactlineage.Dependency, error) {
	if err := validateVersionRef(dependent); err != nil {
		return nil, err
	}
	return queryDependencies(ctx, d.sql, dependencySelect+` WHERE dependent_artifact_id = ?
		AND dependent_artifact_version = ? ORDER BY source_artifact_id, source_artifact_version`,
		dependent.ArtifactID, dependent.Version)
}

// Dependents lists the direct consumers of one exact source version.
func (d *Database) Dependents(ctx context.Context, source artifactregistry.VersionRef) ([]artifactlineage.Dependency, error) {
	if err := validateVersionRef(source); err != nil {
		return nil, err
	}
	return queryDependencies(ctx, d.sql, dependencySelect+` WHERE source_artifact_id = ?
		AND source_artifact_version = ? ORDER BY dependent_artifact_id, dependent_artifact_version`,
		source.ArtifactID, source.Version)
}

// Freshness returns the strongest observation recorded for an exact version.
func (d *Database) Freshness(ctx context.Context, reference artifactregistry.VersionRef) (artifactlineage.Freshness, error) {
	if err := validateVersionRef(reference); err != nil {
		return "", err
	}
	if err := requireArtifactVersion(ctx, d.sql, reference); err != nil {
		return "", err
	}
	var rank int
	err := d.sql.QueryRowContext(ctx, `SELECT COALESCE(MAX(CASE freshness
		WHEN 'invalidated' THEN 2 WHEN 'potentially_stale' THEN 1 END), 0)
		FROM artifact_invalidations WHERE descendant_artifact_id = ? AND descendant_artifact_version = ?`,
		reference.ArtifactID, reference.Version).Scan(&rank)
	if err != nil {
		return "", fmt.Errorf("read artifact freshness: %w", err)
	}
	return freshnessFromRank(rank)
}

// Invalidations lists every upstream revision that affected one exact version.
func (d *Database) Invalidations(ctx context.Context, reference artifactregistry.VersionRef) ([]artifactlineage.Invalidation, error) {
	if err := validateVersionRef(reference); err != nil {
		return nil, err
	}
	rows, err := d.sql.QueryContext(ctx, `SELECT trigger_artifact_id, trigger_artifact_version,
		descendant_artifact_id, descendant_artifact_version, freshness, created_at
		FROM artifact_invalidations WHERE descendant_artifact_id = ? AND descendant_artifact_version = ?
		ORDER BY trigger_artifact_id, trigger_artifact_version`, reference.ArtifactID, reference.Version)
	if err != nil {
		return nil, fmt.Errorf("list artifact invalidations: %w", err)
	}
	defer func() { _ = rows.Close() }()
	result := make([]artifactlineage.Invalidation, 0)
	for rows.Next() {
		var value artifactlineage.Invalidation
		var createdAt string
		if err := rows.Scan(&value.Trigger.ArtifactID, &value.Trigger.Version,
			&value.Descendant.ArtifactID, &value.Descendant.Version, &value.Freshness, &createdAt); err != nil {
			return nil, fmt.Errorf("scan artifact invalidation: %w", err)
		}
		value.CreatedAt, err = parseTime(createdAt)
		if err != nil {
			return nil, fmt.Errorf("parse artifact invalidation time: %w", err)
		}
		result = append(result, value)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate artifact invalidations: %w", err)
	}
	return result, nil
}

// AffectedBy lists the exact descendants affected by one upstream revision.
func (d *Database) AffectedBy(ctx context.Context, trigger artifactregistry.VersionRef) ([]artifactlineage.Invalidation, error) {
	if err := validateVersionRef(trigger); err != nil {
		return nil, err
	}
	rows, err := d.sql.QueryContext(ctx, `SELECT trigger_artifact_id, trigger_artifact_version,
		descendant_artifact_id, descendant_artifact_version, freshness, created_at
		FROM artifact_invalidations WHERE trigger_artifact_id = ? AND trigger_artifact_version = ?
		ORDER BY CASE freshness WHEN 'invalidated' THEN 0 ELSE 1 END,
			descendant_artifact_id, descendant_artifact_version`, trigger.ArtifactID, trigger.Version)
	if err != nil {
		return nil, fmt.Errorf("list revision impact: %w", err)
	}
	defer func() { _ = rows.Close() }()
	result := make([]artifactlineage.Invalidation, 0)
	for rows.Next() {
		var value artifactlineage.Invalidation
		var createdAt string
		if err := rows.Scan(&value.Trigger.ArtifactID, &value.Trigger.Version,
			&value.Descendant.ArtifactID, &value.Descendant.Version, &value.Freshness, &createdAt); err != nil {
			return nil, fmt.Errorf("scan revision impact: %w", err)
		}
		value.CreatedAt, err = parseTime(createdAt)
		if err != nil {
			return nil, fmt.Errorf("parse revision impact time: %w", err)
		}
		result = append(result, value)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate revision impact: %w", err)
	}
	return result, nil
}

func recordArtifactInvalidations(ctx context.Context, tx *sql.Tx, previous, current artifactregistry.VersionRef, createdAt string) error {
	rows, err := tx.QueryContext(ctx, `WITH RECURSIVE affected(artifact_id, version, rank) AS (
		SELECT dependent_artifact_id, dependent_artifact_version,
			CASE impact WHEN 'invalidated' THEN 2 ELSE 1 END
		FROM artifact_dependencies WHERE source_artifact_id = ? AND source_artifact_version = ?
		UNION
		SELECT edge.dependent_artifact_id, edge.dependent_artifact_version,
			CASE WHEN parent.rank = 2 OR edge.impact = 'invalidated' THEN 2 ELSE 1 END
		FROM artifact_dependencies edge JOIN affected parent
			ON edge.source_artifact_id = parent.artifact_id AND edge.source_artifact_version = parent.version
	)
	SELECT artifact_id, version, MAX(rank) FROM affected GROUP BY artifact_id, version
	ORDER BY artifact_id, version`, previous.ArtifactID, previous.Version)
	if err != nil {
		return fmt.Errorf("resolve artifact invalidation scope: %w", err)
	}
	defer func() { _ = rows.Close() }()
	type affectedVersion struct {
		reference artifactregistry.VersionRef
		rank      int
	}
	affected := make([]affectedVersion, 0)
	for rows.Next() {
		var item affectedVersion
		if err := rows.Scan(&item.reference.ArtifactID, &item.reference.Version, &item.rank); err != nil {
			return fmt.Errorf("scan artifact invalidation scope: %w", err)
		}
		affected = append(affected, item)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate artifact invalidation scope: %w", err)
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("close artifact invalidation scope: %w", err)
	}
	for _, item := range affected {
		freshness, err := freshnessFromRank(item.rank)
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO artifact_invalidations(
			trigger_artifact_id, trigger_artifact_version, descendant_artifact_id,
			descendant_artifact_version, freshness, created_at) VALUES (?, ?, ?, ?, ?, ?)`,
			current.ArtifactID, current.Version, item.reference.ArtifactID, item.reference.Version,
			freshness, createdAt)
		if err != nil {
			return fmt.Errorf("insert artifact invalidation: %w", err)
		}
	}
	return nil
}

func normalizeDependencyRequest(request artifactlineage.AddRequest) (artifactlineage.AddRequest, error) {
	if err := validateVersionRef(request.Source); err != nil {
		return request, fmt.Errorf("source: %w", err)
	}
	if err := validateVersionRef(request.Dependent); err != nil {
		return request, fmt.Errorf("dependent: %w", err)
	}
	if request.Source == request.Dependent {
		return request, artifactlineage.ErrDependencyCycle
	}
	if request.Impact != artifactlineage.ImpactPotentiallyStale && request.Impact != artifactlineage.ImpactInvalidated {
		return request, fmt.Errorf("invalid artifact dependency impact %q", request.Impact)
	}
	if request.CreatedAt.IsZero() {
		return request, errors.New("artifact dependency creation time is required")
	}
	request.CreatedAt = request.CreatedAt.UTC()
	return request, nil
}

func validateVersionRef(reference artifactregistry.VersionRef) error {
	if !strings.HasPrefix(reference.ArtifactID, "artifact_") || reference.Version == 0 {
		return errors.New("artifact reference must identify an exact version")
	}
	return nil
}

type sqlQuerier interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

type sqlQueryRower interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func requireArtifactVersion(ctx context.Context, query sqlQueryRower, reference artifactregistry.VersionRef) error {
	var exists int
	err := query.QueryRowContext(ctx, `SELECT 1 FROM artifact_versions WHERE artifact_id = ? AND version = ?`,
		reference.ArtifactID, reference.Version).Scan(&exists)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("%w: artifact %s version %d", artifactregistry.ErrNotFound, reference.ArtifactID, reference.Version)
	}
	if err != nil {
		return fmt.Errorf("read artifact version: %w", err)
	}
	return nil
}

func queryDependencies(ctx context.Context, query sqlQuerier, statement string, args ...any) ([]artifactlineage.Dependency, error) {
	rows, err := query.QueryContext(ctx, statement, args...)
	if err != nil {
		return nil, fmt.Errorf("list artifact dependencies: %w", err)
	}
	defer func() { _ = rows.Close() }()
	result := make([]artifactlineage.Dependency, 0)
	for rows.Next() {
		value, err := scanDependency(rows)
		if err != nil {
			return nil, fmt.Errorf("scan artifact dependency: %w", err)
		}
		result = append(result, value)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate artifact dependencies: %w", err)
	}
	return result, nil
}

func scanDependency(row rowScanner) (artifactlineage.Dependency, error) {
	var value artifactlineage.Dependency
	var createdAt string
	if err := row.Scan(&value.Source.ArtifactID, &value.Source.Version, &value.Dependent.ArtifactID,
		&value.Dependent.Version, &value.Impact, &createdAt); err != nil {
		return artifactlineage.Dependency{}, err
	}
	parsed, err := parseTime(createdAt)
	if err != nil {
		return artifactlineage.Dependency{}, err
	}
	value.CreatedAt = parsed
	return value, nil
}

func freshnessFromRank(rank int) (artifactlineage.Freshness, error) {
	switch rank {
	case 0:
		return artifactlineage.FreshnessCurrent, nil
	case 1:
		return artifactlineage.FreshnessPotentiallyStale, nil
	case 2:
		return artifactlineage.FreshnessInvalidated, nil
	default:
		return "", fmt.Errorf("invalid artifact freshness rank %d", rank)
	}
}
