package api

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/fdsprod/darkstar/runtime/src/ports/statestore"
)

func TestEventStreamReplaysStrictlyAfterCursorAndReconnectsWithoutLoss(t *testing.T) {
	t.Parallel()

	source := &memoryEventSource{events: []statestore.Event{streamEvent(1), streamEvent(2), streamEvent(3)}}
	server, endpoint := startStreamingServer(t, source, &memoryLogSource{})
	defer closeTestServer(t, server)

	firstContext, cancelFirst := context.WithCancel(context.Background())
	first := openEventStream(t, firstContext, endpoint, "1")
	reader := bufio.NewReader(first.Body)
	second := readSSEMessage(t, reader)
	third := readSSEMessage(t, reader)
	if second.ID != "2" || second.Event != "run.event_2" || third.ID != "3" || third.Event != "run.event_3" {
		t.Fatalf("replayed messages = %#v, %#v", second, third)
	}
	var envelope statestore.Event
	if err := json.Unmarshal([]byte(second.Data), &envelope); err != nil {
		t.Fatalf("decode SSE event envelope: %v", err)
	}
	if envelope.GlobalPosition != 2 || envelope.AggregateID == "" || !strings.Contains(second.Data, `"globalPosition":2`) {
		t.Fatalf("SSE envelope = %#v, data = %s", envelope, second.Data)
	}
	cancelFirst()
	_ = first.Body.Close()

	secondContext, cancelSecond := context.WithCancel(context.Background())
	defer cancelSecond()
	reconnected := openEventStream(t, secondContext, endpoint, "2")
	defer func() {
		_ = reconnected.Body.Close()
	}()
	message := readSSEMessage(t, bufio.NewReader(reconnected.Body))
	if message.ID != "3" {
		t.Fatalf("reconnected message ID = %q, want 3", message.ID)
	}
}

func TestEventStreamDeliversEventsCommittedAfterConnection(t *testing.T) {
	t.Parallel()

	source := &memoryEventSource{events: []statestore.Event{streamEvent(1)}}
	server, endpoint := startStreamingServer(t, source, &memoryLogSource{})
	defer closeTestServer(t, server)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	response := openEventStream(t, ctx, endpoint, "1")
	defer func() {
		_ = response.Body.Close()
	}()
	source.append(streamEvent(2))
	message := readSSEMessage(t, bufio.NewReader(response.Body))
	if message.ID != "2" {
		t.Fatalf("live message ID = %q, want 2", message.ID)
	}
}

func TestEventStreamRejectsUnavailableAndInvalidCursors(t *testing.T) {
	t.Parallel()

	source := &memoryEventSource{events: []statestore.Event{streamEvent(5), streamEvent(6)}}
	server, endpoint := startStreamingServer(t, source, &memoryLogSource{})
	defer closeTestServer(t, server)

	unavailable := eventRequest(t, endpoint, "2")
	defer func() {
		_ = unavailable.Body.Close()
	}()
	problem := assertAPIError(t, unavailable, http.StatusGone, "EVENT_REPLAY_UNAVAILABLE")
	if len(problem.Details) != 2 || problem.Details[0].Message != "5" {
		t.Fatalf("replay problem details = %#v", problem.Details)
	}

	invalid := eventRequest(t, endpoint, "7")
	defer func() {
		_ = invalid.Body.Close()
	}()
	assertAPIError(t, invalid, http.StatusBadRequest, "EVENT_CURSOR_INVALID")

	malformed := eventRequest(t, endpoint, "not-a-position")
	defer func() {
		_ = malformed.Body.Close()
	}()
	assertAPIError(t, malformed, http.StatusBadRequest, "VALIDATION_FAILED")
}

func TestStreamingEndpointsRequireAuthenticationAndGET(t *testing.T) {
	t.Parallel()

	server, endpoint := startStreamingServer(t, &memoryEventSource{}, &memoryLogSource{})
	defer closeTestServer(t, server)

	unauthorized := get(t, endpoint.BaseURL()+"/api/v1/events", "")
	defer func() {
		_ = unauthorized.Body.Close()
	}()
	assertAPIError(t, unauthorized, http.StatusUnauthorized, "UNAUTHENTICATED")

	request, err := http.NewRequest(http.MethodPost, endpoint.BaseURL()+"/api/v1/events", nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", endpoint.AuthorizationHeader())
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = response.Body.Close()
	}()
	assertAPIError(t, response, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED")
}

func TestLogStreamReadsBoundedChunksByOpaqueReference(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "attempt_01.log"), []byte("alpha\nbeta\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	logs, err := NewDirectoryLogs(root)
	if err != nil {
		t.Fatal(err)
	}
	server, endpoint := startStreamingServer(t, &memoryEventSource{}, logs)
	defer closeTestServer(t, server)

	first := get(t, endpoint.BaseURL()+"/api/v1/logs/attempt_01.log?limit=5", endpoint.AuthorizationHeader())
	defer func() {
		_ = first.Body.Close()
	}()
	if first.StatusCode != http.StatusOK {
		t.Fatalf("first log status = %d", first.StatusCode)
	}
	content, err := io.ReadAll(first.Body)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "alpha" || first.Header.Get("X-Darkstar-Log-Next-Offset") != "5" || first.Header.Get("X-Darkstar-Log-Complete") != "false" {
		t.Fatalf("first log chunk = %q headers=%v", content, first.Header)
	}

	second := get(t, endpoint.BaseURL()+"/api/v1/logs/attempt_01.log?after=5&limit=6", endpoint.AuthorizationHeader())
	defer func() {
		_ = second.Body.Close()
	}()
	content, err = io.ReadAll(second.Body)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "\nbeta\n" || second.Header.Get("X-Darkstar-Log-Next-Offset") != "11" || second.Header.Get("X-Darkstar-Log-Complete") != "true" {
		t.Fatalf("second log chunk = %q headers=%v", content, second.Header)
	}
}

func TestLogStreamRejectsTraversalUnknownQueriesAndOutOfRangeCursors(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "known.log"), []byte("safe"), 0o600); err != nil {
		t.Fatal(err)
	}
	logs, err := NewDirectoryLogs(root)
	if err != nil {
		t.Fatal(err)
	}
	server, endpoint := startStreamingServer(t, &memoryEventSource{}, logs)
	defer closeTestServer(t, server)

	outOfRange := get(t, endpoint.BaseURL()+"/api/v1/logs/known.log?after=5", endpoint.AuthorizationHeader())
	defer func() {
		_ = outOfRange.Body.Close()
	}()
	assertAPIError(t, outOfRange, http.StatusRequestedRangeNotSatisfiable, "LOG_CURSOR_INVALID")

	unknownQuery := get(t, endpoint.BaseURL()+"/api/v1/logs/known.log?follow=true", endpoint.AuthorizationHeader())
	defer func() {
		_ = unknownQuery.Body.Close()
	}()
	assertAPIError(t, unknownQuery, http.StatusBadRequest, "VALIDATION_FAILED")

	missing := get(t, endpoint.BaseURL()+"/api/v1/logs/not-found.log", endpoint.AuthorizationHeader())
	defer func() {
		_ = missing.Body.Close()
	}()
	assertAPIError(t, missing, http.StatusNotFound, "NOT_FOUND")

	if _, err := logs.ReadLog(context.Background(), "../outside.log", 0, 10); !errors.Is(err, ErrLogNotFound) {
		t.Fatalf("traversal read error = %v, want ErrLogNotFound", err)
	}
}

func startStreamingServer(t *testing.T, events EventSource, logs LogSource) (*Server, Endpoint) {
	t.Helper()
	server, err := NewServer(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	server.streamPollInterval = 5 * time.Millisecond
	server.streamKeepaliveInterval = 100 * time.Millisecond
	if err := server.SetStreams(StreamServices{Events: events, Logs: logs}); err != nil {
		t.Fatal(err)
	}
	if err := server.Start(context.Background(), os.Getpid(), time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	endpoint, err := ReadEndpoint(filepath.Dir(server.endpointPath))
	if err != nil {
		t.Fatal(err)
	}
	return server, endpoint
}

func openEventStream(t *testing.T, ctx context.Context, endpoint Endpoint, cursor string) *http.Response {
	t.Helper()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.BaseURL()+"/api/v1/events", nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", endpoint.AuthorizationHeader())
	if cursor != "" {
		request.Header.Set("Last-Event-ID", cursor)
	}
	client := &http.Client{Timeout: 5 * time.Second}
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusOK {
		defer func() {
			_ = response.Body.Close()
		}()
		content, _ := io.ReadAll(response.Body)
		t.Fatalf("event stream status = %d body=%s", response.StatusCode, content)
	}
	if contentType := response.Header.Get("Content-Type"); !strings.HasPrefix(contentType, "text/event-stream") {
		t.Fatalf("event stream Content-Type = %q", contentType)
	}
	return response
}

func eventRequest(t *testing.T, endpoint Endpoint, cursor string) *http.Response {
	t.Helper()
	request, err := http.NewRequest(http.MethodGet, endpoint.BaseURL()+"/api/v1/events", nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", endpoint.AuthorizationHeader())
	request.Header.Set("Last-Event-ID", cursor)
	response, err := (&http.Client{Timeout: 5 * time.Second}).Do(request)
	if err != nil {
		t.Fatal(err)
	}
	return response
}

type sseMessage struct {
	ID    string
	Event string
	Data  string
}

func readSSEMessage(t *testing.T, reader *bufio.Reader) sseMessage {
	t.Helper()
	var message sseMessage
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			t.Fatalf("read SSE message: %v", err)
		}
		line = strings.TrimSuffix(line, "\n")
		line = strings.TrimSuffix(line, "\r")
		if line == "" {
			if message.Event != "" || message.Data != "" || message.ID != "" {
				return message
			}
			continue
		}
		switch {
		case strings.HasPrefix(line, "id: "):
			message.ID = strings.TrimPrefix(line, "id: ")
		case strings.HasPrefix(line, "event: "):
			message.Event = strings.TrimPrefix(line, "event: ")
		case strings.HasPrefix(line, "data: "):
			message.Data = strings.TrimPrefix(line, "data: ")
		}
	}
}

type memoryEventSource struct {
	mu     sync.RWMutex
	events []statestore.Event
}

func (source *memoryEventSource) EventsAfter(ctx context.Context, position uint64, limit int) ([]statestore.Event, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	source.mu.RLock()
	defer source.mu.RUnlock()
	result := make([]statestore.Event, 0, limit)
	for _, event := range source.events {
		if event.GlobalPosition > position {
			result = append(result, event)
			if len(result) == limit {
				break
			}
		}
	}
	return result, nil
}

func (source *memoryEventSource) EventBounds(ctx context.Context) (statestore.EventBounds, error) {
	if err := ctx.Err(); err != nil {
		return statestore.EventBounds{}, err
	}
	source.mu.RLock()
	defer source.mu.RUnlock()
	if len(source.events) == 0 {
		return statestore.EventBounds{}, nil
	}
	return statestore.EventBounds{Oldest: source.events[0].GlobalPosition, Latest: source.events[len(source.events)-1].GlobalPosition}, nil
}

func (source *memoryEventSource) append(event statestore.Event) {
	source.mu.Lock()
	defer source.mu.Unlock()
	source.events = append(source.events, event)
}

type memoryLogSource struct{}

func (*memoryLogSource) ReadLog(context.Context, string, int64, int) (LogChunk, error) {
	return LogChunk{}, ErrLogNotFound
}

func streamEvent(position uint64) statestore.Event {
	when := time.Date(2026, time.August, 31, 20, 0, int(position), 0, time.UTC)
	return statestore.Event{
		SchemaVersion: 1, ID: "event_" + strconv.FormatUint(position, 10), GlobalPosition: position,
		AggregateType: statestore.AggregateRun, AggregateID: "run_01K3Z1C2AAAAAAAAAAAAAAAAAA", AggregateRevision: position,
		Kind: "run.event_" + strconv.FormatUint(position, 10), OccurredAt: when, RecordedAt: when,
		CorrelationID: "run_01K3Z1C2AAAAAAAAAAAAAAAAAA", CommandID: "command-" + strconv.FormatUint(position, 10),
		Actor: statestore.Actor{Type: statestore.ActorSystem, ID: "test"}, Data: json.RawMessage(`{"position":` + strconv.FormatUint(position, 10) + `}`), Metadata: json.RawMessage(`{}`),
	}
}
