package runexecution

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"darkstar/src/ports/statestore"
)

func TestGetReturnsNonNullAdditiveRunCollections(t *testing.T) {
	store := runViewStore{evidence: statestore.RunEvidence{
		Run: statestore.RunProjection{RunID: "run_01K3Z1C2AAAAAAAAAAAAAAAAAA"},
	}}
	service := &Service{store: store}

	view, err := service.Get(context.Background(), store.evidence.Run.RunID)
	if err != nil {
		t.Fatalf("get run view: %v", err)
	}
	encoded, err := json.Marshal(view)
	if err != nil {
		t.Fatalf("marshal run view: %v", err)
	}
	var document map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &document); err != nil {
		t.Fatalf("decode run view: %v", err)
	}
	for _, field := range []string{"nodes", "attempts", "timeline", "commands"} {
		if string(document[field]) != "[]" {
			t.Errorf("%s = %s, want non-null empty array", field, document[field])
		}
	}
}

func TestGetExposesOnlySafeTimelineAndCommandSummaries(t *testing.T) {
	now := time.Date(2026, 9, 3, 12, 0, 0, 0, time.UTC)
	responseStatus := 200
	position := uint64(9)
	store := runViewStore{evidence: statestore.RunEvidence{
		Run: statestore.RunProjection{RunID: "run_01K3Z1C2AAAAAAAAAAAAAAAAAA"},
		Events: []statestore.Event{{
			ID: "event_01K3Z1C2AAAAAAAAAAAAAAAAAA", GlobalPosition: position,
			AggregateType: statestore.AggregateRun, AggregateID: "run_01K3Z1C2AAAAAAAAAAAAAAAAAA",
			AggregateRevision: 2, Kind: "run.completed", OccurredAt: now, RecordedAt: now,
			CorrelationID: "sensitive-correlation", CommandID: "sensitive-command-id",
			Actor:    statestore.Actor{Type: statestore.ActorUser, ID: "sensitive-actor-id"},
			Data:     json.RawMessage(`{"status":"completed","secret":"event-data-secret"}`),
			Metadata: json.RawMessage(`{"token":"event-metadata-secret"}`),
		}},
		Commands: []statestore.CommandEvidence{{
			Scope: "runs.continue", IdempotencyKey: "sensitive-idempotency-key",
			RequestDigest: "sensitive-request-digest", Status: "completed",
			ResponseStatus: &responseStatus, Response: json.RawMessage(`{"token":"command-response-secret"}`),
			FirstEventPosition: &position, LastEventPosition: &position, CreatedAt: now, CompletedAt: &now,
		}},
	}}
	service := &Service{store: store}

	view, err := service.Get(context.Background(), store.evidence.Run.RunID)
	if err != nil {
		t.Fatalf("get run view: %v", err)
	}
	encoded, err := json.Marshal(view)
	if err != nil {
		t.Fatalf("marshal run view: %v", err)
	}
	public := string(encoded)
	for _, forbidden := range []string{
		`"data"`, `"metadata"`, `"commandId"`, `"idempotencyKey"`, `"requestDigest"`, `"response"`,
		"event-data-secret", "event-metadata-secret", "sensitive-command-id", "sensitive-idempotency-key",
		"sensitive-request-digest", "command-response-secret", "sensitive-actor-id",
	} {
		if strings.Contains(public, forbidden) {
			t.Errorf("public run view contains forbidden timeline/command material %q: %s", forbidden, public)
		}
	}
	if len(view.Timeline) != 1 || len(view.Commands) != 1 {
		t.Fatalf("summary lengths = timeline %d, commands %d; want 1 each", len(view.Timeline), len(view.Commands))
	}
}

func TestRunViewHistoryWindowsKeepNewestEvidenceAndAdvertiseEarlierRecords(t *testing.T) {
	now := time.Date(2026, 9, 3, 12, 0, 0, 0, time.UTC)
	events := make([]statestore.Event, viewTimelineLimit+1)
	for index := range events {
		position := uint64(index + 1)
		events[index] = statestore.Event{
			ID: "event", GlobalPosition: position, AggregateType: statestore.AggregateRun,
			AggregateID: "run_01K3Z1C2AAAAAAAAAAAAAAAAAA", AggregateRevision: position,
			Kind: "run.updated", OccurredAt: now, RecordedAt: now,
			Actor: statestore.Actor{Type: statestore.ActorSystem},
		}
	}
	commands := make([]statestore.CommandEvidence, viewCommandLimit+1)
	for index := range commands {
		commands[index] = statestore.CommandEvidence{Scope: "runs.update", Status: "completed", CreatedAt: now.Add(time.Duration(index) * time.Second)}
	}

	timeline, timelinePage := summarizeTimeline(events)
	commandWindow, commandPage := summarizeCommands(commands)
	if len(timeline) != viewTimelineLimit || timeline[0].GlobalPosition != 2 || timeline[len(timeline)-1].GlobalPosition != uint64(viewTimelineLimit+1) {
		t.Fatalf("timeline window = len %d, positions %d..%d", len(timeline), timeline[0].GlobalPosition, timeline[len(timeline)-1].GlobalPosition)
	}
	if !timelinePage.HasEarlier || timelinePage.FirstPosition == nil || *timelinePage.FirstPosition != 2 || timelinePage.LastPosition == nil || *timelinePage.LastPosition != uint64(viewTimelineLimit+1) {
		t.Fatalf("timeline page info = %#v", timelinePage)
	}
	if len(commandWindow) != viewCommandLimit || !commandPage.HasEarlier || !commandWindow[0].CreatedAt.Equal(commands[1].CreatedAt) {
		t.Fatalf("command window = len %d, page %#v", len(commandWindow), commandPage)
	}
}

type runViewStore struct {
	statestore.Store
	evidence statestore.RunEvidence
	nodes    []statestore.NodeProjection
	attempts []statestore.AttemptProjection
}

func (s runViewStore) RunEvidence(context.Context, string) (statestore.RunEvidence, error) {
	return s.evidence, nil
}

func (s runViewStore) NodesForRun(context.Context, string) ([]statestore.NodeProjection, error) {
	return s.nodes, nil
}

func (s runViewStore) AttemptsForRun(context.Context, string) ([]statestore.AttemptProjection, error) {
	return s.attempts, nil
}
