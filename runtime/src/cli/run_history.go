package cli

import (
	"context"
	"encoding/json"

	"darkstar/src/adapters/provider/workflowtools"
	"darkstar/src/core/runexecution"
)

func (w *daemonProviderWiring) ReadTranscriptPayloads(ctx context.Context, attempt string, sequences []uint64) (map[uint64]json.RawMessage, error) {
	if reader, ok := w.evidence.(interface {
		ReadPayloads(context.Context, string, []uint64) (map[uint64]json.RawMessage, error)
	}); ok {
		return reader.ReadPayloads(ctx, attempt, sequences)
	}
	return nil, nil
}
func (w *daemonProviderWiring) ReadRunArtifacts(ctx context.Context, runID string, after uint64, limit int) ([]runexecution.RunArtifactRecord, error) {
	return workflowtools.ReadRunArtifacts(ctx, w.toolDatabase, runID, after, limit)
}
