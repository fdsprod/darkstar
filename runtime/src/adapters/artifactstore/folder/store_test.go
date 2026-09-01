package folder

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/fdsprod/darkstar/runtime/src/ports"
	"github.com/fdsprod/darkstar/runtime/src/ports/artifactstore"
)

func TestNewRequiresAbsoluteRoot(t *testing.T) {
	t.Parallel()
	if _, err := New("relative/artifacts"); err == nil {
		t.Fatal("New() error = nil, want relative-path rejection")
	}
}

func TestResolveRootUsesProjectDefaultOrAbsoluteConfiguration(t *testing.T) {
	t.Parallel()
	project := t.TempDir()
	root, err := ResolveRoot("", project)
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(project, ".darkstar", "artifacts"); root != want {
		t.Fatalf("default root = %q, want %q", root, want)
	}
	configured := filepath.Join(t.TempDir(), "external-artifacts")
	root, err = ResolveRoot(configured, project)
	if err != nil || root != configured {
		t.Fatalf("configured root = %q, %v, want %q", root, err, configured)
	}
	if _, err := ResolveRoot("relative", project); err == nil {
		t.Fatal("ResolveRoot() error = nil, want relative configured-root rejection")
	}
}

func TestPutOpenAndStatArbitraryBinaryContent(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	store := newStore(t, root)
	content := []byte{0x00, 0xff, 0x10, 0x00, 0x80}
	digest := digestOf(content)
	size := int64(len(content))

	blob, err := store.Put(context.Background(), artifactstore.PutRequest{
		IdempotencyKey: "operation-binary",
		Content:        bytes.NewReader(content),
		ExpectedDigest: digest,
		ExpectedSize:   &size,
		MediaType:      "application/octet-stream",
	})
	if err != nil {
		t.Fatal(err)
	}
	if blob.Digest != digest || blob.Size != size || blob.MediaType != "application/octet-stream" || blob.StoredAt.IsZero() {
		t.Fatalf("Put() blob = %#v", blob)
	}
	if got, want := blob.Locator, artifactstore.Locator("sha256:"+digest); got != want {
		t.Fatalf("locator = %q, want %q", got, want)
	}
	if strings.Contains(string(blob.Locator), root) {
		t.Fatalf("locator %q exposes configured root", blob.Locator)
	}

	reader, err := store.Open(context.Background(), artifactstore.OpenRequest{Locator: blob.Locator, ExpectedDigest: digest})
	if err != nil {
		t.Fatal(err)
	}
	opened, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	if err := reader.Close(); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(opened, content) {
		t.Fatalf("Open() content = %v, want %v", opened, content)
	}

	stat, err := store.Stat(context.Background(), artifactstore.StatRequest{Locator: blob.Locator})
	if err != nil {
		t.Fatal(err)
	}
	if stat != blob {
		t.Fatalf("Stat() = %#v, want %#v", stat, blob)
	}
}

func TestPutRejectsOversizeContentWithoutPublishingPartialBlob(t *testing.T) {
	t.Parallel()
	store := newStore(t, t.TempDir())
	_, err := store.Put(context.Background(), artifactstore.PutRequest{
		IdempotencyKey: "operation-oversize", Content: strings.NewReader("12345"), MaxBytes: 4,
		MediaType: "text/plain",
	})
	var failure *ports.Failure
	if !errors.As(err, &failure) || failure.Code != ports.FailureResourceExhausted {
		t.Fatalf("Put() error = %v", err)
	}
	page, err := store.List(context.Background(), artifactstore.ListRequest{})
	if err != nil || len(page.Blobs) != 0 {
		t.Fatalf("List() after rejection = %#v, %v", page, err)
	}
}

func TestPutDeduplicatesContentAndPersistsFirstBlobMetadata(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	store := newStore(t, root)
	content := []byte("same bytes")
	first := put(t, store, "operation-one", content, "text/plain")
	second := put(t, store, "operation-two", content, "application/octet-stream")
	if second != first {
		t.Fatalf("deduplicated blob = %#v, want %#v", second, first)
	}

	reopened := newStore(t, root)
	stat, err := reopened.Stat(context.Background(), artifactstore.StatRequest{Locator: first.Locator})
	if err != nil {
		t.Fatal(err)
	}
	if stat != first {
		t.Fatalf("reopened Stat() = %#v, want %#v", stat, first)
	}
	if got := countRegularFiles(t, filepath.Join(root, "blobs")); got != 1 {
		t.Fatalf("blob file count = %d, want 1", got)
	}
}

func TestPutRejectsIdempotencyConflict(t *testing.T) {
	t.Parallel()
	store := newStore(t, t.TempDir())
	first := put(t, store, "one-operation", []byte("first"), "text/plain")
	_, err := store.Put(context.Background(), artifactstore.PutRequest{
		IdempotencyKey: "one-operation",
		Content:        strings.NewReader("second"),
		MediaType:      "text/plain",
	})
	assertFailureCode(t, err, ports.FailureConflict)
	reader, err := store.Open(context.Background(), artifactstore.OpenRequest{Locator: first.Locator})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reader.Close() }()
	content, err := io.ReadAll(reader)
	if err != nil || string(content) != "first" {
		t.Fatalf("original content = %q, %v", content, err)
	}
}

func TestPutIntegrityFailurePublishesNothing(t *testing.T) {
	t.Parallel()
	store := newStore(t, t.TempDir())
	wrongSize := int64(99)
	tests := []artifactstore.PutRequest{
		{IdempotencyKey: "bad-digest", Content: strings.NewReader("content"), ExpectedDigest: strings.Repeat("0", 64)},
		{IdempotencyKey: "bad-size", Content: strings.NewReader("content"), ExpectedSize: &wrongSize},
	}
	for _, request := range tests {
		if _, err := store.Put(context.Background(), request); err == nil {
			t.Fatal("Put() error = nil, want integrity failure")
		}
	}
	page, err := store.List(context.Background(), artifactstore.ListRequest{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Blobs) != 0 {
		t.Fatalf("List() = %#v, want empty", page)
	}
}

func TestPutReaderFailurePublishesNothing(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	store := newStore(t, root)
	_, err := store.Put(context.Background(), artifactstore.PutRequest{
		IdempotencyKey: "reader-failure",
		Content:        io.MultiReader(strings.NewReader("partial"), failingReader{}),
	})
	if err == nil {
		t.Fatal("Put() error = nil, want reader failure")
	}
	page, listErr := store.List(context.Background(), artifactstore.ListRequest{Limit: 10})
	if listErr != nil || len(page.Blobs) != 0 {
		t.Fatalf("List() = %#v, %v, want empty", page, listErr)
	}
	if got := countRegularFiles(t, filepath.Join(root, ".tmp")); got != 0 {
		t.Fatalf("temporary file count = %d, want 0", got)
	}
}

func TestListIsDeterministicAndPaginated(t *testing.T) {
	t.Parallel()
	store := newStore(t, t.TempDir())
	for _, value := range []string{"charlie", "alpha", "bravo"} {
		put(t, store, "put-"+value, []byte(value), "text/plain")
	}
	first, err := store.List(context.Background(), artifactstore.ListRequest{Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Blobs) != 2 || first.NextAfter == "" {
		t.Fatalf("first page = %#v", first)
	}
	if first.Blobs[0].Digest >= first.Blobs[1].Digest {
		t.Fatalf("first page is not sorted: %#v", first.Blobs)
	}
	second, err := store.List(context.Background(), artifactstore.ListRequest{Limit: 2, After: first.NextAfter})
	if err != nil {
		t.Fatal(err)
	}
	if len(second.Blobs) != 1 || second.NextAfter != "" || second.Blobs[0].Digest <= first.Blobs[1].Digest {
		t.Fatalf("second page = %#v", second)
	}
}

func TestListUsesDefaultLimit(t *testing.T) {
	t.Parallel()
	store := newStore(t, t.TempDir())
	put(t, store, "default-list", []byte("content"), "text/plain")
	page, err := store.List(context.Background(), artifactstore.ListRequest{})
	if err != nil || len(page.Blobs) != 1 {
		t.Fatalf("List() = %#v, %v, want one blob", page, err)
	}
}

func TestLocatorsCannotAddressPaths(t *testing.T) {
	t.Parallel()
	store := newStore(t, t.TempDir())
	for _, locator := range []artifactstore.Locator{"", "../outside", "sha256:../outside", artifactstore.Locator("sha256:" + strings.Repeat("A", 64))} {
		_, err := store.Stat(context.Background(), artifactstore.StatRequest{Locator: locator})
		assertFailureCode(t, err, ports.FailureInvalidRequest)
	}
	_, err := store.List(context.Background(), artifactstore.ListRequest{Limit: 10, After: "../outside"})
	assertFailureCode(t, err, ports.FailureInvalidRequest)
}

func TestOpenDetectsContentCorruption(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	store := newStore(t, root)
	blob := put(t, store, "corruption", []byte("original"), "text/plain")
	path := filepath.Join(root, "blobs", "sha256", blob.Digest[:2], blob.Digest)
	if err := os.WriteFile(path, []byte("tampered"), fileMode); err != nil {
		t.Fatal(err)
	}
	_, err := store.Open(context.Background(), artifactstore.OpenRequest{Locator: blob.Locator})
	assertFailureCode(t, err, ports.FailureInternal)
}

func TestOpenRejectsMismatchedExpectedDigest(t *testing.T) {
	t.Parallel()
	store := newStore(t, t.TempDir())
	blob := put(t, store, "expected", []byte("content"), "text/plain")
	_, err := store.Open(context.Background(), artifactstore.OpenRequest{
		Locator:        blob.Locator,
		ExpectedDigest: strings.Repeat("0", 64),
	})
	assertFailureCode(t, err, ports.FailureInvalidRequest)
}

func TestCancelledOperationsAreClassified(t *testing.T) {
	t.Parallel()
	store := newStore(t, t.TempDir())
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := store.Put(ctx, artifactstore.PutRequest{IdempotencyKey: "cancelled", Content: strings.NewReader("content")})
	assertFailureCode(t, err, ports.FailureCancelled)
}

func TestConcurrentDuplicatePutsPublishOneBlob(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	store := newStore(t, root)
	content := bytes.Repeat([]byte("concurrent"), 1024)
	const workers = 8
	results := make(chan artifactstore.Blob, workers)
	errorsChannel := make(chan error, workers)
	var wait sync.WaitGroup
	for index := 0; index < workers; index++ {
		index := index
		wait.Add(1)
		go func() {
			defer wait.Done()
			blob, err := store.Put(context.Background(), artifactstore.PutRequest{
				IdempotencyKey: fmt.Sprintf("concurrent-%d", index),
				Content:        bytes.NewReader(content),
				MediaType:      "application/octet-stream",
			})
			if err != nil {
				errorsChannel <- err
				return
			}
			results <- blob
		}()
	}
	wait.Wait()
	close(results)
	close(errorsChannel)
	for err := range errorsChannel {
		t.Errorf("concurrent Put() error = %v", err)
	}
	var first artifactstore.Blob
	for blob := range results {
		if first.Locator == "" {
			first = blob
		} else if blob != first {
			t.Errorf("concurrent blob = %#v, want %#v", blob, first)
		}
	}
	if got := countRegularFiles(t, filepath.Join(root, "blobs")); got != 1 {
		t.Fatalf("blob file count = %d, want 1", got)
	}
}

func TestConcurrentIdempotencyConflictLeavesOnlyWinnerVisible(t *testing.T) {
	t.Parallel()
	store := newStore(t, t.TempDir())
	start := make(chan struct{})
	results := make(chan error, 2)
	var wait sync.WaitGroup
	for _, content := range []string{"first", "second"} {
		content := content
		wait.Add(1)
		go func() {
			defer wait.Done()
			<-start
			_, err := store.Put(context.Background(), artifactstore.PutRequest{
				IdempotencyKey: "one-concurrent-operation",
				Content:        strings.NewReader(content),
				MediaType:      "text/plain",
			})
			results <- err
		}()
	}
	close(start)
	wait.Wait()
	close(results)
	successes := 0
	conflicts := 0
	for err := range results {
		if err == nil {
			successes++
			continue
		}
		var classified *ports.Failure
		if errors.As(err, &classified) && classified.Code == ports.FailureConflict {
			conflicts++
			continue
		}
		t.Fatalf("Put() error = %v, want conflict", err)
	}
	if successes != 1 || conflicts != 1 {
		t.Fatalf("successes = %d, conflicts = %d, want 1 each", successes, conflicts)
	}
	page, err := store.List(context.Background(), artifactstore.ListRequest{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Blobs) != 1 {
		t.Fatalf("visible blobs = %d, want 1", len(page.Blobs))
	}
}

func TestPutRepairsOperationCommittedBeforeBlobVisibility(t *testing.T) {
	t.Parallel()
	store := newStore(t, t.TempDir())
	content := []byte("recoverable")
	digest := digestOf(content)
	locator := artifactstore.Locator("sha256:" + digest)
	if err := store.ensureOperation("recover-operation", operationRecord{SchemaVersion: recordVersion, Locator: locator}); err != nil {
		t.Fatal(err)
	}
	blob := put(t, store, "recover-operation", content, "text/plain")
	if blob.Locator != locator {
		t.Fatalf("locator = %q, want %q", blob.Locator, locator)
	}
}

type failingReader struct{}

func (failingReader) Read([]byte) (int, error) { return 0, errors.New("injected read failure") }

func newStore(t *testing.T, root string) *Store {
	t.Helper()
	store, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	return store
}

func put(t *testing.T, store *Store, key string, content []byte, mediaType string) artifactstore.Blob {
	t.Helper()
	blob, err := store.Put(context.Background(), artifactstore.PutRequest{
		IdempotencyKey: key,
		Content:        bytes.NewReader(content),
		MediaType:      mediaType,
	})
	if err != nil {
		t.Fatal(err)
	}
	return blob
}

func digestOf(content []byte) string {
	sum := sha256.Sum256(content)
	return hex.EncodeToString(sum[:])
}

func assertFailureCode(t *testing.T, err error, want ports.FailureCode) {
	t.Helper()
	var failure *ports.Failure
	if !errors.As(err, &failure) || failure.Code != want {
		t.Fatalf("error = %T %v, want *ports.Failure code %q", err, err, want)
	}
}

func countRegularFiles(t *testing.T, root string) int {
	t.Helper()
	count := 0
	err := filepath.WalkDir(root, func(_ string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !entry.IsDir() {
			count++
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return count
}
