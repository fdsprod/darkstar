// Package artifactingest coordinates immutable ingestion without interpreting
// supplied bytes as instructions.
package artifactingest

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/fdsprod/darkstar/runtime/src/core/artifactsafety"
	"github.com/fdsprod/darkstar/runtime/src/ports/artifactregistry"
	"github.com/fdsprod/darkstar/runtime/src/ports/artifactstore"
	"github.com/fdsprod/darkstar/runtime/src/ports/contentprocessor"
)

const (
	producerName    = "darkstar-ingest"
	producerVersion = "1.0.0"
)

// CapabilityResolver reports whether installed processors can inspect exact
// source metadata. It does not receive or interpret the source bytes.
type CapabilityResolver interface {
	Supports(context.Context, contentprocessor.SourceDescriptor) (contentprocessor.Support, error)
}

// Request is one logical immutable ingestion. SourceKind is a closed choice;
// source-specific helpers populate it for files, pastes, and stdin.
type Request struct {
	ArtifactID        string
	OperationID       string
	IdempotencyKey    string
	SourceKind        artifactregistry.SourceKind
	SourceName        string
	Content           io.Reader
	DeclaredMediaType string
	Sensitivity       artifactregistry.Sensitivity
	Creator           string
	Roles             []string
	Tags              []string
	Metadata          map[string]string
}

// Result keeps duplicate storage and processor support as derived observations;
// the authoritative immutable record remains Artifact.
type Result struct {
	Artifact    artifactregistry.ArtifactVersion `json:"artifact"`
	Created     bool                             `json:"created"`
	DuplicateOf *artifactregistry.VersionRef     `json:"duplicateOf,omitempty"`
	Capability  contentprocessor.Support         `json:"capability"`
	Diagnostics []string                         `json:"diagnostics"`
}

// Service composes the content store and metadata registry at the application
// boundary. Storage always completes before metadata registration.
type Service struct {
	store        artifactstore.Store
	registry     artifactregistry.Registry
	capabilities CapabilityResolver
	policy       artifactsafety.Policy
}

func New(store artifactstore.Store, registry artifactregistry.Registry, capabilities CapabilityResolver) (*Service, error) {
	return NewWithPolicy(store, registry, capabilities, artifactsafety.DefaultPolicy())
}

func NewWithPolicy(store artifactstore.Store, registry artifactregistry.Registry, capabilities CapabilityResolver, policy artifactsafety.Policy) (*Service, error) {
	if store == nil || registry == nil {
		return nil, errors.New("artifact store and registry are required")
	}
	if err := policy.Validate(); err != nil {
		return nil, err
	}
	return &Service{store: store, registry: registry, capabilities: capabilities, policy: policy}, nil
}

// Ingest streams one source to immutable storage, registers a distinct artifact
// identity, and reports any pre-existing version with equal bytes.
func (s *Service) Ingest(ctx context.Context, request Request) (Result, error) {
	request, err := normalizeRequest(request)
	if err != nil {
		return Result{}, err
	}
	buffered := bufio.NewReader(request.Content)
	header, peekErr := buffered.Peek(512)
	if peekErr != nil && !errors.Is(peekErr, io.EOF) && !errors.Is(peekErr, bufio.ErrBufferFull) {
		return Result{}, fmt.Errorf("inspect artifact header: %w", peekErr)
	}
	assessment := assessMediaType(header, request.DeclaredMediaType)
	blob, err := s.store.Put(ctx, artifactstore.PutRequest{
		IdempotencyKey: request.IdempotencyKey,
		Content:        buffered,
		MaxBytes:       s.policy.SourceBytes,
		MediaType:      assessment.mediaType,
	})
	if err != nil {
		return Result{}, fmt.Errorf("store artifact original: %w", err)
	}

	matching, err := s.registry.VersionsByDigest(ctx, blob.Digest)
	if err != nil {
		return Result{}, fmt.Errorf("detect duplicate artifact content: %w", err)
	}
	metadata := cloneMetadata(request.Metadata)
	metadata["ingest.policyVersion"] = s.policy.Version
	if assessment.mismatch {
		metadata["ingest.mediaTypeMismatch"] = "true"
	}
	if assessment.quarantineReason != "" {
		metadata["ingest.quarantineReason"] = assessment.quarantineReason
	}
	status := assessment.status
	createdAt := blob.StoredAt.UTC()
	if createdAt.IsZero() {
		return Result{}, errors.New("artifact store returned no durable storage time")
	}
	artifact, created, err := s.registry.Register(ctx, artifactregistry.RegisterRequest{
		ArtifactID: request.ArtifactID, IdempotencyKey: request.IdempotencyKey,
		SourceKind: request.SourceKind, SourceName: request.SourceName,
		BlobDigest: blob.Digest, Size: blob.Size, DeclaredMediaType: request.DeclaredMediaType,
		DetectedMediaType: assessment.mediaType, Locator: blob.Locator, Sensitivity: request.Sensitivity,
		Creator: request.Creator, Status: status,
		Producer: artifactregistry.Producer{Name: producerName, Version: producerVersion},
		Roles:    request.Roles, Tags: request.Tags, Metadata: metadata,
		Provenance: artifactregistry.OperationProvenance{OperationID: request.OperationID},
		CreatedAt:  createdAt,
	})
	if err != nil {
		return Result{}, fmt.Errorf("register artifact original: %w", err)
	}

	capability := contentprocessor.Support{State: contentprocessor.SupportUnsupported, MediaType: assessment.mediaType}
	if status == artifactregistry.StatusQuarantined {
		capability = contentprocessor.Support{State: contentprocessor.SupportQuarantined, MediaType: assessment.mediaType, Diagnostics: append([]string(nil), assessment.diagnostics...)}
	} else if !s.policy.AllowsProcessing(artifact.Sensitivity) {
		capability.Diagnostics = []string{"artifact sensitivity is not eligible for processing under the active disclosure policy"}
	} else if s.capabilities != nil {
		capability, err = s.capabilities.Supports(ctx, contentprocessor.SourceDescriptor{
			ArtifactID: artifact.ArtifactID, DeclaredMediaType: artifact.DeclaredMediaType,
			DetectedMediaType: artifact.DetectedMediaType, Digest: artifact.BlobDigest, Size: artifact.Size,
		})
		if err != nil {
			return Result{}, fmt.Errorf("resolve artifact processor capability: %w", err)
		}
	}
	result := Result{Artifact: artifact, Created: created, Capability: capability, Diagnostics: assessment.diagnostics}
	for _, duplicate := range matching {
		if duplicate.ArtifactID != artifact.ArtifactID || duplicate.Version != artifact.Version {
			result.DuplicateOf = &artifactregistry.VersionRef{ArtifactID: duplicate.ArtifactID, Version: duplicate.Version}
			break
		}
	}
	return result, nil
}

// IngestFile opens a local regular file and preserves only its display name in
// artifact metadata; the source path never becomes a storage locator.
func (s *Service) IngestFile(ctx context.Context, path string, request Request) (Result, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return Result{}, fmt.Errorf("resolve artifact file: %w", err)
	}
	file, err := os.Open(absolute)
	if err != nil {
		return Result{}, fmt.Errorf("open artifact file: %w", err)
	}
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil {
		return Result{}, fmt.Errorf("inspect artifact file: %w", err)
	}
	if !info.Mode().IsRegular() {
		return Result{}, errors.New("artifact file must be a regular file")
	}
	request.SourceKind, request.SourceName, request.Content = artifactregistry.SourceFile, filepath.Base(absolute), file
	if strings.TrimSpace(request.DeclaredMediaType) == "" {
		request.DeclaredMediaType = mediaTypeFromExtension(absolute)
	}
	return s.Ingest(ctx, request)
}

func (s *Service) IngestPaste(ctx context.Context, content string, request Request) (Result, error) {
	request.SourceKind, request.Content = artifactregistry.SourcePaste, strings.NewReader(content)
	if request.SourceName == "" {
		request.SourceName = "pasted-note.txt"
	}
	if request.DeclaredMediaType == "" {
		request.DeclaredMediaType = "text/plain; charset=utf-8"
	}
	return s.Ingest(ctx, request)
}

func (s *Service) IngestStdin(ctx context.Context, content io.Reader, request Request) (Result, error) {
	request.SourceKind, request.Content = artifactregistry.SourceStdin, content
	if request.SourceName == "" {
		request.SourceName = "stdin.txt"
	}
	if request.DeclaredMediaType == "" {
		request.DeclaredMediaType = "text/plain; charset=utf-8"
	}
	return s.Ingest(ctx, request)
}

func normalizeRequest(request Request) (Request, error) {
	if !strings.HasPrefix(request.ArtifactID, "artifact_") || strings.TrimSpace(request.OperationID) == "" || strings.TrimSpace(request.IdempotencyKey) == "" {
		return request, errors.New("artifact ID, operation ID, and idempotency key are required")
	}
	if request.Content == nil || strings.TrimSpace(request.SourceName) == "" || strings.TrimSpace(request.DeclaredMediaType) == "" || strings.TrimSpace(request.Creator) == "" {
		return request, errors.New("artifact content, source name, media type, and creator are required")
	}
	switch request.SourceKind {
	case artifactregistry.SourceFile, artifactregistry.SourcePaste, artifactregistry.SourceStdin:
	default:
		return request, fmt.Errorf("unsupported supplied artifact source kind %q", request.SourceKind)
	}
	if request.Sensitivity == "" {
		request.Sensitivity = artifactregistry.SensitivityUnknown
	}
	declared, _, err := mime.ParseMediaType(request.DeclaredMediaType)
	if err != nil {
		return request, fmt.Errorf("parse declared artifact media type: %w", err)
	}
	request.DeclaredMediaType = strings.ToLower(declared)
	return request, nil
}

func mediaTypeFromExtension(path string) string {
	if value := mime.TypeByExtension(strings.ToLower(filepath.Ext(path))); value != "" {
		mediaType, _, err := mime.ParseMediaType(value)
		if err == nil {
			return strings.ToLower(mediaType)
		}
	}
	return "application/octet-stream"
}

type mediaAssessment struct {
	mediaType        string
	status           artifactregistry.Status
	mismatch         bool
	quarantineReason string
	diagnostics      []string
}

func assessMediaType(header []byte, declared string) mediaAssessment {
	detected, quarantineReason := sniffMediaType(header)
	base, _, _ := mime.ParseMediaType(declared)
	if len(header) == 0 {
		detected = strings.ToLower(base)
	}
	// DetectContentType intentionally reports generic text for structured text.
	// A compatible explicit declaration retains its more useful semantic type.
	if strings.HasPrefix(detected, "text/plain") && (strings.HasPrefix(base, "text/") || base == "application/json" || base == "application/yaml") {
		detected = strings.ToLower(base)
	}
	assessment := mediaAssessment{mediaType: strings.ToLower(detected), status: artifactregistry.StatusStored, quarantineReason: quarantineReason}
	if quarantineReason != "" {
		assessment.status = artifactregistry.StatusQuarantined
		assessment.diagnostics = append(assessment.diagnostics, "unsafe active or container content was quarantined: "+quarantineReason)
	}
	base = strings.ToLower(base)
	assessment.mismatch = base != "" && base != "application/octet-stream" && base != assessment.mediaType
	if assessment.mismatch {
		assessment.diagnostics = append(assessment.diagnostics, fmt.Sprintf("declared media type %s does not match detected media type %s", base, assessment.mediaType))
	}
	return assessment
}

func sniffMediaType(header []byte) (string, string) {
	switch {
	case len(header) >= 2 && bytes.Equal(header[:2], []byte{'M', 'Z'}):
		return "application/x-msdownload", "windows_executable"
	case len(header) >= 4 && bytes.Equal(header[:4], []byte{0x7f, 'E', 'L', 'F'}):
		return "application/x-executable", "executable"
	case len(header) >= 4 && bytes.Equal(header[:4], []byte{'P', 'K', 0x03, 0x04}):
		return "application/zip", "archive"
	case len(header) >= 2 && bytes.Equal(header[:2], []byte{0x1f, 0x8b}):
		return "application/gzip", "archive"
	}
	detected := strings.ToLower(http.DetectContentType(header))
	if detected == "text/html; charset=utf-8" {
		return "text/html", "active_content"
	}
	return detected, ""
}

func cloneMetadata(value map[string]string) map[string]string {
	result := make(map[string]string, len(value)+3)
	for key, entry := range value {
		result[key] = entry
	}
	return result
}
