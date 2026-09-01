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
	Subsystem Subsystem `json:"subsystem"`
	Status    Status    `json:"status"`
	Code      string    `json:"code"`
	Message   string    `json:"message"`
	Action    string    `json:"action,omitempty"`
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
		Checks:        append([]Check(nil), checks...),
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
	copy.Checks = append([]Check(nil), report.Checks...)
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
	for _, check := range report.Checks {
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
	return nil
}
