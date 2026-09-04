// Package artifactops is the application boundary shared by the artifact API
// and CLI. It coordinates existing ingestion, derivation, binding, lineage, and
// impact services without duplicating their rules at transport edges.
package artifactops

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base32"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"sort"
	"strings"
	"time"

	"darkstar/src/core/artifactderive"
	"darkstar/src/core/artifactingest"
	"darkstar/src/core/identity"
	"darkstar/src/core/lateevidence"
	"darkstar/src/ports/artifactbinding"
	"darkstar/src/ports/artifactlineage"
	"darkstar/src/ports/artifactregistry"
	"darkstar/src/ports/artifactstore"
	"darkstar/src/ports/impactassessment"
	"darkstar/src/ports/representationregistry"
	"darkstar/src/ports/statestore"
)

const PolicyVersion = "artifact-context/v1alpha1"

var ErrContentWithheld = errors.New("artifact content is withheld")

type IngestInput struct {
	ArtifactID  string                       `json:"artifactId,omitempty"`
	SourceKind  artifactregistry.SourceKind  `json:"sourceKind"`
	SourceName  string                       `json:"sourceName"`
	MediaType   string                       `json:"mediaType"`
	Content     []byte                       `json:"content"`
	Sensitivity artifactregistry.Sensitivity `json:"sensitivity,omitempty"`
	Creator     string                       `json:"creator,omitempty"`
	Roles       []string                     `json:"roles,omitempty"`
	Tags        []string                     `json:"tags,omitempty"`
}

type AttachInput struct {
	BindingID string                      `json:"bindingId,omitempty"`
	Artifact  artifactregistry.VersionRef `json:"artifact"`
	Target    artifactbinding.Target      `json:"target"`
}

type ListInput struct {
	Target *artifactbinding.Target `json:"target,omitempty"`
}

type ArtifactView struct {
	Artifact        artifactregistry.ArtifactVersion        `json:"artifact"`
	Freshness       artifactlineage.Freshness               `json:"freshness"`
	Representations []representationregistry.Representation `json:"representations"`
}

type VersionDiff struct {
	ArtifactID      string              `json:"artifactId"`
	From            uint64              `json:"from"`
	To              uint64              `json:"to"`
	Changed         []string            `json:"changed"`
	FromDigest      string              `json:"fromDigest"`
	ToDigest        string              `json:"toDigest"`
	Representations map[string][]string `json:"representations"`
}

type LintIssue struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type LintResult struct {
	Artifact artifactregistry.VersionRef `json:"artifact"`
	Valid    bool                        `json:"valid"`
	Issues   []LintIssue                 `json:"issues"`
}

// Content is an exact immutable stream plus the metadata required for safe
// authenticated HTTP delivery. Callers must close Reader.
type Content struct {
	Reader           io.ReadCloser
	Digest           string
	Size             int64
	MediaType        string
	FileName         string
	RepresentationID string
}

// AuditEventStore is the append-only event boundary required for artifact
// commands to participate in the same global evidence stream as run controls.
type AuditEventStore interface {
	Append(context.Context, ...statestore.PendingEvent) ([]statestore.Event, error)
}

type Service struct {
	store           artifactstore.Store
	artifacts       artifactregistry.Registry
	bindings        artifactbinding.Store
	lineage         artifactlineage.Store
	representations representationregistry.Registry
	audit           AuditEventStore
	ingestion       *artifactingest.Service
	derivation      *artifactderive.Service
	impact          *lateevidence.Service
	now             func() time.Time
}

func New(store artifactstore.Store, artifacts artifactregistry.Registry, bindings artifactbinding.Store, lineage artifactlineage.Store, representations representationregistry.Registry, audit AuditEventStore, ingestion *artifactingest.Service, derivation *artifactderive.Service, impact *lateevidence.Service) (*Service, error) {
	if store == nil || artifacts == nil || bindings == nil || lineage == nil || representations == nil || audit == nil || ingestion == nil || derivation == nil || impact == nil {
		return nil, errors.New("complete artifact operation services are required")
	}
	return &Service{store: store, artifacts: artifacts, bindings: bindings, lineage: lineage, representations: representations, audit: audit, ingestion: ingestion, derivation: derivation, impact: impact, now: time.Now}, nil
}

func (service *Service) Ingest(ctx context.Context, input IngestInput, idempotencyKey string) (artifactingest.Result, error) {
	return service.ingest(ctx, input, idempotencyKey, nil)
}

func (service *Service) ingest(ctx context.Context, input IngestInput, idempotencyKey string, expectedPreviousVersion *uint64) (artifactingest.Result, error) {
	if strings.TrimSpace(idempotencyKey) == "" {
		return artifactingest.Result{}, errors.New("idempotency key is required")
	}
	if input.ArtifactID == "" {
		input.ArtifactID = stableID("artifact_", idempotencyKey)
	}
	if input.Creator == "" {
		input.Creator = "user:local"
	}
	if input.Sensitivity == "" {
		input.Sensitivity = artifactregistry.SensitivityInternal
	}
	result, err := service.ingestion.Ingest(ctx, artifactingest.Request{
		ArtifactID: input.ArtifactID, ExpectedPreviousVersion: expectedPreviousVersion, OperationID: stableID("operation_", "ingest\x00"+idempotencyKey), IdempotencyKey: idempotencyKey,
		SourceKind: input.SourceKind, SourceName: input.SourceName, DeclaredMediaType: input.MediaType,
		Content: bytes.NewReader(input.Content), Sensitivity: input.Sensitivity, Creator: input.Creator,
		Roles: input.Roles, Tags: input.Tags,
	})
	if err != nil {
		return artifactingest.Result{}, err
	}
	reference := artifactregistry.VersionRef{ArtifactID: result.Artifact.ArtifactID, Version: result.Artifact.Version}
	data := artifactIngestedEventData{
		Artifact: reference, BlobDigest: result.Artifact.BlobDigest, Size: result.Artifact.Size,
		DetectedMediaType: result.Artifact.DetectedMediaType, Status: result.Artifact.Status,
	}
	kind := "artifact.ingested"
	if expectedPreviousVersion != nil {
		kind = "artifact.revised"
		data.BaseVersion = expectedPreviousVersion
	}
	if err := service.recordAuditEvent(ctx, kind, identity.Deterministic("operation_", "audit\x00"+kind+"\x00"+idempotencyKey), idempotencyKey, reference.ArtifactID, data); err != nil {
		return artifactingest.Result{}, fmt.Errorf("record %s audit event: %w", kind, err)
	}
	return result, nil
}

func (service *Service) Revise(ctx context.Context, artifactID string, baseVersion uint64, input IngestInput, idempotencyKey string) (artifactingest.Result, error) {
	if !strings.HasPrefix(artifactID, "artifact_") || baseVersion == 0 {
		return artifactingest.Result{}, errors.New("artifact ID is required")
	}
	input.ArtifactID = artifactID
	return service.ingest(ctx, input, idempotencyKey, &baseVersion)
}

func (service *Service) Attach(ctx context.Context, input AttachInput, idempotencyKey string) (artifactbinding.Version, error) {
	if input.BindingID == "" {
		input.BindingID = stableID("binding_", fmt.Sprintf("%s\x00%d\x00%s\x00%s\x00%s", input.Artifact.ArtifactID, input.Artifact.Version, input.Target.Kind, input.Target.ID, idempotencyKey))
	}
	value, _, err := service.bindings.Bind(ctx, artifactbinding.BindRequest{BindingID: input.BindingID, IdempotencyKey: idempotencyKey, Artifact: input.Artifact, Target: input.Target, CreatedAt: service.now().UTC()})
	if err != nil {
		return artifactbinding.Version{}, err
	}
	correlationID := value.Artifact.ArtifactID
	if value.Target.Kind == artifactbinding.TargetRun && len(value.Target.ID) <= 128 {
		correlationID = value.Target.ID
	}
	data := artifactAttachedEventData{
		BindingID: value.BindingID, BindingVersion: value.Version,
		Artifact: value.Artifact, Target: value.Target,
	}
	if err := service.recordAuditEvent(ctx, "artifact.attached", identity.Deterministic("operation_", "audit\x00attach\x00"+idempotencyKey), idempotencyKey, correlationID, data); err != nil {
		return artifactbinding.Version{}, fmt.Errorf("record artifact.attached audit event: %w", err)
	}
	return value, nil
}

func (service *Service) Detach(ctx context.Context, bindingID, idempotencyKey string) (artifactbinding.Version, error) {
	value, _, err := service.bindings.Unbind(ctx, artifactbinding.UnbindRequest{BindingID: bindingID, IdempotencyKey: idempotencyKey, CreatedAt: service.now().UTC()})
	return value, err
}

func (service *Service) List(ctx context.Context, input ListInput) ([]ArtifactView, error) {
	versions := make([]artifactregistry.ArtifactVersion, 0)
	if input.Target == nil {
		values, err := service.artifacts.Artifacts(ctx)
		if err != nil {
			return nil, err
		}
		versions = values
	} else {
		bindings, err := service.bindings.ActiveBindings(ctx, *input.Target)
		if err != nil {
			return nil, err
		}
		for _, binding := range bindings {
			value, err := service.artifacts.ArtifactVersion(ctx, binding.Artifact)
			if err != nil {
				return nil, err
			}
			versions = append(versions, value)
		}
	}
	sort.Slice(versions, func(left, right int) bool {
		if versions[left].ArtifactID != versions[right].ArtifactID {
			return versions[left].ArtifactID < versions[right].ArtifactID
		}
		return versions[left].Version < versions[right].Version
	})
	result := make([]ArtifactView, 0, len(versions))
	for _, version := range versions {
		view, err := service.view(ctx, version)
		if err != nil {
			return nil, err
		}
		result = append(result, view)
	}
	return result, nil
}

func (service *Service) Show(ctx context.Context, artifactID string, version uint64) (ArtifactView, error) {
	var artifact artifactregistry.ArtifactVersion
	var err error
	if version == 0 {
		artifact, err = service.artifacts.LatestVersion(ctx, artifactID)
	} else {
		artifact, err = service.artifacts.ArtifactVersion(ctx, artifactregistry.VersionRef{ArtifactID: artifactID, Version: version})
	}
	if err != nil {
		return ArtifactView{}, err
	}
	return service.view(ctx, artifact)
}

func (service *Service) Representations(ctx context.Context, reference artifactregistry.VersionRef) ([]representationregistry.Representation, error) {
	return service.representations.ForArtifact(ctx, reference)
}

// OriginalContent opens one exact stored original and verifies the registered
// digest at the artifact-store boundary before any bytes reach a client.
func (service *Service) OriginalContent(ctx context.Context, reference artifactregistry.VersionRef) (Content, error) {
	artifact, err := service.artifacts.ArtifactVersion(ctx, reference)
	if err != nil {
		return Content{}, err
	}
	if artifact.Status != artifactregistry.StatusStored {
		return Content{}, ErrContentWithheld
	}
	reader, err := service.store.Open(ctx, artifactstore.OpenRequest{Locator: artifact.Locator, ExpectedDigest: artifact.BlobDigest})
	if err != nil {
		return Content{}, fmt.Errorf("open artifact original: %w", err)
	}
	return Content{Reader: reader, Digest: artifact.BlobDigest, Size: artifact.Size, MediaType: artifact.DetectedMediaType, FileName: artifact.SourceName}, nil
}

// RepresentationContent opens one exact derived representation. Withheld
// representations remain visible as metadata but their bytes never cross this
// boundary.
func (service *Service) RepresentationContent(ctx context.Context, representationID string) (Content, error) {
	representation, err := service.representations.Representation(ctx, representationID)
	if err != nil {
		return Content{}, err
	}
	if representation.Disclosure == representationregistry.DisclosureWithheld {
		return Content{}, ErrContentWithheld
	}
	reader, err := service.store.Open(ctx, artifactstore.OpenRequest{Locator: representation.Locator, ExpectedDigest: representation.Digest})
	if err != nil {
		return Content{}, fmt.Errorf("open artifact representation: %w", err)
	}
	return Content{Reader: reader, Digest: representation.Digest, Size: representation.Size, MediaType: representation.MediaType, FileName: representation.RepresentationID, RepresentationID: representation.RepresentationID}, nil
}

func (service *Service) Extract(ctx context.Context, reference artifactregistry.VersionRef, idempotencyKey string) (artifactderive.Result, error) {
	result, err := service.derivation.Derive(ctx, artifactderive.Request{
		Artifact: reference, OperationID: stableID("operation_", "extract\x00"+idempotencyKey),
		IdempotencyKey: idempotencyKey, PolicyVersion: PolicyVersion,
	})
	if err != nil {
		return artifactderive.Result{}, err
	}
	representations := make([]artifactExtractedRepresentation, len(result.Representations))
	for index, representation := range result.Representations {
		representations[index] = artifactExtractedRepresentation{
			RepresentationID: representation.RepresentationID,
			Kind:             string(representation.Kind),
			Digest:           representation.Digest,
		}
	}
	data := artifactExtractedEventData{Artifact: reference, SupportState: string(result.Support.State), Representations: representations}
	if err := service.recordAuditEvent(ctx, "artifact.extracted", identity.Deterministic("operation_", "audit\x00extract\x00"+idempotencyKey), idempotencyKey, reference.ArtifactID, data); err != nil {
		return artifactderive.Result{}, fmt.Errorf("record artifact.extracted audit event: %w", err)
	}
	return result, nil
}

type artifactIngestedEventData struct {
	Artifact          artifactregistry.VersionRef `json:"artifact"`
	BaseVersion       *uint64                     `json:"baseVersion,omitempty"`
	BlobDigest        string                      `json:"blobDigest"`
	Size              int64                       `json:"size"`
	DetectedMediaType string                      `json:"detectedMediaType"`
	Status            artifactregistry.Status     `json:"status"`
}

type artifactAttachedEventData struct {
	BindingID      string                      `json:"bindingId"`
	BindingVersion uint64                      `json:"bindingVersion"`
	Artifact       artifactregistry.VersionRef `json:"artifact"`
	Target         artifactbinding.Target      `json:"target"`
}

type artifactExtractedRepresentation struct {
	RepresentationID string `json:"representationId"`
	Kind             string `json:"kind"`
	Digest           string `json:"digest"`
}

type artifactExtractedEventData struct {
	Artifact        artifactregistry.VersionRef       `json:"artifact"`
	SupportState    string                            `json:"supportState"`
	Representations []artifactExtractedRepresentation `json:"representations"`
}

func (service *Service) recordAuditEvent(ctx context.Context, kind, operationID, commandID, correlationID string, data any) error {
	encoded, err := json.Marshal(data)
	if err != nil {
		return fmt.Errorf("encode event data: %w", err)
	}
	_, err = service.audit.Append(ctx, statestore.PendingEvent{
		SchemaVersion: 1, ID: identity.Random("event_"), AggregateType: statestore.AggregateOperation,
		AggregateID: operationID, ExpectedRevision: 0, Kind: kind, OccurredAt: service.now().UTC().Round(0),
		CorrelationID: correlationID, CommandID: commandID,
		Actor: statestore.Actor{Type: statestore.ActorUser, ID: "local-user"},
		Data:  encoded, Metadata: json.RawMessage(`{}`),
	})
	return err
}

func (service *Service) Diff(ctx context.Context, artifactID string, from, to uint64) (VersionDiff, error) {
	left, err := service.Show(ctx, artifactID, from)
	if err != nil {
		return VersionDiff{}, err
	}
	right, err := service.Show(ctx, artifactID, to)
	if err != nil {
		return VersionDiff{}, err
	}
	changed := make([]string, 0)
	if left.Artifact.BlobDigest != right.Artifact.BlobDigest {
		changed = append(changed, "content")
	}
	if left.Artifact.DeclaredMediaType != right.Artifact.DeclaredMediaType || left.Artifact.DetectedMediaType != right.Artifact.DetectedMediaType {
		changed = append(changed, "mediaType")
	}
	if left.Artifact.Sensitivity != right.Artifact.Sensitivity {
		changed = append(changed, "sensitivity")
	}
	if !slices.Equal(left.Artifact.Roles, right.Artifact.Roles) {
		changed = append(changed, "roles")
	}
	if !slices.Equal(left.Artifact.Tags, right.Artifact.Tags) {
		changed = append(changed, "tags")
	}
	return VersionDiff{
		ArtifactID: artifactID, From: left.Artifact.Version, To: right.Artifact.Version, Changed: changed,
		FromDigest: left.Artifact.BlobDigest, ToDigest: right.Artifact.BlobDigest,
		Representations: map[string][]string{"from": representationKinds(left.Representations), "to": representationKinds(right.Representations)},
	}, nil
}

func (service *Service) Lint(ctx context.Context, reference artifactregistry.VersionRef) (LintResult, error) {
	view, err := service.Show(ctx, reference.ArtifactID, reference.Version)
	if err != nil {
		return LintResult{}, err
	}
	issues := make([]LintIssue, 0)
	if view.Artifact.Status != artifactregistry.StatusStored {
		issues = append(issues, LintIssue{Code: "ARTIFACT_" + strings.ToUpper(string(view.Artifact.Status)), Message: "artifact is not safely inspectable"})
	}
	if view.Freshness != artifactlineage.FreshnessCurrent {
		issues = append(issues, LintIssue{Code: "ARTIFACT_" + strings.ToUpper(string(view.Freshness)), Message: "artifact freshness requires reconciliation"})
	}
	if len(view.Representations) == 0 {
		issues = append(issues, LintIssue{Code: "REPRESENTATION_MISSING", Message: "artifact has no derived representation"})
	}
	for _, representation := range view.Representations {
		for _, diagnostic := range representation.Diagnostics {
			issues = append(issues, LintIssue{Code: "REPRESENTATION_DIAGNOSTIC", Message: diagnostic})
		}
	}
	return LintResult{Artifact: reference, Valid: len(issues) == 0, Issues: issues}, nil
}

func (service *Service) Impact(ctx context.Context, request lateevidence.Request) (impactassessment.Assessment, error) {
	return service.impact.Assess(ctx, request)
}

func (service *Service) view(ctx context.Context, artifact artifactregistry.ArtifactVersion) (ArtifactView, error) {
	reference := artifactregistry.VersionRef{ArtifactID: artifact.ArtifactID, Version: artifact.Version}
	freshness, err := service.lineage.Freshness(ctx, reference)
	if err != nil {
		return ArtifactView{}, err
	}
	representations, err := service.representations.ForArtifact(ctx, reference)
	if err != nil {
		return ArtifactView{}, err
	}
	return ArtifactView{Artifact: artifact, Freshness: freshness, Representations: representations}, nil
}

func representationKinds(values []representationregistry.Representation) []string {
	result := make([]string, len(values))
	for index, value := range values {
		result[index] = string(value.Kind)
	}
	sort.Strings(result)
	return result
}

func stableID(prefix, seed string) string {
	digest := sha256.Sum256([]byte(seed))
	encoded := base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(digest[:])
	return prefix + encoded[:26]
}
