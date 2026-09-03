package capabilityregistry

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	registryport "darkstar/src/ports/capabilityregistry"
)

type ObservationScope = registryport.ObservationScope

const (
	ObservationCodex   = registryport.ObservationCodex
	ObservationProject = registryport.ObservationProject
	ObservationUser    = registryport.ObservationUser
)

type Observation = registryport.Observation

// EligibilityRule is an exact-name policy decision. Omitting a rule is an
// explicit deny for registered and inherited capabilities.
type EligibilityRule struct {
	Name        string
	Kind        registryport.Kind
	Decision    PolicyDecision
	Permissions []string
}

// InheritanceRequest combines host observations with optional, explicit
// bindings to registered records. Bindings map an observed inherited name to a
// registry record ID; a binding is used only when kind, source, and fingerprint
// all still match.
type InheritanceRequest struct {
	Observations    []Observation
	Registrations   []registryport.Record
	Bindings        map[string]string
	Rules           []EligibilityRule
	HostFingerprint string
}

type InheritanceDiagnosticCode string

const (
	DiagnosticRegistrationMissing  InheritanceDiagnosticCode = "CAPABILITY_REGISTRATION_MISSING"
	DiagnosticRegistrationMismatch InheritanceDiagnosticCode = "CAPABILITY_REGISTRATION_MISMATCH"
)

type InheritanceDiagnostic struct {
	Code       InheritanceDiagnosticCode `json:"code"`
	Capability string                    `json:"capability"`
	Detail     string                    `json:"detail"`
}

// InheritanceSnapshot is resolver-ready. Records remain visible when denied so
// resolution reports policy denial instead of incorrectly reporting absence.
type InheritanceSnapshot struct {
	Records         []registryport.Record   `json:"records"`
	Grants          map[string]Grant        `json:"grants"`
	Diagnostics     []InheritanceDiagnostic `json:"diagnostics"`
	HostFingerprint string                  `json:"hostFingerprint"`
	Digest          string                  `json:"digest"`
}

// InheritCapabilities converts one Codex inventory into deterministic registry
// records. Discovery never grants authority: only an exact allow rule creates
// an allow grant, and deny wins by being the default.
func InheritCapabilities(request InheritanceRequest) (InheritanceSnapshot, error) {
	if strings.TrimSpace(request.HostFingerprint) == "" {
		return InheritanceSnapshot{}, errors.New("host fingerprint is required")
	}
	registrations, err := registrationIndex(request.Registrations)
	if err != nil {
		return InheritanceSnapshot{}, err
	}
	rules, err := eligibilityIndex(request.Rules)
	if err != nil {
		return InheritanceSnapshot{}, err
	}

	snapshot := InheritanceSnapshot{
		Records: []registryport.Record{}, Grants: map[string]Grant{}, Diagnostics: []InheritanceDiagnostic{},
		HostFingerprint: strings.TrimSpace(request.HostFingerprint),
	}
	seen := make(map[string]struct{}, len(request.Observations))
	selectedIDs := make(map[string]struct{}, len(request.Observations))
	for _, observation := range request.Observations {
		inherited, err := inheritedRecord(observation)
		if err != nil {
			return InheritanceSnapshot{}, err
		}
		key := inherited.Name + "\x00" + string(inherited.Kind)
		if _, duplicate := seen[key]; duplicate {
			return InheritanceSnapshot{}, resolutionFailure(FailureAmbiguous, inherited.Name, "Codex reported the same scoped capability more than once")
		}
		seen[key] = struct{}{}

		record := inherited
		if registrationID := strings.TrimSpace(request.Bindings[inherited.Name]); registrationID != "" {
			registered, found := registrations[registrationID]
			switch {
			case !found:
				snapshot.Diagnostics = append(snapshot.Diagnostics, InheritanceDiagnostic{Code: DiagnosticRegistrationMissing, Capability: inherited.Name, Detail: registrationID})
			case registered.Kind != inherited.Kind || registered.Source != inherited.Source || registered.Fingerprint != inherited.Fingerprint:
				snapshot.Diagnostics = append(snapshot.Diagnostics, InheritanceDiagnostic{Code: DiagnosticRegistrationMismatch, Capability: inherited.Name, Detail: registrationID})
			default:
				record = registered
				record.Availability = inherited.Availability
				record.ObservedAt = inherited.ObservedAt
			}
		}

		rule, configured := rules[record.Name+"\x00"+string(record.Kind)]
		grant := Grant{Decision: PolicyDeny, Permissions: []string{}}
		if configured {
			grant = rule
		}
		if _, duplicate := selectedIDs[record.ID]; duplicate {
			return InheritanceSnapshot{}, resolutionFailure(FailureAmbiguous, record.Name, "multiple observations resolved to the same capability record")
		}
		selectedIDs[record.ID] = struct{}{}
		snapshot.Records = append(snapshot.Records, record)
		snapshot.Grants[record.ID] = grant
	}

	sort.Slice(snapshot.Records, func(i, j int) bool {
		if snapshot.Records[i].Name != snapshot.Records[j].Name {
			return snapshot.Records[i].Name < snapshot.Records[j].Name
		}
		return snapshot.Records[i].ID < snapshot.Records[j].ID
	})
	sort.Slice(snapshot.Diagnostics, func(i, j int) bool {
		if snapshot.Diagnostics[i].Capability != snapshot.Diagnostics[j].Capability {
			return snapshot.Diagnostics[i].Capability < snapshot.Diagnostics[j].Capability
		}
		return snapshot.Diagnostics[i].Code < snapshot.Diagnostics[j].Code
	})
	snapshot.Digest, err = inheritanceDigest(snapshot)
	return snapshot, err
}

func inheritedRecord(observation registryport.Observation) (registryport.Record, error) {
	scope := strings.TrimSpace(string(observation.Scope))
	if observation.Scope != ObservationCodex && observation.Scope != ObservationProject && observation.Scope != ObservationUser {
		return registryport.Record{}, fmt.Errorf("invalid Codex capability scope %q", observation.Scope)
	}
	name := "codex-inherited:" + scope + "/" + strings.TrimSpace(observation.Name)
	record := registryport.Record{
		SchemaVersion:   1,
		Name:            name,
		Kind:            observation.Kind,
		Class:           registryport.ClassInherited,
		DeclaredVersion: strings.TrimSpace(observation.DeclaredVersion),
		Fingerprint:     strings.TrimSpace(observation.Fingerprint),
		Source:          observation.Source,
		Interfaces:      observation.Interfaces,
		Dependencies:    append([]string(nil), observation.Dependencies...),
		Risk:            observation.Risk,
		Availability:    observation.Availability,
		ObservedAt:      observation.ObservedAt,
	}
	record.ID = "capability:" + record.Name + "@" + record.Fingerprint
	return normalizeRecord(record)
}

func registrationIndex(records []registryport.Record) (map[string]registryport.Record, error) {
	result := make(map[string]registryport.Record, len(records))
	for _, record := range records {
		normalized, err := normalizeRecord(record)
		if err != nil {
			return nil, err
		}
		if normalized.Class != registryport.ClassRegistered {
			return nil, fmt.Errorf("capability %q is not a registered binding target", normalized.Name)
		}
		if _, duplicate := result[normalized.ID]; duplicate {
			return nil, fmt.Errorf("duplicate registered capability ID %q", normalized.ID)
		}
		result[normalized.ID] = normalized
	}
	return result, nil
}

func eligibilityIndex(rules []EligibilityRule) (map[string]Grant, error) {
	result := make(map[string]Grant, len(rules))
	for _, rule := range rules {
		if !canonicalNamePattern.MatchString(rule.Name) || (rule.Kind != registryport.KindSkill && rule.Kind != registryport.KindTool) {
			return nil, fmt.Errorf("invalid capability eligibility rule %q", rule.Name)
		}
		if rule.Decision != PolicyAllow && rule.Decision != PolicyDeny {
			return nil, fmt.Errorf("capability eligibility rule %q requires allow or deny", rule.Name)
		}
		permissions, err := canonicalStrings(rule.Permissions)
		if err != nil {
			return nil, fmt.Errorf("capability eligibility rule %q: %w", rule.Name, err)
		}
		if rule.Decision == PolicyDeny && len(permissions) != 0 {
			return nil, fmt.Errorf("denied capability eligibility rule %q cannot grant permissions", rule.Name)
		}
		key := rule.Name + "\x00" + string(rule.Kind)
		if _, duplicate := result[key]; duplicate {
			return nil, fmt.Errorf("duplicate capability eligibility rule %q", rule.Name)
		}
		result[key] = Grant{Decision: rule.Decision, Permissions: permissions}
	}
	return result, nil
}

func inheritanceDigest(snapshot InheritanceSnapshot) (string, error) {
	copy := snapshot
	copy.Digest = ""
	content, err := json.Marshal(copy)
	if err != nil {
		return "", fmt.Errorf("encode inherited capability snapshot: %w", err)
	}
	digest := sha256.Sum256(content)
	return hex.EncodeToString(digest[:]), nil
}
