package cli

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/fdsprod/darkstar/runtime/src/core/health"
)

func TestWriteDoctorReportIncludesCodesAndActions(t *testing.T) {
	t.Parallel()
	report, err := health.NewReport(time.Now(), []health.Check{
		{Subsystem: health.SubsystemDatabase, Status: health.StatusHealthy, Code: "DATABASE_READY", Message: "Ready."},
		{Subsystem: health.SubsystemDaemon, Status: health.StatusHealthy, Code: "DAEMON_READY", Message: "Ready."},
		{Subsystem: health.SubsystemPaths, Status: health.StatusHealthy, Code: "PATHS_READY", Message: "Ready."},
		{Subsystem: health.SubsystemGit, Status: health.StatusHealthy, Code: "GIT_READY", Message: "Ready."},
		{Subsystem: health.SubsystemCodex, Status: health.StatusDegraded, Code: "CODEX_AUTH_REQUIRED", Message: "Sign-in required.", Action: "Run codex login."},
		{Subsystem: health.SubsystemGitHub, Status: health.StatusHealthy, Code: "GITHUB_READY", Message: "Ready."},
		{Subsystem: health.SubsystemConfiguration, Status: health.StatusHealthy, Code: "CONFIGURATION_READY", Message: "Ready."},
		{Subsystem: health.SubsystemProvider, Status: health.StatusHealthy, Code: "PROVIDER_READY", Message: "Ready."},
	})
	if err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	if err := writeDoctorReport(&output, report); err != nil {
		t.Fatal(err)
	}
	for _, fragment := range []string{"DARKSTAR doctor: degraded", "[degraded] codex CODEX_AUTH_REQUIRED", "Action: Run codex login."} {
		if !strings.Contains(output.String(), fragment) {
			t.Fatalf("output %q does not contain %q", output.String(), fragment)
		}
	}
}
