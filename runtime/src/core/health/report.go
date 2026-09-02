// Package health defines the stable, provider-neutral subsystem health report.
package health

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"time"
)

const SchemaVersion = 1

// Status is the closed readiness classification used by reports and checks.
type Status string

const (
	StatusHealthy   Status = "healthy"
	StatusDegraded  Status = "degraded"
	StatusUnhealthy Status = "unhealthy"
)

// Subsystem identifies one required DARKSTAR readiness boundary.
type Subsystem string

const (
	SubsystemDatabase      Subsystem = "database"
	SubsystemDaemon        Subsystem = "daemon"
	SubsystemPaths         Subsystem = "paths"
	SubsystemGit           Subsystem = "git"
	SubsystemCodex         Subsystem = "codex"
	SubsystemGitHub        Subsystem = "github"
	SubsystemConfiguration Subsystem = "configuration"
	SubsystemProvider      Subsystem = "provider"
)

var subsystemOrder = []Subsystem{
	SubsystemDatabase,
	SubsystemDaemon,
	SubsystemPaths,
	SubsystemGit,
	SubsystemCodex,
	SubsystemGitHub,
	SubsystemConfiguration,
	SubsystemProvider,
}

var codePattern = regexp.MustCompile(`^[A-Z][A-Z0-9_]*$`)

// Check is one safe, actionable subsystem observation. Action is empty only
// when the subsystem is healthy.
type Check struct {
	Subsystem       Subsystem        `json:"subsystem"`
	Status          Status           `json:"status"`
	Code            string           `json:"code"`
	Message         string           `json:"message"`
	Action          string           `json:"action,omitempty"`
	ProviderDetails *ProviderDetails `json:"providerDetails,omitempty"`
}

// AuthenticationState and UsageReadiness are credential-free readiness
// projections. They deliberately report no account identity, balance, or raw
// provider response.
type AuthenticationState string

const (
	AuthenticationAuthenticated   AuthenticationState = "authenticated"
	AuthenticationUnauthenticated AuthenticationState = "unauthenticated"
	AuthenticationUnknown         AuthenticationState = "unknown"
)

type UsageReadiness string

const (
	UsageReady     UsageReadiness = "ready"
	UsageExhausted UsageReadiness = "exhausted"
	UsageUnknown   UsageReadiness = "unknown"
)

// AvailableCapability and UnavailableCapability are separate collections so
// a capability cannot simultaneously carry a version and an unavailable
// reason.
type AvailableCapability struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

type UnavailableCapability struct {
	Name   string `json:"name"`
	Reason string `json:"reason"`
}

// ProviderDetails is the safe adapter/install projection attached only to the
// Codex and selected-provider checks.
type ProviderDetails struct {
	Name                    string                  `json:"name"`
	Version                 string                  `json:"version"`
	ExecutableIdentity      string                  `json:"executableIdentity"`
	Platform                string                  `json:"platform"`
	Authentication          AuthenticationState     `json:"authentication"`
	Usage                   UsageReadiness          `json:"usage"`
	InstructionSources      []string                `json:"instructionSources"`
	ConflictingExecutables  []string                `json:"conflictingExecutables"`
	AvailableCapabilities   []AvailableCapability   `json:"availableCapabilities"`
	UnavailableCapabilities []UnavailableCapability `json:"unavailableCapabilities"`
}

// Report is a point-in-time snapshot. Its JSON status is derived from Checks
// during encoding and verified during decoding, so it cannot drift.
type Report struct {
	SchemaVersion int       `json:"schemaVersion"`
	GeneratedAt   time.Time `json:"generatedAt"`
	Checks        []Check   `json:"checks"`
}

// NewReport validates, orders, and defensively copies a complete report.
func NewReport(generatedAt time.Time, checks []Check) (Report, error) {
	report := Report{
		SchemaVersion: SchemaVersion,
		GeneratedAt:   generatedAt.UTC(),
		Checks:        cloneChecks(checks),
	}
	if err := report.validateAndOrder(); err != nil {
		return Report{}, err
	}
	return report, nil
}

// Status derives the worst observed state from the complete check set.
func (report Report) Status() Status {
	status := StatusHealthy
	for _, check := range report.Checks {
		if check.Status == StatusUnhealthy {
			return StatusUnhealthy
		}
		if check.Status == StatusDegraded {
			status = StatusDegraded
		}
	}
	return status
}

func (report Report) MarshalJSON() ([]byte, error) {
	copy := report
	copy.Checks = cloneChecks(report.Checks)
	if err := copy.validateAndOrder(); err != nil {
		return nil, err
	}
	type wireReport struct {
		SchemaVersion int       `json:"schemaVersion"`
		Status        Status    `json:"status"`
		GeneratedAt   time.Time `json:"generatedAt"`
		Checks        []Check   `json:"checks"`
	}
	return json.Marshal(wireReport{
		SchemaVersion: copy.SchemaVersion,
		Status:        copy.Status(),
		GeneratedAt:   copy.GeneratedAt,
		Checks:        copy.Checks,
	})
}

func (report *Report) UnmarshalJSON(content []byte) error {
	if report == nil {
		return errors.New("health report destination is nil")
	}
	type wireReport struct {
		SchemaVersion int       `json:"schemaVersion"`
		Status        Status    `json:"status"`
		GeneratedAt   time.Time `json:"generatedAt"`
		Checks        []Check   `json:"checks"`
	}
	var wire wireReport
	if err := json.Unmarshal(content, &wire); err != nil {
		return err
	}
	decoded, err := NewReport(wire.GeneratedAt, wire.Checks)
	if err != nil {
		return err
	}
	if wire.SchemaVersion != SchemaVersion {
		return fmt.Errorf("unsupported health schemaVersion %d", wire.SchemaVersion)
	}
	if wire.Status != decoded.Status() {
		return fmt.Errorf("health report status %q contradicts checks with status %q", wire.Status, decoded.Status())
	}
	*report = decoded
	return nil
}

func (report *Report) validateAndOrder() error {
	if report.SchemaVersion != SchemaVersion {
		return fmt.Errorf("unsupported health schemaVersion %d", report.SchemaVersion)
	}
	if report.GeneratedAt.IsZero() {
		return errors.New("health report generatedAt is required")
	}
	if len(report.Checks) != len(subsystemOrder) {
		return fmt.Errorf("health report requires %d subsystem checks, got %d", len(subsystemOrder), len(report.Checks))
	}
	rank := make(map[Subsystem]int, len(subsystemOrder))
	for index, subsystem := range subsystemOrder {
		rank[subsystem] = index
	}
	seen := make(map[Subsystem]struct{}, len(report.Checks))
	for index := range report.Checks {
		normalizeProviderDetails(report.Checks[index].ProviderDetails)
		check := report.Checks[index]
		if _, known := rank[check.Subsystem]; !known {
			return fmt.Errorf("unknown health subsystem %q", check.Subsystem)
		}
		if _, duplicate := seen[check.Subsystem]; duplicate {
			return fmt.Errorf("duplicate health subsystem %q", check.Subsystem)
		}
		seen[check.Subsystem] = struct{}{}
		if err := validateCheck(check); err != nil {
			return fmt.Errorf("%s health check: %w", check.Subsystem, err)
		}
	}
	sort.Slice(report.Checks, func(left, right int) bool {
		return rank[report.Checks[left].Subsystem] < rank[report.Checks[right].Subsystem]
	})
	return nil
}

func validateCheck(check Check) error {
	if check.Status != StatusHealthy && check.Status != StatusDegraded && check.Status != StatusUnhealthy {
		return fmt.Errorf("invalid status %q", check.Status)
	}
	if !codePattern.MatchString(check.Code) {
		return fmt.Errorf("invalid actionable code %q", check.Code)
	}
	if check.Message == "" {
		return errors.New("message is required")
	}
	if check.Status == StatusHealthy && check.Action != "" {
		return errors.New("healthy check cannot require an action")
	}
	if check.Status != StatusHealthy && check.Action == "" {
		return errors.New("degraded or unhealthy check requires an action")
	}
	if check.ProviderDetails != nil {
		if check.Subsystem != SubsystemCodex && check.Subsystem != SubsystemProvider {
			return errors.New("provider details are only valid for codex or provider checks")
		}
		if err := validateProviderDetails(*check.ProviderDetails); err != nil {
			return err
		}
		if check.Status == StatusHealthy && (check.ProviderDetails.Authentication == AuthenticationUnauthenticated || check.ProviderDetails.Usage == UsageExhausted) {
			return errors.New("healthy check contradicts provider readiness")
		}
	}
	return nil
}

func cloneChecks(checks []Check) []Check {
	cloned := append([]Check(nil), checks...)
	for index := range cloned {
		if cloned[index].ProviderDetails == nil {
			continue
		}
		details := *cloned[index].ProviderDetails
		details.InstructionSources = append([]string(nil), details.InstructionSources...)
		details.ConflictingExecutables = append([]string(nil), details.ConflictingExecutables...)
		details.AvailableCapabilities = append([]AvailableCapability(nil), details.AvailableCapabilities...)
		details.UnavailableCapabilities = append([]UnavailableCapability(nil), details.UnavailableCapabilities...)
		cloned[index].ProviderDetails = &details
	}
	return cloned
}

func normalizeProviderDetails(details *ProviderDetails) {
	if details == nil {
		return
	}
	if details.InstructionSources == nil {
		details.InstructionSources = []string{}
	}
	if details.ConflictingExecutables == nil {
		details.ConflictingExecutables = []string{}
	}
	if details.AvailableCapabilities == nil {
		details.AvailableCapabilities = []AvailableCapability{}
	}
	if details.UnavailableCapabilities == nil {
		details.UnavailableCapabilities = []UnavailableCapability{}
	}
	sort.Strings(details.InstructionSources)
	sort.Strings(details.ConflictingExecutables)
	sort.Slice(details.AvailableCapabilities, func(left, right int) bool {
		return details.AvailableCapabilities[left].Name < details.AvailableCapabilities[right].Name
	})
	sort.Slice(details.UnavailableCapabilities, func(left, right int) bool {
		return details.UnavailableCapabilities[left].Name < details.UnavailableCapabilities[right].Name
	})
}

func validateProviderDetails(details ProviderDetails) error {
	if details.Name == "" {
		return errors.New("provider details require a name")
	}
	if details.Authentication != AuthenticationAuthenticated && details.Authentication != AuthenticationUnauthenticated && details.Authentication != AuthenticationUnknown {
		return fmt.Errorf("invalid provider authentication state %q", details.Authentication)
	}
	if details.Usage != UsageReady && details.Usage != UsageExhausted && details.Usage != UsageUnknown {
		return fmt.Errorf("invalid provider usage readiness %q", details.Usage)
	}
	if err := validateUniqueDetails("instruction source", details.InstructionSources); err != nil {
		return err
	}
	if err := validateUniqueDetails("conflicting executable", details.ConflictingExecutables); err != nil {
		return err
	}
	seen := make(map[string]struct{}, len(details.AvailableCapabilities)+len(details.UnavailableCapabilities))
	for _, capability := range details.AvailableCapabilities {
		if capability.Name == "" || capability.Version == "" {
			return errors.New("available capability requires name and version")
		}
		if _, duplicate := seen[capability.Name]; duplicate {
			return fmt.Errorf("duplicate provider capability %q", capability.Name)
		}
		seen[capability.Name] = struct{}{}
	}
	for _, capability := range details.UnavailableCapabilities {
		if capability.Name == "" || capability.Reason == "" {
			return errors.New("unavailable capability requires name and reason")
		}
		if _, duplicate := seen[capability.Name]; duplicate {
			return fmt.Errorf("duplicate provider capability %q", capability.Name)
		}
		seen[capability.Name] = struct{}{}
	}
	return nil
}

func validateUniqueDetails(kind string, values []string) error {
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		if value == "" {
			return fmt.Errorf("%s cannot be empty", kind)
		}
		if _, duplicate := seen[value]; duplicate {
			return fmt.Errorf("duplicate %s %q", kind, value)
		}
		seen[value] = struct{}{}
	}
	return nil
}
