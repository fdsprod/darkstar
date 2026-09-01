package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/fdsprod/darkstar/runtime/src/core/runexport"
	"github.com/fdsprod/darkstar/runtime/src/ports/statestore"
)

const (
	eventBatchSize  = 100
	defaultLogLimit = 64 << 10
	maximumLogLimit = 1 << 20
)

var (
	// ErrLogNotFound means that an opaque log reference is unknown.
	ErrLogNotFound = runexport.ErrLogNotFound
	// ErrLogOffsetOutOfRange means that a cursor is beyond the current log size.
	ErrLogOffsetOutOfRange = errors.New("log offset out of range")
	logReferencePattern    = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)
)

// EventSource exposes the authoritative retained event sequence needed by SSE.
type EventSource interface {
	EventsAfter(context.Context, uint64, int) ([]statestore.Event, error)
	EventBounds(context.Context) (statestore.EventBounds, error)
}

// LogSource reads a bounded byte range from an opaque append-only log.
type LogSource = runexport.LogSource

// StreamServices is the complete streaming capability published by the API.
type StreamServices struct {
	Events EventSource
	Logs   LogSource
}

// LogChunk is one snapshot-consistent bounded read.
type LogChunk = runexport.LogChunk

// DirectoryLogs maps opaque single-segment references to protected log files.
// References never contain path separators and are not interpreted as paths.
type DirectoryLogs struct{ root string }

// NewDirectoryLogs constructs a bounded log source rooted at an absolute path.
func NewDirectoryLogs(root string) (*DirectoryLogs, error) {
	if !filepath.IsAbs(root) {
		return nil, fmt.Errorf("log directory must be absolute: %q", root)
	}
	return &DirectoryLogs{root: filepath.Clean(root)}, nil
}

// ReadLog reads at most limit bytes beginning at offset.
func (logs *DirectoryLogs) ReadLog(ctx context.Context, reference string, offset int64, limit int) (LogChunk, error) {
	if err := ctx.Err(); err != nil {
		return LogChunk{}, err
	}
	if !logReferencePattern.MatchString(reference) {
		return LogChunk{}, ErrLogNotFound
	}
	if offset < 0 || limit < 1 || limit > maximumLogLimit {
		return LogChunk{}, errors.New("invalid bounded log read")
	}

	logPath := filepath.Join(logs.root, reference)
	info, err := os.Lstat(logPath)
	if errors.Is(err, os.ErrNotExist) {
		return LogChunk{}, ErrLogNotFound
	}
	if err != nil {
		return LogChunk{}, fmt.Errorf("inspect log reference: %w", err)
	}
	if !info.Mode().IsRegular() {
		return LogChunk{}, ErrLogNotFound
	}
	if offset > info.Size() {
		return LogChunk{}, ErrLogOffsetOutOfRange
	}

	file, err := os.Open(logPath)
	if errors.Is(err, os.ErrNotExist) {
		return LogChunk{}, ErrLogNotFound
	}
	if err != nil {
		return LogChunk{}, fmt.Errorf("open log reference: %w", err)
	}
	defer file.Close()
	if _, err := file.Seek(offset, io.SeekStart); err != nil {
		return LogChunk{}, fmt.Errorf("seek log reference: %w", err)
	}
	remaining := info.Size() - offset
	readSize := int64(limit)
	if remaining < readSize {
		readSize = remaining
	}
	content := make([]byte, readSize)
	if _, err := io.ReadFull(file, content); err != nil {
		return LogChunk{}, fmt.Errorf("read log reference: %w", err)
	}
	return LogChunk{Offset: offset, Size: info.Size(), Content: content}, nil
}

func (s *Server) serveEvents(response http.ResponseWriter, request *http.Request, requestID string) {
	if request.Method != http.MethodGet {
		response.Header().Set("Allow", "GET")
		writeAPIError(response, http.StatusMethodNotAllowed, apiError{
			SchemaVersion: 1, Code: "METHOD_NOT_ALLOWED",
			Message: "The HTTP method is not supported for this resource.", RequestID: requestID,
		})
		return
	}
	cursor, ok := parseEventCursor(request.Header.Values("Last-Event-ID"))
	if !ok {
		writeAPIError(response, http.StatusBadRequest, apiError{
			SchemaVersion: 1, Code: "VALIDATION_FAILED", Message: "Last-Event-ID must be one non-negative decimal position.",
			RequestID: requestID, Details: []errorDetail{{Field: "Last-Event-ID", Code: "INVALID"}},
		})
		return
	}
	s.mu.RLock()
	services := s.streams
	pollInterval := s.streamPollInterval
	keepaliveInterval := s.streamKeepaliveInterval
	s.mu.RUnlock()
	if services == nil {
		writeAPIError(response, http.StatusServiceUnavailable, apiError{
			SchemaVersion: 1, Code: "EVENT_STREAM_UNAVAILABLE", Message: "Event streaming is not configured.",
			RequestID: requestID, Retryable: true,
		})
		return
	}
	bounds, err := services.Events.EventBounds(request.Context())
	if err != nil || !validEventBounds(bounds) {
		writeAPIError(response, http.StatusServiceUnavailable, apiError{
			SchemaVersion: 1, Code: "EVENT_STREAM_UNAVAILABLE", Message: "The retained event range could not be read.",
			RequestID: requestID, Retryable: true,
		})
		return
	}
	if replayUnavailable(cursor, bounds) {
		writeAPIError(response, http.StatusGone, apiError{
			SchemaVersion: 1, Code: "EVENT_REPLAY_UNAVAILABLE", Message: "The requested event position is outside retained online history.",
			RequestID: requestID,
			Details: []errorDetail{
				{Field: "oldestAvailablePosition", Code: "MINIMUM", Message: strconv.FormatUint(bounds.Oldest, 10)},
				{Field: "resync", Code: "USE_PROJECTION", Message: "/api/v1/runs"},
			},
		})
		return
	}
	if cursor > bounds.Latest {
		writeAPIError(response, http.StatusBadRequest, apiError{
			SchemaVersion: 1, Code: "EVENT_CURSOR_INVALID", Message: "Last-Event-ID is newer than the authoritative event log.",
			RequestID: requestID, Details: []errorDetail{{Field: "Last-Event-ID", Code: "OUT_OF_RANGE"}},
		})
		return
	}
	flusher, ok := response.(http.Flusher)
	if !ok {
		writeAPIError(response, http.StatusInternalServerError, apiError{
			SchemaVersion: 1, Code: "EVENT_STREAM_UNAVAILABLE", Message: "The HTTP response does not support streaming.", RequestID: requestID,
		})
		return
	}

	response.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	response.Header().Set("Connection", "keep-alive")
	response.Header().Set("X-Accel-Buffering", "no")
	response.WriteHeader(http.StatusOK)
	flusher.Flush()

	poll := time.NewTicker(pollInterval)
	defer poll.Stop()
	keepalive := time.NewTicker(keepaliveInterval)
	defer keepalive.Stop()
	for {
		events, err := services.Events.EventsAfter(request.Context(), cursor, eventBatchSize)
		if err != nil {
			if request.Context().Err() != nil {
				return
			}
			writeSSEProblem(response, requestID, "EVENT_STREAM_FAILED", "The event stream could not continue.")
			flusher.Flush()
			return
		}
		for _, event := range events {
			if event.GlobalPosition != cursor+1 {
				writeSSEProblem(response, requestID, "EVENT_SEQUENCE_INVALID", "The authoritative event sequence is not contiguous.")
				flusher.Flush()
				return
			}
			if err := writeSSEEvent(response, event); err != nil {
				return
			}
			cursor = event.GlobalPosition
		}
		if len(events) > 0 {
			flusher.Flush()
			if len(events) == eventBatchSize {
				continue
			}
		}

		select {
		case <-request.Context().Done():
			return
		case <-poll.C:
		case <-keepalive.C:
			if _, err := io.WriteString(response, ": keepalive\n\n"); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}

func (s *Server) serveLog(response http.ResponseWriter, request *http.Request, requestID string) {
	if request.Method != http.MethodGet {
		response.Header().Set("Allow", "GET")
		writeAPIError(response, http.StatusMethodNotAllowed, apiError{
			SchemaVersion: 1, Code: "METHOD_NOT_ALLOWED",
			Message: "The HTTP method is not supported for this resource.", RequestID: requestID,
		})
		return
	}
	reference := strings.TrimPrefix(path.Clean(request.URL.Path), "/api/v1/logs/")
	if !logReferencePattern.MatchString(reference) {
		writeAPIError(response, http.StatusNotFound, apiError{
			SchemaVersion: 1, Code: "NOT_FOUND", Message: "The requested log reference was not found.", RequestID: requestID,
		})
		return
	}
	offset, limit, ok := parseLogRange(request)
	if !ok {
		writeAPIError(response, http.StatusBadRequest, apiError{
			SchemaVersion: 1, Code: "VALIDATION_FAILED", Message: "The bounded log range is invalid.", RequestID: requestID,
			Details: []errorDetail{{Field: "after/limit", Code: "INVALID"}},
		})
		return
	}
	s.mu.RLock()
	services := s.streams
	s.mu.RUnlock()
	if services == nil {
		writeAPIError(response, http.StatusServiceUnavailable, apiError{
			SchemaVersion: 1, Code: "LOG_STREAM_UNAVAILABLE", Message: "Log streaming is not configured.",
			RequestID: requestID, Retryable: true,
		})
		return
	}
	chunk, err := services.Logs.ReadLog(request.Context(), reference, offset, limit)
	if errors.Is(err, ErrLogNotFound) {
		writeAPIError(response, http.StatusNotFound, apiError{
			SchemaVersion: 1, Code: "NOT_FOUND", Message: "The requested log reference was not found.", RequestID: requestID,
		})
		return
	}
	if errors.Is(err, ErrLogOffsetOutOfRange) {
		writeAPIError(response, http.StatusRequestedRangeNotSatisfiable, apiError{
			SchemaVersion: 1, Code: "LOG_CURSOR_INVALID", Message: "The log byte cursor is beyond the current log size.", RequestID: requestID,
		})
		return
	}
	if err != nil || !validLogChunk(chunk, offset, limit) {
		writeAPIError(response, http.StatusServiceUnavailable, apiError{
			SchemaVersion: 1, Code: "LOG_STREAM_UNAVAILABLE", Message: "The requested log range could not be read.",
			RequestID: requestID, Retryable: true,
		})
		return
	}
	response.Header().Set("Content-Type", "application/octet-stream")
	response.Header().Set("X-Darkstar-Log-Offset", strconv.FormatInt(chunk.Offset, 10))
	response.Header().Set("X-Darkstar-Log-Next-Offset", strconv.FormatInt(chunk.NextOffset(), 10))
	response.Header().Set("X-Darkstar-Log-Size", strconv.FormatInt(chunk.Size, 10))
	response.Header().Set("X-Darkstar-Log-Complete", strconv.FormatBool(chunk.Complete()))
	response.Header().Set("Content-Length", strconv.Itoa(len(chunk.Content)))
	response.WriteHeader(http.StatusOK)
	_, _ = response.Write(chunk.Content)
}

func parseEventCursor(values []string) (uint64, bool) {
	if len(values) == 0 {
		return 0, true
	}
	if len(values) != 1 || values[0] == "" || strings.TrimSpace(values[0]) != values[0] || strings.HasPrefix(values[0], "+") {
		return 0, false
	}
	position, err := strconv.ParseUint(values[0], 10, 64)
	return position, err == nil
}

func parseLogRange(request *http.Request) (int64, int, bool) {
	query := request.URL.Query()
	if len(query) > 2 || len(query["after"]) > 1 || len(query["limit"]) > 1 {
		return 0, 0, false
	}
	for key := range query {
		if key != "after" && key != "limit" {
			return 0, 0, false
		}
	}
	offset := int64(0)
	if value := query.Get("after"); value != "" {
		parsed, err := strconv.ParseInt(value, 10, 64)
		if err != nil || parsed < 0 {
			return 0, 0, false
		}
		offset = parsed
	}
	limit := defaultLogLimit
	if value := query.Get("limit"); value != "" {
		parsed, err := strconv.Atoi(value)
		if err != nil || parsed < 1 || parsed > maximumLogLimit {
			return 0, 0, false
		}
		limit = parsed
	}
	return offset, limit, true
}

func validEventBounds(bounds statestore.EventBounds) bool {
	return bounds == (statestore.EventBounds{}) || (bounds.Oldest > 0 && bounds.Latest >= bounds.Oldest)
}

func replayUnavailable(cursor uint64, bounds statestore.EventBounds) bool {
	return bounds.Oldest > 1 && cursor < bounds.Oldest-1
}

func validLogChunk(chunk LogChunk, offset int64, limit int) bool {
	return chunk.Offset == offset && chunk.Size >= 0 && chunk.Offset <= chunk.Size && len(chunk.Content) <= limit && int64(len(chunk.Content)) <= chunk.Size-chunk.Offset
}

func writeSSEEvent(response io.Writer, event statestore.Event) error {
	content, err := json.Marshal(event)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(response, "id: %d\nevent: %s\ndata: %s\n\n", event.GlobalPosition, event.Kind, content)
	return err
}

func writeSSEProblem(response io.Writer, requestID, code, message string) {
	content, _ := json.Marshal(apiError{SchemaVersion: 1, Code: code, Message: message, RequestID: requestID, Retryable: true})
	_, _ = fmt.Fprintf(response, "event: problem\ndata: %s\n\n", content)
}

var _ LogSource = (*DirectoryLogs)(nil)
