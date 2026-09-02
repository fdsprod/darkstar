package artifactingest

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"testing"
	"time"

	"github.com/fdsprod/darkstar/runtime/src/core/artifactsafety"
	"github.com/fdsprod/darkstar/runtime/src/ports"
	"github.com/fdsprod/darkstar/runtime/src/ports/artifactregistry"
	"github.com/fdsprod/darkstar/runtime/src/ports/artifactstore"
	"github.com/fdsprod/darkstar/runtime/src/ports/contentprocessor"
)

func TestIngestPasteAndStdinPreserveDistinctIdentityWhileSharingBlob(t *testing.T) {
	t.Parallel()
	service := newTestService(t, supportedResolver{})
	ctx := context.Background()
	content := "meeting notes\n"
	first, err := service.IngestPaste(ctx, content, testRequest("artifact_PASTE", "paste"))
	if err != nil {
		t.Fatal(err)
	}
	second, err := service.IngestStdin(ctx, bytes.NewBufferString(content), testRequest("artifact_STDIN", "stdin"))
	if err != nil {
		t.Fatal(err)
	}
	if first.Artifact.SourceKind != artifactregistry.SourcePaste || second.Artifact.SourceKind != artifactregistry.SourceStdin {
		t.Fatalf("source kinds = %q, %q", first.Artifact.SourceKind, second.Artifact.SourceKind)
	}
	if first.Artifact.BlobDigest != second.Artifact.BlobDigest || first.Artifact.Locator != second.Artifact.Locator {
		t.Fatal("equal input did not share immutable storage")
	}
	if second.DuplicateOf == nil || second.DuplicateOf.ArtifactID != first.Artifact.ArtifactID || second.Artifact.ArtifactID == first.Artifact.ArtifactID {
		t.Fatalf("duplicate result = %#v", second)
	}
	if first.Capability.State != contentprocessor.SupportSupported || first.Artifact.Trust != "untrusted" {
		t.Fatalf("capability/artifact = %#v / %#v", first.Capability, first.Artifact)
	}
}

func TestIngestFilePreservesOriginalBytesAndHidesSourcePath(t *testing.T) {
	t.Parallel()
	service := newTestService(t, nil)
	path := filepath.Join(t.TempDir(), "evidence.json")
	content := []byte(`{"answer":42}`)
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatal(err)
	}
	result, err := service.IngestFile(context.Background(), path, testRequest("artifact_FILE", "file"))
	if err != nil {
		t.Fatal(err)
	}
	if result.Artifact.SourceName != "evidence.json" || result.Artifact.DeclaredMediaType != "application/json" || result.Artifact.DetectedMediaType != "application/json" {
		t.Fatalf("artifact = %#v", result.Artifact)
	}
	if string(result.Artifact.Locator) == path || result.Capability.State != contentprocessor.SupportUnsupported {
		t.Fatalf("path leaked or capability incorrect: %#v", result)
	}
}

func TestSafetyPolicyRejectsOversizeAndQuarantinesUnsafeMagic(t *testing.T) {
	t.Parallel()
	store := &memoryStore{blobs: map[artifactstore.Locator][]byte{}}
	registry := &memoryRegistry{}
	policy := artifactsafety.DefaultPolicy()
	policy.SourceBytes = 4
	service, err := NewWithPolicy(store, registry, supportedResolver{}, policy)
	if err != nil {
		t.Fatal(err)
	}
	_, err = service.IngestPaste(context.Background(), "12345", testRequest("artifact_BIG", "big"))
	var failure *ports.Failure
	if !errors.As(err, &failure) || failure.Code != ports.FailureResourceExhausted || len(registry.versions) != 0 {
		t.Fatalf("oversize error/registry = %v / %#v", err, registry.versions)
	}

	policy.SourceBytes = 1024
	service, err = NewWithPolicy(store, registry, supportedResolver{}, policy)
	if err != nil {
		t.Fatal(err)
	}
	request := testRequest("artifact_EXE", "exe")
	request.SourceKind, request.SourceName, request.Content = artifactregistry.SourceFile, "notes.txt", bytes.NewReader([]byte{'M', 'Z', 0, 1})
	request.DeclaredMediaType = "text/plain"
	result, err := service.Ingest(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if result.Artifact.Status != artifactregistry.StatusQuarantined || result.Capability.State != contentprocessor.SupportQuarantined || result.Artifact.Metadata["ingest.mediaTypeMismatch"] != "true" {
		t.Fatalf("unsafe result = %#v", result)
	}
}

func TestSafetyPolicyBlocksUnclassifiedAndSecretProcessing(t *testing.T) {
	t.Parallel()
	service := newTestService(t, supportedResolver{})
	request := testRequest("artifact_SECRET", "secret")
	request.Sensitivity = artifactregistry.SensitivitySecret
	result, err := service.IngestPaste(context.Background(), "secret", request)
	if err != nil {
		t.Fatal(err)
	}
	if result.Artifact.Status != artifactregistry.StatusStored || result.Capability.State != contentprocessor.SupportUnsupported || len(result.Capability.Diagnostics) == 0 {
		t.Fatalf("secret result = %#v", result)
	}
}

func newTestService(t *testing.T, resolver CapabilityResolver) *Service {
	t.Helper()
	store := &memoryStore{blobs: map[artifactstore.Locator][]byte{}}
	registry := &memoryRegistry{}
	service, err := New(store, registry, resolver)
	if err != nil {
		t.Fatal(err)
	}
	return service
}

func testRequest(artifactID, key string) Request {
	return Request{
		ArtifactID: artifactID, OperationID: "operation_" + key, IdempotencyKey: key,
		Sensitivity: artifactregistry.SensitivityInternal, Creator: "user:local", Roles: []string{"note"},
	}
}

type supportedResolver struct{}

func (supportedResolver) Supports(_ context.Context, source contentprocessor.SourceDescriptor) (contentprocessor.Support, error) {
	return contentprocessor.Support{State: contentprocessor.SupportSupported, MediaType: source.DetectedMediaType}, nil
}

type memoryStore struct {
	blobs map[artifactstore.Locator][]byte
}

func (store *memoryStore) Put(_ context.Context, request artifactstore.PutRequest) (artifactstore.Blob, error) {
	content, err := io.ReadAll(request.Content)
	if err != nil {
		return artifactstore.Blob{}, err
	}
	if request.MaxBytes > 0 && int64(len(content)) > request.MaxBytes {
		return artifactstore.Blob{}, &ports.Failure{Code: ports.FailureResourceExhausted, Message: "artifact content exceeds source byte limit"}
	}
	digestBytes := sha256.Sum256(content)
	digest := hex.EncodeToString(digestBytes[:])
	locator := artifactstore.Locator("sha256:" + digest)
	store.blobs[locator] = append([]byte(nil), content...)
	return artifactstore.Blob{Locator: locator, Digest: digest, Size: int64(len(content)), MediaType: request.MediaType, StoredAt: time.Now()}, nil
}

func (store *memoryStore) Open(_ context.Context, request artifactstore.OpenRequest) (io.ReadCloser, error) {
	content, ok := store.blobs[request.Locator]
	if !ok {
		return nil, errors.New("not found")
	}
	return io.NopCloser(bytes.NewReader(content)), nil
}

func (store *memoryStore) Stat(_ context.Context, request artifactstore.StatRequest) (artifactstore.Blob, error) {
	content, ok := store.blobs[request.Locator]
	if !ok {
		return artifactstore.Blob{}, errors.New("not found")
	}
	return artifactstore.Blob{Locator: request.Locator, Digest: string(request.Locator)[7:], Size: int64(len(content))}, nil
}

func (store *memoryStore) List(context.Context, artifactstore.ListRequest) (artifactstore.Page, error) {
	return artifactstore.Page{}, nil
}

type memoryRegistry struct {
	versions []artifactregistry.ArtifactVersion
}

func (registry *memoryRegistry) Register(_ context.Context, request artifactregistry.RegisterRequest) (artifactregistry.ArtifactVersion, bool, error) {
	for _, existing := range registry.versions {
		if existing.ArtifactID == request.ArtifactID && existing.Metadata["idempotencyKey"] == request.IdempotencyKey {
			return existing, false, nil
		}
	}
	version := uint64(1)
	for _, existing := range registry.versions {
		if existing.ArtifactID == request.ArtifactID && existing.Version >= version {
			version = existing.Version + 1
		}
	}
	metadata := make(map[string]string, len(request.Metadata)+1)
	for key, value := range request.Metadata {
		metadata[key] = value
	}
	metadata["idempotencyKey"] = request.IdempotencyKey
	value := artifactregistry.ArtifactVersion{
		ArtifactID: request.ArtifactID, Version: version, SourceKind: request.SourceKind, SourceName: request.SourceName,
		BlobDigest: request.BlobDigest, Size: request.Size, DeclaredMediaType: request.DeclaredMediaType,
		DetectedMediaType: request.DetectedMediaType, Locator: request.Locator, Sensitivity: request.Sensitivity,
		Trust: "untrusted", Creator: request.Creator, Status: request.Status, Producer: request.Producer,
		Roles: append([]string(nil), request.Roles...), Tags: append([]string(nil), request.Tags...), Metadata: metadata,
		Provenance: request.Provenance, CreatedAt: request.CreatedAt,
	}
	registry.versions = append(registry.versions, value)
	return value, true, nil
}

func (registry *memoryRegistry) ArtifactVersion(_ context.Context, reference artifactregistry.VersionRef) (artifactregistry.ArtifactVersion, error) {
	for _, value := range registry.versions {
		if value.ArtifactID == reference.ArtifactID && value.Version == reference.Version {
			return value, nil
		}
	}
	return artifactregistry.ArtifactVersion{}, artifactregistry.ErrNotFound
}

func (registry *memoryRegistry) LatestVersion(ctx context.Context, artifactID string) (artifactregistry.ArtifactVersion, error) {
	values, _ := registry.Versions(ctx, artifactID)
	if len(values) == 0 {
		return artifactregistry.ArtifactVersion{}, artifactregistry.ErrNotFound
	}
	return values[len(values)-1], nil
}

func (registry *memoryRegistry) Versions(_ context.Context, artifactID string) ([]artifactregistry.ArtifactVersion, error) {
	result := make([]artifactregistry.ArtifactVersion, 0)
	for _, value := range registry.versions {
		if value.ArtifactID == artifactID {
			result = append(result, value)
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Version < result[j].Version })
	return result, nil
}

func (registry *memoryRegistry) VersionsByDigest(_ context.Context, digest string) ([]artifactregistry.ArtifactVersion, error) {
	result := make([]artifactregistry.ArtifactVersion, 0)
	for _, value := range registry.versions {
		if value.BlobDigest == digest {
			result = append(result, value)
		}
	}
	return result, nil
}

func (registry *memoryRegistry) Artifacts(_ context.Context) ([]artifactregistry.ArtifactVersion, error) {
	latest := make(map[string]artifactregistry.ArtifactVersion)
	for _, value := range registry.versions {
		if value.Version > latest[value.ArtifactID].Version {
			latest[value.ArtifactID] = value
		}
	}
	result := make([]artifactregistry.ArtifactVersion, 0, len(latest))
	for _, value := range latest {
		result = append(result, value)
	}
	return result, nil
}
