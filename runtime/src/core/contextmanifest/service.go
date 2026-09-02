// Package contextmanifest deterministically selects representation revisions and
// freezes every other attempt input into one immutable manifest.
package contextmanifest

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	manifestport "darkstar/src/ports/contextmanifest"
	"darkstar/src/ports/representationregistry"
)

var (
	ErrRequiredUnavailable   = errors.New("required context representation is unavailable")
	ErrRequiredExceedsBudget = errors.New("required context exceeds budget")
)

type CandidateState string

const (
	CandidateEligible          CandidateState = "eligible"
	CandidateUnsupported       CandidateState = "unsupported"
	CandidateSensitivityDenied CandidateState = "sensitivity_denied"
	CandidateCapabilityMissing CandidateState = "capability_missing"
	CandidateStale             CandidateState = "stale"
)

type Candidate struct {
	RepresentationID string
	Required         bool
	Rank             int
	Arrival          uint64
	State            CandidateState
}

type Request struct {
	RunID          string
	NodeID         string
	AttemptID      string
	IdempotencyKey string
	PolicyVersion  string
	Budget         int64
	Reserved       int64
	Candidates     []Candidate
	Instructions   []manifestport.DigestRef
	Schemas        []manifestport.DigestRef
	Permissions    []string
	Workspace      manifestport.Workspace
	Capabilities   []manifestport.DigestRef
}

type Service struct {
	representations representationregistry.Registry
	manifests       manifestport.Store
	now             func() time.Time
}

func New(representations representationregistry.Registry, manifests manifestport.Store) (*Service, error) {
	if representations == nil || manifests == nil {
		return nil, errors.New("representation registry and context manifest store are required")
	}
	return &Service{representations: representations, manifests: manifests, now: time.Now}, nil
}

func (service *Service) Prepare(ctx context.Context, request Request) (manifestport.Manifest, bool, error) {
	normalized, err := normalizeRequest(request)
	if err != nil {
		return manifestport.Manifest{}, false, err
	}
	entries, omissions, err := service.selectEntries(ctx, normalized)
	if err != nil {
		return manifestport.Manifest{}, false, err
	}
	manifest := manifestport.Manifest{
		ManifestID: stableManifestID(normalized.AttemptID), RunID: normalized.RunID, NodeID: normalized.NodeID,
		AttemptID: normalized.AttemptID, PolicyVersion: normalized.PolicyVersion, Budget: normalized.Budget,
		Reserved: normalized.Reserved, Entries: entries, Omissions: omissions, Instructions: normalized.Instructions,
		Schemas: normalized.Schemas, Permissions: normalized.Permissions, Workspace: normalized.Workspace,
		Capabilities: normalized.Capabilities, FrozenAt: service.now().UTC(),
	}
	manifest.Digest, err = manifestDigest(manifest)
	if err != nil {
		return manifestport.Manifest{}, false, err
	}
	existing, err := service.manifests.ManifestForAttempt(ctx, normalized.AttemptID)
	if err == nil {
		if existing.Digest != manifest.Digest {
			return manifestport.Manifest{}, false, manifestport.ErrFrozen
		}
		return existing, false, nil
	}
	if !errors.Is(err, manifestport.ErrNotFound) {
		return manifestport.Manifest{}, false, fmt.Errorf("read frozen attempt context: %w", err)
	}
	stored, created, err := service.manifests.StoreManifest(ctx, manifest, normalized.IdempotencyKey)
	if err != nil {
		return manifestport.Manifest{}, false, fmt.Errorf("freeze attempt context: %w", err)
	}
	return stored, created, nil
}

func (service *Service) selectEntries(ctx context.Context, request Request) ([]manifestport.Entry, []manifestport.Omission, error) {
	entries := make([]manifestport.Entry, 0, len(request.Candidates))
	omissions := make([]manifestport.Omission, 0)
	used := request.Reserved
	type resolvedCandidate struct {
		candidate      Candidate
		representation representationregistry.Representation
		missing        bool
	}
	resolved := make([]resolvedCandidate, 0, len(request.Candidates))
	for _, candidate := range request.Candidates {
		representation, err := service.representations.Representation(ctx, candidate.RepresentationID)
		if err != nil {
			if errors.Is(err, representationregistry.ErrNotFound) {
				if candidate.Required {
					return nil, nil, fmt.Errorf("%w: %s", ErrRequiredUnavailable, candidate.RepresentationID)
				}
				resolved = append(resolved, resolvedCandidate{candidate: candidate, missing: true})
				continue
			}
			return nil, nil, fmt.Errorf("read context representation %s: %w", candidate.RepresentationID, err)
		}
		resolved = append(resolved, resolvedCandidate{candidate: candidate, representation: representation})
	}
	sort.Slice(resolved, func(left, right int) bool {
		a, b := resolved[left], resolved[right]
		if a.candidate.Required != b.candidate.Required {
			return a.candidate.Required
		}
		if a.candidate.Rank != b.candidate.Rank {
			return a.candidate.Rank < b.candidate.Rank
		}
		if a.candidate.Arrival != b.candidate.Arrival {
			return a.candidate.Arrival < b.candidate.Arrival
		}
		if a.representation.Artifact.ArtifactID != b.representation.Artifact.ArtifactID {
			return a.representation.Artifact.ArtifactID < b.representation.Artifact.ArtifactID
		}
		return a.representation.RepresentationID < b.representation.RepresentationID
	})
	for _, item := range resolved {
		candidate, representation := item.candidate, item.representation
		if item.missing {
			omissions = append(omissions, manifestport.Omission{RepresentationID: candidate.RepresentationID, Reason: manifestport.OmissionUnsupported})
			continue
		}
		if candidate.State != CandidateEligible || representation.Disclosure == representationregistry.DisclosureWithheld {
			reason := omissionReason(candidate.State, representation.Disclosure)
			if candidate.Required {
				return nil, nil, fmt.Errorf("%w: %s (%s)", ErrRequiredUnavailable, candidate.RepresentationID, reason)
			}
			omissions = append(omissions, manifestport.Omission{RepresentationID: candidate.RepresentationID, Reason: reason})
			continue
		}
		if used+representation.TokenEstimate > request.Budget {
			if candidate.Required {
				return nil, nil, fmt.Errorf("%w: %s", ErrRequiredExceedsBudget, candidate.RepresentationID)
			}
			omissions = append(omissions, manifestport.Omission{RepresentationID: candidate.RepresentationID, Reason: manifestport.OmissionBudget})
			continue
		}
		entries = append(entries, manifestport.Entry{
			ArtifactID: representation.Artifact.ArtifactID, ArtifactVersion: representation.Artifact.Version, RepresentationID: representation.RepresentationID,
			Digest: representation.Digest, Required: candidate.Required, TokenEstimate: representation.TokenEstimate,
		})
		used += representation.TokenEstimate
	}
	return entries, omissions, nil
}

func normalizeRequest(request Request) (Request, error) {
	if strings.TrimSpace(request.RunID) == "" || strings.TrimSpace(request.NodeID) == "" || strings.TrimSpace(request.AttemptID) == "" || strings.TrimSpace(request.IdempotencyKey) == "" || strings.TrimSpace(request.PolicyVersion) == "" {
		return request, errors.New("run, node, attempt, idempotency, and policy identity are required")
	}
	if request.Budget < 0 || request.Reserved < 0 || request.Reserved > request.Budget {
		return request, errors.New("context token budget and reservation are invalid")
	}
	if strings.TrimSpace(request.Workspace.ID) == "" || !validDigest(request.Workspace.Digest) {
		return request, errors.New("workspace identity, digest, and access are required")
	}
	switch request.Workspace.Access {
	case manifestport.WorkspaceReadOnly, manifestport.WorkspaceWrite:
	default:
		return request, errors.New("workspace access is invalid")
	}
	var err error
	if request.Instructions, err = canonicalDigestRefs("instruction", request.Instructions); err != nil {
		return request, err
	}
	if request.Schemas, err = canonicalDigestRefs("schema", request.Schemas); err != nil {
		return request, err
	}
	if request.Capabilities, err = canonicalDigestRefs("capability", request.Capabilities); err != nil {
		return request, err
	}
	request.Permissions, err = canonicalStrings("permission", request.Permissions)
	if err != nil {
		return request, err
	}
	request.Candidates = append([]Candidate(nil), request.Candidates...)
	seen := make(map[string]struct{}, len(request.Candidates))
	for _, candidate := range request.Candidates {
		if strings.TrimSpace(candidate.RepresentationID) == "" || candidate.Rank < 0 || candidate.Arrival == 0 {
			return request, errors.New("context candidate identity, rank, and arrival are invalid")
		}
		switch candidate.State {
		case CandidateEligible, CandidateUnsupported, CandidateSensitivityDenied, CandidateCapabilityMissing, CandidateStale:
		default:
			return request, fmt.Errorf("invalid context candidate state %q", candidate.State)
		}
		if _, duplicate := seen[candidate.RepresentationID]; duplicate {
			return request, fmt.Errorf("duplicate context candidate %q", candidate.RepresentationID)
		}
		seen[candidate.RepresentationID] = struct{}{}
	}
	return request, nil
}

func canonicalDigestRefs(kind string, values []manifestport.DigestRef) ([]manifestport.DigestRef, error) {
	result := append([]manifestport.DigestRef(nil), values...)
	sort.Slice(result, func(i, j int) bool { return result[i].ID < result[j].ID })
	for index, value := range result {
		if strings.TrimSpace(value.ID) == "" || !validDigest(value.Digest) {
			return nil, fmt.Errorf("%s digest reference is invalid", kind)
		}
		if index > 0 && result[index-1].ID == value.ID {
			return nil, fmt.Errorf("duplicate %s reference %q", kind, value.ID)
		}
	}
	if result == nil {
		result = []manifestport.DigestRef{}
	}
	return result, nil
}

func canonicalStrings(kind string, values []string) ([]string, error) {
	result := append([]string(nil), values...)
	sort.Strings(result)
	for index, value := range result {
		if strings.TrimSpace(value) == "" || value != strings.TrimSpace(value) {
			return nil, fmt.Errorf("%s values must be non-empty and trimmed", kind)
		}
		if index > 0 && result[index-1] == value {
			return nil, fmt.Errorf("duplicate %s %q", kind, value)
		}
	}
	if result == nil {
		result = []string{}
	}
	return result, nil
}

func omissionReason(state CandidateState, disclosure representationregistry.Disclosure) manifestport.OmissionReason {
	if disclosure == representationregistry.DisclosureWithheld || state == CandidateSensitivityDenied {
		return manifestport.OmissionSensitivity
	}
	switch state {
	case CandidateCapabilityMissing:
		return manifestport.OmissionCapability
	case CandidateStale:
		return manifestport.OmissionStale
	default:
		return manifestport.OmissionUnsupported
	}
}

func manifestDigest(manifest manifestport.Manifest) (string, error) {
	payload := struct {
		RunID         string                   `json:"runId"`
		NodeID        string                   `json:"nodeId"`
		AttemptID     string                   `json:"attemptId"`
		PolicyVersion string                   `json:"policyVersion"`
		Budget        int64                    `json:"budget"`
		Reserved      int64                    `json:"reservedTokens"`
		Entries       []manifestport.Entry     `json:"entries"`
		Omissions     []manifestport.Omission  `json:"omissions"`
		Instructions  []manifestport.DigestRef `json:"instructions"`
		Schemas       []manifestport.DigestRef `json:"schemas"`
		Permissions   []string                 `json:"permissions"`
		Workspace     manifestport.Workspace   `json:"workspace"`
		Capabilities  []manifestport.DigestRef `json:"capabilities"`
	}{manifest.RunID, manifest.NodeID, manifest.AttemptID, manifest.PolicyVersion, manifest.Budget, manifest.Reserved,
		manifest.Entries, manifest.Omissions, manifest.Instructions, manifest.Schemas, manifest.Permissions, manifest.Workspace, manifest.Capabilities}
	content, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("encode context manifest digest: %w", err)
	}
	digest := sha256.Sum256(content)
	return hex.EncodeToString(digest[:]), nil
}

func stableManifestID(attemptID string) string {
	digest := sha256.Sum256([]byte("context-manifest\x00" + attemptID))
	return "manifest_" + hex.EncodeToString(digest[:16])
}

func validDigest(value string) bool {
	if len(value) != sha256.Size*2 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil && value == strings.ToLower(value)
}
