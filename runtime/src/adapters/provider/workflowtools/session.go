// Package workflowtools supplies daemon-owned artifact and journal tools. Its
// database lives outside the agent workspace; Markdown is a read projection.
package workflowtools

import (
	"bytes"
	"context"
	valueschemaadapter "darkstar/src/adapters/valueschema/jsonschema"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"

	"darkstar/src/core/workflow"
	_ "modernc.org/sqlite"
)

// ValidateFinal prevents a final response from silently replacing, omitting,
// or inventing the separately validated submissions.
func (s *Session) ValidateFinal(raw json.RawMessage) error {
	var outputs map[workflow.Identifier]json.RawMessage
	if err := json.Unmarshal(raw, &outputs); err != nil {
		return err
	}
	if s.Node.Type() == workflow.NodeImplementation {
		if err := s.validateWorkspaceResult(context.Background(), outputs["changeset"]); err != nil {
			return err
		}
	}
	db, err := s.open(context.Background())
	if err != nil {
		return err
	}
	defer db.Close()
	for id, declaration := range s.Node.Fields().Outputs {
		value, exists := outputs[id]
		if !exists {
			if declaration.Required == nil || *declaration.Required {
				return fmt.Errorf("missing output %s", id)
			}
			continue
		}
		var staged string
		err = db.QueryRow(`SELECT content FROM workflow_tool_events WHERE run_id=? AND attempt_id=? AND resource=? AND operation='submit' ORDER BY sequence DESC LIMIT 1`, s.RunID, s.AttemptID, "output:"+s.AttemptID+":"+string(id)).Scan(&staged)
		if err != nil {
			return fmt.Errorf("output %s must be submitted with submit_output before completion", id)
		}
		var left, right any
		leftDecoder, rightDecoder := json.NewDecoder(bytes.NewReader(value)), json.NewDecoder(strings.NewReader(staged))
		leftDecoder.UseNumber()
		rightDecoder.UseNumber()
		if leftDecoder.Decode(&left) != nil || rightDecoder.Decode(&right) != nil || !reflect.DeepEqual(left, right) {
			return fmt.Errorf("final output %s differs from its submitted value", id)
		}
	}
	return nil
}

type Session struct {
	Workspace        string
	Database         string
	RunID, AttemptID string
	Node             workflow.Node
	Inputs           map[workflow.Identifier]json.RawMessage
	ResourcePlugin   *ResourcePlugin
	AdditionalTools  []Tool
}

func (s *Session) open(ctx context.Context) (*sql.DB, error) {
	if !filepath.IsAbs(s.Database) {
		return nil, errors.New("workflow tool storage requires an absolute path")
	}
	if err := os.MkdirAll(filepath.Dir(s.Database), 0700); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", s.Database)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	_, err = db.ExecContext(ctx, `PRAGMA busy_timeout=10000;
CREATE TABLE IF NOT EXISTS workflow_tool_events (
 sequence INTEGER PRIMARY KEY AUTOINCREMENT, run_id TEXT NOT NULL, attempt_id TEXT NOT NULL,
 resource TEXT NOT NULL, operation TEXT NOT NULL, entry_id TEXT NOT NULL, operation_key TEXT NOT NULL,
 content TEXT NOT NULL, UNIQUE(run_id, resource, operation_key));`)
	if err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}

func (s *Session) readInput(ctx context.Context, callID string, raw json.RawMessage) (json.RawMessage, error) {
	var args struct {
		ID workflow.Identifier `json:"id"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return nil, err
	}
	value, ok := s.Inputs[args.ID]
	if !ok {
		return nil, errors.New("input is not connected")
	}
	return value, nil
}

func (s *Session) submitOutput(ctx context.Context, callID string, raw json.RawMessage) (json.RawMessage, error) {
	var args struct {
		ID    workflow.Identifier `json:"id"`
		Value json.RawMessage     `json:"value"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return nil, err
	}
	if err := workflow.ValidateDeliverable(s.Node, args.ID, args.Value, s.Inputs, valueschemaadapter.Validator{}); err != nil {
		return nil, s.recordRejectedOutput(ctx, callID, args.ID, err)
	}
	if s.Node.Type() == workflow.NodeImplementation && args.ID == "changeset" {
		if err := s.validateWorkspaceResult(ctx, args.Value); err != nil {
			return nil, s.recordRejectedOutput(ctx, callID, args.ID, err)
		}
		if s.Node.Fields().Outputs[args.ID].Type == "schema:changeset_v1" {
			sealed, err := s.sealChangeset(ctx, args.Value)
			if err != nil {
				return nil, err
			}
			args.Value = sealed
		}
	}
	return s.append(ctx, "output:"+s.AttemptID+":"+string(args.ID), "submit", string(args.ID), callID, string(args.Value), false)
}

func (s *Session) append(ctx context.Context, resource, operation, entry, key, content string, requiresEntry bool, creationOperations ...string) (json.RawMessage, error) {
	return s.appendEvent(ctx, resource, operation, entry, key, content, requiresEntry, nil, creationOperations...)
}

func (s *Session) appendEvent(ctx context.Context, resource, operation, entry, key, content string, requiresEntry bool, expectedRevision *uint64, creationOperations ...string) (json.RawMessage, error) {
	db, err := s.open(ctx)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var previousOp, previousEntry, previousContent string
	var previousSequence int64
	err = tx.QueryRowContext(ctx, `SELECT sequence,operation,entry_id,content FROM workflow_tool_events WHERE run_id=? AND resource=? AND operation_key=?`, s.RunID, resource, key).Scan(&previousSequence, &previousOp, &previousEntry, &previousContent)
	if err == nil {
		if previousOp != operation || previousEntry != entry || previousContent != content {
			return nil, errors.New("operation key was reused with different content")
		}
		var revision uint64
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM workflow_tool_events WHERE run_id=? AND resource=? AND sequence<=?`, s.RunID, resource, previousSequence).Scan(&revision); err != nil {
			return nil, err
		}
		return json.Marshal(map[string]any{"entryId": entry, "status": "already_recorded", "revision": revision})
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	var revision uint64
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM workflow_tool_events WHERE run_id=? AND resource=?`, s.RunID, resource).Scan(&revision); err != nil {
		return nil, err
	}
	if expectedRevision != nil && *expectedRevision != revision {
		return nil, fmt.Errorf("RESOURCE_REVISION_CONFLICT: expected %d, current %d", *expectedRevision, revision)
	}
	if requiresEntry {
		var count int
		if len(creationOperations) == 0 {
			creationOperations = []string{"add", "record"}
		}
		placeholders := make([]string, len(creationOperations))
		arguments := []any{s.RunID, resource, entry}
		for i, operation := range creationOperations {
			placeholders[i] = "?"
			arguments = append(arguments, operation)
		}
		err = tx.QueryRowContext(ctx, `SELECT count(*) FROM workflow_tool_events WHERE run_id=? AND resource=? AND entry_id=? AND operation IN (`+strings.Join(placeholders, ",")+`)`, arguments...).Scan(&count)
		if err != nil {
			return nil, err
		}
		if count == 0 {
			return nil, errors.New("item does not exist in this journal")
		}
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO workflow_tool_events(run_id,attempt_id,resource,operation,entry_id,operation_key,content) VALUES(?,?,?,?,?,?,?)`, s.RunID, s.AttemptID, resource, operation, entry, key, content)
	if err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return json.Marshal(map[string]any{"entryId": entry, "status": "recorded", "revision": revision + 1})
}

func (s *Session) read(ctx context.Context, resource string) (json.RawMessage, error) {
	db, err := s.open(ctx)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	rows, err := db.QueryContext(ctx, `SELECT entry_id,operation,content,attempt_id FROM workflow_tool_events WHERE run_id=? AND resource=? ORDER BY sequence`, s.RunID, resource)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var md strings.Builder
	var revision uint64
	fmt.Fprintf(&md, "# %s\n", resource)
	for rows.Next() {
		revision++
		var id, op, content, attempt string
		if err := rows.Scan(&id, &op, &content, &attempt); err != nil {
			return nil, err
		}
		fmt.Fprintf(&md, "\n## %s · %s\n\n%s\n\nAttempt: %s\n", id, op, content, attempt)
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	return json.Marshal(map[string]any{"markdown": md.String(), "revision": revision})
}

// Rejections are diagnostic evidence, never successful output submissions.
func (s *Session) recordRejectedOutput(ctx context.Context, callID string, id workflow.Identifier, cause error) error {
	encoded, _ := json.Marshal(cause.Error())
	_, err := s.append(ctx, "rejected_output:"+s.AttemptID+":"+string(id), "reject", string(id), callID, string(encoded), false)
	if err != nil {
		return fmt.Errorf("%v (could not save rejection evidence: %w)", cause, err)
	}
	return cause
}

// ResolveSubmittedOutputs uses durable tool submissions as the sole output authority.
// Final assistant prose is transcript content, not a second competing result.
func (s *Session) ResolveSubmittedOutputs(ctx context.Context) (json.RawMessage, error) {
	db, err := s.open(ctx)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	outputs := map[workflow.Identifier]json.RawMessage{}
	for id, declaration := range s.Node.Fields().Outputs {
		var staged string
		err = db.QueryRowContext(ctx, "SELECT content FROM workflow_tool_events WHERE run_id=? AND attempt_id=? AND resource=? AND operation='submit' ORDER BY sequence DESC LIMIT 1", s.RunID, s.AttemptID, "output:"+s.AttemptID+":"+string(id)).Scan(&staged)
		if errors.Is(err, sql.ErrNoRows) && declaration.Required != nil && !*declaration.Required {
			continue
		}
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				var rejected string
				rejectionErr := db.QueryRowContext(ctx, "SELECT content FROM workflow_tool_events WHERE run_id=? AND attempt_id=? AND resource=? AND operation='reject' ORDER BY sequence DESC LIMIT 1", s.RunID, s.AttemptID, "rejected_output:"+s.AttemptID+":"+string(id)).Scan(&rejected)
				if rejectionErr == nil {
					var reason string
					if json.Unmarshal([]byte(rejected), &reason) == nil {
						return nil, fmt.Errorf("required output %s was rejected: %s", id, reason)
					}
				} else if !errors.Is(rejectionErr, sql.ErrNoRows) {
					return nil, fmt.Errorf("read output rejection evidence: %w", rejectionErr)
				}
				return nil, fmt.Errorf("required output %s was not submitted; call submit_output and correct any validation errors before completing", id)
			}
			return nil, fmt.Errorf("required output %s has no validated submission: %w", id, err)
		}
		value := json.RawMessage(staged)
		if err = workflow.ValidateDeliverable(s.Node, id, value, s.Inputs, valueschemaadapter.Validator{}); err != nil {
			return nil, err
		}
		outputs[id] = value
	}
	raw, err := json.Marshal(outputs)
	if err != nil {
		return nil, err
	}
	if err = s.ValidateFinal(raw); err != nil {
		return nil, err
	}
	return raw, nil
}
