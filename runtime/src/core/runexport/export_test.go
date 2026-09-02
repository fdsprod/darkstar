package runexport

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"darkstar/src/ports/statestore"
)

func TestBuildCreatesRedactedSelfContainedBundle(t *testing.T) {
	t.Parallel()
	runID := "run_01K3Z1C2AAAAAAAAAAAAAAAAAA"
	when := time.Date(2026, time.September, 1, 4, 0, 0, 0, time.UTC)
	source := staticEvidence{value: statestore.RunEvidence{
		Run: statestore.RunProjection{RunID: runID, WorkItemID: "work_01K3Z1C1AAAAAAAAAAAAAAAAAA", WorkflowID: "delivery", WorkflowVersion: "1", Status: statestore.RunCompleted, ResourceVersion: 2, LastGlobalPosition: 2, CreatedAt: when, UpdatedAt: when},
		Events: []statestore.Event{
			{SchemaVersion: 1, ID: "event_01K3Z1D0000000000000000000", GlobalPosition: 1, AggregateType: statestore.AggregateRun, AggregateID: runID, AggregateRevision: 1, Kind: "run.created", OccurredAt: when, RecordedAt: when, CorrelationID: runID, CommandID: "create-run", Actor: statestore.Actor{Type: statestore.ActorUser, ID: "operator"}, Data: json.RawMessage(`{"password":"do-not-export","workflow":"delivery"}`), Metadata: json.RawMessage(`{}`)},
			{SchemaVersion: 1, ID: "event_01K3Z1D0000000000000000001", GlobalPosition: 2, AggregateType: statestore.AggregateArtifact, AggregateID: "artifact_01K3Z1C5AAAAAAAAAAAAAAAAAA", AggregateRevision: 1, Kind: "artifact.created", OccurredAt: when, RecordedAt: when, CorrelationID: runID, CommandID: "record-artifact", Actor: statestore.Actor{Type: statestore.ActorSystem, ID: "daemon"}, Data: json.RawMessage(`{"logReference":"attempt-1.log","locator":"C:\\secret\\blob","note":"Authorization: Bearer abc.def"}`), Metadata: json.RawMessage(`{"apiKey":"also-secret"}`)},
		},
		Commands: []statestore.CommandEvidence{{Scope: runID, IdempotencyKey: "token=command-secret", RequestDigest: "sha256:request", Status: "completed", Response: json.RawMessage(`{"secret":"response-secret"}`), CreatedAt: when}},
	}}
	logs := staticLogs{content: map[string][]byte{"attempt-1.log": []byte("safe line\nAuthorization: Bearer log-secret\npassword=hunter2\n")}}
	exporter, err := New(source, logs)
	if err != nil {
		t.Fatal(err)
	}
	exporter.now = func() time.Time { return when }

	content, manifest, err := exporter.Build(context.Background(), runID)
	if err != nil {
		t.Fatal(err)
	}
	if manifest.RunID != runID || manifest.Policy != "default-v1" || len(manifest.Omissions) != 0 {
		t.Fatalf("manifest = %#v", manifest)
	}
	files := readArchive(t, content)
	for _, name := range []string{"manifest.json", "run.json", "events.jsonl", "commands.json", "artifacts/index.json", "logs/attempt-1.log"} {
		if _, ok := files[name]; !ok {
			t.Errorf("archive missing %s", name)
		}
	}
	joined := string(bytes.Join(mapValues(files), nil))
	for _, secret := range []string{"do-not-export", "C:\\secret\\blob", "abc.def", "also-secret", "command-secret", "response-secret", "log-secret", "hunter2"} {
		if strings.Contains(joined, secret) {
			t.Errorf("archive contains secret %q", secret)
		}
	}
	if !strings.Contains(string(files["logs/attempt-1.log"]), "safe line") || !strings.Contains(joined, redactionReplacement) {
		t.Fatalf("redacted archive content = %q", joined)
	}
	for _, entry := range manifest.Entries {
		payload, ok := files[entry.Path]
		if !ok {
			t.Fatalf("manifest entry %s is absent", entry.Path)
		}
		digest := sha256.Sum256(payload)
		if entry.SHA256 != hex.EncodeToString(digest[:]) || entry.Size != int64(len(payload)) {
			t.Errorf("manifest entry %s integrity = %#v", entry.Path, entry)
		}
	}
}

func TestBuildRecordsUnavailableLogWithoutFailing(t *testing.T) {
	t.Parallel()
	runID := "run_01K3Z1C2AAAAAAAAAAAAAAAAAA"
	event := statestore.Event{AggregateType: statestore.AggregateRun, Data: json.RawMessage(`{"logReference":"missing.log"}`), Metadata: json.RawMessage(`{}`)}
	exporter, err := New(staticEvidence{value: statestore.RunEvidence{Run: statestore.RunProjection{RunID: runID}, Events: []statestore.Event{event}}}, staticLogs{})
	if err != nil {
		t.Fatal(err)
	}
	_, manifest, err := exporter.Build(context.Background(), runID)
	if err != nil {
		t.Fatal(err)
	}
	if len(manifest.Omissions) != 1 || manifest.Omissions[0].Reference != "missing.log" || manifest.Omissions[0].Reason != "unavailable" {
		t.Fatalf("omissions = %#v", manifest.Omissions)
	}
}

type staticEvidence struct {
	value statestore.RunEvidence
	err   error
}

func (source staticEvidence) RunEvidence(context.Context, string) (statestore.RunEvidence, error) {
	return source.value, source.err
}

type staticLogs struct{ content map[string][]byte }

func (logs staticLogs) ReadLog(_ context.Context, reference string, offset int64, limit int) (LogChunk, error) {
	content, ok := logs.content[reference]
	if !ok {
		return LogChunk{}, ErrLogNotFound
	}
	if offset > int64(len(content)) {
		return LogChunk{}, errors.New("offset out of range")
	}
	end := offset + int64(limit)
	if end > int64(len(content)) {
		end = int64(len(content))
	}
	return LogChunk{Offset: offset, Size: int64(len(content)), Content: append([]byte(nil), content[offset:end]...)}, nil
}

func readArchive(t *testing.T, content []byte) map[string][]byte {
	t.Helper()
	reader, err := zip.NewReader(bytes.NewReader(content), int64(len(content)))
	if err != nil {
		t.Fatal(err)
	}
	result := make(map[string][]byte)
	for _, file := range reader.File {
		stream, err := file.Open()
		if err != nil {
			t.Fatal(err)
		}
		result[file.Name], err = io.ReadAll(stream)
		_ = stream.Close()
		if err != nil {
			t.Fatal(err)
		}
	}
	return result
}

func mapValues(values map[string][]byte) [][]byte {
	result := make([][]byte, 0, len(values))
	for _, value := range values {
		result = append(result, value)
	}
	return result
}
