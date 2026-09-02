package doctor

import (
	"context"
	"errors"
	"os"
	"path/filepath"
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
}

func (runner fakeRunner) LookPath(name string) (string, error) {
	if runner.missing[name] {
		return "", os.ErrNotExist
	}
	return name, nil
}

func (runner fakeRunner) Run(_ context.Context, name string, _ ...string) error {
	if runner.failing[name] {
		return errors.New("command failed")
	}
	return nil
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

func testPaths(root string) platform.Paths {
	return platform.Paths{
		Config:  filepath.Join(root, "config"),
		Data:    filepath.Join(root, "data"),
		Cache:   filepath.Join(root, "cache"),
		Logs:    filepath.Join(root, "logs"),
		Runtime: filepath.Join(root, "runtime"),
	}
}
