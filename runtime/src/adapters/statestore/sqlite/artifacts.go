package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"regexp"
	"sort"
	"strings"

	"github.com/fdsprod/darkstar/runtime/src/ports/artifactregistry"
	"github.com/fdsprod/darkstar/runtime/src/ports/artifactstore"
)

var (
	_                     artifactregistry.Registry = (*Database)(nil)
	artifactDigestPattern                           = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

const artifactVersionSelect = `SELECT artifact_id, version, idempotency_key, source_kind, source_name,
	blob_digest, size, declared_media_type, detected_media_type, locator, sensitivity, creator, status,
	producer_name, producer_version, roles_json, tags_json, metadata_json, origin_kind, operation_id,
	run_id, node_id, attempt_id, source_artifact_id, source_artifact_version, created_at
	FROM artifact_versions`

// Register allocates the next immutable version for an artifact. The
// idempotency key is scoped to the stable artifact identity.
func (d *Database) Register(ctx context.Context, request artifactregistry.RegisterRequest) (artifactregistry.ArtifactVersion, bool, error) {
	normalized, err := normalizeArtifactRequest(request)
	if err != nil {
		return artifactregistry.ArtifactVersion{}, false, err
	}
	rolesJSON, _ := json.Marshal(normalized.Roles)
	tagsJSON, _ := json.Marshal(normalized.Tags)
	metadataJSON, _ := json.Marshal(normalized.Metadata)
	origin, operationID, runID, nodeID, attemptID, sourceArtifactID, sourceVersion := provenanceColumns(normalized.Provenance)

	tx, err := d.sql.BeginTx(ctx, nil)
	if err != nil {
		return artifactregistry.ArtifactVersion{}, false, fmt.Errorf("begin artifact registration: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	existing, _, err := scanArtifactVersion(tx.QueryRowContext(ctx,
		artifactVersionSelect+` WHERE artifact_id = ? AND idempotency_key = ?`, normalized.ArtifactID, normalized.IdempotencyKey))
	if err == nil {
		if !sameRegistration(existing, normalized) {
			return artifactregistry.ArtifactVersion{}, false, fmt.Errorf("%w: artifact %s key %s", artifactregistry.ErrVersionConflict, normalized.ArtifactID, normalized.IdempotencyKey)
		}
		if err := tx.Commit(); err != nil {
			return artifactregistry.ArtifactVersion{}, false, fmt.Errorf("commit repeated artifact registration: %w", err)
		}
		return existing, false, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return artifactregistry.ArtifactVersion{}, false, fmt.Errorf("read artifact registration: %w", err)
	}

	_, err = tx.ExecContext(ctx, `INSERT INTO artifact_versions(
		artifact_id, version, idempotency_key, source_kind, source_name, blob_digest, size,
		declared_media_type, detected_media_type, locator, sensitivity, creator, status,
		producer_name, producer_version, roles_json, tags_json, metadata_json, origin_kind,
		operation_id, run_id, node_id, attempt_id, source_artifact_id, source_artifact_version, created_at)
		SELECT ?, COALESCE(MAX(version), 0) + 1, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?
		FROM artifact_versions WHERE artifact_id = ?`,
		normalized.ArtifactID, normalized.IdempotencyKey, normalized.SourceKind, normalized.SourceName,
		normalized.BlobDigest, normalized.Size, normalized.DeclaredMediaType, normalized.DetectedMediaType,
		string(normalized.Locator), normalized.Sensitivity, normalized.Creator, normalized.Status,
		normalized.Producer.Name, normalized.Producer.Version, string(rolesJSON), string(tagsJSON), string(metadataJSON),
		origin, operationID, runID, nodeID, attemptID, sourceArtifactID, sourceVersion,
		formatTime(normalized.CreatedAt), normalized.ArtifactID)
	if err != nil {
		return artifactregistry.ArtifactVersion{}, false, fmt.Errorf("insert artifact version: %w", err)
	}
	created, _, err := scanArtifactVersion(tx.QueryRowContext(ctx,
		artifactVersionSelect+` WHERE artifact_id = ? AND idempotency_key = ?`, normalized.ArtifactID, normalized.IdempotencyKey))
	if err != nil {
		return artifactregistry.ArtifactVersion{}, false, fmt.Errorf("read created artifact version: %w", err)
	}
	if created.Version > 1 {
		previous := artifactregistry.VersionRef{ArtifactID: created.ArtifactID, Version: created.Version - 1}
		current := artifactregistry.VersionRef{ArtifactID: created.ArtifactID, Version: created.Version}
		if err := recordArtifactInvalidations(ctx, tx, previous, current, formatTime(created.CreatedAt)); err != nil {
			return artifactregistry.ArtifactVersion{}, false, err
		}
	}
	if err := tx.Commit(); err != nil {
		return artifactregistry.ArtifactVersion{}, false, fmt.Errorf("commit artifact registration: %w", err)
	}
	return created, true, nil
}

// ArtifactVersion returns one exact immutable artifact version.
func (d *Database) ArtifactVersion(ctx context.Context, reference artifactregistry.VersionRef) (artifactregistry.ArtifactVersion, error) {
	value, _, err := scanArtifactVersion(d.sql.QueryRowContext(ctx,
		artifactVersionSelect+` WHERE artifact_id = ? AND version = ?`, reference.ArtifactID, reference.Version))
	if errors.Is(err, sql.ErrNoRows) {
		return artifactregistry.ArtifactVersion{}, fmt.Errorf("%w: artifact %s version %d", artifactregistry.ErrNotFound, reference.ArtifactID, reference.Version)
	}
	if err != nil {
		return artifactregistry.ArtifactVersion{}, fmt.Errorf("read artifact version: %w", err)
	}
	return value, nil
}

// LatestVersion returns the greatest registered version for an artifact.
func (d *Database) LatestVersion(ctx context.Context, artifactID string) (artifactregistry.ArtifactVersion, error) {
	value, _, err := scanArtifactVersion(d.sql.QueryRowContext(ctx,
		artifactVersionSelect+` WHERE artifact_id = ? ORDER BY version DESC LIMIT 1`, artifactID))
	if errors.Is(err, sql.ErrNoRows) {
		return artifactregistry.ArtifactVersion{}, fmt.Errorf("%w: artifact %s", artifactregistry.ErrNotFound, artifactID)
	}
	if err != nil {
		return artifactregistry.ArtifactVersion{}, fmt.Errorf("read latest artifact version: %w", err)
	}
	return value, nil
}

// Versions returns every version in deterministic ascending order.
func (d *Database) Versions(ctx context.Context, artifactID string) ([]artifactregistry.ArtifactVersion, error) {
	rows, err := d.sql.QueryContext(ctx, artifactVersionSelect+` WHERE artifact_id = ? ORDER BY version`, artifactID)
	if err != nil {
		return nil, fmt.Errorf("list artifact versions: %w", err)
	}
	defer func() { _ = rows.Close() }()
	values := make([]artifactregistry.ArtifactVersion, 0)
	for rows.Next() {
		value, _, err := scanArtifactVersion(rows)
		if err != nil {
			return nil, fmt.Errorf("scan artifact version: %w", err)
		}
		values = append(values, value)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate artifact versions: %w", err)
	}
	return values, nil
}

func normalizeArtifactRequest(request artifactregistry.RegisterRequest) (artifactregistry.RegisterRequest, error) {
	if !strings.HasPrefix(request.ArtifactID, "artifact_") || strings.TrimSpace(request.IdempotencyKey) == "" {
		return request, errors.New("artifact ID and idempotency key are required")
	}
	if !artifactDigestPattern.MatchString(request.BlobDigest) {
		return request, errors.New("artifact digest must be 64 lowercase hexadecimal characters")
	}
	if request.Size < 0 || strings.TrimSpace(request.DeclaredMediaType) == "" || strings.TrimSpace(request.DetectedMediaType) == "" || strings.TrimSpace(string(request.Locator)) == "" {
		return request, errors.New("artifact size, media types, and locator are invalid")
	}
	if !validSourceKind(request.SourceKind) || !validSensitivity(request.Sensitivity) || !validArtifactStatus(request.Status) {
		return request, errors.New("artifact source kind, sensitivity, or status is invalid")
	}
	if strings.TrimSpace(request.Creator) == "" || strings.TrimSpace(request.Producer.Name) == "" || strings.TrimSpace(request.Producer.Version) == "" {
		return request, errors.New("artifact creator and producer identity are required")
	}
	if request.CreatedAt.IsZero() {
		return request, errors.New("artifact creation time is required")
	}
	if err := validateProvenance(request.Provenance); err != nil {
		return request, err
	}
	switch provenance := request.Provenance.(type) {
	case *artifactregistry.OperationProvenance:
		request.Provenance = *provenance
	case *artifactregistry.AttemptProvenance:
		request.Provenance = *provenance
	}
	roles, err := canonicalLabels("role", request.Roles)
	if err != nil {
		return request, err
	}
	tags, err := canonicalLabels("tag", request.Tags)
	if err != nil {
		return request, err
	}
	request.Roles = roles
	request.Tags = tags
	request.Metadata = cloneMetadata(request.Metadata)
	request.CreatedAt = request.CreatedAt.UTC()
	return request, nil
}

func validSourceKind(value artifactregistry.SourceKind) bool {
	switch value {
	case artifactregistry.SourceFile, artifactregistry.SourcePaste, artifactregistry.SourceStdin, artifactregistry.SourceGenerated, artifactregistry.SourceExternal:
		return true
	default:
		return false
	}
}

func validSensitivity(value artifactregistry.Sensitivity) bool {
	switch value {
	case artifactregistry.SensitivityUnknown, artifactregistry.SensitivityPublic, artifactregistry.SensitivityInternal, artifactregistry.SensitivitySensitive, artifactregistry.SensitivitySecret:
		return true
	default:
		return false
	}
}

func validArtifactStatus(value artifactregistry.Status) bool {
	switch value {
	case artifactregistry.StatusStored, artifactregistry.StatusStoredUninspectable, artifactregistry.StatusQuarantined:
		return true
	default:
		return false
	}
}

func validateProvenance(value artifactregistry.Provenance) error {
	var operationID string
	var source *artifactregistry.VersionRef
	switch origin := value.(type) {
	case artifactregistry.OperationProvenance:
		operationID, source = origin.OperationID, origin.Source
	case *artifactregistry.OperationProvenance:
		if origin == nil {
			return errors.New("artifact provenance must not be nil")
		}
		operationID, source = origin.OperationID, origin.Source
	case artifactregistry.AttemptProvenance:
		if strings.TrimSpace(origin.RunID) == "" || strings.TrimSpace(origin.NodeID) == "" || strings.TrimSpace(origin.AttemptID) == "" {
			return errors.New("attempt provenance requires run, node, and attempt identity")
		}
		operationID, source = origin.OperationID, origin.Source
	case *artifactregistry.AttemptProvenance:
		if origin == nil || strings.TrimSpace(origin.RunID) == "" || strings.TrimSpace(origin.NodeID) == "" || strings.TrimSpace(origin.AttemptID) == "" {
			return errors.New("attempt provenance requires run, node, and attempt identity")
		}
		operationID, source = origin.OperationID, origin.Source
	default:
		return errors.New("artifact provenance must be an operation or attempt origin")
	}
	if strings.TrimSpace(operationID) == "" {
		return errors.New("artifact provenance requires an operation ID")
	}
	if source != nil && (!strings.HasPrefix(source.ArtifactID, "artifact_") || source.Version == 0) {
		return errors.New("source artifact provenance must identify an exact version")
	}
	return nil
}

func canonicalLabels(kind string, values []string) ([]string, error) {
	result := append([]string(nil), values...)
	for _, value := range result {
		if strings.TrimSpace(value) == "" || value != strings.TrimSpace(value) {
			return nil, fmt.Errorf("artifact %s values must be non-empty and trimmed", kind)
		}
	}
	sort.Strings(result)
	for index := 1; index < len(result); index++ {
		if result[index] == result[index-1] {
			return nil, fmt.Errorf("artifact %s values must be unique", kind)
		}
	}
	if result == nil {
		result = []string{}
	}
	return result, nil
}

func cloneMetadata(value map[string]string) map[string]string {
	result := make(map[string]string, len(value))
	for key, entry := range value {
		result[key] = entry
	}
	return result
}

func provenanceColumns(value artifactregistry.Provenance) (string, string, any, any, any, any, any) {
	var origin, operationID string
	var runID, nodeID, attemptID, sourceArtifactID, sourceVersion any
	switch provenance := value.(type) {
	case artifactregistry.OperationProvenance:
		origin, operationID = "operation", provenance.OperationID
		if provenance.Source != nil {
			sourceArtifactID, sourceVersion = provenance.Source.ArtifactID, provenance.Source.Version
		}
	case *artifactregistry.OperationProvenance:
		origin, operationID = "operation", provenance.OperationID
		if provenance.Source != nil {
			sourceArtifactID, sourceVersion = provenance.Source.ArtifactID, provenance.Source.Version
		}
	case artifactregistry.AttemptProvenance:
		origin, operationID = "attempt", provenance.OperationID
		runID, nodeID, attemptID = provenance.RunID, provenance.NodeID, provenance.AttemptID
		if provenance.Source != nil {
			sourceArtifactID, sourceVersion = provenance.Source.ArtifactID, provenance.Source.Version
		}
	case *artifactregistry.AttemptProvenance:
		origin, operationID = "attempt", provenance.OperationID
		runID, nodeID, attemptID = provenance.RunID, provenance.NodeID, provenance.AttemptID
		if provenance.Source != nil {
			sourceArtifactID, sourceVersion = provenance.Source.ArtifactID, provenance.Source.Version
		}
	}
	return origin, operationID, runID, nodeID, attemptID, sourceArtifactID, sourceVersion
}

func scanArtifactVersion(row rowScanner) (artifactregistry.ArtifactVersion, string, error) {
	var value artifactregistry.ArtifactVersion
	var idempotencyKey, rolesJSON, tagsJSON, metadataJSON, origin, operationID, createdAt string
	var runID, nodeID, attemptID, sourceArtifactID sql.NullString
	var sourceVersion sql.NullInt64
	if err := row.Scan(&value.ArtifactID, &value.Version, &idempotencyKey, &value.SourceKind, &value.SourceName,
		&value.BlobDigest, &value.Size, &value.DeclaredMediaType, &value.DetectedMediaType, &value.Locator,
		&value.Sensitivity, &value.Creator, &value.Status, &value.Producer.Name, &value.Producer.Version,
		&rolesJSON, &tagsJSON, &metadataJSON, &origin, &operationID, &runID, &nodeID, &attemptID,
		&sourceArtifactID, &sourceVersion, &createdAt); err != nil {
		return artifactregistry.ArtifactVersion{}, "", err
	}
	if err := json.Unmarshal([]byte(rolesJSON), &value.Roles); err != nil {
		return artifactregistry.ArtifactVersion{}, "", err
	}
	if err := json.Unmarshal([]byte(tagsJSON), &value.Tags); err != nil {
		return artifactregistry.ArtifactVersion{}, "", err
	}
	if err := json.Unmarshal([]byte(metadataJSON), &value.Metadata); err != nil {
		return artifactregistry.ArtifactVersion{}, "", err
	}
	var source *artifactregistry.VersionRef
	if sourceArtifactID.Valid {
		source = &artifactregistry.VersionRef{ArtifactID: sourceArtifactID.String, Version: uint64(sourceVersion.Int64)}
	}
	switch origin {
	case "operation":
		value.Provenance = artifactregistry.OperationProvenance{OperationID: operationID, Source: source}
	case "attempt":
		value.Provenance = artifactregistry.AttemptProvenance{
			RunID: runID.String, NodeID: nodeID.String, AttemptID: attemptID.String, OperationID: operationID, Source: source,
		}
	default:
		return artifactregistry.ArtifactVersion{}, "", fmt.Errorf("unknown artifact origin %q", origin)
	}
	value.Trust = "untrusted"
	parsed, err := parseTime(createdAt)
	if err != nil {
		return artifactregistry.ArtifactVersion{}, "", err
	}
	value.CreatedAt = parsed
	return value, idempotencyKey, nil
}

func sameRegistration(value artifactregistry.ArtifactVersion, request artifactregistry.RegisterRequest) bool {
	want := artifactregistry.ArtifactVersion{
		ArtifactID: request.ArtifactID, Version: value.Version, SourceKind: request.SourceKind, SourceName: request.SourceName,
		BlobDigest: request.BlobDigest, Size: request.Size, DeclaredMediaType: request.DeclaredMediaType,
		DetectedMediaType: request.DetectedMediaType, Locator: artifactstore.Locator(request.Locator),
		Sensitivity: request.Sensitivity, Trust: "untrusted", Creator: request.Creator, Status: request.Status,
		Producer: request.Producer, Roles: request.Roles, Tags: request.Tags, Metadata: request.Metadata,
		Provenance: request.Provenance, CreatedAt: request.CreatedAt,
	}
	return reflect.DeepEqual(value, want)
}
