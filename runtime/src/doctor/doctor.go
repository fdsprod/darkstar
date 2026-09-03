// Package doctor probes the local runtime and returns a safe, actionable health report.
package doctor

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"darkstar/src/adapters/delivery/githubcli"
	"darkstar/src/adapters/statestore/sqlite"
	"darkstar/src/core/config"
	"darkstar/src/core/health"
	"darkstar/src/daemon"
	"darkstar/src/daemon/configuration"
	"darkstar/src/ports"
	deliveryport "darkstar/src/ports/delivery"
	platformport "darkstar/src/ports/platform"
	providerport "darkstar/src/ports/provider"
)

const commandTimeout = 5 * time.Second

// CommandRunner is the non-mutating executable boundary used by tool probes.
type CommandRunner interface {
	LookPath(string) (string, error)
	Output(context.Context, string, ...string) ([]byte, error)
}

type executableEnumerator interface {
	LookPaths(string) ([]string, error)
}

type osCommandRunner struct{}

func (osCommandRunner) LookPath(name string) (string, error) { return exec.LookPath(name) }

func (osCommandRunner) Output(ctx context.Context, name string, arguments ...string) ([]byte, error) {
	command := exec.CommandContext(ctx, name, arguments...)
	command.Env = append(os.Environ(), "GH_PROMPT_DISABLED=1", "GIT_TERMINAL_PROMPT=0", "GCM_INTERACTIVE=Never")
	command.Stdin = nil
	command.Stderr = io.Discard
	return command.Output()
}

// githubAdapterRunner adapts Doctor's read-only command seam to the delivery
// adapter without introducing adapter dependencies into core or ports.
type githubAdapterRunner struct{ runner CommandRunner }

func (runner githubAdapterRunner) LookPath(name string) (string, error) {
	return runner.runner.LookPath(name)
}

func (runner githubAdapterRunner) Run(ctx context.Context, executable string, arguments []string, input []byte) ([]byte, []byte, error) {
	if len(input) != 0 {
		return nil, nil, errors.New("doctor command probes do not accept input")
	}
	output, err := runner.runner.Output(ctx, executable, arguments...)
	return output, nil, err
}

func (runner osCommandRunner) LookPaths(name string) ([]string, error) {
	selected, err := runner.LookPath(name)
	if err != nil {
		return nil, err
	}
	command := "which"
	arguments := []string{"-a", name}
	if runtime.GOOS == "windows" {
		command = "where.exe"
		arguments = []string{name}
	}
	ctx, cancel := context.WithTimeout(context.Background(), commandTimeout)
	defer cancel()
	output, err := runner.Output(ctx, command, arguments...)
	if err != nil {
		return []string{selected}, nil
	}
	paths := []string{selected}
	for _, line := range strings.Split(strings.ReplaceAll(string(output), "\r\n", "\n"), "\n") {
		if candidate := strings.TrimSpace(line); candidate != "" {
			paths = append(paths, candidate)
		}
	}
	return paths, nil
}

// Options supplies the live dependencies and observations used by Doctor.
type Options struct {
	Paths           platformport.Paths
	Database        *sqlite.Database
	Process         daemon.ProcessIdentity
	ProjectRoot     string
	Provider        providerport.Provider
	CodexExecutable string
	GitHubRemote    string
	Runner          CommandRunner
	Now             func() time.Time
}

// Doctor is a reusable, side-effect-bounded local readiness probe.
type Doctor struct {
	paths        platformport.Paths
	database     *sqlite.Database
	process      daemon.ProcessIdentity
	projectRoot  string
	provider     providerport.Provider
	codex        executableResolution
	githubRemote string
	runner       CommandRunner
	now          func() time.Time
}

// New constructs a doctor. Invalid live dependencies are represented as
// actionable checks instead of preventing the report from being produced.
func New(options Options) *Doctor {
	runner := options.Runner
	if runner == nil {
		runner = osCommandRunner{}
	}
	now := options.Now
	if now == nil {
		now = time.Now
	}
	codex := resolveExecutable(runner, options.CodexExecutable, "codex")
	githubRemote := strings.TrimSpace(options.GitHubRemote)
	if githubRemote == "" {
		githubRemote = "origin"
	}
	return &Doctor{
		paths:        options.Paths,
		database:     options.Database,
		process:      options.Process,
		projectRoot:  options.ProjectRoot,
		provider:     options.Provider,
		codex:        codex,
		githubRemote: githubRemote,
		runner:       runner,
		now:          now,
	}
}

// Report probes independent subsystems concurrently and returns them in the
// stable order enforced by the health package.
func (doctor *Doctor) Report(ctx context.Context) (health.Report, error) {
	return doctor.ReportForProject(ctx, "")
}

// ReportForProject uses projectRoot for repository and project-configuration
// checks. An empty value uses the daemon's startup project root.
func (doctor *Doctor) ReportForProject(ctx context.Context, projectRoot string) (health.Report, error) {
	if doctor == nil {
		return health.Report{}, errors.New("doctor is not configured")
	}
	if projectRoot == "" {
		projectRoot = doctor.projectRoot
	} else if !filepath.IsAbs(projectRoot) {
		return health.Report{}, errors.New("doctor project root must be absolute")
	} else {
		projectRoot = filepath.Clean(projectRoot)
	}
	probes := []func(context.Context) health.Check{
		doctor.databaseCheck,
		doctor.daemonCheck,
		doctor.pathsCheck,
		func(ctx context.Context) health.Check { return doctor.gitCheck(ctx, projectRoot) },
		doctor.codexCheck,
		func(ctx context.Context) health.Check { return doctor.githubCheck(ctx, projectRoot) },
		func(ctx context.Context) health.Check { return doctor.configurationCheck(ctx, projectRoot) },
		doctor.providerCheck,
	}
	checks := make([]health.Check, len(probes))
	var group sync.WaitGroup
	group.Add(len(probes))
	for index, probe := range probes {
		index, probe := index, probe
		go func() {
			defer group.Done()
			checks[index] = probe(ctx)
		}()
	}
	group.Wait()
	return health.NewReport(doctor.now(), checks)
}

func (doctor *Doctor) databaseCheck(ctx context.Context) health.Check {
	if doctor.database == nil {
		return finding(health.SubsystemDatabase, health.StatusUnhealthy, "DATABASE_UNAVAILABLE", "The state database is not open.", "Restart the daemon; if the database still fails, preserve the data directory and inspect the daemon log.")
	}
	if err := doctor.database.SQL().PingContext(ctx); err != nil {
		return finding(health.SubsystemDatabase, health.StatusUnhealthy, "DATABASE_UNREACHABLE", "The state database did not respond.", "Restart the daemon and inspect the daemon log for the database error.")
	}
	var integrity string
	if err := doctor.database.SQL().QueryRowContext(ctx, "PRAGMA quick_check(1)").Scan(&integrity); err != nil || integrity != "ok" {
		return finding(health.SubsystemDatabase, health.StatusUnhealthy, "DATABASE_INTEGRITY_FAILED", "The state database failed its integrity check.", "Stop the daemon, preserve the database and WAL files, and use the documented database recovery procedure.")
	}
	version, err := doctor.database.Version(ctx)
	if err != nil || version < 1 {
		return finding(health.SubsystemDatabase, health.StatusUnhealthy, "DATABASE_SCHEMA_UNAVAILABLE", "The database schema version could not be read.", "Restart the daemon and inspect migration errors in the daemon log.")
	}
	return healthy(health.SubsystemDatabase, "DATABASE_READY", fmt.Sprintf("State database schema version %d is ready.", version))
}

func (doctor *Doctor) daemonCheck(context.Context) health.Check {
	if doctor.process.PID <= 0 || doctor.process.StartedAt.IsZero() || !filepath.IsAbs(doctor.process.Executable) {
		return finding(health.SubsystemDaemon, health.StatusUnhealthy, "DAEMON_IDENTITY_INVALID", "The daemon process identity is incomplete.", "Restart the daemon so it can republish owned process state.")
	}
	return healthy(health.SubsystemDaemon, "DAEMON_READY", fmt.Sprintf("Daemon process %d is serving the local API.", doctor.process.PID))
}

func (doctor *Doctor) pathsCheck(context.Context) health.Check {
	paths := []string{doctor.paths.Config, doctor.paths.Data, doctor.paths.Cache, doctor.paths.Logs, doctor.paths.Runtime}
	seen := make(map[string]struct{}, len(paths))
	for _, candidate := range paths {
		if !filepath.IsAbs(candidate) {
			return finding(health.SubsystemPaths, health.StatusUnhealthy, "PATHS_INVALID", "An application-data path is not absolute.", "Repair Windows Local AppData resolution and restart the daemon.")
		}
		canonical := filepath.Clean(candidate)
		identity := strings.ToLower(canonical)
		if _, duplicate := seen[identity]; duplicate {
			return finding(health.SubsystemPaths, health.StatusUnhealthy, "PATHS_COLLIDE", "Two application-data purposes resolve to the same directory.", "Repair the application path configuration and restart the daemon.")
		}
		seen[identity] = struct{}{}
		if err := os.MkdirAll(canonical, 0o700); err != nil {
			return finding(health.SubsystemPaths, health.StatusUnhealthy, "PATH_DIRECTORY_UNAVAILABLE", "An application-data directory cannot be created or opened.", "Ensure the current Windows user can write to Local AppData, then rerun doctor.")
		}
		temporary, err := os.CreateTemp(canonical, ".darkstar-doctor-*.tmp")
		if err != nil {
			return finding(health.SubsystemPaths, health.StatusUnhealthy, "PATH_NOT_WRITABLE", "An application-data directory is not writable.", "Restore write permission for the current Windows user, then rerun doctor.")
		}
		name := temporary.Name()
		closeErr := temporary.Close()
		removeErr := os.Remove(name)
		if closeErr != nil || removeErr != nil {
			return finding(health.SubsystemPaths, health.StatusUnhealthy, "PATH_WRITE_TEST_FAILED", "An application-data directory failed its write test.", "Inspect filesystem permissions and locks, then rerun doctor.")
		}
	}
	return healthy(health.SubsystemPaths, "PATHS_READY", "Configuration, data, cache, log, and runtime directories are writable and distinct.")
}

func (doctor *Doctor) gitCheck(ctx context.Context, projectRoot string) health.Check {
	git, err := doctor.runner.LookPath("git")
	if err != nil {
		return finding(health.SubsystemGit, health.StatusUnhealthy, "GIT_EXECUTABLE_NOT_FOUND", "Git is not available on PATH.", "Install Git for Windows and add it to PATH, then restart the daemon.")
	}
	if _, err := doctor.runCommand(ctx, git, "--version"); err != nil {
		return finding(health.SubsystemGit, health.StatusUnhealthy, "GIT_EXECUTABLE_FAILED", "Git could not report its version.", "Repair the Git installation and rerun doctor.")
	}
	if !filepath.IsAbs(projectRoot) {
		return finding(health.SubsystemGit, health.StatusDegraded, "GIT_PROJECT_ROOT_UNAVAILABLE", "The daemon has no absolute project root to inspect.", "Start DARKSTAR from the target repository and rerun doctor.")
	}
	if _, err := doctor.runCommand(ctx, git, "-C", projectRoot, "rev-parse", "--show-toplevel"); err != nil {
		return finding(health.SubsystemGit, health.StatusDegraded, "GIT_REPOSITORY_NOT_FOUND", "The current project root is not a Git worktree.", "Run DARKSTAR from a Git worktree or initialize the project repository.")
	}
	return healthy(health.SubsystemGit, "GIT_READY", "Git is executable and the current project is a worktree.")
}

func (doctor *Doctor) codexCheck(ctx context.Context) health.Check {
	details := &health.ProviderDetails{
		Name: "codex", Authentication: health.AuthenticationUnknown, Usage: health.UsageUnknown,
		InstructionSources: []string{}, ConflictingExecutables: append([]string(nil), doctor.codex.Conflicts...),
		AvailableCapabilities: []health.AvailableCapability{}, UnavailableCapabilities: []health.UnavailableCapability{},
	}
	if doctor.codex.Err != nil {
		return finding(health.SubsystemCodex, health.StatusDegraded, "CODEX_EXECUTABLE_NOT_FOUND", "Codex is not available on PATH.", "Install Codex, add it to PATH, and restart the daemon before selecting a Codex provider.")
	}
	details.ExecutableIdentity = doctor.codex.Selected
	versionOutput, err := doctor.runCommand(ctx, doctor.codex.Selected, "--version")
	if err != nil {
		check := finding(health.SubsystemCodex, health.StatusDegraded, "CODEX_VERSION_UNAVAILABLE", "The pinned Codex executable could not report its version.", "Repair or upgrade the pinned Codex installation, then restart the daemon.")
		check.ProviderDetails = details
		return check
	}
	details.Version = exactCodexVersion(string(versionOutput))
	if details.Version == "" {
		check := finding(health.SubsystemCodex, health.StatusDegraded, "CODEX_VERSION_INVALID", "The pinned Codex executable returned an unrecognized version.", "Repair or upgrade the pinned Codex installation, then restart the daemon.")
		check.ProviderDetails = details
		return check
	}
	if _, err := doctor.runCommand(ctx, doctor.codex.Selected, "login", "status"); err != nil {
		details.Authentication = health.AuthenticationUnauthenticated
		check := finding(health.SubsystemCodex, health.StatusDegraded, "CODEX_AUTH_REQUIRED", "The pinned Codex installation requires authentication.", "Sign in with `codex login`, then rerun doctor.")
		check.ProviderDetails = details
		return check
	}
	details.Authentication = health.AuthenticationAuthenticated
	if len(details.ConflictingExecutables) > 0 {
		check := finding(health.SubsystemCodex, health.StatusUnhealthy, "CODEX_EXECUTABLE_CONFLICT", "More than one canonical Codex executable is discoverable.", "Remove stale Codex installations from PATH or configure the selected provider with one explicit executable, then restart the daemon.")
		check.ProviderDetails = details
		return check
	}
	check := healthy(health.SubsystemCodex, "CODEX_READY", fmt.Sprintf("Codex %s is pinned and authenticated.", details.Version))
	check.ProviderDetails = details
	return check
}

func (doctor *Doctor) githubCheck(ctx context.Context, projectRoot string) health.Check {
	github, err := doctor.runner.LookPath("gh")
	if err != nil {
		return finding(health.SubsystemGitHub, health.StatusDegraded, "GITHUB_CLI_NOT_FOUND", "GitHub CLI is not available on PATH.", "Install GitHub CLI, add it to PATH, and restart the daemon before enabling GitHub delivery.")
	}
	if !filepath.IsAbs(projectRoot) {
		return finding(health.SubsystemGitHub, health.StatusDegraded, "GITHUB_PROJECT_ROOT_UNAVAILABLE", "GitHub delivery cannot resolve a remote without an absolute project root.", "Start DARKSTAR from the target Git repository and rerun doctor.")
	}
	git, err := doctor.runner.LookPath("git")
	if err != nil {
		return finding(health.SubsystemGitHub, health.StatusDegraded, "GITHUB_GIT_NOT_FOUND", "GitHub delivery cannot resolve the configured remote because Git is unavailable.", "Install Git for Windows, add it to PATH, and rerun doctor.")
	}
	adapter, err := githubcli.New(githubcli.Options{Executable: github, GitExecutable: git, Runner: githubAdapterRunner{runner: doctor.runner}, Now: doctor.now})
	if err != nil {
		return githubProbeFailure(err, doctor.githubRemote)
	}
	probeContext, cancel := context.WithTimeout(ctx, commandTimeout)
	defer cancel()
	observation, err := adapter.ProbeHealth(probeContext, deliveryport.HealthRequest{
		LocalRepository: filepath.Clean(projectRoot),
		RemoteName:      doctor.githubRemote,
	})
	if err != nil {
		return githubProbeFailure(err, doctor.githubRemote)
	}
	return githubObservationCheck(observation)
}

func githubObservationCheck(observation deliveryport.HealthObservation) health.Check {
	repository := fmt.Sprintf("%s/%s", observation.Repository.Owner, observation.Repository.Name)
	switch outcome := observation.Outcome.(type) {
	case deliveryport.HealthReady:
		return healthy(health.SubsystemGitHub, "GITHUB_READY", fmt.Sprintf("Git remote %s resolves to %s on %s with base branch %s; the authenticated account can push.", observation.Remote.Name, repository, observation.Repository.Host, observation.BaseBranch.Name))
	case deliveryport.HealthReadOnly:
		return finding(health.SubsystemGitHub, health.StatusDegraded, "GITHUB_PUSH_PERMISSION_REQUIRED", fmt.Sprintf("Git remote %s resolves to %s, but the authenticated account cannot push.", observation.Remote.Name, repository), outcome.Reason)
	case deliveryport.HealthUnauthenticated:
		return finding(health.SubsystemGitHub, health.StatusDegraded, "GITHUB_AUTH_REQUIRED", fmt.Sprintf("Git remote %s resolves to %s, but GitHub authentication is not ready.", observation.Remote.Name, repository), outcome.Reason)
	case deliveryport.HealthUnavailable:
		return finding(health.SubsystemGitHub, health.StatusDegraded, "GITHUB_REPOSITORY_UNAVAILABLE", fmt.Sprintf("Git remote %s resolves to %s, but the GitHub repository is unavailable.", observation.Remote.Name, repository), outcome.Reason)
	case deliveryport.HealthDegraded:
		return finding(health.SubsystemGitHub, health.StatusDegraded, "GITHUB_DELIVERY_DEGRADED", fmt.Sprintf("GitHub delivery health for remote %s is degraded.", observation.Remote.Name), outcome.Reason)
	default:
		return finding(health.SubsystemGitHub, health.StatusDegraded, "GITHUB_HEALTH_INVALID", "The GitHub delivery adapter returned an unknown health outcome.", "Repair or upgrade the GitHub delivery adapter, then rerun doctor.")
	}
}

func githubProbeFailure(err error, remoteName string) health.Check {
	var classified *ports.Failure
	if !errors.As(err, &classified) {
		return finding(health.SubsystemGitHub, health.StatusDegraded, "GITHUB_PROBE_FAILED", "GitHub delivery health could not be determined.", "Inspect Git and GitHub CLI configuration, then rerun doctor.")
	}
	switch classified.Code {
	case ports.FailureNotFound:
		return finding(health.SubsystemGitHub, health.StatusDegraded, "GITHUB_REMOTE_NOT_FOUND", fmt.Sprintf("Git remote %s could not be resolved from the selected project.", remoteName), fmt.Sprintf("Configure Git remote %s for the target GitHub repository, then rerun doctor.", remoteName))
	case ports.FailureInvalidRequest:
		return finding(health.SubsystemGitHub, health.StatusDegraded, "GITHUB_REMOTE_INVALID", fmt.Sprintf("Git remote %s does not identify a valid GitHub repository.", remoteName), "Correct the project remote URL or configured remote name, then rerun doctor.")
	case ports.FailureUnavailable:
		return finding(health.SubsystemGitHub, health.StatusDegraded, "GITHUB_DELIVERY_UNAVAILABLE", "GitHub delivery tooling is unavailable.", "Repair the Git and GitHub CLI installations, then rerun doctor.")
	case ports.FailureTimeout:
		return finding(health.SubsystemGitHub, health.StatusDegraded, "GITHUB_PROBE_TIMEOUT", "GitHub delivery health timed out.", "Check network access to GitHub and rerun doctor.")
	case ports.FailureCancelled:
		return finding(health.SubsystemGitHub, health.StatusDegraded, "GITHUB_PROBE_CANCELLED", "GitHub delivery health was cancelled.", "Rerun doctor when the operation can complete.")
	case ports.FailureProtocolDrift:
		return finding(health.SubsystemGitHub, health.StatusDegraded, "GITHUB_RESPONSE_INVALID", "GitHub returned an incompatible health response.", "Upgrade GitHub CLI or the GitHub delivery adapter, then rerun doctor.")
	default:
		return finding(health.SubsystemGitHub, health.StatusDegraded, "GITHUB_PROBE_FAILED", "GitHub delivery health could not be determined.", "Inspect Git and GitHub CLI configuration, then rerun doctor.")
	}
}

func (doctor *Doctor) configurationCheck(_ context.Context, projectRoot string) health.Check {
	if !filepath.IsAbs(projectRoot) {
		return finding(health.SubsystemConfiguration, health.StatusDegraded, "CONFIGURATION_PROJECT_ROOT_UNAVAILABLE", "Project configuration cannot be located without an absolute project root.", "Start DARKSTAR from the target project and rerun doctor.")
	}
	locations, err := configuration.ResolveFileLocations(doctor.paths, projectRoot)
	if err != nil {
		return finding(health.SubsystemConfiguration, health.StatusUnhealthy, "CONFIGURATION_PATHS_INVALID", "Configuration file locations could not be resolved.", "Repair application paths and restart the daemon.")
	}
	defaults, err := config.Defaults(map[string]any{})
	if err != nil {
		return finding(health.SubsystemConfiguration, health.StatusUnhealthy, "CONFIGURATION_DEFAULTS_INVALID", "Shipped configuration defaults are invalid.", "Reinstall the current DARKSTAR build.")
	}
	if _, err := configuration.Resolve(defaults, locations); err != nil {
		return finding(health.SubsystemConfiguration, health.StatusUnhealthy, "CONFIGURATION_INVALID", "User or project configuration is invalid.", "Run configuration validation, correct the reported YAML file, and rerun doctor.")
	}
	return healthy(health.SubsystemConfiguration, "CONFIGURATION_READY", "User, project, and shipped configuration layers are valid.")
}

func (doctor *Doctor) providerCheck(ctx context.Context) health.Check {
	if doctor.provider == nil {
		return finding(health.SubsystemProvider, health.StatusDegraded, "PROVIDER_NOT_CONFIGURED", "No reasoning provider is registered with the daemon.", "Configure and enable a provider before starting a provider-backed run.")
	}
	observation, err := doctor.provider.ProbeHealth(ctx)
	if err != nil {
		return finding(health.SubsystemProvider, health.StatusUnhealthy, "PROVIDER_PROBE_FAILED", "The selected provider health probe failed.", "Inspect provider configuration and authentication, then rerun doctor.")
	}
	manifest, err := doctor.provider.Capabilities(ctx)
	if err != nil {
		check := finding(health.SubsystemProvider, health.StatusUnhealthy, "PROVIDER_CAPABILITIES_UNAVAILABLE", "The selected provider could not report its final capability manifest.", "Repair or upgrade the provider adapter, then rerun doctor.")
		check.ProviderDetails = projectProviderDetails(observation, providerport.CapabilityManifest{})
		return check
	}
	details := projectProviderDetails(observation, manifest)
	capabilitySummary := fmt.Sprintf("%d available and %d unavailable capabilities", len(details.AvailableCapabilities), len(details.UnavailableCapabilities))
	withDetails := func(check health.Check) health.Check {
		check.ProviderDetails = details
		return check
	}
	switch observation.State {
	case providerport.HealthAvailable:
		return withDetails(healthy(health.SubsystemProvider, "PROVIDER_READY", fmt.Sprintf("Provider %s %s is available with %s.", observation.Provider, observation.ProviderVersion, capabilitySummary)))
	case providerport.HealthDegraded:
		return withDetails(finding(health.SubsystemProvider, health.StatusDegraded, "PROVIDER_DEGRADED", "The selected provider reports degraded readiness.", "Inspect provider authentication, usage, version, instruction sources, and capabilities before starting a run."))
	case providerport.HealthUnauthenticated:
		return withDetails(finding(health.SubsystemProvider, health.StatusUnhealthy, "PROVIDER_AUTH_REQUIRED", "The selected provider requires authentication.", "Authenticate the selected provider, then rerun doctor."))
	case providerport.HealthUsageExhausted:
		return withDetails(finding(health.SubsystemProvider, health.StatusUnhealthy, "PROVIDER_USAGE_EXHAUSTED", "The selected provider cannot admit another attempt because usage is exhausted.", "Wait for the provider limit to reset or resolve the account usage limit, then rerun doctor."))
	case providerport.HealthUnavailable:
		return withDetails(finding(health.SubsystemProvider, health.StatusUnhealthy, "PROVIDER_UNAVAILABLE", "The selected provider is unavailable.", "Repair the provider executable or connectivity, then rerun doctor."))
	default:
		return withDetails(finding(health.SubsystemProvider, health.StatusUnhealthy, "PROVIDER_HEALTH_INVALID", "The selected provider returned an unknown health state.", "Upgrade or repair the provider adapter, then rerun doctor."))
	}
}

func (doctor *Doctor) runCommand(ctx context.Context, executable string, arguments ...string) ([]byte, error) {
	commandContext, cancel := context.WithTimeout(ctx, commandTimeout)
	defer cancel()
	return doctor.runner.Output(commandContext, executable, arguments...)
}

type executableResolution struct {
	Selected  string
	Conflicts []string
	Err       error
}

func resolveExecutable(runner CommandRunner, configured, name string) executableResolution {
	target := strings.TrimSpace(configured)
	if target == "" {
		target = name
	}
	selected, err := runner.LookPath(target)
	if err != nil {
		return executableResolution{Err: err}
	}
	selected = canonicalPath(selected)
	candidates := []string{selected}
	if enumerator, ok := runner.(executableEnumerator); ok {
		if discovered, discoverErr := enumerator.LookPaths(name); discoverErr == nil {
			candidates = append(candidates, discovered...)
		}
	}
	seen := map[string]struct{}{strings.ToLower(selected): {}}
	conflicts := []string{}
	for _, candidate := range candidates {
		canonical := canonicalPath(candidate)
		identity := strings.ToLower(canonical)
		if canonical == "" {
			continue
		}
		if _, duplicate := seen[identity]; duplicate {
			continue
		}
		seen[identity] = struct{}{}
		conflicts = append(conflicts, canonical)
	}
	sort.Strings(conflicts)
	return executableResolution{Selected: selected, Conflicts: conflicts}
}

func canonicalPath(path string) string {
	absolute, err := filepath.Abs(strings.TrimSpace(path))
	if err != nil {
		return filepath.Clean(strings.TrimSpace(path))
	}
	if evaluated, evaluateErr := filepath.EvalSymlinks(absolute); evaluateErr == nil {
		absolute = evaluated
	}
	return filepath.Clean(absolute)
}

func exactCodexVersion(output string) string {
	fields := strings.Fields(strings.TrimSpace(output))
	if len(fields) < 2 || (fields[0] != "codex-cli" && fields[0] != "codex") {
		return ""
	}
	return fields[len(fields)-1]
}

func projectProviderDetails(observation providerport.Health, manifest providerport.CapabilityManifest) *health.ProviderDetails {
	authentication := health.AuthenticationState(observation.Authentication)
	if authentication == "" {
		authentication = health.AuthenticationUnknown
	}
	usage := health.UsageReadiness(observation.Usage)
	if usage == "" {
		usage = health.UsageUnknown
	}
	details := &health.ProviderDetails{
		Name: observation.Provider, Version: observation.ProviderVersion, ExecutableIdentity: observation.ExecutableIdentity, Platform: observation.Platform,
		Authentication: authentication, Usage: usage,
		InstructionSources: append([]string(nil), observation.InstructionSources...), ConflictingExecutables: []string{},
		AvailableCapabilities: []health.AvailableCapability{}, UnavailableCapabilities: []health.UnavailableCapability{},
	}
	for name, capability := range manifest.Features {
		switch value := capability.(type) {
		case providerport.AvailableCapability:
			details.AvailableCapabilities = append(details.AvailableCapabilities, health.AvailableCapability{Name: name, Version: value.Version})
		case providerport.UnavailableCapability:
			details.UnavailableCapabilities = append(details.UnavailableCapabilities, health.UnavailableCapability{Name: name, Reason: value.Reason})
		}
	}
	return details
}

func healthy(subsystem health.Subsystem, code, message string) health.Check {
	return health.Check{Subsystem: subsystem, Status: health.StatusHealthy, Code: code, Message: message}
}

func finding(subsystem health.Subsystem, status health.Status, code, message, action string) health.Check {
	return health.Check{Subsystem: subsystem, Status: status, Code: code, Message: message, Action: action}
}
