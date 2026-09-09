package runexecution

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"darkstar/src/ports/statestore"
)

type TranscriptEvent struct {
	Position uint64          `json:"position"`
	Time     time.Time       `json:"time"`
	Kind     string          `json:"kind"`
	Subject  string          `json:"subject"`
	Data     json.RawMessage `json:"data"`
}
type TranscriptPage struct {
	Events  []TranscriptEvent `json:"events"`
	Next    uint64            `json:"next"`
	HasMore bool              `json:"hasMore"`
}
type TranscriptPayloadReader interface {
	ReadTranscriptPayloads(context.Context, string, []uint64) (map[uint64]json.RawMessage, error)
}

func (s *Service) Transcript(ctx context.Context, runID string, after uint64, limit int) (TranscriptPage, error) {
	if _, err := s.store.Run(ctx, runID); err != nil {
		return TranscriptPage{}, err
	}
	source, ok := s.store.(interface {
		RunEventsAfter(context.Context, string, uint64, int) ([]statestore.Event, error)
	})
	if !ok {
		return TranscriptPage{}, errors.New("transcript paging unavailable")
	}
	events, err := source.RunEventsAfter(ctx, runID, after, limit)
	if err != nil {
		return TranscriptPage{}, err
	}
	page := TranscriptPage{Events: make([]TranscriptEvent, 0, len(events)), Next: after, HasMore: len(events) == limit}
	// Older events retained payload digests only. Recover their original observations
	// from the provider's immutable store, using server-owned attempt identities.
	missing := map[string][]uint64{}
	for _, event := range events {
		if event.Kind != "attempt.provider_event" {
			continue
		}
		var data struct {
			Sequence uint64          `json:"sequence"`
			Payload  json.RawMessage `json:"payload"`
		}
		_ = json.Unmarshal(event.Data, &data)
		if len(data.Payload) == 0 {
			missing[event.AggregateID] = append(missing[event.AggregateID], data.Sequence)
		}
	}
	recovered := map[string]map[uint64]json.RawMessage{}
	if reader, ok := s.requestBuilder.(TranscriptPayloadReader); ok {
		for attempt, sequences := range missing {
			values, readErr := reader.ReadTranscriptPayloads(ctx, attempt, sequences)
			if readErr != nil {
				return TranscriptPage{}, readErr
			}
			recovered[attempt] = values
		}
	}
	for _, event := range events {
		data := event.Data
		if event.Kind == "attempt.provider_event" {
			var values map[string]json.RawMessage
			_ = json.Unmarshal(data, &values)
			if len(values["payload"]) == 0 {
				var sequence uint64
				_ = json.Unmarshal(values["sequence"], &sequence)
				if payload := recovered[event.AggregateID][sequence]; len(payload) > 0 {
					values["payload"] = payload
					delete(values, "redacted")
				} else {
					values["historyGap"] = json.RawMessage(`true`)
				}
				data, _ = json.Marshal(values)
			}
		}
		page.Events = append(page.Events, TranscriptEvent{Position: event.GlobalPosition, Time: event.OccurredAt, Kind: event.Kind, Subject: event.AggregateID, Data: data})
		page.Next = event.GlobalPosition
	}
	return page, nil
}

type RunArtifactRecord struct {
	Sequence  uint64 `json:"sequence"`
	AttemptID string `json:"attemptId"`
	Resource  string `json:"resource"`
	EntryID   string `json:"entryId"`
	Operation string `json:"operation"`
	Content   string `json:"content"`
}
type RunArtifactPage struct {
	Records []RunArtifactRecord `json:"records"`
	Next    uint64              `json:"next"`
	HasMore bool                `json:"hasMore"`
}

func (s *Service) Artifacts(ctx context.Context, runID string, after uint64, limit int) (RunArtifactPage, error) {
	if _, err := s.store.Run(ctx, runID); err != nil {
		return RunArtifactPage{}, err
	}
	reader, ok := s.requestBuilder.(interface {
		ReadRunArtifacts(context.Context, string, uint64, int) ([]RunArtifactRecord, error)
	})
	page := RunArtifactPage{Records: []RunArtifactRecord{}, Next: after}
	if !ok {
		return page, nil
	}
	records, err := reader.ReadRunArtifacts(ctx, runID, after, limit)
	if err != nil {
		return page, err
	}
	if records != nil {
		page.Records = records
	}
	page.HasMore = len(records) == limit
	if len(records) > 0 {
		page.Next = records[len(records)-1].Sequence
	}
	return page, nil
}
