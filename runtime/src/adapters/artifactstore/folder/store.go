// Package folder implements the artifact store port with an immutable,
// content-addressed directory.
package folder

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/fdsprod/darkstar/runtime/src/ports"
	"github.com/fdsprod/darkstar/runtime/src/ports/artifactstore"
)

const (
	locatorPrefix    = "sha256:"
	recordVersion    = 1
	DefaultListLimit = 100
	MaxListLimit     = 1000
	maxRecordSize    = 64 * 1024
	directoryMode    = 0o700
	fileMode         = 0o600
	digestHexLength  = sha256.Size * 2
)

// Store keeps immutable blobs below one configured absolute directory. The
// directory layout is adapter-owned; callers retain only opaque locators.
type Store struct {
	root           string
	blobsRoot      string
	metadataRoot   string
	operationsRoot string
	temporaryRoot  string
}

var _ artifactstore.Store = (*Store)(nil)

type metadataRecord struct {
	SchemaVersion int       `json:"schemaVersion"`
	MediaType     string    `json:"mediaType,omitempty"`
	StoredAt      time.Time `json:"storedAt"`
}

type operationRecord struct {
	SchemaVersion int                   `json:"schemaVersion"`
	Locator       artifactstore.Locator `json:"locator"`
}

// ResolveRoot returns the configured absolute root or the project-local MVP
// default. Composition resolves configuration before constructing the adapter.
func ResolveRoot(configuredRoot, projectRoot string) (string, error) {
	if !filepath.IsAbs(projectRoot) {
		return "", fmt.Errorf("project root must be absolute: %q", projectRoot)
	}
	if strings.TrimSpace(configuredRoot) == "" {
		return filepath.Join(filepath.Clean(projectRoot), ".darkstar", "artifacts"), nil
	}
	if !filepath.IsAbs(configuredRoot) {
		return "", fmt.Errorf("configured artifact store root must be absolute: %q", configuredRoot)
	}
	return filepath.Clean(configuredRoot), nil
}

// New opens or creates a folder store rooted at an absolute path.
func New(root string) (*Store, error) {
	if strings.TrimSpace(root) == "" {
		return nil, errors.New("artifact store root is required")
	}
	if !filepath.IsAbs(root) {
		return nil, fmt.Errorf("artifact store root must be absolute: %q", root)
	}
	root = filepath.Clean(root)
	store := &Store{
		root:           root,
		blobsRoot:      filepath.Join(root, "blobs", "sha256"),
		metadataRoot:   filepath.Join(root, "metadata", "sha256"),
		operationsRoot: filepath.Join(root, "operations", "sha256"),
		temporaryRoot:  filepath.Join(root, ".tmp"),
	}
	for _, directory := range []string{store.blobsRoot, store.metadataRoot, store.operationsRoot, store.temporaryRoot} {
		if err := os.MkdirAll(directory, directoryMode); err != nil {
			return nil, fmt.Errorf("create artifact store directory: %w", err)
		}
	}
	return store, nil
}

// Put streams content through SHA-256 into a same-volume temporary file, then
// atomically publishes it. Repeated content shares one blob; repeated operation
// keys must resolve to that same content.
func (s *Store) Put(ctx context.Context, request artifactstore.PutRequest) (artifactstore.Blob, error) {
	if err := s.ready(ctx); err != nil {
		return artifactstore.Blob{}, err
	}
	if strings.TrimSpace(request.IdempotencyKey) == "" {
		return artifactstore.Blob{}, failure(ports.FailureInvalidRequest, "artifact operation idempotency key is required", false)
	}
	if request.Content == nil {
		return artifactstore.Blob{}, failure(ports.FailureInvalidRequest, "artifact content is required", false)
	}
	expectedDigest, err := optionalDigest(request.ExpectedDigest)
	if err != nil {
		return artifactstore.Blob{}, err
	}
	if request.ExpectedSize != nil && *request.ExpectedSize < 0 {
		return artifactstore.Blob{}, failure(ports.FailureInvalidRequest, "expected artifact size cannot be negative", false)
	}
	if request.MaxBytes < 0 {
		return artifactstore.Blob{}, failure(ports.FailureInvalidRequest, "artifact byte limit cannot be negative", false)
	}
	mediaType, err := normalizeMediaType(request.MediaType)
	if err != nil {
		return artifactstore.Blob{}, err
	}

	temporary, err := os.CreateTemp(s.temporaryRoot, "put-*")
	if err != nil {
		return artifactstore.Blob{}, filesystemFailure("create artifact temporary file", err)
	}
	temporaryPath := temporary.Name()
	removeTemporary := true
	defer func() {
		if removeTemporary {
			_ = os.Remove(temporaryPath)
		}
	}()
	if err := temporary.Chmod(fileMode); err != nil {
		_ = temporary.Close()
		return artifactstore.Blob{}, filesystemFailure("protect artifact temporary file", err)
	}

	hasher := sha256.New()
	content := request.Content
	if request.MaxBytes > 0 {
		content = io.LimitReader(content, request.MaxBytes+1)
	}
	size, copyErr := copyWithContext(ctx, io.MultiWriter(temporary, hasher), content)
	if copyErr == nil && request.MaxBytes > 0 && size > request.MaxBytes {
		_ = temporary.Close()
		return artifactstore.Blob{}, failure(ports.FailureResourceExhausted, "artifact content exceeds source byte limit", false)
	}
	if copyErr == nil {
		copyErr = temporary.Sync()
	}
	closeErr := temporary.Close()
	if copyErr != nil {
		if contextErr := contextFailure(ctx); contextErr != nil {
			return artifactstore.Blob{}, contextErr
		}
		return artifactstore.Blob{}, filesystemFailure("write artifact content", copyErr)
	}
	if closeErr != nil {
		return artifactstore.Blob{}, filesystemFailure("close artifact temporary file", closeErr)
	}

	digest := hex.EncodeToString(hasher.Sum(nil))
	if expectedDigest != "" && digest != expectedDigest {
		return artifactstore.Blob{}, failure(ports.FailureInvalidRequest, "artifact content does not match expected digest", false)
	}
	if request.ExpectedSize != nil && size != *request.ExpectedSize {
		return artifactstore.Blob{}, failure(ports.FailureInvalidRequest, "artifact content does not match expected size", false)
	}
	locator := locatorForDigest(digest)

	if operation, found, loadErr := s.loadOperation(request.IdempotencyKey); loadErr != nil {
		return artifactstore.Blob{}, loadErr
	} else if found {
		if operation.Locator != locator {
			return artifactstore.Blob{}, failure(ports.FailureConflict, "artifact operation already stored different content", false)
		}
	}

	blobPath := s.blobPath(digest)
	if err := os.MkdirAll(filepath.Dir(blobPath), directoryMode); err != nil {
		return artifactstore.Blob{}, filesystemFailure("create artifact blob directory", err)
	}
	if err := publishFile(temporaryPath, blobPath); err != nil {
		return artifactstore.Blob{}, filesystemFailure("publish artifact blob", err)
	}
	removeTemporary = false
	if err := verifyFile(ctx, blobPath, digest, size); err != nil {
		return artifactstore.Blob{}, err
	}

	if err := s.ensureOperation(request.IdempotencyKey, operationRecord{
		SchemaVersion: recordVersion,
		Locator:       locator,
	}); err != nil {
		return artifactstore.Blob{}, err
	}
	metadata, err := s.ensureMetadata(digest, metadataRecord{
		SchemaVersion: recordVersion,
		MediaType:     mediaType,
		StoredAt:      time.Now().UTC(),
	})
	if err != nil {
		return artifactstore.Blob{}, err
	}
	return artifactstore.Blob{
		Locator:   locator,
		Digest:    digest,
		Size:      size,
		MediaType: metadata.MediaType,
		StoredAt:  metadata.StoredAt,
	}, nil
}

// Open verifies the stored bytes before returning a reader positioned at the
// beginning of the immutable blob.
func (s *Store) Open(ctx context.Context, request artifactstore.OpenRequest) (io.ReadCloser, error) {
	if err := s.ready(ctx); err != nil {
		return nil, err
	}
	digest, err := digestFromLocator(request.Locator)
	if err != nil {
		return nil, err
	}
	expectedDigest, err := optionalDigest(request.ExpectedDigest)
	if err != nil {
		return nil, err
	}
	if expectedDigest != "" && expectedDigest != digest {
		return nil, failure(ports.FailureInvalidRequest, "artifact locator does not match expected digest", false)
	}
	blob, err := s.Stat(ctx, artifactstore.StatRequest{Locator: request.Locator})
	if err != nil {
		return nil, err
	}
	file, err := os.Open(s.blobPath(digest))
	if errors.Is(err, os.ErrNotExist) {
		return nil, failure(ports.FailureNotFound, "artifact blob was not found", false)
	}
	if err != nil {
		return nil, filesystemFailure("open artifact blob", err)
	}
	if err := verifyOpenFile(ctx, file, digest, blob.Size); err != nil {
		_ = file.Close()
		return nil, err
	}
	return file, nil
}

// Stat returns immutable blob metadata without exposing a backend path.
func (s *Store) Stat(ctx context.Context, request artifactstore.StatRequest) (artifactstore.Blob, error) {
	if err := s.ready(ctx); err != nil {
		return artifactstore.Blob{}, err
	}
	digest, err := digestFromLocator(request.Locator)
	if err != nil {
		return artifactstore.Blob{}, err
	}
	metadata, err := s.readMetadata(digest)
	if err != nil {
		return artifactstore.Blob{}, err
	}
	info, err := os.Stat(s.blobPath(digest))
	if errors.Is(err, os.ErrNotExist) {
		return artifactstore.Blob{}, failure(ports.FailureInternal, "artifact metadata references missing content", false)
	}
	if err != nil {
		return artifactstore.Blob{}, filesystemFailure("inspect artifact blob", err)
	}
	if !info.Mode().IsRegular() {
		return artifactstore.Blob{}, failure(ports.FailureInternal, "artifact content is not a regular file", false)
	}
	return artifactstore.Blob{
		Locator:   request.Locator,
		Digest:    digest,
		Size:      info.Size(),
		MediaType: metadata.MediaType,
		StoredAt:  metadata.StoredAt,
	}, nil
}

// List returns blobs in digest order. NextAfter is an opaque locator suitable
// for the next request.
func (s *Store) List(ctx context.Context, request artifactstore.ListRequest) (artifactstore.Page, error) {
	if err := s.ready(ctx); err != nil {
		return artifactstore.Page{}, err
	}
	limit := request.Limit
	if limit == 0 {
		limit = DefaultListLimit
	}
	if limit < 0 || limit > MaxListLimit {
		return artifactstore.Page{}, failure(ports.FailureInvalidRequest, fmt.Sprintf("artifact list limit must be between 0 and %d", MaxListLimit), false)
	}
	afterDigest := ""
	if request.After != "" {
		var err error
		afterDigest, err = digestFromLocator(artifactstore.Locator(request.After))
		if err != nil {
			return artifactstore.Page{}, err
		}
	}

	digests, err := s.metadataDigests(ctx)
	if err != nil {
		return artifactstore.Page{}, err
	}
	start := sort.SearchStrings(digests, afterDigest)
	for start < len(digests) && digests[start] <= afterDigest {
		start++
	}
	remaining := digests[start:]
	count := len(remaining)
	if count > limit {
		count = limit
	}
	blobs := make([]artifactstore.Blob, 0, count)
	for _, digest := range remaining[:count] {
		if err := contextFailure(ctx); err != nil {
			return artifactstore.Page{}, err
		}
		blob, err := s.Stat(ctx, artifactstore.StatRequest{Locator: locatorForDigest(digest)})
		if err != nil {
			return artifactstore.Page{}, err
		}
		blobs = append(blobs, blob)
	}
	page := artifactstore.Page{Blobs: blobs}
	if count < len(remaining) && count > 0 {
		page.NextAfter = string(blobs[len(blobs)-1].Locator)
	}
	return page, nil
}

func (s *Store) ready(ctx context.Context) error {
	if s == nil || s.root == "" {
		return failure(ports.FailureInternal, "artifact folder store is not initialized", false)
	}
	return contextFailure(ctx)
}

func (s *Store) ensureMetadata(digest string, proposed metadataRecord) (metadataRecord, error) {
	path := s.metadataPath(digest)
	if existing, err := s.readMetadata(digest); err == nil {
		return existing, nil
	} else if !isFailureCode(err, ports.FailureNotFound) {
		return metadataRecord{}, err
	}
	if err := os.MkdirAll(filepath.Dir(path), directoryMode); err != nil {
		return metadataRecord{}, filesystemFailure("create artifact metadata directory", err)
	}
	created, err := s.publishRecord(path, proposed)
	if err != nil {
		return metadataRecord{}, err
	}
	if !created {
		return s.readMetadata(digest)
	}
	return proposed, nil
}

func (s *Store) readMetadata(digest string) (metadataRecord, error) {
	var record metadataRecord
	if err := readRecord(s.metadataPath(digest), &record); errors.Is(err, os.ErrNotExist) {
		return metadataRecord{}, failure(ports.FailureNotFound, "artifact blob was not found", false)
	} else if err != nil {
		var pathError *os.PathError
		if errors.As(err, &pathError) {
			return metadataRecord{}, filesystemFailure("read artifact metadata", err)
		}
		return metadataRecord{}, failure(ports.FailureInternal, "artifact metadata is invalid", false)
	}
	if record.SchemaVersion != recordVersion || record.StoredAt.IsZero() {
		return metadataRecord{}, failure(ports.FailureInternal, "artifact metadata is invalid", false)
	}
	if _, err := normalizeMediaType(record.MediaType); err != nil {
		return metadataRecord{}, failure(ports.FailureInternal, "artifact metadata contains an invalid media type", false)
	}
	return record, nil
}

func (s *Store) loadOperation(key string) (operationRecord, bool, error) {
	var record operationRecord
	if err := readRecord(s.operationPath(key), &record); errors.Is(err, os.ErrNotExist) {
		return operationRecord{}, false, nil
	} else if err != nil {
		var pathError *os.PathError
		if errors.As(err, &pathError) {
			return operationRecord{}, false, filesystemFailure("read artifact operation", err)
		}
		return operationRecord{}, false, failure(ports.FailureInternal, "artifact operation record is invalid", false)
	}
	if record.SchemaVersion != recordVersion {
		return operationRecord{}, false, failure(ports.FailureInternal, "artifact operation record is invalid", false)
	}
	if _, err := digestFromLocator(record.Locator); err != nil {
		return operationRecord{}, false, failure(ports.FailureInternal, "artifact operation locator is invalid", false)
	}
	return record, true, nil
}

func (s *Store) ensureOperation(key string, proposed operationRecord) error {
	path := s.operationPath(key)
	if err := os.MkdirAll(filepath.Dir(path), directoryMode); err != nil {
		return filesystemFailure("create artifact operation directory", err)
	}
	created, err := s.publishRecord(path, proposed)
	if err != nil {
		return err
	}
	if created {
		return nil
	}
	existing, _, err := s.loadOperation(key)
	if err != nil {
		return err
	}
	if existing.Locator != proposed.Locator {
		return failure(ports.FailureConflict, "artifact operation already stored different content", false)
	}
	return nil
}

func (s *Store) publishRecord(path string, value any) (bool, error) {
	content, err := json.Marshal(value)
	if err != nil {
		return false, failure(ports.FailureInternal, "encode artifact store record", false)
	}
	content = append(content, '\n')
	temporary, err := os.CreateTemp(s.temporaryRoot, "record-*")
	if err != nil {
		return false, filesystemFailure("create artifact record temporary file", err)
	}
	temporaryPath := temporary.Name()
	defer func() { _ = os.Remove(temporaryPath) }()
	if err := temporary.Chmod(fileMode); err != nil {
		_ = temporary.Close()
		return false, filesystemFailure("protect artifact store record", err)
	}
	if _, err := temporary.Write(content); err != nil {
		_ = temporary.Close()
		return false, filesystemFailure("write artifact store record", err)
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return false, filesystemFailure("flush artifact store record", err)
	}
	if err := temporary.Close(); err != nil {
		return false, filesystemFailure("close artifact store record", err)
	}
	if err := os.Link(temporaryPath, path); err == nil {
		return true, nil
	} else if errors.Is(err, os.ErrExist) {
		return false, nil
	} else {
		return false, filesystemFailure("publish artifact store record", err)
	}
}

func (s *Store) metadataDigests(ctx context.Context) ([]string, error) {
	entries, err := os.ReadDir(s.metadataRoot)
	if err != nil {
		return nil, filesystemFailure("list artifact metadata", err)
	}
	var digests []string
	for _, shard := range entries {
		if err := contextFailure(ctx); err != nil {
			return nil, err
		}
		if !shard.IsDir() || !validShard(shard.Name()) {
			continue
		}
		files, err := os.ReadDir(filepath.Join(s.metadataRoot, shard.Name()))
		if err != nil {
			return nil, filesystemFailure("list artifact metadata shard", err)
		}
		for _, file := range files {
			if file.IsDir() || filepath.Ext(file.Name()) != ".json" {
				continue
			}
			digest := strings.TrimSuffix(file.Name(), ".json")
			if validDigest(digest) && strings.HasPrefix(digest, shard.Name()) {
				digests = append(digests, digest)
			}
		}
	}
	sort.Strings(digests)
	return digests, nil
}

func (s *Store) blobPath(digest string) string {
	return filepath.Join(s.blobsRoot, digest[:2], digest)
}

func (s *Store) metadataPath(digest string) string {
	return filepath.Join(s.metadataRoot, digest[:2], digest+".json")
}

func (s *Store) operationPath(key string) string {
	sum := sha256.Sum256([]byte(key))
	digest := hex.EncodeToString(sum[:])
	return filepath.Join(s.operationsRoot, digest[:2], digest+".json")
}

func publishFile(temporaryPath, destination string) error {
	if err := os.Link(temporaryPath, destination); err == nil {
		return os.Remove(temporaryPath)
	} else if !errors.Is(err, os.ErrExist) {
		return err
	}
	return os.Remove(temporaryPath)
}

func verifyFile(ctx context.Context, path, digest string, size int64) error {
	file, err := os.Open(path)
	if err != nil {
		return filesystemFailure("open stored artifact for verification", err)
	}
	defer func() { _ = file.Close() }()
	return verifyOpenFile(ctx, file, digest, size)
}

func verifyOpenFile(ctx context.Context, file *os.File, digest string, size int64) error {
	hasher := sha256.New()
	actualSize, err := copyWithContext(ctx, hasher, file)
	if err != nil {
		if contextErr := contextFailure(ctx); contextErr != nil {
			return contextErr
		}
		return filesystemFailure("verify stored artifact", err)
	}
	if actualSize != size || hex.EncodeToString(hasher.Sum(nil)) != digest {
		return failure(ports.FailureInternal, "stored artifact failed integrity verification", false)
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return filesystemFailure("rewind stored artifact", err)
	}
	return nil
}

func copyWithContext(ctx context.Context, destination io.Writer, source io.Reader) (int64, error) {
	buffer := make([]byte, 64*1024)
	var written int64
	for {
		if err := ctx.Err(); err != nil {
			return written, err
		}
		read, readErr := source.Read(buffer)
		if read > 0 {
			count, writeErr := destination.Write(buffer[:read])
			written += int64(count)
			if writeErr != nil {
				return written, writeErr
			}
			if count != read {
				return written, io.ErrShortWrite
			}
		}
		if readErr != nil {
			if errors.Is(readErr, io.EOF) {
				return written, nil
			}
			return written, readErr
		}
		if read == 0 {
			return written, io.ErrNoProgress
		}
	}
}

func readRecord(path string, destination any) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Size() > maxRecordSize {
		return errors.New("artifact store record is not a bounded regular file")
	}
	decoder := json.NewDecoder(file)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("artifact store record contains multiple JSON values")
		}
		return err
	}
	return nil
}

func locatorForDigest(digest string) artifactstore.Locator {
	return artifactstore.Locator(locatorPrefix + digest)
}

func digestFromLocator(locator artifactstore.Locator) (string, error) {
	value := string(locator)
	if !strings.HasPrefix(value, locatorPrefix) {
		return "", failure(ports.FailureInvalidRequest, "artifact locator is invalid", false)
	}
	digest := strings.TrimPrefix(value, locatorPrefix)
	if !validDigest(digest) {
		return "", failure(ports.FailureInvalidRequest, "artifact locator is invalid", false)
	}
	return digest, nil
}

func optionalDigest(value string) (string, error) {
	if value == "" {
		return "", nil
	}
	if !validDigest(value) {
		return "", failure(ports.FailureInvalidRequest, "expected artifact digest must be lowercase SHA-256 hex", false)
	}
	return value, nil
}

func validDigest(value string) bool {
	if len(value) != digestHexLength {
		return false
	}
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == sha256.Size && value == strings.ToLower(value)
}

func validShard(value string) bool {
	if len(value) != 2 || value != strings.ToLower(value) {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func normalizeMediaType(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", nil
	}
	if _, _, err := mime.ParseMediaType(value); err != nil {
		return "", failure(ports.FailureInvalidRequest, "artifact media type is invalid", false)
	}
	return value, nil
}

func contextFailure(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return failure(ports.FailureTimeout, "artifact store operation deadline exceeded", true)
		}
		return failure(ports.FailureCancelled, "artifact store operation cancelled", false)
	}
	return nil
}

func filesystemFailure(message string, err error) *ports.Failure {
	if errors.Is(err, os.ErrPermission) {
		return failure(ports.FailurePermissionDenied, message, false)
	}
	return failure(ports.FailureUnavailable, message, true)
}

func failure(code ports.FailureCode, message string, retryable bool) *ports.Failure {
	return &ports.Failure{Code: code, Message: message, Retryable: retryable}
}

func isFailureCode(err error, code ports.FailureCode) bool {
	var classified *ports.Failure
	return errors.As(err, &classified) && classified.Code == code
}
