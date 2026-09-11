package sqlite

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"

	"darkstar/src/ports/contentstore"
)

func TestRunPromptSnapshotsAreFrozenAndIntegrityChecked(t *testing.T) {
	ctx := context.Background()
	database := openEventTestDatabase(t)
	runID := testID("run", 'P')
	createRunAggregate(t, database, runID)
	input := executionContextFixture(runID)
	document := contentstore.Document{Kind: "prompt", Instructions: "Implement only the scoped point."}
	raw, _ := json.Marshal(document)
	sum := sha256.Sum256(raw)
	version := contentstore.Version{Reference: contentstore.Reference{ID: "prompt", Version: "1.0.0", Digest: hex.EncodeToString(sum[:])}, Document: document, CreatedAt: testTime}
	input.PromptSnapshots = map[string]contentstore.Version{"implement": version}
	saved, err := database.SaveRunExecutionContext(ctx, input, 0)
	if err != nil {
		t.Fatal(err)
	}
	if saved.PromptSnapshots["implement"].Reference != version.Reference {
		t.Fatal("saved prompt identity changed")
	}
	saved.PromptSnapshots = nil
	if _, err := database.SaveRunExecutionContext(ctx, saved, 1); err == nil || !strings.Contains(err.Error(), "immutable") {
		t.Fatalf("snapshot removal accepted: %v", err)
	}
	if _, err := database.SQL().ExecContext(ctx, `UPDATE run_execution_contexts SET prompt_snapshots_json='{}' WHERE run_id=?`, runID); err != nil {
		t.Fatal(err)
	}
	if _, err := database.RunExecutionContext(ctx, runID); err == nil || !strings.Contains(err.Error(), "integrity") {
		t.Fatalf("snapshot tampering accepted: %v", err)
	}
}
