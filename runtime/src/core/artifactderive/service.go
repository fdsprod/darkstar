// Package artifactderive coordinates deterministic, versioned representations
// while keeping processor failures isolated from immutable source artifacts.
package artifactderive

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"

	"github.com/fdsprod/darkstar/runtime/src/core/artifactsafety"
	"github.com/fdsprod/darkstar/runtime/src/ports/artifactregistry"
	"github.com/fdsprod/darkstar/runtime/src/ports/artifactstore"
	"github.com/fdsprod/darkstar/runtime/src/ports/contentprocessor"
	"github.com/fdsprod/darkstar/runtime/src/ports/representationregistry"
)

var ErrProcessorTimeout = errors.New("artifact processor timed out")

type Request struct {
	Artifact       artifactregistry.VersionRef
	OperationID    string
	IdempotencyKey string
	PolicyVersion  string
	Limits         contentprocessor.Limits
}

type Result struct {
	Support         contentprocessor.Support                `json:"support"`
	Representations []representationregistry.Representation `json:"representations"`
	Diagnostics     []string                                `json:"diagnostics"`
	Limited         bool                                    `json:"limited"`
}

type Service struct {
	store           artifactstore.Store
	artifacts       artifactregistry.Registry
	representations representationregistry.Registry
	processors      []contentprocessor.Processor
	policy          artifactsafety.Policy
}

func New(store artifactstore.Store, artifacts artifactregistry.Registry, representations representationregistry.Registry, processors ...contentprocessor.Processor) (*Service, error) {
	return NewWithPolicy(store, artifacts, representations, artifactsafety.DefaultPolicy(), processors...)
}

func NewWithPolicy(store artifactstore.Store, artifacts artifactregistry.Registry, representations representationregistry.Registry, policy artifactsafety.Policy, processors ...contentprocessor.Processor) (*Service, error) {
	if store == nil || artifacts == nil || representations == nil {
		return nil, errors.New("artifact store, artifact registry, and representation registry are required")
	}
	if err := policy.Validate(); err != nil {
		return nil, err
	}
	for _, processor := range processors {
		if processor == nil {
			return nil, errors.New("content processors must not be nil")
		}
	}
	return &Service{store: store, artifacts: artifacts, representations: representations, processors: append([]contentprocessor.Processor(nil), processors...), policy: policy}, nil
}

// Supports reports the first installed processor in stable configured order.
func (service *Service) Supports(ctx context.Context, source contentprocessor.SourceDescriptor) (contentprocessor.Support, error) {
	for _, processor := range service.processors {
		support, err := processor.Supports(ctx, source)
		if err != nil {
			return contentprocessor.Support{}, err
		}
		if support.State == contentprocessor.SupportSupported || support.State == contentprocessor.SupportQuarantined {
			return support, nil
		}
	}
	return contentprocessor.Support{State: contentprocessor.SupportUnsupported, MediaType: source.DetectedMediaType}, nil
}

func (service *Service) Derive(ctx context.Context, request Request) (Result, error) {
	if !strings.HasPrefix(request.Artifact.ArtifactID, "artifact_") || request.Artifact.Version == 0 || strings.TrimSpace(request.OperationID) == "" || strings.TrimSpace(request.IdempotencyKey) == "" || strings.TrimSpace(request.PolicyVersion) == "" {
		return Result{}, errors.New("exact artifact, operation ID, idempotency key, and policy version are required")
	}
	artifact, err := service.artifacts.ArtifactVersion(ctx, request.Artifact)
	if err != nil {
		return Result{}, fmt.Errorf("read source artifact: %w", err)
	}
	if artifact.Status == artifactregistry.StatusQuarantined {
		return Result{Support: contentprocessor.Support{State: contentprocessor.SupportQuarantined, MediaType: artifact.DetectedMediaType, Diagnostics: []string{"quarantined artifacts cannot be processed"}}}, nil
	}
	if !service.policy.AllowsProcessing(artifact.Sensitivity) {
		return Result{Support: contentprocessor.Support{State: contentprocessor.SupportUnsupported, MediaType: artifact.DetectedMediaType}, Diagnostics: []string{"artifact sensitivity is not eligible for processing under the active disclosure policy"}}, nil
	}
	source := contentprocessor.SourceDescriptor{
		ArtifactID: artifact.ArtifactID, DeclaredMediaType: artifact.DeclaredMediaType,
		DetectedMediaType: artifact.DetectedMediaType, Digest: artifact.BlobDigest, Size: artifact.Size,
	}
	var selected contentprocessor.Processor
	var support contentprocessor.Support
	for _, processor := range service.processors {
		support, err = processor.Supports(ctx, source)
		if err != nil {
			return Result{}, fmt.Errorf("inspect processor support: %w", err)
		}
		if support.State == contentprocessor.SupportSupported {
			selected = processor
			break
		}
		if support.State == contentprocessor.SupportQuarantined {
			return Result{Support: support, Diagnostics: support.Diagnostics}, nil
		}
	}
	if selected == nil {
		return Result{Support: contentprocessor.Support{State: contentprocessor.SupportUnsupported, MediaType: artifact.DetectedMediaType}}, nil
	}
	content, err := service.store.Open(ctx, artifactstore.OpenRequest{Locator: artifact.Locator, ExpectedDigest: artifact.BlobDigest})
	if err != nil {
		return Result{}, fmt.Errorf("open source artifact: %w", err)
	}
	defer func() { _ = content.Close() }()
	limits := effectiveLimits(request.Limits, service.policy.ProcessorLimits())
	sink := &registrySink{
		store: service.store, registry: service.representations, source: request.Artifact,
		processor: selected.Descriptor(), operationID: request.OperationID, idempotencyKey: request.IdempotencyKey,
		maxBytes: limits.OutputBytes, maxSourceBytes: limits.SourceBytes,
	}
	processCtx, cancel := context.WithTimeout(ctx, limits.WallTime)
	defer cancel()
	processed, err := selected.Process(processCtx, contentprocessor.ProcessRequest{
		OperationID: request.OperationID, IdempotencyKey: request.IdempotencyKey,
		Source: source, Content: content, Limits: limits, PolicyVersion: request.PolicyVersion,
	}, sink)
	if errors.Is(processCtx.Err(), context.DeadlineExceeded) {
		return Result{Support: support, Diagnostics: append([]string(nil), processed.Diagnostics...), Limited: true}, ErrProcessorTimeout
	}
	if err != nil {
		return Result{Support: support, Diagnostics: append([]string(nil), processed.Diagnostics...), Limited: processed.Limited}, fmt.Errorf("process artifact representation: %w", err)
	}
	return Result{Support: support, Representations: sink.created, Diagnostics: processed.Diagnostics, Limited: processed.Limited}, nil
}

func effectiveLimits(requested, defaults contentprocessor.Limits) contentprocessor.Limits {
	if requested.SourceBytes <= 0 || requested.SourceBytes > defaults.SourceBytes {
		requested.SourceBytes = defaults.SourceBytes
	}
	if requested.OutputBytes <= 0 || requested.OutputBytes > defaults.OutputBytes {
		requested.OutputBytes = defaults.OutputBytes
	}
	if requested.ExpandedBytes <= 0 || requested.ExpandedBytes > defaults.ExpandedBytes {
		requested.ExpandedBytes = defaults.ExpandedBytes
	}
	if requested.Representations <= 0 || requested.Representations > defaults.Representations {
		requested.Representations = defaults.Representations
	}
	if requested.TableCells <= 0 || requested.TableCells > defaults.TableCells {
		requested.TableCells = defaults.TableCells
	}
	if requested.Pages <= 0 || requested.Pages > defaults.Pages {
		requested.Pages = defaults.Pages
	}
	if requested.Pixels <= 0 || requested.Pixels > defaults.Pixels {
		requested.Pixels = defaults.Pixels
	}
	if requested.WallTime <= 0 || requested.WallTime > defaults.WallTime {
		requested.WallTime = defaults.WallTime
	}
	if requested.MemoryBytes <= 0 || requested.MemoryBytes > defaults.MemoryBytes {
		requested.MemoryBytes = defaults.MemoryBytes
	}
	return requested
}

type registrySink struct {
	store          artifactstore.Store
	registry       representationregistry.Registry
	source         artifactregistry.VersionRef
	processor      contentprocessor.Descriptor
	operationID    string
	idempotencyKey string
	maxBytes       int64
	maxSourceBytes int64
	created        []representationregistry.Representation
}

func (sink *registrySink) Store(ctx context.Context, representation contentprocessor.Representation) (contentprocessor.Receipt, error) {
	key := sink.idempotencyKey + "/" + string(representation.Kind)
	maxBytes := sink.maxBytes
	// Image representations intentionally retain source bytes so their locator
	// is directly usable by a model adapter. The source-byte policy remains the
	// governing bound; previews still use the smaller decoded-output bound.
	if representation.Kind == contentprocessor.RepresentationImage {
		maxBytes = sink.maxSourceBytes
	}
	blob, err := sink.store.Put(ctx, artifactstore.PutRequest{
		IdempotencyKey: key, Content: representation.Content, ExpectedDigest: representation.Digest,
		ExpectedSize: &representation.Size, MaxBytes: maxBytes, MediaType: representation.MediaType,
	})
	if err != nil {
		return contentprocessor.Receipt{}, err
	}
	id := stableRepresentationID(sink.source, sink.processor, representation.Kind, key)
	registered, _, err := sink.registry.RegisterRepresentation(ctx, representationregistry.RegisterRequest{
		RepresentationID: id, IdempotencyKey: sink.idempotencyKey, Artifact: sink.source,
		Kind: representation.Kind, Processor: sink.processor, MediaType: representation.MediaType,
		Locator: blob.Locator, Digest: blob.Digest, Size: blob.Size, TokenEstimate: representation.TokenEstimate,
		Truncated: representation.Truncated, Disclosure: representationregistry.DisclosureRaw,
		Diagnostics: representation.Diagnostics, Metadata: representation.Metadata, CreatedAt: blob.StoredAt,
	})
	if err != nil {
		return contentprocessor.Receipt{}, err
	}
	sink.created = append(sink.created, registered)
	return contentprocessor.Receipt{RepresentationID: id, Locator: string(blob.Locator), Digest: blob.Digest, Size: blob.Size}, nil
}

func stableRepresentationID(source artifactregistry.VersionRef, processor contentprocessor.Descriptor, kind contentprocessor.RepresentationKind, key string) string {
	seed := fmt.Sprintf("%s\x00%d\x00%s\x00%s\x00%s\x00%s", source.ArtifactID, source.Version, processor.Name, processor.Version, kind, key)
	digest := sha256.Sum256([]byte(seed))
	return "representation_" + hex.EncodeToString(digest[:16])
}
