// Package capabilityregistry validates capability snapshots and resolves attempt requirements.
package capabilityregistry

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"

	registryport "darkstar/src/ports/capabilityregistry"
)

type FailureCode string

const (
	FailureRequiredMissing     FailureCode = "CAPABILITY_REQUIRED_MISSING"
	FailureVersionMismatch     FailureCode = "CAPABILITY_VERSION_MISMATCH"
	FailureFingerprintChanged  FailureCode = "CAPABILITY_FINGERPRINT_CHANGED"
	FailureDependencyMissing   FailureCode = "CAPABILITY_DEPENDENCY_MISSING"
	FailurePolicyDenied        FailureCode = "CAPABILITY_POLICY_DENIED"
	FailureUnhealthy           FailureCode = "CAPABILITY_UNHEALTHY"
	FailureAmbiguous           FailureCode = "CAPABILITY_AMBIGUOUS"
	FailureInheritedNotAllowed FailureCode = "CAPABILITY_INHERITED_NOT_ALLOWED"
)

type ResolutionError struct {
	Code       FailureCode
	Capability string
	Detail     string
}

func (failure *ResolutionError) Error() string {
	if failure.Detail == "" {
		return fmt.Sprintf("%s: %s", failure.Code, failure.Capability)
	}
	return fmt.Sprintf("%s: %s: %s", failure.Code, failure.Capability, failure.Detail)
}

type RequirementMode string

const (
	RequirementRequired  RequirementMode = "required"
	RequirementPreferred RequirementMode = "preferred"
	RequirementOptional  RequirementMode = "optional"
)

type Alternative struct {
	Name        string
	Version     string
	Fingerprint string
}

type Requirement struct {
	Name            string
	Kind            registryport.Kind
	Mode            RequirementMode
	Version         string
	Fingerprint     string
	AcceptInherited bool
	Fallbacks       []Alternative
}

type PolicyDecision string

const (
	PolicyAllow PolicyDecision = "allow"
	PolicyDeny  PolicyDecision = "deny"
)

type Grant struct {
	Decision    PolicyDecision
	Permissions []string
}

type Request struct {
	Requirements    []Requirement
	Grants          map[string]Grant
	PolicyDigest    string
	HostFingerprint string
}

type Omission struct {
	Name   string      `json:"name"`
	Reason FailureCode `json:"reason"`
}

type Manifest struct {
	Selections      []registryport.Selection `json:"selections"`
	Omissions       []Omission               `json:"omissions"`
	PolicyDigest    string                   `json:"policyDigest"`
	HostFingerprint string                   `json:"hostFingerprint"`
	Digest          string                   `json:"digest"`
	FallbacksUsed   []string                 `json:"fallbacksUsed"`
}

func (manifest Manifest) Degraded() bool {
	if len(manifest.Omissions) != 0 || len(manifest.FallbacksUsed) != 0 {
		return true
	}
	for _, selection := range manifest.Selections {
		if selection.Class == registryport.ClassInherited {
			return true
		}
	}
	return false
}

type Resolver struct {
	records map[string][]registryport.Record
}

func Load(ctx context.Context, registry registryport.Registry) (*Resolver, error) {
	if registry == nil {
		return nil, errors.New("capability registry is required")
	}
	records, err := registry.Snapshot(ctx)
	if err != nil {
		return nil, fmt.Errorf("read capability registry snapshot: %w", err)
	}
	return New(records)
}

var (
	canonicalNamePattern = regexp.MustCompile(`^(?:darkstar|project|user|admin):[a-z0-9][a-z0-9._-]*$|^(?:plugin:[a-z0-9][a-z0-9._-]*/|mcp:[a-z0-9][a-z0-9._-]*/|codex-inherited:[a-z0-9][a-z0-9._-]*/)[a-z0-9][a-z0-9._/-]*$`)
	hexDigestPattern     = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

func New(records []registryport.Record) (*Resolver, error) {
	byName := make(map[string][]registryport.Record)
	ids := make(map[string]struct{}, len(records))
	classKeys := make(map[string]struct{}, len(records))
	kinds := make(map[string]registryport.Kind, len(records))
	for _, record := range records {
		normalized, err := normalizeRecord(record)
		if err != nil {
			return nil, err
		}
		if _, duplicate := ids[normalized.ID]; duplicate {
			return nil, resolutionFailure(FailureAmbiguous, normalized.Name, "duplicate record ID")
		}
		ids[normalized.ID] = struct{}{}
		if existingKind, exists := kinds[normalized.Name]; exists && existingKind != normalized.Kind {
			return nil, resolutionFailure(FailureAmbiguous, normalized.Name, "canonical name is declared with multiple kinds")
		}
		kinds[normalized.Name] = normalized.Kind
		classKey := normalized.Name + "\x00" + string(normalized.Class)
		if _, duplicate := classKeys[classKey]; duplicate {
			return nil, resolutionFailure(FailureAmbiguous, normalized.Name, "multiple records share the same provenance class")
		}
		classKeys[classKey] = struct{}{}
		byName[normalized.Name] = append(byName[normalized.Name], normalized)
	}
	for name := range byName {
		sort.Slice(byName[name], func(i, j int) bool {
			left, right := byName[name][i], byName[name][j]
			if classRank(left.Class) != classRank(right.Class) {
				return classRank(left.Class) < classRank(right.Class)
			}
			return left.ID < right.ID
		})
	}
	return &Resolver{records: byName}, nil
}

func (resolver *Resolver) Resolve(request Request) (Manifest, error) {
	if resolver == nil || strings.TrimSpace(request.PolicyDigest) == "" || strings.TrimSpace(request.HostFingerprint) == "" {
		return Manifest{}, errors.New("resolver, policy digest, and host fingerprint are required")
	}
	requirements, grants, err := normalizeRequest(request)
	if err != nil {
		return Manifest{}, err
	}
	selected := make(map[string]registryport.Selection)
	omissions := make([]Omission, 0)
	fallbacks := make([]string, 0)
	for _, requirement := range requirements {
		record, failure := resolver.resolveOne(requirement.Name, requirement.Kind, requirement.Version, requirement.Fingerprint, requirement.AcceptInherited, grants, map[string]bool{})
		usedFallback := ""
		if failure != nil {
			for _, fallback := range requirement.Fallbacks {
				record, failure = resolver.resolveOne(fallback.Name, requirement.Kind, fallback.Version, fallback.Fingerprint, false, grants, map[string]bool{})
				if failure == nil {
					usedFallback = requirement.Name + "->" + fallback.Name
					break
				}
			}
		}
		if failure != nil {
			if requirement.Mode == RequirementRequired {
				return Manifest{}, failure
			}
			omissions = append(omissions, Omission{Name: requirement.Name, Reason: failure.Code})
			continue
		}
		candidateSelections := cloneSelections(selected)
		resolver.addSelection(record, grants, candidateSelections)
		if err := resolver.addDependencies(record, grants, candidateSelections, map[string]bool{}); err != nil {
			if requirement.Mode == RequirementRequired {
				return Manifest{}, err
			}
			failure := err.(*ResolutionError)
			omissions = append(omissions, Omission{Name: requirement.Name, Reason: failure.Code})
			continue
		}
		selected = candidateSelections
		if usedFallback != "" {
			fallbacks = append(fallbacks, usedFallback)
		}
	}
	manifest := Manifest{
		Selections: make([]registryport.Selection, 0, len(selected)), Omissions: omissions,
		PolicyDigest: request.PolicyDigest, HostFingerprint: request.HostFingerprint, FallbacksUsed: fallbacks,
	}
	for _, selection := range selected {
		manifest.Selections = append(manifest.Selections, selection)
	}
	sort.Slice(manifest.Selections, func(i, j int) bool {
		if manifest.Selections[i].Name != manifest.Selections[j].Name {
			return manifest.Selections[i].Name < manifest.Selections[j].Name
		}
		return manifest.Selections[i].ID < manifest.Selections[j].ID
	})
	sort.Slice(manifest.Omissions, func(i, j int) bool { return manifest.Omissions[i].Name < manifest.Omissions[j].Name })
	sort.Strings(manifest.FallbacksUsed)
	manifest.Digest, err = manifestDigest(manifest)
	return manifest, err
}

func (resolver *Resolver) resolveOne(name string, kind registryport.Kind, version, fingerprint string, acceptInherited bool, grants map[string]Grant, visiting map[string]bool) (registryport.Record, *ResolutionError) {
	candidates := resolver.records[name]
	if len(candidates) == 0 {
		return registryport.Record{}, resolutionFailure(FailureRequiredMissing, name, "not registered")
	}
	kindMatches := filter(candidates, func(record registryport.Record) bool { return record.Kind == kind })
	if len(kindMatches) == 0 {
		return registryport.Record{}, resolutionFailure(FailureRequiredMissing, name, "kind does not match")
	}
	versionMatches := kindMatches
	if version != "" {
		versionMatches = filter(kindMatches, func(record registryport.Record) bool { return record.DeclaredVersion == version })
		if len(versionMatches) == 0 {
			return registryport.Record{}, resolutionFailure(FailureVersionMismatch, name, version)
		}
	}
	fingerprintMatches := versionMatches
	if fingerprint != "" {
		fingerprintMatches = filter(versionMatches, func(record registryport.Record) bool { return record.Fingerprint == fingerprint })
		if len(fingerprintMatches) == 0 {
			return registryport.Record{}, resolutionFailure(FailureFingerprintChanged, name, fingerprint)
		}
	}
	eligible := filter(fingerprintMatches, func(record registryport.Record) bool { return record.Class != registryport.ClassUnsupportedDiscovery })
	if len(eligible) == 0 {
		return registryport.Record{}, resolutionFailure(FailureRequiredMissing, name, "only unsupported discovery exists")
	}
	allowed := filter(eligible, func(record registryport.Record) bool { return grants[record.ID].Decision != PolicyDeny })
	if len(allowed) == 0 {
		return registryport.Record{}, resolutionFailure(FailurePolicyDenied, name, "all candidates denied")
	}
	available := filter(allowed, func(record registryport.Record) bool {
		return record.Availability == registryport.AvailabilityAvailable
	})
	if len(available) == 0 {
		return registryport.Record{}, resolutionFailure(FailureUnhealthy, name, "no candidate is available")
	}
	bestRank := classRank(available[0].Class)
	best := filter(available, func(record registryport.Record) bool { return classRank(record.Class) == bestRank })
	if len(best) != 1 {
		return registryport.Record{}, resolutionFailure(FailureAmbiguous, name, "multiple equally preferred candidates")
	}
	if best[0].Class == registryport.ClassInherited && !acceptInherited {
		return registryport.Record{}, resolutionFailure(FailureInheritedNotAllowed, name, "requirement did not opt in")
	}
	if visiting[name] {
		return registryport.Record{}, resolutionFailure(FailureDependencyMissing, name, "dependency cycle")
	}
	return best[0], nil
}

func (resolver *Resolver) addDependencies(record registryport.Record, grants map[string]Grant, selected map[string]registryport.Selection, visiting map[string]bool) error {
	if visiting[record.Name] {
		return resolutionFailure(FailureDependencyMissing, record.Name, "dependency cycle")
	}
	visiting[record.Name] = true
	defer delete(visiting, record.Name)
	for _, dependency := range record.Dependencies {
		resolved, failure := resolver.resolveOne(dependency, inferKind(resolver.records[dependency]), "", "", false, grants, visiting)
		if failure != nil {
			return resolutionFailure(FailureDependencyMissing, record.Name, dependency+": "+string(failure.Code))
		}
		resolver.addSelection(resolved, grants, selected)
		if err := resolver.addDependencies(resolved, grants, selected, visiting); err != nil {
			return err
		}
	}
	return nil
}

func (resolver *Resolver) addSelection(record registryport.Record, grants map[string]Grant, selected map[string]registryport.Selection) {
	permissions := append([]string(nil), grants[record.ID].Permissions...)
	sort.Strings(permissions)
	selected[record.ID] = registryport.Selection{
		ID: record.ID, Name: record.Name, Kind: record.Kind, Class: record.Class, Version: record.DeclaredVersion,
		Fingerprint: record.Fingerprint, Source: record.Source, Permissions: permissions,
	}
}

func normalizeRecord(record registryport.Record) (registryport.Record, error) {
	if record.SchemaVersion != 1 || strings.TrimSpace(record.ID) == "" || !canonicalNamePattern.MatchString(record.Name) || !hexDigestPattern.MatchString(record.Fingerprint) || record.ObservedAt.IsZero() {
		return record, fmt.Errorf("invalid capability record %q", record.ID)
	}
	if record.Kind != registryport.KindSkill && record.Kind != registryport.KindTool {
		return record, fmt.Errorf("invalid capability kind %q", record.Kind)
	}
	switch record.Class {
	case registryport.ClassGuaranteed, registryport.ClassRegistered, registryport.ClassInherited, registryport.ClassUnsupportedDiscovery:
	default:
		return record, fmt.Errorf("invalid capability class %q", record.Class)
	}
	switch record.Availability {
	case registryport.AvailabilityAvailable, registryport.AvailabilityUnavailable, registryport.AvailabilityUnhealthy:
	default:
		return record, fmt.Errorf("invalid capability availability %q", record.Availability)
	}
	if strings.TrimSpace(record.Source.Type) == "" || strings.TrimSpace(record.Source.Locator) == "" {
		return record, fmt.Errorf("capability %q source is required", record.Name)
	}
	dependencies, err := canonicalNames(record.Dependencies)
	if err != nil {
		return record, fmt.Errorf("capability %q dependencies: %w", record.Name, err)
	}
	record.Dependencies = dependencies
	record.ObservedAt = record.ObservedAt.UTC()
	return record, nil
}

func normalizeRequest(request Request) ([]Requirement, map[string]Grant, error) {
	requirements := append([]Requirement(nil), request.Requirements...)
	seen := make(map[string]struct{}, len(requirements))
	for index := range requirements {
		requirement := &requirements[index]
		if !canonicalNamePattern.MatchString(requirement.Name) || (requirement.Kind != registryport.KindSkill && requirement.Kind != registryport.KindTool) {
			return nil, nil, fmt.Errorf("invalid capability requirement %q", requirement.Name)
		}
		if requirement.Mode == "" {
			requirement.Mode = RequirementRequired
		}
		if requirement.Mode != RequirementRequired && requirement.Mode != RequirementPreferred && requirement.Mode != RequirementOptional {
			return nil, nil, fmt.Errorf("invalid requirement mode %q", requirement.Mode)
		}
		key := requirement.Name + "\x00" + string(requirement.Kind)
		if _, duplicate := seen[key]; duplicate {
			return nil, nil, fmt.Errorf("duplicate capability requirement %q", requirement.Name)
		}
		seen[key] = struct{}{}
		fallbackNames := make(map[string]struct{}, len(requirement.Fallbacks))
		for _, fallback := range requirement.Fallbacks {
			if !canonicalNamePattern.MatchString(fallback.Name) || fallback.Name == requirement.Name {
				return nil, nil, fmt.Errorf("invalid fallback for %q", requirement.Name)
			}
			if _, duplicate := fallbackNames[fallback.Name]; duplicate {
				return nil, nil, fmt.Errorf("duplicate fallback %q", fallback.Name)
			}
			fallbackNames[fallback.Name] = struct{}{}
		}
	}
	grants := make(map[string]Grant, len(request.Grants))
	for id, grant := range request.Grants {
		if strings.TrimSpace(id) == "" || (grant.Decision != PolicyAllow && grant.Decision != PolicyDeny) {
			return nil, nil, errors.New("capability grants require an ID and explicit allow or deny")
		}
		permissions, err := canonicalStrings(grant.Permissions)
		if err != nil {
			return nil, nil, fmt.Errorf("grant %q: %w", id, err)
		}
		grant.Permissions = permissions
		grants[id] = grant
	}
	return requirements, grants, nil
}

func canonicalNames(values []string) ([]string, error) {
	result := append([]string(nil), values...)
	sort.Strings(result)
	for index, value := range result {
		if !canonicalNamePattern.MatchString(value) {
			return nil, fmt.Errorf("invalid canonical name %q", value)
		}
		if index > 0 && result[index-1] == value {
			return nil, fmt.Errorf("duplicate canonical name %q", value)
		}
	}
	if result == nil {
		result = []string{}
	}
	return result, nil
}

func canonicalStrings(values []string) ([]string, error) {
	result := append([]string(nil), values...)
	sort.Strings(result)
	for index, value := range result {
		if strings.TrimSpace(value) == "" || value != strings.TrimSpace(value) {
			return nil, errors.New("permission values must be non-empty and trimmed")
		}
		if index > 0 && result[index-1] == value {
			return nil, fmt.Errorf("duplicate permission %q", value)
		}
	}
	if result == nil {
		result = []string{}
	}
	return result, nil
}

func manifestDigest(manifest Manifest) (string, error) {
	manifest.Digest = ""
	content, err := json.Marshal(manifest)
	if err != nil {
		return "", fmt.Errorf("encode capability manifest: %w", err)
	}
	digest := sha256.Sum256(content)
	return hex.EncodeToString(digest[:]), nil
}

func filter(records []registryport.Record, keep func(registryport.Record) bool) []registryport.Record {
	result := make([]registryport.Record, 0, len(records))
	for _, record := range records {
		if keep(record) {
			result = append(result, record)
		}
	}
	return result
}

func inferKind(records []registryport.Record) registryport.Kind {
	if len(records) == 0 {
		return registryport.KindTool
	}
	return records[0].Kind
}

func cloneSelections(values map[string]registryport.Selection) map[string]registryport.Selection {
	result := make(map[string]registryport.Selection, len(values))
	for id, value := range values {
		value.Permissions = append([]string(nil), value.Permissions...)
		result[id] = value
	}
	return result
}

func classRank(class registryport.Class) int {
	switch class {
	case registryport.ClassGuaranteed:
		return 0
	case registryport.ClassRegistered:
		return 1
	case registryport.ClassInherited:
		return 2
	default:
		return 3
	}
}

func resolutionFailure(code FailureCode, capability, detail string) *ResolutionError {
	return &ResolutionError{Code: code, Capability: capability, Detail: detail}
}
