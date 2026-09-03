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
	"darkstar/src/ports"
	"darkstar/src/ports/delivery"
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
	base := strings.TrimSuffix(strings.ToLower(filepath.Base(name)), ".exe")
	if runner.failing[name] || runner.failing[base] {
		return nil, errors.New("command failed")
	}
	if len(arguments) == 1 && arguments[0] == "--version" && base == "codex" {
		return []byte("codex-cli 0.151.0-alpha.7.2\n"), nil
	}
	if base == "git" && len(arguments) >= 5 && arguments[2] == "remote" && arguments[3] == "get-url" {
		return []byte("git@github.com:darkstar/runtime.git\n"), nil
	}
	if base == "gh" && len(arguments) > 0 && arguments[0] == "auth" {
		return []byte("ok\n"), nil
	}
	if base == "gh" && len(arguments) > 3 && arguments[0] == "api" && arguments[3] == "user" {
		return []byte("octocat\n"), nil
	}
	if base == "gh" && len(arguments) > 3 && arguments[0] == "api" && strings.HasPrefix(arguments[3], "repos/") {
		return []byte(`{"full_name":"darkstar/runtime","default_branch":"main","html_url":"https://github.com/darkstar/runtime","permissions":{"push":true}}`), nil
	}
	return []byte("ok\n"), nil
}

type recordedDoctorCommand struct {
	executable string
	arguments  []string
}

type githubHealthRunner struct {
	root  string
	calls []recordedDoctorCommand
}

func (runner *githubHealthRunner) LookPath(name string) (string, error) {
	if filepath.IsAbs(name) {
		return name, nil
	}
	return filepath.Join(runner.root, "tools", name+".exe"), nil
}

func (runner *githubHealthRunner) Output(_ context.Context, name string, arguments ...string) ([]byte, error) {
	runner.calls = append(runner.calls, recordedDoctorCommand{executable: name, arguments: append([]string(nil), arguments...)})
	base := strings.TrimSuffix(strings.ToLower(filepath.Base(name)), ".exe")
	switch {
	case base == "git" && len(arguments) == 5 && arguments[2] == "remote" && arguments[3] == "get-url" && arguments[4] == "upstream":
		return []byte("git@github.com:acme/widget.git\n"), nil
	case base == "gh" && len(arguments) > 0 && arguments[0] == "auth":
		return []byte("ok\n"), nil
	case base == "gh" && len(arguments) > 3 && arguments[0] == "api" && arguments[3] == "user":
		return []byte("delivery-bot\n"), nil
	case base == "gh" && len(arguments) > 3 && arguments[0] == "api" && arguments[3] == "repos/acme/widget":
		return []byte(`{"full_name":"acme/widget","default_branch":"trunk","html_url":"https://github.com/acme/widget","permissions":{"push":true}}`), nil
	default:
		return nil, errors.New("unexpected command")
	}
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

func TestGitHubCheckResolvesConfiguredRemoteBaseAndPushWithoutMutation(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	runner := &githubHealthRunner{root: root}
	doctor := New(Options{ProjectRoot: root, GitHubRemote: "upstream", Runner: runner, Now: func() time.Time { return time.Date(2026, 9, 3, 8, 0, 0, 0, time.UTC) }})
	check := doctor.githubCheck(context.Background(), root)
	if check.Status != health.StatusHealthy || check.Code != "GITHUB_READY" {
		t.Fatalf("check = %#v", check)
	}
	for _, fragment := range []string{"upstream", "acme/widget", "github.com", "trunk", "can push"} {
		if !strings.Contains(check.Message, fragment) {
			t.Fatalf("message %q does not contain %q", check.Message, fragment)
		}
	}
	if len(runner.calls) != 4 {
		t.Fatalf("commands = %#v", runner.calls)
	}
	wantRemote := []string{"-C", filepath.Clean(root), "remote", "get-url", "upstream"}
	if !reflect.DeepEqual(runner.calls[0].arguments, wantRemote) {
		t.Fatalf("remote command = %#v", runner.calls[0].arguments)
	}
	for _, call := range runner.calls {
		joined := strings.Join(call.arguments, " ")
		for _, mutation := range []string{" push ", " commit ", " checkout ", " reset ", "--method POST", "--method PATCH", "--method DELETE"} {
			if strings.Contains(" "+joined+" ", mutation) {
				t.Fatalf("mutating doctor command = %#v", call.arguments)
			}
		}
	}
}

func TestDoctorDefaultsGitHubRemoteToOrigin(t *testing.T) {
	t.Parallel()
	doctor := New(Options{Runner: fakeRunner{}})
	if doctor.githubRemote != "origin" {
		t.Fatalf("GitHub remote = %q, want origin", doctor.githubRemote)
	}
}

func TestGitHubCheckMapsEveryClosedDeliveryHealthOutcome(t *testing.T) {
	t.Parallel()
	base := deliveryHealthObservation()
	tests := []struct {
		name    string
		outcome delivery.HealthOutcome
		code    string
		status  health.Status
	}{
		{name: "ready", outcome: delivery.HealthReady{}, code: "GITHUB_READY", status: health.StatusHealthy},
		{name: "read only", outcome: delivery.HealthReadOnly{Reason: "Grant push access."}, code: "GITHUB_PUSH_PERMISSION_REQUIRED", status: health.StatusDegraded},
		{name: "unauthenticated", outcome: delivery.HealthUnauthenticated{Reason: "Authenticate."}, code: "GITHUB_AUTH_REQUIRED", status: health.StatusDegraded},
		{name: "unavailable", outcome: delivery.HealthUnavailable{Reason: "Check repository access."}, code: "GITHUB_REPOSITORY_UNAVAILABLE", status: health.StatusDegraded},
		{name: "degraded", outcome: delivery.HealthDegraded{Reason: "Use the configured account."}, code: "GITHUB_DELIVERY_DEGRADED", status: health.StatusDegraded},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			observation := base
			observation.Outcome = test.outcome
			check := githubObservationCheck(observation)
			if check.Code != test.code || check.Status != test.status {
				t.Fatalf("check = %#v", check)
			}
			if check.Status != health.StatusHealthy && check.Action == "" {
				t.Fatal("non-healthy outcome has no action")
			}
		})
	}
}

func TestGitHubProbeFailureMapsRemoteResolutionToActionableCheck(t *testing.T) {
	t.Parallel()
	check := githubProbeFailure(&ports.Failure{Code: ports.FailureNotFound, Message: "safe"}, "upstream")
	if check.Code != "GITHUB_REMOTE_NOT_FOUND" || check.Status != health.StatusDegraded || check.Action == "" || !strings.Contains(check.Message, "upstream") {
		t.Fatalf("check = %#v", check)
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

func deliveryHealthObservation() delivery.HealthObservation {
	repository := delivery.Repository{Provider: "github", Host: "github.com", Owner: "acme", Name: "widget"}
	return delivery.HealthObservation{
		Remote:     delivery.Remote{Name: "upstream"},
		Repository: repository,
		BaseBranch: delivery.BranchRef{Repository: repository, Name: "trunk"},
		Account:    "delivery-bot",
	}
}
