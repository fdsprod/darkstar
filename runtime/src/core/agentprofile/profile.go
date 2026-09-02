// Package agentprofile defines immutable agent execution profiles and
// deterministic provider selection policy.
package agentprofile

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"darkstar/src/ports/provider"
)

var ErrInvalidProfile = errors.New("invalid agent profile")

// Role groups the human-readable responsibility and the instructions used for
// that responsibility. Profiles describe behavior; they do not own lifecycle
// state.
type Role struct {
	Name         string
	Instructions string
}

// CapabilityRequirements separates hard admission requirements from ranking
// preferences. A preferred capability can never silently become required.
type CapabilityRequirements struct {
	Required  []string
	Preferred []string
}

// ProviderEligibility is a closed choice. An empty provider list therefore
// cannot ambiguously mean either no providers or every provider.
type ProviderEligibility interface {
	isProviderEligibility()
}

// AnyCompatible permits every provider that satisfies the profile.
type AnyCompatible struct{}

func (AnyCompatible) isProviderEligibility() {}

// AllowlistedProviders permits only the named configured provider identities.
type AllowlistedProviders struct {
	ProviderIDs []string
}

func (AllowlistedProviders) isProviderEligibility() {}

// ProviderPolicy defines eligibility and ordered provider preferences.
// Preferred contains configured provider identities, not adapter family names.
type ProviderPolicy struct {
	Eligibility ProviderEligibility
	Preferred   []string
}

// InteractionPermissions are independent ceilings for provider-requested
// command, file, and tool effects.
type InteractionPermissions struct {
	Command provider.InteractionPolicy
	File    provider.InteractionPolicy
	Tool    provider.InteractionPolicy
}

// Permissions is the profile's provider access ceiling. A later project,
// workflow, attempt, or approval layer may restrict it but never widen it.
type Permissions struct {
	Workspace   provider.AccessClass
	Network     provider.NetworkPolicy
	Interaction InteractionPermissions
}

// Limits freezes the resource ceilings used to prepare an attempt.
type Limits struct {
	ContextTokens     int64
	OutputTokens      int64
	CostUnits         int64
	Timeout           time.Duration
	CancellationGrace time.Duration
}

// Definition is the declarative input used to construct an immutable Profile.
type Definition struct {
	ID           string
	Version      string
	Role         Role
	Capabilities CapabilityRequirements
	Providers    ProviderPolicy
	Permissions  Permissions
	Limits       Limits
}

// Profile is a validated, canonical execution profile. Its definition is kept
// private so capability sets and provider allowlists cannot be mutated after
// the fingerprint is computed.
type Profile struct {
	definition  Definition
	fingerprint string
}

// New validates and freezes a profile definition.
func New(definition Definition) (Profile, error) {
	normalized, err := normalizeDefinition(definition)
	if err != nil {
		return Profile{}, err
	}
	fingerprint, err := profileFingerprint(normalized)
	if err != nil {
		return Profile{}, err
	}
	return Profile{definition: normalized, fingerprint: fingerprint}, nil
}

// Definition returns a defensive copy of the canonical definition.
func (profile Profile) Definition() Definition {
	return cloneDefinition(profile.definition)
}

// Fingerprint identifies the exact canonical profile policy and instructions.
func (profile Profile) Fingerprint() string {
	return profile.fingerprint
}

func normalizeDefinition(definition Definition) (Definition, error) {
	for _, field := range []struct {
		name  string
		value string
	}{
		{"profile ID", definition.ID}, {"profile version", definition.Version},
		{"role name", definition.Role.Name}, {"role instructions", definition.Role.Instructions},
	} {
		if strings.TrimSpace(field.value) == "" || field.value != strings.TrimSpace(field.value) {
			return Definition{}, fmt.Errorf("%w: %s is required and must be trimmed", ErrInvalidProfile, field.name)
		}
	}

	required, err := canonicalSet("required capability", definition.Capabilities.Required)
	if err != nil {
		return Definition{}, err
	}
	preferred, err := canonicalSet("preferred capability", definition.Capabilities.Preferred)
	if err != nil {
		return Definition{}, err
	}
	requiredSet := make(map[string]struct{}, len(required))
	for _, capability := range required {
		requiredSet[capability] = struct{}{}
	}
	for _, capability := range preferred {
		if _, duplicate := requiredSet[capability]; duplicate {
			return Definition{}, fmt.Errorf("%w: capability %q cannot be both required and preferred", ErrInvalidProfile, capability)
		}
	}
	definition.Capabilities = CapabilityRequirements{Required: required, Preferred: preferred}

	preferredProviders, err := orderedUnique("preferred provider", definition.Providers.Preferred)
	if err != nil {
		return Definition{}, err
	}
	switch eligibility := definition.Providers.Eligibility.(type) {
	case AnyCompatible:
		definition.Providers.Eligibility = AnyCompatible{}
	case AllowlistedProviders:
		allowlist, setErr := canonicalSet("allowlisted provider", eligibility.ProviderIDs)
		if setErr != nil {
			return Definition{}, setErr
		}
		if len(allowlist) == 0 {
			return Definition{}, fmt.Errorf("%w: provider allowlist cannot be empty", ErrInvalidProfile)
		}
		allowed := make(map[string]struct{}, len(allowlist))
		for _, providerID := range allowlist {
			allowed[providerID] = struct{}{}
		}
		for _, providerID := range preferredProviders {
			if _, ok := allowed[providerID]; !ok {
				return Definition{}, fmt.Errorf("%w: preferred provider %q is not allowlisted", ErrInvalidProfile, providerID)
			}
		}
		definition.Providers.Eligibility = AllowlistedProviders{ProviderIDs: allowlist}
	default:
		return Definition{}, fmt.Errorf("%w: provider eligibility is required", ErrInvalidProfile)
	}
	definition.Providers.Preferred = preferredProviders

	if !validAccess(definition.Permissions.Workspace) {
		return Definition{}, fmt.Errorf("%w: workspace permission %q is invalid", ErrInvalidProfile, definition.Permissions.Workspace)
	}
	if !validNetwork(definition.Permissions.Network) {
		return Definition{}, fmt.Errorf("%w: network permission %q is invalid", ErrInvalidProfile, definition.Permissions.Network)
	}
	for name, policy := range map[string]provider.InteractionPolicy{
		"command": definition.Permissions.Interaction.Command,
		"file":    definition.Permissions.Interaction.File,
		"tool":    definition.Permissions.Interaction.Tool,
	} {
		if !validInteraction(policy) {
			return Definition{}, fmt.Errorf("%w: %s interaction permission %q is invalid", ErrInvalidProfile, name, policy)
		}
	}
	if definition.Limits.ContextTokens <= 0 || definition.Limits.OutputTokens <= 0 || definition.Limits.CostUnits < 0 || definition.Limits.Timeout <= 0 || definition.Limits.CancellationGrace <= 0 {
		return Definition{}, fmt.Errorf("%w: context/output tokens, timeout, and cancellation grace must be positive; cost units cannot be negative", ErrInvalidProfile)
	}
	return definition, nil
}

func canonicalSet(kind string, values []string) ([]string, error) {
	result := append([]string(nil), values...)
	sort.Strings(result)
	for index, value := range result {
		if strings.TrimSpace(value) == "" || value != strings.TrimSpace(value) {
			return nil, fmt.Errorf("%w: %s values must be non-empty and trimmed", ErrInvalidProfile, kind)
		}
		if index > 0 && result[index-1] == value {
			return nil, fmt.Errorf("%w: duplicate %s %q", ErrInvalidProfile, kind, value)
		}
	}
	if result == nil {
		result = []string{}
	}
	return result, nil
}

func orderedUnique(kind string, values []string) ([]string, error) {
	result := append([]string(nil), values...)
	seen := make(map[string]struct{}, len(result))
	for _, value := range result {
		if strings.TrimSpace(value) == "" || value != strings.TrimSpace(value) {
			return nil, fmt.Errorf("%w: %s values must be non-empty and trimmed", ErrInvalidProfile, kind)
		}
		if _, duplicate := seen[value]; duplicate {
			return nil, fmt.Errorf("%w: duplicate %s %q", ErrInvalidProfile, kind, value)
		}
		seen[value] = struct{}{}
	}
	if result == nil {
		result = []string{}
	}
	return result, nil
}

func validAccess(value provider.AccessClass) bool {
	return value == provider.AccessReadOnly || value == provider.AccessWorkspaceWrite
}

func validNetwork(value provider.NetworkPolicy) bool {
	return value == provider.NetworkDenied || value == provider.NetworkRestricted || value == provider.NetworkAllowed
}

func validInteraction(value provider.InteractionPolicy) bool {
	return value == provider.InteractionDeny || value == provider.InteractionAsk || value == provider.InteractionAllow
}

func profileFingerprint(definition Definition) (string, error) {
	type eligibilityJSON struct {
		Kind        string   `json:"kind"`
		ProviderIDs []string `json:"providerIds"`
	}
	eligibility := eligibilityJSON{ProviderIDs: []string{}}
	switch value := definition.Providers.Eligibility.(type) {
	case AnyCompatible:
		eligibility.Kind = "any_compatible"
	case AllowlistedProviders:
		eligibility.Kind = "allowlist"
		eligibility.ProviderIDs = value.ProviderIDs
	}
	payload := struct {
		ID           string                 `json:"id"`
		Version      string                 `json:"version"`
		Role         Role                   `json:"role"`
		Capabilities CapabilityRequirements `json:"capabilities"`
		Providers    struct {
			Eligibility eligibilityJSON `json:"eligibility"`
			Preferred   []string        `json:"preferred"`
		} `json:"providers"`
		Permissions Permissions `json:"permissions"`
		Limits      struct {
			ContextTokens     int64 `json:"contextTokens"`
			OutputTokens      int64 `json:"outputTokens"`
			CostUnits         int64 `json:"costUnits"`
			TimeoutNanos      int64 `json:"timeoutNanos"`
			CancellationNanos int64 `json:"cancellationGraceNanos"`
		} `json:"limits"`
	}{ID: definition.ID, Version: definition.Version, Role: definition.Role, Capabilities: definition.Capabilities, Permissions: definition.Permissions}
	payload.Providers.Eligibility = eligibility
	payload.Providers.Preferred = definition.Providers.Preferred
	payload.Limits.ContextTokens = definition.Limits.ContextTokens
	payload.Limits.OutputTokens = definition.Limits.OutputTokens
	payload.Limits.CostUnits = definition.Limits.CostUnits
	payload.Limits.TimeoutNanos = int64(definition.Limits.Timeout)
	payload.Limits.CancellationNanos = int64(definition.Limits.CancellationGrace)
	content, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("fingerprint agent profile: %w", err)
	}
	digest := sha256.Sum256(content)
	return hex.EncodeToString(digest[:]), nil
}

func cloneDefinition(definition Definition) Definition {
	definition.Capabilities.Required = append([]string(nil), definition.Capabilities.Required...)
	definition.Capabilities.Preferred = append([]string(nil), definition.Capabilities.Preferred...)
	definition.Providers.Preferred = append([]string(nil), definition.Providers.Preferred...)
	if eligibility, ok := definition.Providers.Eligibility.(AllowlistedProviders); ok {
		definition.Providers.Eligibility = AllowlistedProviders{ProviderIDs: append([]string(nil), eligibility.ProviderIDs...)}
	}
	return definition
}
