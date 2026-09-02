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
	"strings"
	"sync"
	"time"

	"darkstar/src/adapters/statestore/sqlite"
	"darkstar/src/core/config"
	"darkstar/src/core/health"
	"darkstar/src/daemon"
	"darkstar/src/daemon/configuration"
	platformport "darkstar/src/ports/platform"
	providerport "darkstar/src/ports/provider"
)

const commandTimeout = 5 * time.Second

// CommandRunner is the non-mutating executable boundary used by tool probes.
type CommandRunner interface {
	LookPath(string) (string, error)
	Run(context.Context, string, ...string) error
}

type osCommandRunner struct{}

func (osCommandRunner) LookPath(name string) (string, error) { return exec.LookPath(name) }

func (osCommandRunner) Run(ctx context.Context, name string, arguments ...string) error {
	command := exec.CommandContext(ctx, name, arguments...)
	command.Stdin = nil
	command.Stdout = io.Discard
	command.Stderr = io.Discard
	return command.Run()
}

// Options supplies the live dependencies and observations used by Doctor.
type Options struct {
	Paths       platformport.Paths
	Database    *sqlite.Database
	Process     daemon.ProcessIdentity
	ProjectRoot string
	Provider    providerport.Provider
	Runner      CommandRunner
	Now         func() time.Time
}

// Doctor is a reusable, side-effect-bounded local readiness probe.
type Doctor struct {
	paths       platformport.Paths
	database    *sqlite.Database
	process     daemon.ProcessIdentity
	projectRoot string
	provider    providerport.Provider
	runner      CommandRunner
	now         func() time.Time
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
	return &Doctor{
		paths:       options.Paths,
		database:    options.Database,
		process:     options.Process,
		projectRoot: options.ProjectRoot,
		provider:    options.Provider,
		runner:      runner,
		now:         now,
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
		doctor.githubCheck,
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
	if err := doctor.runCommand(ctx, git, "--version"); err != nil {
		return finding(health.SubsystemGit, health.StatusUnhealthy, "GIT_EXECUTABLE_FAILED", "Git could not report its version.", "Repair the Git installation and rerun doctor.")
	}
	if !filepath.IsAbs(projectRoot) {
		return finding(health.SubsystemGit, health.StatusDegraded, "GIT_PROJECT_ROOT_UNAVAILABLE", "The daemon has no absolute project root to inspect.", "Start DARKSTAR from the target repository and rerun doctor.")
	}
	if err := doctor.runCommand(ctx, git, "-C", projectRoot, "rev-parse", "--show-toplevel"); err != nil {
		return finding(health.SubsystemGit, health.StatusDegraded, "GIT_REPOSITORY_NOT_FOUND", "The current project root is not a Git worktree.", "Run DARKSTAR from a Git worktree or initialize the project repository.")
	}
	return healthy(health.SubsystemGit, "GIT_READY", "Git is executable and the current project is a worktree.")
}

func (doctor *Doctor) codexCheck(ctx context.Context) health.Check {
	codex, err := doctor.runner.LookPath("codex")
	if err != nil {
		return finding(health.SubsystemCodex, health.StatusDegraded, "CODEX_EXECUTABLE_NOT_FOUND", "Codex is not available on PATH.", "Install Codex, add it to PATH, and restart the daemon before selecting a Codex provider.")
	}
	if err := doctor.runCommand(ctx, codex, "--version"); err != nil {
		return finding(health.SubsystemCodex, health.StatusDegraded, "CODEX_VERSION_UNAVAILABLE", "Codex could not report its version.", "Repair or upgrade the Codex installation, then rerun doctor.")
	}
	if err := doctor.runCommand(ctx, codex, "login", "status"); err != nil {
		return finding(health.SubsystemCodex, health.StatusDegraded, "CODEX_AUTH_REQUIRED", "Codex is installed but authentication is not ready.", "Sign in with `codex login`, then rerun doctor.")
	}
	return healthy(health.SubsystemCodex, "CODEX_READY", "Codex is executable and authenticated.")
}

func (doctor *Doctor) githubCheck(ctx context.Context) health.Check {
	github, err := doctor.runner.LookPath("gh")
	if err != nil {
		return finding(health.SubsystemGitHub, health.StatusDegraded, "GITHUB_CLI_NOT_FOUND", "GitHub CLI is not available on PATH.", "Install GitHub CLI, add it to PATH, and restart the daemon before enabling GitHub delivery.")
	}
	if err := doctor.runCommand(ctx, github, "--version"); err != nil {
		return finding(health.SubsystemGitHub, health.StatusDegraded, "GITHUB_CLI_FAILED", "GitHub CLI could not report its version.", "Repair the GitHub CLI installation, then rerun doctor.")
	}
	if err := doctor.runCommand(ctx, github, "auth", "status", "--hostname", "github.com"); err != nil {
		return finding(health.SubsystemGitHub, health.StatusDegraded, "GITHUB_AUTH_REQUIRED", "GitHub CLI is installed but github.com authentication is not ready.", "Run `gh auth login --hostname github.com`, then rerun doctor.")
	}
	return healthy(health.SubsystemGitHub, "GITHUB_READY", "GitHub CLI is executable and authenticated for github.com.")
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
	switch observation.State {
	case providerport.HealthAvailable:
		return healthy(health.SubsystemProvider, "PROVIDER_READY", fmt.Sprintf("Provider %s is available.", observation.Provider))
	case providerport.HealthDegraded:
		return finding(health.SubsystemProvider, health.StatusDegraded, "PROVIDER_DEGRADED", "The selected provider reports degraded readiness.", "Inspect provider diagnostics and capabilities before starting a run.")
	case providerport.HealthUnauthenticated:
		return finding(health.SubsystemProvider, health.StatusUnhealthy, "PROVIDER_AUTH_REQUIRED", "The selected provider requires authentication.", "Authenticate the selected provider, then rerun doctor.")
	case providerport.HealthUnavailable:
		return finding(health.SubsystemProvider, health.StatusUnhealthy, "PROVIDER_UNAVAILABLE", "The selected provider is unavailable.", "Repair the provider executable or connectivity, then rerun doctor.")
	default:
		return finding(health.SubsystemProvider, health.StatusUnhealthy, "PROVIDER_HEALTH_INVALID", "The selected provider returned an unknown health state.", "Upgrade or repair the provider adapter, then rerun doctor.")
	}
}

func (doctor *Doctor) runCommand(ctx context.Context, executable string, arguments ...string) error {
	commandContext, cancel := context.WithTimeout(ctx, commandTimeout)
	defer cancel()
	return doctor.runner.Run(commandContext, executable, arguments...)
}

func healthy(subsystem health.Subsystem, code, message string) health.Check {
	return health.Check{Subsystem: subsystem, Status: health.StatusHealthy, Code: code, Message: message}
}

func finding(subsystem health.Subsystem, status health.Status, code, message, action string) health.Check {
	return health.Check{Subsystem: subsystem, Status: status, Code: code, Message: message, Action: action}
}
