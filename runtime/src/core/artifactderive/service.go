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

	"github.com/fdsprod/darkstar/runtime/src/ports/artifactregistry"
	"github.com/fdsprod/darkstar/runtime/src/ports/artifactstore"
	"github.com/fdsprod/darkstar/runtime/src/ports/contentprocessor"
	"github.com/fdsprod/darkstar/runtime/src/ports/representationregistry"
)

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
}

func New(store artifactstore.Store, artifacts artifactregistry.Registry, representations representationregistry.Registry, processors ...contentprocessor.Processor) (*Service, error) {
	if store == nil || artifacts == nil || representations == nil {
		return nil, errors.New("artifact store, artifact registry, and representation registry are required")
	}
	for _, processor := range processors {
		if processor == nil {
			return nil, errors.New("content processors must not be nil")
		}
	}
	return &Service{store: store, artifacts: artifacts, representations: representations, processors: append([]contentprocessor.Processor(nil), processors...)}, nil
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
	sink := &registrySink{
		store: service.store, registry: service.representations, source: request.Artifact,
		processor: selected.Descriptor(), operationID: request.OperationID, idempotencyKey: request.IdempotencyKey,
	}
	processed, err := selected.Process(ctx, contentprocessor.ProcessRequest{
		OperationID: request.OperationID, IdempotencyKey: request.IdempotencyKey,
		Source: source, Content: content, Limits: request.Limits, PolicyVersion: request.PolicyVersion,
	}, sink)
	if err != nil {
		return Result{Support: support, Diagnostics: append([]string(nil), processed.Diagnostics...), Limited: processed.Limited}, fmt.Errorf("process artifact representation: %w", err)
	}
	return Result{Support: support, Representations: sink.created, Diagnostics: processed.Diagnostics, Limited: processed.Limited}, nil
}

type registrySink struct {
	store          artifactstore.Store
	registry       representationregistry.Registry
	source         artifactregistry.VersionRef
	processor      contentprocessor.Descriptor
	operationID    string
	idempotencyKey string
	created        []representationregistry.Representation
}

func (sink *registrySink) Store(ctx context.Context, representation contentprocessor.Representation) (contentprocessor.Receipt, error) {
	key := sink.idempotencyKey + "/" + string(representation.Kind)
	blob, err := sink.store.Put(ctx, artifactstore.PutRequest{
		IdempotencyKey: key, Content: representation.Content, ExpectedDigest: representation.Digest,
		ExpectedSize: &representation.Size, MediaType: representation.MediaType,
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
