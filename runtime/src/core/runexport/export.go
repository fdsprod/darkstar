// Package runexport builds portable, default-redacted evidence bundles for one run.
package runexport

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/fdsprod/darkstar/runtime/src/ports/statestore"
)

const (
	redactionReplacement = "[REDACTED]"
	logReadSize          = 1 << 20
	maximumLogSize       = 16 << 20
	maximumBundleSize    = 64 << 20
)

var (
	// ErrLogNotFound means that an opaque log reference is unknown.
	ErrLogNotFound   = errors.New("log reference not found")
	bearerPattern    = regexp.MustCompile(`(?i)(bearer\s+)[A-Za-z0-9._~+/=-]+`)
	valuePattern     = regexp.MustCompile(`(?i)\b(token|password|passphrase|secret|api[_-]?key|authorization|credential)\b(\s*[:=]\s*)([^\s,;]+)`)
	jsonValuePattern = regexp.MustCompile(`(?i)("(?:token|password|passphrase|secret|api[_-]?key|authorization|credential)"\s*:\s*")[^"]*(")`)
)

// EvidenceSource returns one transactionally consistent run snapshot.
type EvidenceSource interface {
	RunEvidence(context.Context, string) (statestore.RunEvidence, error)
}

// LogSource supports bounded reads through an opaque reference.
type LogSource interface {
	ReadLog(context.Context, string, int64, int) (LogChunk, error)
}

// LogChunk is one bounded range from an append-only log.
type LogChunk struct {
	Offset  int64
	Size    int64
	Content []byte
}

// NextOffset returns the first unread byte.
func (chunk LogChunk) NextOffset() int64 { return chunk.Offset + int64(len(chunk.Content)) }

// Complete reports whether this chunk reaches the current end of the log.
func (chunk LogChunk) Complete() bool { return chunk.NextOffset() == chunk.Size }

// Exporter creates finite ZIP archives from durable evidence.
type Exporter struct {
	source EvidenceSource
	logs   LogSource
	now    func() time.Time
}

// New constructs an exporter. Logs may be nil; discovered references are then
// represented as explicit unavailable omissions rather than silently dropped.
func New(source EvidenceSource, logs LogSource) (*Exporter, error) {
	if source == nil {
		return nil, errors.New("run export evidence source is required")
	}
	return &Exporter{source: source, logs: logs, now: time.Now}, nil
}

// Manifest describes every payload entry and every unavailable evidence item.
// manifest.json is not listed because a file cannot contain its own digest.
type Manifest struct {
	SchemaVersion int        `json:"schemaVersion"`
	RunID         string     `json:"runId"`
	ExportedAt    time.Time  `json:"exportedAt"`
	Policy        string     `json:"redactionPolicy"`
	Entries       []Entry    `json:"entries"`
	Omissions     []Omission `json:"omissions"`
}

// Entry is an immutable file included in the archive.
type Entry struct {
	Path      string `json:"path"`
	Kind      string `json:"kind"`
	MediaType string `json:"mediaType"`
	SHA256    string `json:"sha256"`
	Size      int64  `json:"size"`
}

// Omission explicitly records evidence that could not safely be embedded.
type Omission struct {
	Kind      string `json:"kind"`
	Reference string `json:"reference"`
	Reason    string `json:"reason"`
}

type artifactIndex struct {
	SchemaVersion int                 `json:"schemaVersion"`
	Artifacts     []statestore.Event  `json:"artifacts"`
	References    []evidenceReference `json:"references"`
}

type evidenceReference struct {
	Kind  string `json:"kind"`
	Value string `json:"value"`
}

type archiveFile struct {
	path      string
	kind      string
	mediaType string
	content   []byte
}

// Build returns a complete ZIP archive. Every JSON boundary and included log is
// redacted even though upstream contracts already prohibit secret persistence.
func (exporter *Exporter) Build(ctx context.Context, runID string) ([]byte, Manifest, error) {
	if err := ctx.Err(); err != nil {
		return nil, Manifest{}, err
	}
	evidence, err := exporter.source.RunEvidence(ctx, runID)
	if err != nil {
		return nil, Manifest{}, err
	}
	redactedEvents, err := redactEvents(evidence.Events)
	if err != nil {
		return nil, Manifest{}, err
	}
	redactedCommands, err := redactValue(evidence.Commands)
	if err != nil {
		return nil, Manifest{}, fmt.Errorf("redact command evidence: %w", err)
	}
	runJSON, err := marshalDocument(evidence.Run)
	if err != nil {
		return nil, Manifest{}, fmt.Errorf("encode run snapshot: %w", err)
	}
	eventJSONL, err := marshalJSONLines(redactedEvents)
	if err != nil {
		return nil, Manifest{}, fmt.Errorf("encode events: %w", err)
	}
	commandJSON, err := marshalDocument(redactedCommands)
	if err != nil {
		return nil, Manifest{}, fmt.Errorf("encode commands: %w", err)
	}

	artifacts := make([]statestore.Event, 0)
	for _, event := range redactedEvents {
		if event.AggregateType == statestore.AggregateArtifact {
			artifacts = append(artifacts, event)
		}
	}
	references := discoverReferences(redactedEvents)
	indexJSON, err := marshalDocument(artifactIndex{SchemaVersion: 1, Artifacts: artifacts, References: references})
	if err != nil {
		return nil, Manifest{}, fmt.Errorf("encode artifact index: %w", err)
	}

	files := []archiveFile{
		{path: "artifacts/index.json", kind: "artifact_index", mediaType: "application/json", content: indexJSON},
		{path: "commands.json", kind: "command_evidence", mediaType: "application/json", content: commandJSON},
		{path: "events.jsonl", kind: "events", mediaType: "application/x-ndjson", content: eventJSONL},
		{path: "run.json", kind: "run_snapshot", mediaType: "application/json", content: runJSON},
	}
	manifest := Manifest{
		SchemaVersion: 1, RunID: runID, ExportedAt: exporter.now().UTC().Round(0),
		Policy: "default-v1", Entries: make([]Entry, 0), Omissions: make([]Omission, 0),
	}
	for _, reference := range references {
		if reference.Kind != "log" {
			continue
		}
		if exporter.logs == nil {
			manifest.Omissions = append(manifest.Omissions, Omission{Kind: "log", Reference: reference.Value, Reason: "unavailable"})
			continue
		}
		content, readErr := readCompleteLog(ctx, exporter.logs, reference.Value)
		if errors.Is(readErr, ErrLogNotFound) {
			manifest.Omissions = append(manifest.Omissions, Omission{Kind: "log", Reference: reference.Value, Reason: "unavailable"})
			continue
		}
		if readErr != nil {
			return nil, Manifest{}, fmt.Errorf("read log %s: %w", reference.Value, readErr)
		}
		files = append(files, archiveFile{path: "logs/" + reference.Value, kind: "log", mediaType: "text/plain; charset=utf-8", content: redactText(content)})
	}
	sort.Slice(files, func(i, j int) bool { return files[i].path < files[j].path })
	for _, file := range files {
		digest := sha256.Sum256(file.content)
		manifest.Entries = append(manifest.Entries, Entry{
			Path: file.path, Kind: file.kind, MediaType: file.mediaType,
			SHA256: hex.EncodeToString(digest[:]), Size: int64(len(file.content)),
		})
	}
	sort.Slice(manifest.Omissions, func(i, j int) bool {
		return manifest.Omissions[i].Kind+manifest.Omissions[i].Reference < manifest.Omissions[j].Kind+manifest.Omissions[j].Reference
	})
	manifestJSON, err := marshalDocument(manifest)
	if err != nil {
		return nil, Manifest{}, fmt.Errorf("encode manifest: %w", err)
	}
	files = append(files, archiveFile{path: "manifest.json", kind: "manifest", mediaType: "application/json", content: manifestJSON})
	sort.Slice(files, func(i, j int) bool { return files[i].path < files[j].path })
	var totalSize int64
	for _, file := range files {
		totalSize += int64(len(file.content))
	}
	if totalSize > maximumBundleSize {
		return nil, Manifest{}, fmt.Errorf("run export exceeds limit of %d bytes", maximumBundleSize)
	}

	var destination bytes.Buffer
	archive := zip.NewWriter(&destination)
	for _, file := range files {
		header := &zip.FileHeader{Name: file.path, Method: zip.Deflate}
		header.SetModTime(time.Date(1980, time.January, 1, 0, 0, 0, 0, time.UTC))
		writer, createErr := archive.CreateHeader(header)
		if createErr != nil {
			return nil, Manifest{}, fmt.Errorf("create archive entry %s: %w", file.path, createErr)
		}
		if _, writeErr := writer.Write(file.content); writeErr != nil {
			return nil, Manifest{}, fmt.Errorf("write archive entry %s: %w", file.path, writeErr)
		}
	}
	if err := archive.Close(); err != nil {
		return nil, Manifest{}, fmt.Errorf("finish archive: %w", err)
	}
	return destination.Bytes(), manifest, nil
}

func readCompleteLog(ctx context.Context, source LogSource, reference string) ([]byte, error) {
	var content []byte
	var offset int64
	for {
		chunk, err := source.ReadLog(ctx, reference, offset, logReadSize)
		if err != nil {
			return nil, err
		}
		if chunk.Offset != offset || chunk.Size < chunk.NextOffset() || (len(chunk.Content) == 0 && !chunk.Complete()) {
			return nil, errors.New("log source returned an invalid chunk")
		}
		if chunk.Size > maximumLogSize {
			return nil, fmt.Errorf("log exceeds export limit of %d bytes", maximumLogSize)
		}
		content = append(content, chunk.Content...)
		offset = chunk.NextOffset()
		if chunk.Complete() {
			return content, nil
		}
	}
}

func redactEvents(events []statestore.Event) ([]statestore.Event, error) {
	result := make([]statestore.Event, len(events))
	for index, event := range events {
		result[index] = event
		var err error
		result[index].Data, err = redactJSON(event.Data)
		if err != nil {
			return nil, fmt.Errorf("redact event %s data: %w", event.ID, err)
		}
		result[index].Metadata, err = redactJSON(event.Metadata)
		if err != nil {
			return nil, fmt.Errorf("redact event %s metadata: %w", event.ID, err)
		}
		result[index].Actor.ID = redactString(result[index].Actor.ID)
		result[index].CommandID = redactString(result[index].CommandID)
	}
	return result, nil
}

func redactValue(value any) (any, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	redacted, err := redactJSON(encoded)
	if err != nil {
		return nil, err
	}
	var result any
	decoder := json.NewDecoder(bytes.NewReader(redacted))
	decoder.UseNumber()
	if err := decoder.Decode(&result); err != nil {
		return nil, err
	}
	return result, nil
}

func redactJSON(document []byte) (json.RawMessage, error) {
	var value any
	decoder := json.NewDecoder(bytes.NewReader(document))
	decoder.UseNumber()
	if err := decoder.Decode(&value); err != nil {
		return nil, err
	}
	value = redactNode(value)
	encoded, err := json.Marshal(value)
	return json.RawMessage(encoded), err
}

func redactNode(value any) any {
	switch current := value.(type) {
	case map[string]any:
		for key, item := range current {
			if sensitiveKey(key) {
				current[key] = redactionReplacement
			} else {
				current[key] = redactNode(item)
			}
		}
		return current
	case []any:
		for index := range current {
			current[index] = redactNode(current[index])
		}
		return current
	case string:
		return redactString(current)
	default:
		return current
	}
}

func sensitiveKey(key string) bool {
	normalized := strings.NewReplacer("_", "", "-", "").Replace(strings.ToLower(key))
	switch normalized {
	case "authorization", "cookie", "setcookie", "password", "passphrase", "secret", "token", "accesstoken", "refreshtoken", "apikey", "credential", "credentials", "clientsecret", "privatekey", "locator", "opaquerefciphertext":
		return true
	default:
		return strings.HasSuffix(normalized, "password") || strings.HasSuffix(normalized, "secret") || strings.HasSuffix(normalized, "token") || strings.HasSuffix(normalized, "apikey")
	}
}

func redactText(content []byte) []byte { return []byte(redactString(string(content))) }

func redactString(value string) string {
	value = jsonValuePattern.ReplaceAllString(value, `${1}`+redactionReplacement+`${2}`)
	value = bearerPattern.ReplaceAllString(value, `${1}`+redactionReplacement)
	return valuePattern.ReplaceAllString(value, `${1}${2}`+redactionReplacement)
}

func discoverReferences(events []statestore.Event) []evidenceReference {
	seen := make(map[string]evidenceReference)
	for _, event := range events {
		for _, document := range []json.RawMessage{event.Data, event.Metadata} {
			var value any
			if json.Unmarshal(document, &value) == nil {
				walkReferences(value, seen)
			}
		}
	}
	result := make([]evidenceReference, 0, len(seen))
	for _, reference := range seen {
		result = append(result, reference)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Kind+result[i].Value < result[j].Kind+result[j].Value })
	return result
}

func walkReferences(value any, found map[string]evidenceReference) {
	switch current := value.(type) {
	case map[string]any:
		for key, item := range current {
			if text, ok := item.(string); ok && text != "" && text != redactionReplacement {
				normalized := strings.NewReplacer("_", "", "-", "").Replace(strings.ToLower(key))
				kind := ""
				switch {
				case normalized == "logref" || normalized == "logreference" || strings.HasSuffix(normalized, "logreference"):
					kind = "log"
				case normalized == "artifactref" || normalized == "artifactreference" || strings.HasSuffix(normalized, "artifactreference"):
					kind = "artifact"
				}
				if kind != "" {
					found[kind+"\x00"+text] = evidenceReference{Kind: kind, Value: text}
				}
			}
			walkReferences(item, found)
		}
	case []any:
		for _, item := range current {
			walkReferences(item, found)
		}
	}
}

func marshalDocument(value any) ([]byte, error) {
	content, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(content, '\n'), nil
}

func marshalJSONLines(values []statestore.Event) ([]byte, error) {
	var destination bytes.Buffer
	for _, value := range values {
		line, err := json.Marshal(value)
		if err != nil {
			return nil, err
		}
		if _, err := destination.Write(append(line, '\n')); err != nil && !errors.Is(err, io.EOF) {
			return nil, err
		}
	}
	return destination.Bytes(), nil
}
