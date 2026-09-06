package sqlite

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"darkstar/src/ports/statestore"
)

const runExecutionContextSelect = `SELECT schema_version, run_id, revision,
	run_inputs_json, accepted_outputs_json, frame_json, digest
	FROM run_execution_contexts`

type runExecutionContextContent struct {
	SchemaVersion   uint64                                `json:"schemaVersion"`
	RunID           string                                `json:"runId"`
	RunInputs       map[string]json.RawMessage            `json:"runInputs"`
	AcceptedOutputs map[string]map[string]json.RawMessage `json:"acceptedOutputs"`
	FrameSnapshot   json.RawMessage                       `json:"frameSnapshot"`
}

func (d *Database) RunExecutionContext(ctx context.Context, runID string) (statestore.RunExecutionContext, error) {
	value, err := scanRunExecutionContext(d.sql.QueryRowContext(ctx, runExecutionContextSelect+` WHERE run_id = ?`, runID))
	if errors.Is(err, sql.ErrNoRows) {
		return statestore.RunExecutionContext{}, statestore.ErrNotFound
	}
	if err != nil {
		return statestore.RunExecutionContext{}, fmt.Errorf("read run execution context %s: %w", runID, err)
	}
	return value, nil
}

// SaveRunExecutionContext creates at expected revision zero and otherwise uses
// compare-and-swap. The immutable run inputs are never included in UPDATE.
func (d *Database) SaveRunExecutionContext(ctx context.Context, value statestore.RunExecutionContext, expectedRevision uint64) (result statestore.RunExecutionContext, err error) {
	normalized, runInputsJSON, outputsJSON, frameJSON, err := normalizeRunExecutionContext(value)
	if err != nil {
		return statestore.RunExecutionContext{}, err
	}

	tx, err := d.sql.BeginTx(ctx, nil)
	if err != nil {
		return statestore.RunExecutionContext{}, fmt.Errorf("begin run execution context write: %w", err)
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()

	var actual uint64
	var existingInputs string
	readErr := tx.QueryRowContext(ctx, `SELECT revision, run_inputs_json FROM run_execution_contexts WHERE run_id = ?`, normalized.RunID).Scan(&actual, &existingInputs)
	switch {
	case errors.Is(readErr, sql.ErrNoRows) && expectedRevision != 0:
		return statestore.RunExecutionContext{}, contextRevisionConflict(normalized.RunID, expectedRevision, 0)
	case readErr != nil && !errors.Is(readErr, sql.ErrNoRows):
		return statestore.RunExecutionContext{}, fmt.Errorf("read current run execution context revision: %w", readErr)
	case readErr == nil && actual != expectedRevision:
		return statestore.RunExecutionContext{}, contextRevisionConflict(normalized.RunID, expectedRevision, actual)
	case readErr == nil && existingInputs != runInputsJSON:
		return statestore.RunExecutionContext{}, errors.New("run execution inputs are immutable")
	}

	revision := expectedRevision + 1
	normalized.Revision = revision
	normalized.Digest, err = executionContextDigest(normalized)
	if err != nil {
		return statestore.RunExecutionContext{}, err
	}
	now := formatTime(d.now().UTC().Round(0))
	if errors.Is(readErr, sql.ErrNoRows) {
		_, err = tx.ExecContext(ctx, `INSERT INTO run_execution_contexts(
			run_id, schema_version, revision, run_inputs_json, accepted_outputs_json, frame_json, digest, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`, normalized.RunID, normalized.SchemaVersion,
			revision, runInputsJSON, outputsJSON, frameJSON, normalized.Digest, now, now)
	} else {
		_, err = tx.ExecContext(ctx, `UPDATE run_execution_contexts
			SET schema_version = ?, revision = ?, accepted_outputs_json = ?, frame_json = ?, digest = ?, updated_at = ?
			WHERE run_id = ? AND revision = ?`, normalized.SchemaVersion, revision, outputsJSON,
			frameJSON, normalized.Digest, now, normalized.RunID, expectedRevision)
	}
	if err != nil {
		return statestore.RunExecutionContext{}, fmt.Errorf("write run execution context %s: %w", normalized.RunID, err)
	}
	result, err = scanRunExecutionContext(tx.QueryRowContext(ctx, runExecutionContextSelect+` WHERE run_id = ?`, normalized.RunID))
	if err != nil {
		return statestore.RunExecutionContext{}, fmt.Errorf("read saved run execution context: %w", err)
	}
	if err = tx.Commit(); err != nil {
		return statestore.RunExecutionContext{}, fmt.Errorf("commit run execution context: %w", err)
	}
	return result, nil
}

func normalizeRunExecutionContext(value statestore.RunExecutionContext) (statestore.RunExecutionContext, string, string, string, error) {
	if value.SchemaVersion != statestore.RunExecutionContextSchemaVersion {
		return value, "", "", "", fmt.Errorf("unsupported run execution context schema version %d", value.SchemaVersion)
	}
	if !strings.HasPrefix(value.RunID, "run_") || strings.TrimSpace(value.RunID) != value.RunID || value.RunID == "run_" {
		return value, "", "", "", errors.New("run execution context requires a valid run ID")
	}
	inputs, err := normalizeRawMap(value.RunInputs, "run input")
	if err != nil {
		return value, "", "", "", err
	}
	outputs := make(map[string]map[string]json.RawMessage, len(value.AcceptedOutputs))
	for nodeID, nodeOutputs := range value.AcceptedOutputs {
		if strings.TrimSpace(nodeID) == "" || strings.TrimSpace(nodeID) != nodeID {
			return value, "", "", "", errors.New("accepted output requires a valid node ID")
		}
		normalized, err := normalizeRawMap(nodeOutputs, "accepted output")
		if err != nil {
			return value, "", "", "", fmt.Errorf("node %s: %w", nodeID, err)
		}
		outputs[nodeID] = normalized
	}
	if outputs == nil {
		outputs = map[string]map[string]json.RawMessage{}
	}
	frame, err := normalizeJSONObject(value.FrameSnapshot, "workflow frame snapshot")
	if err != nil {
		return value, "", "", "", err
	}
	value.RunInputs, value.AcceptedOutputs, value.FrameSnapshot = inputs, outputs, frame
	inputsEncoded, err := json.Marshal(inputs)
	if err != nil {
		return value, "", "", "", fmt.Errorf("encode run inputs: %w", err)
	}
	outputsEncoded, err := json.Marshal(outputs)
	if err != nil {
		return value, "", "", "", fmt.Errorf("encode accepted outputs: %w", err)
	}
	return value, string(inputsEncoded), string(outputsEncoded), string(frame), nil
}

func normalizeRawMap(values map[string]json.RawMessage, kind string) (map[string]json.RawMessage, error) {
	result := make(map[string]json.RawMessage, len(values))
	for name, raw := range values {
		if strings.TrimSpace(name) == "" || strings.TrimSpace(name) != name {
			return nil, fmt.Errorf("%s requires a valid name", kind)
		}
		encoded, err := normalizeJSON(raw)
		if err != nil {
			return nil, fmt.Errorf("%s %q contains invalid JSON: %w", kind, name, err)
		}
		result[name] = encoded
	}
	return result, nil
}

func normalizeJSONObject(raw json.RawMessage, kind string) (json.RawMessage, error) {
	encoded, decoded, err := decodeCanonicalJSON(raw)
	if err != nil {
		return nil, fmt.Errorf("%s contains invalid JSON: %w", kind, err)
	}
	if _, ok := decoded.(map[string]any); !ok {
		return nil, fmt.Errorf("%s must be a JSON object", kind)
	}
	return encoded, nil
}

func normalizeJSON(raw json.RawMessage) (json.RawMessage, error) {
	encoded, _, err := decodeCanonicalJSON(raw)
	return encoded, err
}

func decodeCanonicalJSON(raw json.RawMessage) (json.RawMessage, any, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var decoded any
	if err := decoder.Decode(&decoded); err != nil {
		return nil, nil, err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return nil, nil, errors.New("multiple JSON values")
		}
		return nil, nil, err
	}
	encoded, err := json.Marshal(decoded)
	return encoded, decoded, err
}

func executionContextDigest(value statestore.RunExecutionContext) (string, error) {
	content := runExecutionContextContent{SchemaVersion: value.SchemaVersion, RunID: value.RunID,
		RunInputs: value.RunInputs, AcceptedOutputs: value.AcceptedOutputs, FrameSnapshot: value.FrameSnapshot}
	encoded, err := json.Marshal(content)
	if err != nil {
		return "", fmt.Errorf("encode run execution context digest: %w", err)
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}

func scanRunExecutionContext(row rowScanner) (statestore.RunExecutionContext, error) {
	var value statestore.RunExecutionContext
	var inputsJSON, outputsJSON, frameJSON string
	if err := row.Scan(&value.SchemaVersion, &value.RunID, &value.Revision, &inputsJSON, &outputsJSON, &frameJSON, &value.Digest); err != nil {
		return statestore.RunExecutionContext{}, err
	}
	decoder := json.NewDecoder(strings.NewReader(inputsJSON))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&value.RunInputs); err != nil {
		return statestore.RunExecutionContext{}, fmt.Errorf("decode run inputs: %w", err)
	}
	decoder = json.NewDecoder(strings.NewReader(outputsJSON))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&value.AcceptedOutputs); err != nil {
		return statestore.RunExecutionContext{}, fmt.Errorf("decode accepted outputs: %w", err)
	}
	value.FrameSnapshot = json.RawMessage(frameJSON)
	normalized, _, _, _, err := normalizeRunExecutionContext(value)
	if err != nil {
		return statestore.RunExecutionContext{}, err
	}
	normalized.Revision, normalized.Digest = value.Revision, value.Digest
	want, err := executionContextDigest(normalized)
	if err != nil {
		return statestore.RunExecutionContext{}, err
	}
	if normalized.Revision == 0 || normalized.Digest != want {
		return statestore.RunExecutionContext{}, errors.New("run execution context failed integrity validation")
	}
	return normalized, nil
}

func contextRevisionConflict(runID string, expected, actual uint64) error {
	return &statestore.RunExecutionContextRevisionConflictError{RunID: runID, Expected: expected, Actual: actual}
}
