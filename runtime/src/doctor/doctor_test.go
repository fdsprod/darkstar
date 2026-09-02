package doctor

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"darkstar/src/adapters/provider/fake"
	"darkstar/src/adapters/statestore/sqlite"
	"darkstar/src/core/health"
	"darkstar/src/daemon"
	"darkstar/src/ports/platform"
	"darkstar/src/ports/provider"
)

type fakeRunner struct {
	missing map[string]bool
	failing map[string]bool
	paths   map[string][]string
}

func (runner fakeRunner) LookPath(name string) (string, error) {
	if runner.missing[name] {
		return "", os.ErrNotExist
	}
	if len(runner.paths[name]) > 0 {
		return runner.paths[name][0], nil
	}
	return name, nil
}

func (runner fakeRunner) LookPaths(name string) ([]string, error) {
	if runner.missing[name] {
		return nil, os.ErrNotExist
	}
	if len(runner.paths[name]) > 0 {
		return append([]string(nil), runner.paths[name]...), nil
	}
	return []string{name}, nil
}

func (runner fakeRunner) Output(_ context.Context, name string, arguments ...string) ([]byte, error) {
	if runner.failing[name] {
		return nil, errors.New("command failed")
	}
	if len(arguments) == 1 && arguments[0] == "--version" && strings.Contains(strings.ToLower(filepath.Base(name)), "codex") {
		return []byte("codex-cli 0.151.0-alpha.7.2\n"), nil
	}
	return []byte("ok\n"), nil
}

func TestReportReturnsCompleteHealthySnapshot(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "data"), 0o700); err != nil {
		t.Fatal(err)
	}
	database, err := sqlite.Open(context.Background(), filepath.Join(root, "data", "darkstar.db"), sqlite.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = database.Close()
	}()
	providerAdapter, err := fake.New(fake.Scenario{})
	if err != nil {
		t.Fatal(err)
	}
	generatedAt := time.Date(2026, 9, 1, 2, 0, 0, 0, time.UTC)
	doctor := New(Options{
		Paths:       testPaths(root),
		Database:    database,
		Process:     daemon.ProcessIdentity{PID: 123, StartedAt: generatedAt, Executable: filepath.Join(root, "darkstar.exe")},
		ProjectRoot: root,
		Provider:    providerAdapter,
		Runner:      fakeRunner{},
		Now:         func() time.Time { return generatedAt },
	})
	report, err := doctor.Report(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if report.Status() != health.StatusHealthy || len(report.Checks) != 8 || report.GeneratedAt != generatedAt {
		t.Fatalf("report = %#v", report)
	}
	want := []health.Subsystem{health.SubsystemDatabase, health.SubsystemDaemon, health.SubsystemPaths, health.SubsystemGit, health.SubsystemCodex, health.SubsystemGitHub, health.SubsystemConfiguration, health.SubsystemProvider}
	for index, subsystem := range want {
		if report.Checks[index].Subsystem != subsystem || report.Checks[index].Status != health.StatusHealthy {
			t.Fatalf("check %d = %#v, want healthy %s", index, report.Checks[index], subsystem)
		}
	}
}

func TestReportUsesActionableCodesWithoutFailingTheRequest(t *testing.T) {
	t.Parallel()
	doctor := New(Options{Runner: fakeRunner{missing: map[string]bool{"git": true, "codex": true, "gh": true}}})
	report, err := doctor.Report(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if report.Status() != health.StatusUnhealthy {
		t.Fatalf("Status() = %q, want unhealthy", report.Status())
	}
	wantCodes := map[health.Subsystem]string{
		health.SubsystemDatabase:      "DATABASE_UNAVAILABLE",
		health.SubsystemDaemon:        "DAEMON_IDENTITY_INVALID",
		health.SubsystemPaths:         "PATHS_INVALID",
		health.SubsystemGit:           "GIT_EXECUTABLE_NOT_FOUND",
		health.SubsystemCodex:         "CODEX_EXECUTABLE_NOT_FOUND",
		health.SubsystemGitHub:        "GITHUB_CLI_NOT_FOUND",
		health.SubsystemConfiguration: "CONFIGURATION_PROJECT_ROOT_UNAVAILABLE",
		health.SubsystemProvider:      "PROVIDER_NOT_CONFIGURED",
	}
	for _, check := range report.Checks {
		if check.Code != wantCodes[check.Subsystem] || check.Action == "" {
			t.Errorf("check = %#v, want code %s and an action", check, wantCodes[check.Subsystem])
		}
	}
}

func TestReportClassifiesProviderHealthStates(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	providerAdapter, err := fake.New(fake.Scenario{Health: provider.Health{State: provider.HealthUnauthenticated, Provider: "test"}})
	if err != nil {
		t.Fatal(err)
	}
	doctor := New(Options{Paths: testPaths(root), ProjectRoot: root, Provider: providerAdapter, Runner: fakeRunner{}})
	report, err := doctor.Report(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got := report.Checks[7].Code; got != "PROVIDER_AUTH_REQUIRED" {
		t.Fatalf("provider code = %q", got)
	}
}

func TestReportPinsCodexAndRejectsConflictingInstallations(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	first := filepath.Join(root, "desktop", "codex.exe")
	second := filepath.Join(root, "npm", "codex.exe")
	doctor := New(Options{Runner: fakeRunner{paths: map[string][]string{"codex": {first, second}}}})
	report, err := doctor.Report(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	check := report.Checks[4]
	if check.Code != "CODEX_EXECUTABLE_CONFLICT" || check.ProviderDetails == nil {
		t.Fatalf("codex check = %#v", check)
	}
	if check.ProviderDetails.ExecutableIdentity != filepath.Clean(first) || !reflect.DeepEqual(check.ProviderDetails.ConflictingExecutables, []string{filepath.Clean(second)}) {
		t.Fatalf("codex details = %#v", check.ProviderDetails)
	}
}

func TestReportProjectsProviderCapabilitiesAndUsageWithoutAccountData(t *testing.T) {
	t.Parallel()
	providerAdapter, err := fake.New(fake.Scenario{
		Health: provider.Health{
			State: provider.HealthUsageExhausted, Provider: "codex", ProviderVersion: "0.151.0-alpha.7.2",
			ExecutableIdentity: `C:\Program Files\Codex\codex.exe`, Platform: "windows",
			Authentication: provider.AuthenticationAuthenticated, Usage: provider.UsageExhausted,
			InstructionSources: []string{"user:instructions"},
		},
		Capabilities: provider.CapabilityManifest{Provider: "codex", Fingerprint: strings.Repeat("a", 64), Features: map[string]provider.Capability{
			"local_image_input": provider.AvailableCapability{Version: "v2", Metadata: map[string]string{"inputType": "localImage"}},
			"workspace_write":   provider.UnavailableCapability{Reason: "read-only selection"},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	report, err := New(Options{Provider: providerAdapter, Runner: fakeRunner{missing: map[string]bool{"git": true, "codex": true, "gh": true}}}).Report(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	check := report.Checks[7]
	if check.Code != "PROVIDER_USAGE_EXHAUSTED" || check.ProviderDetails == nil || check.ProviderDetails.Usage != health.UsageExhausted {
		t.Fatalf("provider check = %#v", check)
	}
	if len(check.ProviderDetails.AvailableCapabilities) != 1 || len(check.ProviderDetails.UnavailableCapabilities) != 1 {
		t.Fatalf("provider capabilities = %#v", check.ProviderDetails)
	}
	encoded, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	for _, secretLike := range []string{"email", "token", "balance"} {
		if strings.Contains(strings.ToLower(string(encoded)), secretLike) {
			t.Fatalf("report unexpectedly contains %q: %s", secretLike, encoded)
		}
	}
}

func testPaths(root string) platform.Paths {
	return platform.Paths{
		Config:  filepath.Join(root, "config"),
		Data:    filepath.Join(root, "data"),
		Cache:   filepath.Join(root, "cache"),
		Logs:    filepath.Join(root, "logs"),
		Runtime: filepath.Join(root, "runtime"),
	}
}
