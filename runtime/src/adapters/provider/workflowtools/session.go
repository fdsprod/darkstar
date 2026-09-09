// Package workflowtools supplies daemon-owned artifact and journal tools. Its
// database lives outside the agent workspace; Markdown is a read projection.
package workflowtools

import (
	"bytes"
	"context"
	"crypto/sha256"
	valueschemaadapter "darkstar/src/adapters/valueschema/jsonschema"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"

	"darkstar/src/core/workflow"
	"darkstar/src/ports/provider"
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
		err = db.QueryRow(`SELECT content FROM workflow_tool_events WHERE run_id=? AND attempt_id=? AND resource=? ORDER BY sequence DESC LIMIT 1`, s.RunID, s.AttemptID, "output:"+s.AttemptID+":"+string(id)).Scan(&staged)
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
}

func (s *Session) Definitions() []provider.ToolDefinition {
	makeTool := func(name, description string, properties map[string]any, required []string) provider.ToolDefinition {
		schema, _ := json.Marshal(map[string]any{"type": "object", "additionalProperties": false, "properties": properties, "required": required})
		return provider.ToolDefinition{Type: "function", Name: name, Description: description, InputSchema: schema}
	}
	text := map[string]any{"type": "string"}
	tools := []provider.ToolDefinition{
		makeTool("read_input", "Read an input or template by its exact input ID.", map[string]any{"id": text}, []string{"id"}),
		makeTool("submit_output", "Validate and stage one output. Correct any reported errors and submit again. The daemon collects validated submissions; do not repeat their contents in your final message.", map[string]any{"id": text, "value": map[string]any{}}, []string{"id", "value"}),
	}
	if s.Node.Type() == workflow.NodeImplementation {
		tools = append(tools, makeTool("inspect_workspace_changes", "Read the actual added, modified, and deleted files since this attempt began. Use these exact paths in changeset.files. This does not modify or publish anything.", map[string]any{}, []string{}))
	}
	ids := make([]string, 0, len(s.Inputs))
	for id := range s.Inputs {
		ids = append(ids, string(id))
	}
	sort.Strings(ids)
	for _, id := range ids {
		kind, _ := s.journal(workflow.Identifier(id))
		if kind == "" {
			continue
		}
		operations := []string{"read", "add", "resolve", "defer"}
		if kind == "decision_log" {
			operations = []string{"read", "record", "supersede"}
		}
		tools = append(tools, makeTool("journal_"+id, "Use this append-only "+kind+" journal. Use entryId for an existing item; use an empty entryId to create one. key is a stable operation key, reused on retry. text records the item, decision, or resolution rationale.", map[string]any{"operation": map[string]any{"type": "string", "enum": operations}, "entryId": text, "text": text, "key": text}, []string{"operation", "entryId", "text", "key"}))
	}
	return tools
}

func (s *Session) journal(id workflow.Identifier) (string, string) {
	var ref struct {
		Kind       string `json:"kind"`
		ResourceID string `json:"resourceId"`
	}
	if json.Unmarshal(s.Inputs[id], &ref) != nil || (ref.Kind != "open_items" && ref.Kind != "decision_log") || ref.ResourceID == "" {
		return "", ""
	}
	return ref.Kind, ref.ResourceID
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

func (s *Session) Call(ctx context.Context, callID, name string, raw json.RawMessage) (json.RawMessage, error) {
	if len(raw) > 1024*1024 {
		return nil, errors.New("tool arguments exceed 1 MiB")
	}
	if name == "inspect_workspace_changes" && s.Node.Type() == workflow.NodeImplementation {
		changes, err := s.workspaceChanges(ctx)
		if err != nil {
			return nil, err
		}
		return json.Marshal(map[string]any{"files": changes})
	}
	if name == "read_input" {
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
	if name == "submit_output" {
		var args struct {
			ID    workflow.Identifier `json:"id"`
			Value json.RawMessage     `json:"value"`
		}
		if err := json.Unmarshal(raw, &args); err != nil {
			return nil, err
		}
		if err := workflow.ValidateDeliverable(s.Node, args.ID, args.Value, s.Inputs, valueschemaadapter.Validator{}); err != nil {
			return nil, err
		}
		if s.Node.Type() == workflow.NodeImplementation && args.ID == "changeset" {
			if err := s.validateWorkspaceResult(ctx, args.Value); err != nil {
				return nil, err
			}
		}
		return s.append(ctx, "output:"+s.AttemptID+":"+string(args.ID), "submit", string(args.ID), callID, string(args.Value), false)
	}
	id := workflow.Identifier(strings.TrimPrefix(name, "journal_"))
	kind, resource := s.journal(id)
	if !strings.HasPrefix(name, "journal_") || kind == "" {
		return nil, errors.New("tool is not connected to this node")
	}
	var args struct {
		Operation string `json:"operation"`
		EntryID   string `json:"entryId"`
		Text      string `json:"text"`
		Key       string `json:"key"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return nil, err
	}
	if args.Operation == "read" {
		return s.read(ctx, resource)
	}
	create := args.Operation == "add" && kind == "open_items" || args.Operation == "record" && kind == "decision_log"
	update := kind == "open_items" && (args.Operation == "resolve" || args.Operation == "defer") || kind == "decision_log" && args.Operation == "supersede"
	if !create && !update {
		return nil, errors.New("operation is not allowed for this journal")
	}
	if strings.TrimSpace(args.Text) == "" || strings.TrimSpace(args.Key) == "" {
		return nil, errors.New("text and a stable operation key are required")
	}
	if create {
		if args.EntryID != "" {
			return nil, errors.New("new items must not supply entryId")
		}
		args.EntryID = fmt.Sprintf("item_%x", sha256.Sum256([]byte(s.RunID+"\x00"+resource+"\x00"+args.Key)))[:29]
	} else if args.EntryID == "" {
		return nil, errors.New("entryId is required")
	}
	return s.append(ctx, resource, args.Operation, args.EntryID, args.Key, args.Text, update)
}

func (s *Session) append(ctx context.Context, resource, operation, entry, key, content string, requiresEntry bool) (json.RawMessage, error) {
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
	err = tx.QueryRowContext(ctx, `SELECT operation,entry_id,content FROM workflow_tool_events WHERE run_id=? AND resource=? AND operation_key=?`, s.RunID, resource, key).Scan(&previousOp, &previousEntry, &previousContent)
	if err == nil {
		if previousOp != operation || previousEntry != entry || previousContent != content {
			return nil, errors.New("operation key was reused with different content")
		}
		return json.Marshal(map[string]any{"entryId": entry, "status": "already_recorded"})
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	if requiresEntry {
		var count int
		err = tx.QueryRowContext(ctx, `SELECT count(*) FROM workflow_tool_events WHERE run_id=? AND resource=? AND entry_id=? AND operation IN ('add','record')`, s.RunID, resource, entry).Scan(&count)
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
	return json.Marshal(map[string]any{"entryId": entry, "status": "recorded"})
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
	fmt.Fprintf(&md, "# %s\n", resource)
	for rows.Next() {
		var id, op, content, attempt string
		if err := rows.Scan(&id, &op, &content, &attempt); err != nil {
			return nil, err
		}
		fmt.Fprintf(&md, "\n## %s · %s\n\n%s\n\nAttempt: %s\n", id, op, content, attempt)
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	return json.Marshal(map[string]string{"markdown": md.String()})
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
		err = db.QueryRowContext(ctx, "SELECT content FROM workflow_tool_events WHERE run_id=? AND attempt_id=? AND resource=? ORDER BY sequence DESC LIMIT 1", s.RunID, s.AttemptID, "output:"+s.AttemptID+":"+string(id)).Scan(&staged)
		if errors.Is(err, sql.ErrNoRows) && declaration.Required != nil && !*declaration.Required {
			continue
		}
		if err != nil {
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
