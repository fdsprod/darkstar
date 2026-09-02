package health

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestReportDerivesWorstStatusAndCanonicalOrder(t *testing.T) {
	t.Parallel()
	checks := completeChecks(StatusHealthy)
	checks[0], checks[len(checks)-1] = checks[len(checks)-1], checks[0]
	checks[0] = Check{Subsystem: SubsystemProvider, Status: StatusDegraded, Code: "PROVIDER_NOT_CONFIGURED", Message: "No provider is selected.", Action: "Select a provider."}
	report, err := NewReport(time.Date(2026, 9, 1, 1, 2, 3, 0, time.FixedZone("test", -7*60*60)), checks)
	if err != nil {
		t.Fatal(err)
	}
	if report.Status() != StatusDegraded {
		t.Fatalf("Status() = %q, want degraded", report.Status())
	}
	if report.GeneratedAt.Location() != time.UTC || report.Checks[0].Subsystem != SubsystemDatabase || report.Checks[len(report.Checks)-1].Subsystem != SubsystemProvider {
		t.Fatalf("report was not normalized: %#v", report)
	}
}

func TestReportJSONRejectsContradictoryDerivedStatus(t *testing.T) {
	t.Parallel()
	report, err := NewReport(time.Now(), completeChecks(StatusHealthy))
	if err != nil {
		t.Fatal(err)
	}
	content, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	contradictory := strings.Replace(string(content), `"status":"healthy"`, `"status":"unhealthy"`, 1)
	var decoded Report
	if err := json.Unmarshal([]byte(contradictory), &decoded); err == nil {
		t.Fatal("contradictory report decoded successfully")
	}
}

func TestReportRequiresEverySubsystemAndActionsForFindings(t *testing.T) {
	t.Parallel()
	checks := completeChecks(StatusHealthy)
	if _, err := NewReport(time.Now(), checks[:len(checks)-1]); err == nil {
		t.Fatal("incomplete report succeeded")
	}
	checks = completeChecks(StatusHealthy)
	checks[0] = Check{Subsystem: checks[0].Subsystem, Status: StatusUnhealthy, Code: "DATABASE_UNAVAILABLE", Message: "Database is unavailable."}
	if _, err := NewReport(time.Now(), checks); err == nil {
		t.Fatal("unhealthy check without action succeeded")
	}
}

func TestReportNormalizesProviderDetailsAndRejectsContradictoryCapabilityStates(t *testing.T) {
	t.Parallel()
	checks := completeChecks(StatusHealthy)
	checks[len(checks)-1].ProviderDetails = &ProviderDetails{
		Name: "codex", Authentication: AuthenticationAuthenticated, Usage: UsageReady,
		AvailableCapabilities: []AvailableCapability{{Name: "zeta", Version: "v1"}, {Name: "alpha", Version: "v2"}},
	}
	report, err := NewReport(time.Now(), checks)
	if err != nil {
		t.Fatal(err)
	}
	details := report.Checks[len(report.Checks)-1].ProviderDetails
	if details.AvailableCapabilities[0].Name != "alpha" || details.InstructionSources == nil {
		t.Fatalf("provider details were not normalized: %#v", details)
	}

	checks = completeChecks(StatusDegraded)
	checks[len(checks)-1].ProviderDetails = &ProviderDetails{
		Name: "codex", Authentication: AuthenticationUnknown, Usage: UsageUnknown,
		AvailableCapabilities:   []AvailableCapability{{Name: "duplicate", Version: "v1"}},
		UnavailableCapabilities: []UnavailableCapability{{Name: "duplicate", Reason: "not ready"}},
	}
	if _, err := NewReport(time.Now(), checks); err == nil {
		t.Fatal("contradictory capability states succeeded")
	}
}

func TestHealthyProviderCannotReportUnauthenticatedOrExhausted(t *testing.T) {
	t.Parallel()
	for name, test := range map[string]struct {
		authentication AuthenticationState
		usage          UsageReadiness
	}{
		"authentication": {AuthenticationUnauthenticated, UsageUnknown},
		"usage":          {AuthenticationAuthenticated, UsageExhausted},
	} {
		t.Run(name, func(t *testing.T) {
			checks := completeChecks(StatusHealthy)
			checks[len(checks)-1].ProviderDetails = &ProviderDetails{Name: "codex", Authentication: test.authentication, Usage: test.usage}
			if _, err := NewReport(time.Now(), checks); err == nil {
				t.Fatal("contradictory healthy provider details succeeded")
			}
		})
	}
}

func completeChecks(status Status) []Check {
	checks := make([]Check, 0, len(subsystemOrder))
	for _, subsystem := range subsystemOrder {
		check := Check{Subsystem: subsystem, Status: status, Code: strings.ToUpper(string(subsystem)) + "_READY", Message: "Ready."}
		if status != StatusHealthy {
			check.Action = "Repair it."
		}
		checks = append(checks, check)
	}
	return checks
}
