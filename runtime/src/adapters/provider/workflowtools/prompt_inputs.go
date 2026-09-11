package workflowtools

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"darkstar/src/core/workflow"
)

// SnapshotPromptInputs converts connected journal handles to immutable scoped
// values once per visit. Retries and review revisions reuse the same evidence.
// Snapshots omit host record IDs and never grant a journal mutation capability.
func (s *Session) SnapshotPromptInputs(ctx context.Context, visitID string) (map[workflow.Identifier]json.RawMessage, error) {
	result := make(map[workflow.Identifier]json.RawMessage, len(s.Inputs))
	for id, value := range s.Inputs {
		result[id] = value
	}
	if s.Node.Fields().Prompt == nil {
		return result, nil
	}
	for _, id := range []workflow.Identifier{"open_items", "deferred_work"} {
		var ref struct {
			Kind       string `json:"kind"`
			ResourceID string `json:"resourceId"`
		}
		if json.Unmarshal(s.Inputs[id], &ref) != nil || ref.ResourceID == "" {
			continue
		}
		if visitID == "" || ref.Kind != "open_items" || strings.Contains(ref.ResourceID, ":") {
			return nil, fmt.Errorf("cannot snapshot linked input %s", id)
		}
		value, err := s.snapshotPromptJournal(ctx, visitID, string(id), ref.ResourceID)
		if err != nil {
			return nil, err
		}
		result[id] = value
	}
	return result, nil
}

type promptJournalItem struct {
	ID          string `json:"id"`
	Description string `json:"description"`
	Status      string `json:"status"`
	LatestNote  string `json:"latestNote"`
}

func (s *Session) snapshotPromptJournal(ctx context.Context, visitID, inputID, resourceID string) (json.RawMessage, error) {
	db, err := s.open(ctx)
	if err != nil {
		return nil, err
	}
	defer func() {
		_ = db.Close()
	}()
	_, err = db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS workflow_prompt_inputs (run_id TEXT NOT NULL, visit_id TEXT NOT NULL, input_id TEXT NOT NULL, content TEXT NOT NULL, PRIMARY KEY(run_id,visit_id,input_id))`)
	if err != nil {
		return nil, err
	}
	var existing string
	err = db.QueryRowContext(ctx, `SELECT content FROM workflow_prompt_inputs WHERE run_id=? AND visit_id=? AND input_id=?`, s.RunID, visitID, inputID).Scan(&existing)
	if err == nil {
		return json.RawMessage(existing), nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	rows, err := db.QueryContext(ctx, `SELECT entry_id,operation,content FROM workflow_tool_events WHERE run_id=? AND resource=? ORDER BY sequence`, s.RunID, resourceID)
	if err != nil {
		return nil, err
	}
	items := map[string]promptJournalItem{}
	var revision uint64
	for rows.Next() {
		var id, operation, content string
		if err := rows.Scan(&id, &operation, &content); err != nil {
			_ = rows.Close()
			return nil, err
		}
		revision++
		item := items[id]
		item.ID, item.LatestNote = id, content
		switch operation {
		case "add":
			item.Description, item.Status = content, "open"
		case "defer":
			item.Status = "deferred"
		case "resolve":
			item.Status = "resolved"
		default:
			_ = rows.Close()
			return nil, fmt.Errorf("unsupported open-item operation %q", operation)
		}
		items[id] = item
	}
	err = rows.Err()
	closeErr := rows.Close()
	if err != nil {
		return nil, err
	}
	if closeErr != nil {
		return nil, closeErr
	}
	selected := []promptJournalItem{}
	for _, item := range items {
		if inputID == "deferred_work" && item.Status == "deferred" || inputID == "open_items" && item.Status == "open" {
			selected = append(selected, item)
		}
	}
	sort.Slice(selected, func(i, j int) bool {
		return selected[i].ID < selected[j].ID
	})
	encoded, err := json.Marshal(struct {
		Items    []promptJournalItem `json:"items"`
		Revision uint64              `json:"revision"`
	}{Items: selected, Revision: revision})
	if err != nil {
		return nil, err
	}
	_, err = db.ExecContext(ctx, `INSERT OR IGNORE INTO workflow_prompt_inputs(run_id,visit_id,input_id,content) VALUES(?,?,?,?)`, s.RunID, visitID, inputID, string(encoded))
	if err != nil {
		return nil, err
	}
	err = db.QueryRowContext(ctx, `SELECT content FROM workflow_prompt_inputs WHERE run_id=? AND visit_id=? AND input_id=?`, s.RunID, visitID, inputID).Scan(&existing)
	return json.RawMessage(existing), err
}
