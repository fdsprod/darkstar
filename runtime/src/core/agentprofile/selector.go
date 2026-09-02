package agentprofile

import (
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strings"

	"darkstar/src/ports/provider"
)

var (
	ErrInvalidCandidates = errors.New("invalid provider candidates")
)

const (
	CapabilityWorkspaceWrite = "workspace_write"
	CapabilityInteractions   = "interactions"
)

// Candidate is one configured provider plus the exact observations made before
// scheduling. ID names the configured instance; Health.Provider and
// Capabilities.Provider name its adapter family.
type Candidate struct {
	ID           string
	Health       provider.Health
	Capabilities provider.CapabilityManifest
}

// RejectionCode is a closed provider admission failure vocabulary.
type RejectionCode string

const (
	RejectionPolicyDenied       RejectionCode = "policy_denied"
	RejectionHealthUnavailable  RejectionCode = "health_unavailable"
	RejectionObservationInvalid RejectionCode = "observation_invalid"
	RejectionCapabilityMissing  RejectionCode = "capability_missing"
)

// Rejection explains one independent reason a provider was not admitted.
type Rejection struct {
	Code       RejectionCode
	Capability string
	Detail     string
}

// Evaluation is the auditable compatibility and preference result for one
// candidate. Compatibility is derived from Rejections and is not stored as a
// second source of truth.
type Evaluation struct {
	ProviderID            string
	CapabilityFingerprint string
	Rejections            []Rejection
	UnavailablePreferred  []string
	preferenceRank        int
	preferredAvailable    int
}

// Compatible reports whether the candidate passed every admission rule.
func (evaluation Evaluation) Compatible() bool {
	return len(evaluation.Rejections) == 0
}

// SelectionResult is a closed success/unavailable choice.
type SelectionResult interface {
	isSelectionResult()
}

// Selected identifies one compatible provider and preserves all evaluations
// used to make the deterministic choice.
type Selected struct {
	ProfileID            string
	ProfileFingerprint   string
	ProviderID           string
	CapabilityManifest   provider.CapabilityManifest
	UnavailablePreferred []string
	Evaluations          []Evaluation
}

func (Selected) isSelectionResult() {}

// Unavailable proves that every configured provider was rejected.
type Unavailable struct {
	ProfileID          string
	ProfileFingerprint string
	Evaluations        []Evaluation
}

func (Unavailable) isSelectionResult() {}

// Select evaluates every candidate, then chooses by ordered provider
// preference, available preferred-capability count, and stable provider ID.
func Select(profile Profile, candidates []Candidate) (SelectionResult, error) {
	if profile.fingerprint == "" {
		return nil, fmt.Errorf("%w: profile was not constructed by New", ErrInvalidProfile)
	}
	if len(candidates) == 0 {
		return Unavailable{ProfileID: profile.definition.ID, ProfileFingerprint: profile.fingerprint, Evaluations: []Evaluation{}}, nil
	}

	seen := make(map[string]struct{}, len(candidates))
	evaluations := make([]Evaluation, 0, len(candidates))
	byID := make(map[string]Candidate, len(candidates))
	for _, candidate := range candidates {
		if strings.TrimSpace(candidate.ID) == "" || candidate.ID != strings.TrimSpace(candidate.ID) {
			return nil, fmt.Errorf("%w: candidate ID is required and must be trimmed", ErrInvalidCandidates)
		}
		if _, duplicate := seen[candidate.ID]; duplicate {
			return nil, fmt.Errorf("%w: duplicate candidate %q", ErrInvalidCandidates, candidate.ID)
		}
		seen[candidate.ID] = struct{}{}
		byID[candidate.ID] = candidate
		evaluations = append(evaluations, evaluate(profile.definition, candidate))
	}

	sort.Slice(evaluations, func(left, right int) bool {
		a, b := evaluations[left], evaluations[right]
		if a.Compatible() != b.Compatible() {
			return a.Compatible()
		}
		if a.preferenceRank != b.preferenceRank {
			return a.preferenceRank < b.preferenceRank
		}
		if a.preferredAvailable != b.preferredAvailable {
			return a.preferredAvailable > b.preferredAvailable
		}
		return a.ProviderID < b.ProviderID
	})
	publicEvaluations := cloneEvaluations(evaluations)
	if !evaluations[0].Compatible() {
		return Unavailable{ProfileID: profile.definition.ID, ProfileFingerprint: profile.fingerprint, Evaluations: publicEvaluations}, nil
	}
	winner := evaluations[0]
	return Selected{
		ProfileID: profile.definition.ID, ProfileFingerprint: profile.fingerprint, ProviderID: winner.ProviderID,
		CapabilityManifest:   cloneManifest(byID[winner.ProviderID].Capabilities),
		UnavailablePreferred: append([]string(nil), winner.UnavailablePreferred...), Evaluations: publicEvaluations,
	}, nil
}

func evaluate(profile Definition, candidate Candidate) Evaluation {
	evaluation := Evaluation{
		ProviderID: candidate.ID, CapabilityFingerprint: candidate.Capabilities.Fingerprint,
		Rejections: []Rejection{}, UnavailablePreferred: []string{}, preferenceRank: len(profile.Providers.Preferred),
	}
	for index, providerID := range profile.Providers.Preferred {
		if candidate.ID == providerID {
			evaluation.preferenceRank = index
			break
		}
	}
	if !eligible(profile.Providers.Eligibility, candidate.ID) {
		evaluation.Rejections = append(evaluation.Rejections, Rejection{Code: RejectionPolicyDenied, Detail: "provider is outside the profile eligibility policy"})
		return evaluation
	}
	if candidate.Health.State != provider.HealthAvailable && candidate.Health.State != provider.HealthDegraded {
		evaluation.Rejections = append(evaluation.Rejections, Rejection{Code: RejectionHealthUnavailable, Detail: string(candidate.Health.State)})
	}
	if reason := invalidObservations(candidate); reason != "" {
		evaluation.Rejections = append(evaluation.Rejections, Rejection{Code: RejectionObservationInvalid, Detail: reason})
		return evaluation
	}

	required := effectiveRequired(profile)
	for _, capability := range required {
		if !capabilityAvailable(candidate.Capabilities.Features[capability]) {
			evaluation.Rejections = append(evaluation.Rejections, Rejection{Code: RejectionCapabilityMissing, Capability: capability, Detail: capabilityReason(candidate.Capabilities.Features[capability])})
		}
	}
	for _, capability := range profile.Capabilities.Preferred {
		if capabilityAvailable(candidate.Capabilities.Features[capability]) {
			evaluation.preferredAvailable++
		} else {
			evaluation.UnavailablePreferred = append(evaluation.UnavailablePreferred, capability)
		}
	}
	return evaluation
}

func effectiveRequired(profile Definition) []string {
	values := append([]string(nil), profile.Capabilities.Required...)
	if profile.Permissions.Workspace == provider.AccessWorkspaceWrite {
		values = append(values, CapabilityWorkspaceWrite)
	}
	interactions := profile.Permissions.Interaction
	if profile.Permissions.Network != provider.NetworkDenied || interactions.Command != provider.InteractionDeny || interactions.File != provider.InteractionDeny || interactions.Tool != provider.InteractionDeny {
		values = append(values, CapabilityInteractions)
	}
	sort.Strings(values)
	result := values[:0]
	for _, value := range values {
		if len(result) == 0 || result[len(result)-1] != value {
			result = append(result, value)
		}
	}
	return result
}

func eligible(policy ProviderEligibility, providerID string) bool {
	switch policy := policy.(type) {
	case AnyCompatible:
		return true
	case AllowlistedProviders:
		index := sort.SearchStrings(policy.ProviderIDs, providerID)
		return index < len(policy.ProviderIDs) && policy.ProviderIDs[index] == providerID
	default:
		return false
	}
}

func invalidObservations(candidate Candidate) string {
	switch {
	case strings.TrimSpace(candidate.Health.Provider) == "":
		return "health omitted provider identity"
	case strings.TrimSpace(candidate.Capabilities.Provider) == "":
		return "capability manifest omitted provider identity"
	case candidate.Health.Provider != candidate.Capabilities.Provider:
		return "health and capability provider identities differ"
	case len(candidate.Capabilities.Fingerprint) != 64:
		return "capability fingerprint is not SHA-256 hex"
	case candidate.Capabilities.ObservedAt.IsZero():
		return "capability manifest omitted observation time"
	case candidate.Capabilities.Features == nil:
		return "capability manifest omitted features"
	}
	_, err := hex.DecodeString(candidate.Capabilities.Fingerprint)
	if err != nil || candidate.Capabilities.Fingerprint != strings.ToLower(candidate.Capabilities.Fingerprint) {
		return "capability fingerprint is not SHA-256 hex"
	}
	return ""
}

func capabilityAvailable(capability provider.Capability) bool {
	switch capability := capability.(type) {
	case provider.AvailableCapability:
		return true
	case *provider.AvailableCapability:
		return capability != nil
	default:
		return false
	}
}

func capabilityReason(capability provider.Capability) string {
	switch capability := capability.(type) {
	case provider.UnavailableCapability:
		return capability.Reason
	case *provider.UnavailableCapability:
		if capability != nil {
			return capability.Reason
		}
	}
	return "not declared"
}

func cloneEvaluations(values []Evaluation) []Evaluation {
	result := make([]Evaluation, len(values))
	for index, value := range values {
		value.Rejections = append([]Rejection(nil), value.Rejections...)
		value.UnavailablePreferred = append([]string(nil), value.UnavailablePreferred...)
		value.preferenceRank = 0
		value.preferredAvailable = 0
		result[index] = value
	}
	return result
}

func cloneManifest(manifest provider.CapabilityManifest) provider.CapabilityManifest {
	features := manifest.Features
	manifest.Features = make(map[string]provider.Capability, len(features))
	for name, capability := range features {
		switch capability := capability.(type) {
		case provider.AvailableCapability:
			metadata := make(map[string]string, len(capability.Metadata))
			for key, value := range capability.Metadata {
				metadata[key] = value
			}
			capability.Metadata = metadata
			manifest.Features[name] = capability
		default:
			manifest.Features[name] = capability
		}
	}
	return manifest
}
