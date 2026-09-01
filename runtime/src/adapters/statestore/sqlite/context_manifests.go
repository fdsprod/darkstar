package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"

	manifestport "github.com/fdsprod/darkstar/runtime/src/ports/contextmanifest"
)

var _ manifestport.Store = (*Database)(nil)

const manifestSelect = `SELECT manifest_id, idempotency_key, run_id, node_id, attempt_id,
	policy_version, budget, reserved_tokens, entries_json, omissions_json, instructions_json,
	schemas_json, permissions_json, workspace_json, capabilities_json, digest, frozen_at
	FROM context_manifests`

func (d *Database) StoreManifest(ctx context.Context, manifest manifestport.Manifest, idempotencyKey string) (manifestport.Manifest, bool, error) {
	normalized, err := normalizeManifest(manifest)
	if err != nil {
		return manifestport.Manifest{}, false, err
	}
	if strings.TrimSpace(idempotencyKey) == "" {
		return manifestport.Manifest{}, false, errors.New("context manifest idempotency key is required")
	}
	encoded, err := encodeManifestCollections(normalized)
	if err != nil {
		return manifestport.Manifest{}, false, err
	}
	tx, err := d.sql.BeginTx(ctx, nil)
	if err != nil {
		return manifestport.Manifest{}, false, fmt.Errorf("begin context manifest: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	existing, key, err := scanManifest(tx.QueryRowContext(ctx, manifestSelect+` WHERE attempt_id = ?`, normalized.AttemptID))
	if err == nil {
		if key != idempotencyKey || !reflect.DeepEqual(existing, normalized) {
			return manifestport.Manifest{}, false, manifestport.ErrFrozen
		}
		if err := tx.Commit(); err != nil {
			return manifestport.Manifest{}, false, fmt.Errorf("commit repeated context manifest: %w", err)
		}
		return existing, false, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return manifestport.Manifest{}, false, fmt.Errorf("read context manifest: %w", err)
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO context_manifests(
		manifest_id, idempotency_key, run_id, node_id, attempt_id, policy_version, budget,
		reserved_tokens, entries_json, omissions_json, instructions_json, schemas_json,
		permissions_json, workspace_json, capabilities_json, digest, frozen_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		normalized.ManifestID, idempotencyKey, normalized.RunID, normalized.NodeID, normalized.AttemptID,
		normalized.PolicyVersion, normalized.Budget, normalized.Reserved, encoded.entries, encoded.omissions,
		encoded.instructions, encoded.schemas, encoded.permissions, encoded.workspace, encoded.capabilities,
		normalized.Digest, formatTime(normalized.FrozenAt))
	if err != nil {
		return manifestport.Manifest{}, false, fmt.Errorf("insert context manifest: %w", err)
	}
	created, _, err := scanManifest(tx.QueryRowContext(ctx, manifestSelect+` WHERE manifest_id = ?`, normalized.ManifestID))
	if err != nil {
		return manifestport.Manifest{}, false, fmt.Errorf("read created context manifest: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return manifestport.Manifest{}, false, fmt.Errorf("commit context manifest: %w", err)
	}
	return created, true, nil
}

func (d *Database) Manifest(ctx context.Context, id string) (manifestport.Manifest, error) {
	value, _, err := scanManifest(d.sql.QueryRowContext(ctx, manifestSelect+` WHERE manifest_id = ?`, id))
	return classifyManifest(value, err)
}

func (d *Database) ManifestForAttempt(ctx context.Context, attemptID string) (manifestport.Manifest, error) {
	value, _, err := scanManifest(d.sql.QueryRowContext(ctx, manifestSelect+` WHERE attempt_id = ?`, attemptID))
	return classifyManifest(value, err)
}

func normalizeManifest(manifest manifestport.Manifest) (manifestport.Manifest, error) {
	if !strings.HasPrefix(manifest.ManifestID, "manifest_") || strings.TrimSpace(manifest.RunID) == "" || strings.TrimSpace(manifest.NodeID) == "" || strings.TrimSpace(manifest.AttemptID) == "" || strings.TrimSpace(manifest.PolicyVersion) == "" {
		return manifest, errors.New("context manifest identity and policy are required")
	}
	if manifest.Budget < 0 || manifest.Reserved < 0 || manifest.Reserved > manifest.Budget || !artifactDigestPattern.MatchString(manifest.Digest) || manifest.FrozenAt.IsZero() {
		return manifest, errors.New("context manifest budget, digest, or frozen time is invalid")
	}
	if strings.TrimSpace(manifest.Workspace.ID) == "" || !artifactDigestPattern.MatchString(manifest.Workspace.Digest) {
		return manifest, errors.New("context manifest workspace is invalid")
	}
	switch manifest.Workspace.Access {
	case manifestport.WorkspaceReadOnly, manifestport.WorkspaceWrite:
	default:
		return manifest, errors.New("context manifest workspace access is invalid")
	}
	if manifest.Reserved+manifest.UsedTokens() > manifest.Budget {
		return manifest, errors.New("context manifest entries exceed budget")
	}
	seenRepresentations := make(map[string]struct{}, len(manifest.Entries)+len(manifest.Omissions))
	for _, entry := range manifest.Entries {
		if !strings.HasPrefix(entry.ArtifactID, "artifact_") || entry.ArtifactVersion == 0 || strings.TrimSpace(entry.RepresentationID) == "" || !artifactDigestPattern.MatchString(entry.Digest) || entry.TokenEstimate < 0 {
			return manifest, errors.New("context manifest entry is invalid")
		}
		if _, duplicate := seenRepresentations[entry.RepresentationID]; duplicate {
			return manifest, errors.New("context manifest repeats a representation")
		}
		seenRepresentations[entry.RepresentationID] = struct{}{}
	}
	for _, omission := range manifest.Omissions {
		if strings.TrimSpace(omission.RepresentationID) == "" || !validOmissionReason(omission.Reason) {
			return manifest, errors.New("context manifest omission is invalid")
		}
		if _, duplicate := seenRepresentations[omission.RepresentationID]; duplicate {
			return manifest, errors.New("context manifest repeats a representation")
		}
		seenRepresentations[omission.RepresentationID] = struct{}{}
	}
	for _, group := range [][]manifestport.DigestRef{manifest.Instructions, manifest.Schemas, manifest.Capabilities} {
		seen := make(map[string]struct{}, len(group))
		for _, reference := range group {
			if strings.TrimSpace(reference.ID) == "" || !artifactDigestPattern.MatchString(reference.Digest) {
				return manifest, errors.New("context manifest digest reference is invalid")
			}
			if _, duplicate := seen[reference.ID]; duplicate {
				return manifest, errors.New("context manifest repeats a digest reference")
			}
			seen[reference.ID] = struct{}{}
		}
	}
	seenPermissions := make(map[string]struct{}, len(manifest.Permissions))
	for _, permission := range manifest.Permissions {
		if strings.TrimSpace(permission) == "" || permission != strings.TrimSpace(permission) {
			return manifest, errors.New("context manifest permission is invalid")
		}
		if _, duplicate := seenPermissions[permission]; duplicate {
			return manifest, errors.New("context manifest repeats a permission")
		}
		seenPermissions[permission] = struct{}{}
	}
	manifest.Entries = append([]manifestport.Entry(nil), manifest.Entries...)
	manifest.Omissions = append([]manifestport.Omission(nil), manifest.Omissions...)
	manifest.Instructions = append([]manifestport.DigestRef(nil), manifest.Instructions...)
	manifest.Schemas = append([]manifestport.DigestRef(nil), manifest.Schemas...)
	manifest.Permissions = append([]string(nil), manifest.Permissions...)
	manifest.Capabilities = append([]manifestport.DigestRef(nil), manifest.Capabilities...)
	if manifest.Entries == nil {
		manifest.Entries = []manifestport.Entry{}
	}
	if manifest.Omissions == nil {
		manifest.Omissions = []manifestport.Omission{}
	}
	if manifest.Instructions == nil {
		manifest.Instructions = []manifestport.DigestRef{}
	}
	if manifest.Schemas == nil {
		manifest.Schemas = []manifestport.DigestRef{}
	}
	if manifest.Permissions == nil {
		manifest.Permissions = []string{}
	}
	if manifest.Capabilities == nil {
		manifest.Capabilities = []manifestport.DigestRef{}
	}
	manifest.FrozenAt = manifest.FrozenAt.UTC()
	return manifest, nil
}

type manifestCollections struct{ entries, omissions, instructions, schemas, permissions, workspace, capabilities string }

func encodeManifestCollections(manifest manifestport.Manifest) (manifestCollections, error) {
	values := []any{manifest.Entries, manifest.Omissions, manifest.Instructions, manifest.Schemas, manifest.Permissions, manifest.Workspace, manifest.Capabilities}
	encoded := make([]string, len(values))
	for index, value := range values {
		content, err := json.Marshal(value)
		if err != nil {
			return manifestCollections{}, fmt.Errorf("encode context manifest: %w", err)
		}
		encoded[index] = string(content)
	}
	return manifestCollections{encoded[0], encoded[1], encoded[2], encoded[3], encoded[4], encoded[5], encoded[6]}, nil
}

func scanManifest(row rowScanner) (manifestport.Manifest, string, error) {
	var value manifestport.Manifest
	var key, entries, omissions, instructions, schemas, permissions, workspace, capabilities, frozenAt string
	if err := row.Scan(&value.ManifestID, &key, &value.RunID, &value.NodeID, &value.AttemptID,
		&value.PolicyVersion, &value.Budget, &value.Reserved, &entries, &omissions, &instructions,
		&schemas, &permissions, &workspace, &capabilities, &value.Digest, &frozenAt); err != nil {
		return manifestport.Manifest{}, "", err
	}
	for _, target := range []struct {
		content string
		value   any
	}{
		{entries, &value.Entries}, {omissions, &value.Omissions}, {instructions, &value.Instructions},
		{schemas, &value.Schemas}, {permissions, &value.Permissions}, {workspace, &value.Workspace}, {capabilities, &value.Capabilities},
	} {
		if err := json.Unmarshal([]byte(target.content), target.value); err != nil {
			return manifestport.Manifest{}, "", err
		}
	}
	parsed, err := parseTime(frozenAt)
	if err != nil {
		return manifestport.Manifest{}, "", err
	}
	value.FrozenAt = parsed
	return value, key, nil
}

func classifyManifest(value manifestport.Manifest, err error) (manifestport.Manifest, error) {
	if errors.Is(err, sql.ErrNoRows) {
		return manifestport.Manifest{}, manifestport.ErrNotFound
	}
	if err != nil {
		return manifestport.Manifest{}, fmt.Errorf("read context manifest: %w", err)
	}
	return value, nil
}

func validOmissionReason(reason manifestport.OmissionReason) bool {
	switch reason {
	case manifestport.OmissionBudget, manifestport.OmissionUnsupported, manifestport.OmissionSensitivity, manifestport.OmissionCapability, manifestport.OmissionStale:
		return true
	default:
		return false
	}
}
