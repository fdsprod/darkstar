package localconnection

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"darkstar/src/ports"
	"darkstar/src/ports/trackerconnection"
)

var connectionIdentity = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]{0,79}$`)

type Registry struct {
	root string
}

func NewRegistry(root string) (*Registry, error) {
	if !filepath.IsAbs(root) {
		return nil, safeFailure(ports.FailureInvalidRequest, "tracker registry requires an absolute directory")
	}
	return &Registry{root: filepath.Clean(root)}, nil
}

func ValidateConnectionIdentity(version int, id, revision string) error {
	if version != 1 || !connectionIdentity.MatchString(id) || !connectionIdentity.MatchString(revision) {
		return safeFailure(ports.FailureInvalidRequest, "schemaVersion 1 and safe nonempty connection/revision identifiers are required")
	}
	return nil
}

func connectionFilename(id, revision string) string {
	digest := sha256.Sum256([]byte(id + "\x00" + revision))
	return hex.EncodeToString(digest[:]) + ".connection"
}

func (r *Registry) Get(ctx context.Context, id, revision string) (trackerconnection.Record, error) {
	if err := ValidateConnectionIdentity(1, id, revision); err != nil {
		return trackerconnection.Record{}, err
	}
	if err := ctx.Err(); err != nil {
		return trackerconnection.Record{}, err
	}
	path := filepath.Join(r.root, connectionFilename(id, revision))
	if _, err := os.Lstat(path); errors.Is(err, os.ErrNotExist) {
		return trackerconnection.Record{}, safeFailure(ports.FailureNotFound, "tracker connection revision is not configured")
	}
	content, err := boundedRead(path, 64<<10)
	if err != nil {
		return trackerconnection.Record{}, safeFailure(ports.FailureUnavailable, "tracker connection revision is unavailable")
	}
	var record trackerconnection.Record
	if json.Unmarshal(content, &record) != nil || record.ConnectionID != id || record.Revision != revision {
		return trackerconnection.Record{}, safeFailure(ports.FailureProtocolDrift, "stored tracker connection revision is invalid")
	}
	return record, nil
}

func (r *Registry) Publish(ctx context.Context, record trackerconnection.Record) (trackerconnection.Record, error) {
	if err := ValidateConnectionIdentity(record.SchemaVersion, record.ConnectionID, record.Revision); err != nil {
		return trackerconnection.Record{}, err
	}
	if err := ctx.Err(); err != nil {
		return trackerconnection.Record{}, err
	}
	content, err := json.Marshal(record)
	if err != nil || len(content) > 64<<10 {
		return trackerconnection.Record{}, safeFailure(ports.FailureInvalidRequest, "tracker connection cannot be encoded")
	}
	if err := os.MkdirAll(r.root, 0o700); err != nil {
		return trackerconnection.Record{}, safeFailure(ports.FailureUnavailable, "tracker registry is unavailable")
	}
	file, err := os.CreateTemp(r.root, ".connection-")
	if err != nil {
		return trackerconnection.Record{}, safeFailure(ports.FailureUnavailable, "tracker registry is unavailable")
	}
	temporary := file.Name()
	defer func() {
		_ = os.Remove(temporary)
	}()
	if err := protectStoredFile(file); err != nil {
		_ = file.Close()
		return trackerconnection.Record{}, safeFailure(ports.FailureUnavailable, "tracker connection could not be protected")
	}
	if err := writeAndClose(file, content); err != nil {
		return trackerconnection.Record{}, safeFailure(ports.FailureUnavailable, "tracker connection could not be retained")
	}
	destination := filepath.Join(r.root, connectionFilename(record.ConnectionID, record.Revision))
	if err := os.Link(temporary, destination); err != nil {
		if !errors.Is(err, os.ErrExist) {
			return trackerconnection.Record{}, safeFailure(ports.FailureUnavailable, "tracker connection could not be published")
		}
		previous, readErr := boundedRead(destination, 64<<10)
		if readErr != nil || !bytes.Equal(previous, content) {
			return trackerconnection.Record{}, safeFailure(ports.FailureConflict, "connection revision already exists; choose a new revision")
		}
	}
	if err := syncStorageDirectory(r.root); err != nil {
		return trackerconnection.Record{}, safeFailure(ports.FailureUnavailable, "tracker registry could not be synchronized")
	}
	return r.Get(ctx, record.ConnectionID, record.Revision)
}

func (r *Registry) List(ctx context.Context) ([]trackerconnection.Record, error) {
	entries, err := os.ReadDir(r.root)
	if errors.Is(err, os.ErrNotExist) {
		return []trackerconnection.Record{}, nil
	}
	if err != nil {
		return nil, safeFailure(ports.FailureUnavailable, "tracker registry is unavailable")
	}
	result := []trackerconnection.Record{}
	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), ".connection") {
			continue
		}
		content, err := boundedRead(filepath.Join(r.root, entry.Name()), 64<<10)
		var record trackerconnection.Record
		if err != nil || json.Unmarshal(content, &record) != nil || connectionFilename(record.ConnectionID, record.Revision) != entry.Name() {
			return nil, safeFailure(ports.FailureProtocolDrift, "tracker registry contains an invalid revision")
		}
		verified, err := r.Get(ctx, record.ConnectionID, record.Revision)
		if err != nil {
			return nil, err
		}
		result = append(result, verified)
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].ConnectionID != result[j].ConnectionID {
			return result[i].ConnectionID < result[j].ConnectionID
		}
		return result[i].Revision < result[j].Revision
	})
	return result, nil
}
