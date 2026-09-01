package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"

	"github.com/fdsprod/darkstar/runtime/src/ports/artifactregistry"
	"github.com/fdsprod/darkstar/runtime/src/ports/contentprocessor"
	"github.com/fdsprod/darkstar/runtime/src/ports/representationregistry"
)

var _ representationregistry.Registry = (*Database)(nil)

const representationSelect = `SELECT representation_id, idempotency_key, artifact_id, artifact_version,
	representation_kind, processor_name, processor_version, media_type, locator, digest, size,
	token_estimate, truncated, disclosure, diagnostics_json, metadata_json, created_at
	FROM artifact_representations`

func (d *Database) RegisterRepresentation(ctx context.Context, request representationregistry.RegisterRequest) (representationregistry.Representation, bool, error) {
	normalized, err := normalizeRepresentationRequest(request)
	if err != nil {
		return representationregistry.Representation{}, false, err
	}
	diagnosticsJSON, _ := json.Marshal(normalized.Diagnostics)
	metadataJSON, _ := json.Marshal(normalized.Metadata)
	tx, err := d.sql.BeginTx(ctx, nil)
	if err != nil {
		return representationregistry.Representation{}, false, fmt.Errorf("begin representation registration: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	existing, key, err := scanRepresentation(tx.QueryRowContext(ctx,
		representationSelect+` WHERE artifact_id = ? AND artifact_version = ? AND idempotency_key = ? AND representation_kind = ?`,
		normalized.Artifact.ArtifactID, normalized.Artifact.Version, normalized.IdempotencyKey, normalized.Kind))
	if err == nil {
		if key != normalized.IdempotencyKey || !sameRepresentation(existing, normalized) {
			return representationregistry.Representation{}, false, representationregistry.ErrConflict
		}
		if err := tx.Commit(); err != nil {
			return representationregistry.Representation{}, false, fmt.Errorf("commit repeated representation: %w", err)
		}
		return existing, false, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return representationregistry.Representation{}, false, fmt.Errorf("read repeated representation: %w", err)
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO artifact_representations(
		representation_id, idempotency_key, artifact_id, artifact_version, representation_kind,
		processor_name, processor_version, media_type, locator, digest, size, token_estimate,
		truncated, disclosure, diagnostics_json, metadata_json, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		normalized.RepresentationID, normalized.IdempotencyKey, normalized.Artifact.ArtifactID,
		normalized.Artifact.Version, normalized.Kind, normalized.Processor.Name, normalized.Processor.Version,
		normalized.MediaType, normalized.Locator, normalized.Digest, normalized.Size, normalized.TokenEstimate,
		normalized.Truncated, normalized.Disclosure, string(diagnosticsJSON), string(metadataJSON), formatTime(normalized.CreatedAt))
	if err != nil {
		return representationregistry.Representation{}, false, fmt.Errorf("insert representation: %w", err)
	}
	created, _, err := scanRepresentation(tx.QueryRowContext(ctx, representationSelect+` WHERE representation_id = ?`, normalized.RepresentationID))
	if err != nil {
		return representationregistry.Representation{}, false, fmt.Errorf("read created representation: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return representationregistry.Representation{}, false, fmt.Errorf("commit representation: %w", err)
	}
	return created, true, nil
}

func (d *Database) Representation(ctx context.Context, id string) (representationregistry.Representation, error) {
	value, _, err := scanRepresentation(d.sql.QueryRowContext(ctx, representationSelect+` WHERE representation_id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return representationregistry.Representation{}, representationregistry.ErrNotFound
	}
	if err != nil {
		return representationregistry.Representation{}, fmt.Errorf("read representation: %w", err)
	}
	return value, nil
}

func (d *Database) ForArtifact(ctx context.Context, artifact artifactregistry.VersionRef) ([]representationregistry.Representation, error) {
	if err := validateVersionRef(artifact); err != nil {
		return nil, err
	}
	rows, err := d.sql.QueryContext(ctx, representationSelect+` WHERE artifact_id = ? AND artifact_version = ? ORDER BY representation_kind, representation_id`, artifact.ArtifactID, artifact.Version)
	if err != nil {
		return nil, fmt.Errorf("list artifact representations: %w", err)
	}
	defer func() { _ = rows.Close() }()
	values := make([]representationregistry.Representation, 0)
	for rows.Next() {
		value, _, err := scanRepresentation(rows)
		if err != nil {
			return nil, fmt.Errorf("scan artifact representation: %w", err)
		}
		values = append(values, value)
	}
	return values, rows.Err()
}

func normalizeRepresentationRequest(request representationregistry.RegisterRequest) (representationregistry.RegisterRequest, error) {
	if !strings.HasPrefix(request.RepresentationID, "representation_") || strings.TrimSpace(request.IdempotencyKey) == "" {
		return request, errors.New("representation ID and idempotency key are required")
	}
	if err := validateVersionRef(request.Artifact); err != nil {
		return request, err
	}
	if !artifactDigestPattern.MatchString(request.Digest) || request.Size < 0 || request.TokenEstimate < 0 || strings.TrimSpace(string(request.Locator)) == "" || strings.TrimSpace(request.MediaType) == "" {
		return request, errors.New("representation content metadata is invalid")
	}
	if strings.TrimSpace(request.Processor.Name) == "" || strings.TrimSpace(request.Processor.Version) == "" || request.CreatedAt.IsZero() {
		return request, errors.New("representation processor and creation time are required")
	}
	switch request.Kind {
	case contentprocessor.RepresentationText, contentprocessor.RepresentationStructured, contentprocessor.RepresentationTable,
		contentprocessor.RepresentationImage, contentprocessor.RepresentationPreview, contentprocessor.RepresentationDescriptor:
	default:
		return request, fmt.Errorf("invalid representation kind %q", request.Kind)
	}
	switch request.Disclosure {
	case representationregistry.DisclosureRaw, representationregistry.DisclosureRedacted, representationregistry.DisclosureWithheld:
	default:
		return request, fmt.Errorf("invalid representation disclosure %q", request.Disclosure)
	}
	request.Diagnostics = append([]string(nil), request.Diagnostics...)
	sort.Strings(request.Diagnostics)
	if request.Diagnostics == nil {
		request.Diagnostics = []string{}
	}
	request.Processor.MediaTypes = nil
	request.Metadata = cloneMetadata(request.Metadata)
	request.CreatedAt = request.CreatedAt.UTC()
	return request, nil
}

func scanRepresentation(row rowScanner) (representationregistry.Representation, string, error) {
	var value representationregistry.Representation
	var key, diagnosticsJSON, metadataJSON, createdAt string
	if err := row.Scan(&value.RepresentationID, &key, &value.Artifact.ArtifactID, &value.Artifact.Version,
		&value.Kind, &value.Processor.Name, &value.Processor.Version, &value.MediaType, &value.Locator,
		&value.Digest, &value.Size, &value.TokenEstimate, &value.Truncated, &value.Disclosure,
		&diagnosticsJSON, &metadataJSON, &createdAt); err != nil {
		return representationregistry.Representation{}, "", err
	}
	if err := json.Unmarshal([]byte(diagnosticsJSON), &value.Diagnostics); err != nil {
		return representationregistry.Representation{}, "", err
	}
	if err := json.Unmarshal([]byte(metadataJSON), &value.Metadata); err != nil {
		return representationregistry.Representation{}, "", err
	}
	parsed, err := parseTime(createdAt)
	if err != nil {
		return representationregistry.Representation{}, "", err
	}
	value.CreatedAt = parsed
	return value, key, nil
}

func sameRepresentation(value representationregistry.Representation, request representationregistry.RegisterRequest) bool {
	want := representationregistry.Representation{
		RepresentationID: request.RepresentationID, Artifact: request.Artifact, Kind: request.Kind,
		Processor: request.Processor, MediaType: request.MediaType, Locator: request.Locator, Digest: request.Digest,
		Size: request.Size, TokenEstimate: request.TokenEstimate, Truncated: request.Truncated,
		Disclosure: request.Disclosure, Diagnostics: request.Diagnostics, Metadata: request.Metadata, CreatedAt: request.CreatedAt,
	}
	return reflect.DeepEqual(value, want)
}
