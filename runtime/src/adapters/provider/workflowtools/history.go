package workflowtools

import (
	"context"
	"database/sql"
	"errors"
	"os"

	"darkstar/src/core/runexecution"
)

func ReadRunArtifacts(ctx context.Context, database, runID string, after uint64, limit int) ([]runexecution.RunArtifactRecord, error) {
	if _, err := os.Stat(database); errors.Is(err, os.ErrNotExist) || database == "" {
		return nil, nil
	} else if err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", database)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	var exists int
	if err = db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='workflow_tool_events'`).Scan(&exists); err != nil || exists == 0 {
		return nil, err
	}
	rows, err := db.QueryContext(ctx, `SELECT sequence,attempt_id,resource,entry_id,operation,content FROM workflow_tool_events WHERE run_id=? AND sequence>? ORDER BY sequence LIMIT ?`, runID, after, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	records := []runexecution.RunArtifactRecord{}
	for rows.Next() {
		var record runexecution.RunArtifactRecord
		if err = rows.Scan(&record.Sequence, &record.AttemptID, &record.Resource, &record.EntryID, &record.Operation, &record.Content); err != nil {
			return nil, err
		}
		records = append(records, record)
	}
	return records, rows.Err()
}
