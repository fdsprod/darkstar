// Package cli implements the runtime's human- and automation-facing command-line boundary.
package cli

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"regexp"
	"time"

	"darkstar/src/adapters/artifactstore/folder"
	configurationfilesystem "darkstar/src/adapters/configurationstore/filesystem"
	"darkstar/src/adapters/contentprocessor/common"
	"darkstar/src/adapters/contentprocessor/commonimage"
	"darkstar/src/adapters/provider/fake"
	"darkstar/src/adapters/statestore/sqlite"
	workflowfilesystem "darkstar/src/adapters/workflowstore/filesystem"
	localapi "darkstar/src/api"
	clientapi "darkstar/src/api/client"
	"darkstar/src/core/artifactcheckpoint"
	"darkstar/src/core/artifactderive"
	"darkstar/src/core/artifactingest"
	"darkstar/src/core/artifactops"
	"darkstar/src/core/configmutation"
	"darkstar/src/core/health"
	"darkstar/src/core/lateevidence"
	"darkstar/src/core/recovery"
	"darkstar/src/core/runexecution"
	"darkstar/src/core/runexport"
	"darkstar/src/core/workflow"
	"darkstar/src/core/workmanagement"
	"darkstar/src/daemon"
	daemonconfiguration "darkstar/src/daemon/configuration"
	"darkstar/src/doctor"
	"darkstar/src/platform/windows"
	platformport "darkstar/src/ports/platform"
	"darkstar/src/ports/statestore"
)

const usage = `DARKSTAR

Usage:
  darkstar [command] [--json]

Commands:
  agent      Inspect and control queued or running agents
  api        Inspect the autostarted local API
  artifact   Ingest, bind, inspect, derive, lint, and revise artifacts
  approval   Inspect and decide artifact review approvals
  checkpoint List and inspect artifact review checkpoints
  configuration Inspect, preview, apply, and restore typed settings
  daemon     Run and control the per-user daemon
  doctor     Report subsystem readiness and remediation codes
  help       Show this help
  input      List, inspect, answer, and retry provider input requests
  review     Inspect and continue checkpoint review sessions
  project    Register, list, and inspect projects
  run        Start, inspect, control, watch, and export runs
  work       Create, import, list, and inspect work
  workflow   Author, validate, publish, list, show, graph, and preview workflows
  version    Show version information

API commands:
  api status [--json]      Discover or autostart the daemon and verify its API

Project commands:
  project add|register [path] [--name <name>] [--idempotency-key <key>] [--json]
  project list [--json]
  project show <project-id> [--json]

Work commands:
  work create <description> [--project <project-id>] [--priority <n>] [--idempotency-key <key>] [--json]
  work import <source-ref> [--project <project-id>] [--title <title>] [--priority <n>] [--idempotency-key <key>] [--json]
  work list [--project <project-id>] [--json]
  work show <work-id> [--json]

Run commands:
  run start <work-id> [--workflow <name>] [--version <version>] [--idempotency-key <key>] [--json]
  run start --scenario <fake-success|fake-restart> [--idempotency-key <key>] [--json]
  run list [--limit <n>] [--after <run-id>] [--json]
  run show <run-id> [--json]
  run watch <run-id> [--json]
  run pause <run-id> [--idempotency-key <key>] [--json]
  run resume <run-id> [--idempotency-key <key>] [--json]
  run retry <run-id> [--node <id>] [--idempotency-key <key>] [--json]
  run continue <run-id> --until <node> [--idempotency-key <key>] [--json]
  run cancel <run-id> [--idempotency-key <key>] [--json]
  run export <run-id> --output <file> [--json]
  run readiness show <run-id> [--json]
  run readiness decide <run-id> --action <continue|accept_route_change|supply_input|cancel> --reason <text> [--remedy <code>] [--idempotency-key <key>] [--json]

Agent commands:
  agent list [--json]
  agent status [<attempt-id>] [--json]
  agent logs <attempt-id> [--follow] [--json]
  agent cancel <attempt-id> [--idempotency-key <key>] [--json]
  agent permissions list [--attempt <attempt-id>] [--status <status>] [--json]
  agent permissions show <permission-id> [--json]
  agent permissions decide <permission-id> <allow_once|deny|cancel> [--idempotency-key <key>] [--json]
  agent permissions retry <permission-id> [--json]

Approval and checkpoint commands:
  approval show <approval-id> [--json]
  approval decide <approval-id> <approve|request_changes|reject> [--comment <text>] [--idempotency-key <key>] [--json]
  checkpoint list [--run <run-id>] [--status <pending|approved|changes_requested|rejected>] [--json]
  checkpoint show <checkpoint-id> [--json]
  checkpoint approve <approval-id> [--message <text>] [--json]
  checkpoint request-changes|reject <approval-id> --message <text> [--json]
  checkpoint answer <input-id> --file <answers.json> [--json]

Review-session commands:
  review show <approval-id> [--json]
  review history <checkpoint-id> [--json]
  review feedback <approval-id> --message <text> [--idempotency-key <key>] [--json]
  review resume <approval-id> --attempt <attempt-id> [--idempotency-key <key>] [--json]
  review respond <approval-id> --attempt <attempt-id> --outcome <revised|failed|cancelled> [--artifact <artifact-id>] [--version <n>] [--next-approval <approval-id>] [--message <text>] [--idempotency-key <key>] [--json]
  review approve|reject <approval-id> [--comment <text>] [--idempotency-key <key>] [--json]

Configuration commands:
  configuration catalog [--json]
  configuration state [--project <project-id>] [--json]
  configuration preview --key <key> (--value-type <type> --value <value> | --unset) --revision <digest> [--project <project-id>] [--json]
  configuration set --key <key> --value-type <type> --value <value> --revision <digest> [--project <project-id>] [--idempotency-key <key>] [--json]
  configuration unset --key <key> --revision <digest> [--project <project-id>] [--idempotency-key <key>] [--json]
  configuration restore --revision <digest> [--project <project-id>] [--idempotency-key <key>] [--json]
  configuration secret-set <name> (--file <path> | --stdin) --revision <digest> [--idempotency-key <key>] [--json]

Input commands:
  input list [--run <run-id> | --attempt <attempt-id>] [--status <pending|answer_recorded|answered>] [--json]
  input show <input-id> [--json]
  input answer <input-id> --answer <json> [--idempotency-key <key>] [--json]
  input retry <input-id> [--json]

Artifact commands:
  artifact ingest (--file <path> | --paste <text> | --stdin) [--media-type <type>] [--role <role>] [--json]
  artifact attach <artifact-id>@<version> --to <kind>:<id> [--json]
  artifact detach <binding-id> [--json]
  artifact list [--target <kind>:<id>] [--json]
  artifact show <artifact-id>@<version> [--json]
  artifact diff <artifact-id> --from <version> --to <version> [--json]
  artifact extract|lint|representations <artifact-id>@<version> [--json]
  artifact revise <artifact-id>@<base-version> (--file <path> | --paste <text> | --stdin) [--media-type <type>] [--json]
  artifact impact <artifact-id>@<version> --target <kind>:<id> [--run <run-id>] [--json]

Workflow commands:
  workflow list [--json]
  workflow show <name> [--version <version>] [--json]
  workflow validate <file> [--json]
  workflow install <file> [--json]
  workflow graph <name> [--version <version>] [--json]
  workflow preview <name> [--version <version>] [--from <node>] [--until <node>]... [--input <file>] [--json]
  workflow library [--json]
  workflow duplicate <name> <new-name> --version <version> --scope <user|project> --scope-reference <reference> --idempotency-key <key> [--json]
  workflow archive <name> <version> [--json]
  workflow draft-create <file> --scope <user|project> --scope-reference <reference> --idempotency-key <key> [--json]
  workflow draft-show <draft-id> [--json]
  workflow draft-update <draft-id> <file> --revision <n> [--json]
  workflow draft-rename <draft-id> <name> --revision <n> [--json]
  workflow draft-validate <draft-id> --revision <n> [--json]
  workflow draft-publish <draft-id> <version> --revision <n> [--json]
  workflow draft-discard <draft-id> --revision <n> [--json]

Daemon commands:
  daemon run [--json]      Run in the foreground
  daemon start [--json]    Start in the background
  daemon stop [--json]     Stop gracefully, then force if required
  daemon restart [--json]  Stop and start the daemon
  daemon status [--json]   Show daemon status without autostarting it

Example:
  darkstar daemon status --json
`

// Version is replaced by release builds through -ldflags.
var Version = "dev"

var runIdentityPattern = regexp.MustCompile(`^run_[0-9A-HJKMNP-TV-Z]{26}$`)

type helpOutput struct {
	SchemaVersion int    `json:"schemaVersion"`
	Usage         string `json:"usage"`
}

type versionOutput struct {
	SchemaVersion int    `json:"schemaVersion"`
	Version       string `json:"version"`
}

// Run executes the command line and returns a process exit code.
func Run(args []string, stdout, stderr io.Writer) int {
	cleanArgs, jsonOutput, err := parseJSONFlag(args)
	if err != nil {
		return writeCommandError(stdout, stderr, jsonOutput, "darkstar", "ARGUMENT_INVALID", err.Error(), false, ExitInvalidInput)
	}
	if len(cleanArgs) == 0 || cleanArgs[0] == "help" || cleanArgs[0] == "--help" || cleanArgs[0] == "-h" {
		if len(cleanArgs) > 1 {
			return writeCommandError(stdout, stderr, jsonOutput, "darkstar help", "ARGUMENT_INVALID", "help accepts no arguments", false, ExitInvalidInput)
		}
		if jsonOutput {
			if err := writeJSON(stdout, helpOutput{SchemaVersion: machineSchemaVersion, Usage: usage}); err != nil {
				return writeCommandError(stdout, stderr, false, "darkstar help", "OUTPUT_FAILED", err.Error(), false, ExitInvariantViolation)
			}
		} else {
			_, _ = fmt.Fprint(stdout, usage)
		}
		return int(ExitSuccess)
	}

	switch cleanArgs[0] {
	case "version", "--version":
		if len(cleanArgs) != 1 {
			return writeCommandError(stdout, stderr, jsonOutput, "darkstar version", "ARGUMENT_INVALID", "version accepts no arguments", false, ExitInvalidInput)
		}
		if jsonOutput {
			if err := writeJSON(stdout, versionOutput{SchemaVersion: machineSchemaVersion, Version: Version}); err != nil {
				return writeCommandError(stdout, stderr, false, "darkstar version", "OUTPUT_FAILED", err.Error(), false, ExitInvariantViolation)
			}
		} else {
			_, _ = fmt.Fprintf(stdout, "darkstar %s\n", Version)
		}
		return int(ExitSuccess)
	case "daemon":
		return runDaemon(cleanArgs[1:], jsonOutput, stdout, stderr)
	case "api":
		return runAPI(cleanArgs[1:], jsonOutput, stdout, stderr)
	case "agent":
		return runAgent(cleanArgs[1:], jsonOutput, stdout, stderr)
	case "approval":
		return runApproval(cleanArgs[1:], jsonOutput, stdout, stderr)
	case "artifact":
		return runArtifact(cleanArgs[1:], jsonOutput, stdout, stderr)
	case "checkpoint":
		return runCheckpoint(cleanArgs[1:], jsonOutput, stdout, stderr)
	case "configuration", "config":
		return runConfiguration(cleanArgs[1:], jsonOutput, stdout, stderr)
	case "doctor":
		return runDoctor(cleanArgs[1:], jsonOutput, stdout, stderr)
	case "project":
		return runProject(cleanArgs[1:], jsonOutput, stdout, stderr)
	case "input":
		return runInput(cleanArgs[1:], jsonOutput, stdout, stderr)
	case "review":
		return runReview(cleanArgs[1:], jsonOutput, stdout, stderr)
	case "run":
		return runRun(cleanArgs[1:], jsonOutput, stdout, stderr)
	case "work":
		return runWork(cleanArgs[1:], jsonOutput, stdout, stderr)
	case "workflow":
		return runWorkflow(cleanArgs[1:], jsonOutput, stdout, stderr)
	default:
		message := fmt.Sprintf("unknown command %q; run 'darkstar help' for usage", cleanArgs[0])
		return writeCommandError(stdout, stderr, jsonOutput, "darkstar", "ARGUMENT_INVALID", message, false, ExitInvalidInput)
	}
}

func parseJSONFlag(args []string) ([]string, bool, error) {
	clean := make([]string, 0, len(args))
	jsonOutput := false
	for index, arg := range args {
		if arg != "--json" {
			clean = append(clean, arg)
			continue
		}
		if jsonOutput {
			return clean, true, errors.New("--json may be specified only once")
		}
		jsonOutput = true
		if index != len(args)-1 {
			return clean, true, errors.New("--json must be the final argument")
		}
	}
	return clean, jsonOutput, nil
}

func runDaemon(args []string, jsonOutput bool, stdout, stderr io.Writer) int {
	if len(args) != 1 {
		return writeCommandError(stdout, stderr, jsonOutput, "darkstar daemon", "ARGUMENT_INVALID", "exactly one command is required (run, start, stop, restart, status)", false, ExitInvalidInput)
	}
	switch args[0] {
	case "run", "start", "stop", "restart", "status":
	default:
		return writeCommandError(stdout, stderr, jsonOutput, "darkstar daemon", "ARGUMENT_INVALID", fmt.Sprintf("unknown command %q", args[0]), false, ExitInvalidInput)
	}

	manager, err := newDaemonManager(context.Background())
	if err != nil {
		return writeCommandError(stdout, stderr, jsonOutput, "darkstar daemon", "DAEMON_RUNTIME_UNAVAILABLE", err.Error(), true, ExitTransientFailure)
	}

	switch args[0] {
	case "run":
		return runDaemonForeground(manager, jsonOutput, stdout, stderr)
	case "start":
		return startDaemon(context.Background(), manager, jsonOutput, stdout, stderr)
	case "stop":
		return stopDaemon(context.Background(), manager, jsonOutput, stdout, stderr)
	case "restart":
		return restartDaemon(context.Background(), manager, jsonOutput, stdout, stderr)
	case "status":
		return daemonStatus(manager, jsonOutput, stdout, stderr)
	default:
		panic("validated daemon command was not handled")
	}
}

type daemonAPIService struct {
	server                   *localapi.Server
	paths                    platformport.Paths
	projectRoot              string
	defaultWorkflowDirectory string
	database                 *sqlite.Database
	executions               *runexecution.Service
}

func (service *daemonAPIService) Start(ctx context.Context, state daemon.State) error {
	if err := os.MkdirAll(service.paths.Data, 0o700); err != nil {
		return fmt.Errorf("create daemon data directory: %w", err)
	}
	database, err := sqlite.Open(ctx, filepath.Join(service.paths.Data, "darkstar.db"), sqlite.Options{})
	if err != nil {
		return fmt.Errorf("open daemon state: %w", err)
	}
	service.database = database
	workflowDirectories, err := service.workflowDirectories()
	if err != nil {
		_ = database.Close()
		service.database = nil
		return fmt.Errorf("configure workflow sources: %w", err)
	}
	workflowSource, err := workflowfilesystem.New(workflowDirectories...)
	if err != nil {
		_ = database.Close()
		service.database = nil
		return fmt.Errorf("configure workflow sources: %w", err)
	}
	workflowCatalog, err := workflow.NewCatalog(workflowSource, database)
	if err != nil {
		_ = database.Close()
		service.database = nil
		return err
	}
	if _, err := workflowCatalog.InstallConfigured(ctx); err != nil {
		_ = database.Close()
		service.database = nil
		return fmt.Errorf("install configured workflows: %w", err)
	}
	if err := service.server.SetWorkflows(workflowCatalog); err != nil {
		_ = database.Close()
		service.database = nil
		return err
	}
	reconciler, err := recovery.New(database, nil)
	if err != nil {
		_ = database.Close()
		service.database = nil
		return err
	}
	report, err := reconciler.Run(ctx, state.InstanceID)
	if err != nil {
		_ = database.Close()
		service.database = nil
		return fmt.Errorf("startup reconciliation: %w", err)
	}
	if err := service.server.SetRecoveryStatus(localapi.RecoveryStatus{
		Reconciled: len(report.Results), ReconcileRequired: report.ReconcileRequired(),
	}); err != nil {
		_ = database.Close()
		service.database = nil
		return err
	}
	providerAdapter, err := fake.New(fake.Scenario{})
	if err != nil {
		_ = database.Close()
		service.database = nil
		return fmt.Errorf("construct default provider: %w", err)
	}
	reporter := doctor.New(doctor.Options{
		Paths:       service.paths,
		Database:    database,
		Process:     state.Process,
		ProjectRoot: service.projectRoot,
		Provider:    providerAdapter,
	})
	if err := service.server.SetDoctor(reporter); err != nil {
		_ = database.Close()
		service.database = nil
		return err
	}
	if err := service.server.SetConfiguration(reporter); err != nil {
		_ = database.Close()
		service.database = nil
		return err
	}
	configurationLocations, err := daemonconfiguration.ResolveFileLocations(service.paths, service.projectRoot)
	if err != nil {
		_ = database.Close()
		service.database = nil
		return err
	}
	configurationFiles, err := configurationfilesystem.New(configurationLocations, service.paths.Data)
	if err != nil {
		_ = database.Close()
		service.database = nil
		return err
	}
	configurationMutations, err := configmutation.New(configurationFiles, database, service.projectRoot)
	if err != nil {
		_ = database.Close()
		service.database = nil
		return err
	}
	if err := service.server.SetConfigurationMutations(configurationMutations); err != nil {
		_ = database.Close()
		service.database = nil
		return err
	}
	logs, err := localapi.NewDirectoryLogs(service.paths.Logs)
	if err != nil {
		_ = database.Close()
		service.database = nil
		return err
	}
	if err := service.server.SetStreams(localapi.StreamServices{Events: database, Logs: logs}); err != nil {
		_ = database.Close()
		service.database = nil
		return err
	}
	work, err := workmanagement.New(database)
	if err != nil {
		_ = database.Close()
		service.database = nil
		return err
	}
	if err := service.server.SetWork(work); err != nil {
		_ = database.Close()
		service.database = nil
		return err
	}
	executions, err := runexecution.New(ctx, database, runexecution.ProviderFactoryFunc(newFakeRunProvider), logs)
	if err != nil {
		_ = database.Close()
		service.database = nil
		return err
	}
	if err := executions.SetAgentWorkspace(service.projectRoot); err != nil {
		_ = executions.Close()
		_ = database.Close()
		service.database = nil
		return err
	}
	if err := executions.SetWorkflowPlanner(workflowCatalog); err != nil {
		_ = executions.Close()
		_ = database.Close()
		service.database = nil
		return err
	}
	service.executions = executions
	if err := executions.ResumeActive(ctx); err != nil {
		_ = executions.Close()
		_ = database.Close()
		service.database = nil
		service.executions = nil
		return fmt.Errorf("resume active runs: %w", err)
	}
	if err := service.server.SetRuns(executions); err != nil {
		_ = executions.Close()
		_ = database.Close()
		service.database = nil
		service.executions = nil
		return err
	}
	if err := service.server.SetAgents(executions); err != nil {
		_ = executions.Close()
		_ = database.Close()
		service.database = nil
		service.executions = nil
		return err
	}
	if err := service.server.SetInputRequests(executions); err != nil {
		_ = executions.Close()
		_ = database.Close()
		service.database = nil
		service.executions = nil
		return err
	}
	if err := configureReadiness(service.server, database, workflowCatalog); err != nil {
		_ = executions.Close()
		_ = database.Close()
		service.database = nil
		service.executions = nil
		return err
	}
	closeArtifactSetup := func(cause error) error {
		_ = executions.Close()
		_ = database.Close()
		service.database = nil
		service.executions = nil
		return cause
	}
	artifactRoot, err := folder.ResolveRoot("", service.projectRoot)
	if err != nil {
		return closeArtifactSetup(err)
	}
	artifactStore, err := folder.New(artifactRoot)
	if err != nil {
		return closeArtifactSetup(err)
	}
	derivation, err := artifactderive.New(artifactStore, database, database, common.New(), commonimage.New())
	if err != nil {
		return closeArtifactSetup(err)
	}
	ingestion, err := artifactingest.New(artifactStore, database, derivation)
	if err != nil {
		return closeArtifactSetup(err)
	}
	impact, err := lateevidence.New(database, database, database, database, database)
	if err != nil {
		return closeArtifactSetup(err)
	}
	artifacts, err := artifactops.New(artifactStore, database, database, database, database, database, ingestion, derivation, impact)
	if err != nil {
		return closeArtifactSetup(err)
	}
	if err := service.server.SetArtifacts(artifacts); err != nil {
		return closeArtifactSetup(err)
	}
	checkpoints, err := artifactcheckpoint.New(database, database, database)
	if err != nil {
		return closeArtifactSetup(err)
	}
	if err := service.server.SetApprovals(checkpoints); err != nil {
		return closeArtifactSetup(err)
	}
	exporter, err := runexport.New(database, logs)
	if err != nil {
		_ = executions.Close()
		_ = database.Close()
		service.database = nil
		service.executions = nil
		return err
	}
	if err := service.server.SetRunExporter(exporter); err != nil {
		_ = executions.Close()
		_ = database.Close()
		service.database = nil
		service.executions = nil
		return err
	}
	if err := service.server.Start(ctx, state.Process.PID, state.Process.StartedAt); err != nil {
		_ = executions.Close()
		_ = database.Close()
		service.database = nil
		service.executions = nil
		return err
	}
	return nil
}

func (service *daemonAPIService) workflowDirectories() ([]workflowfilesystem.Directory, error) {
	defaultDirectory := service.defaultWorkflowDirectory
	if defaultDirectory == "" {
		executable, err := os.Executable()
		if err != nil {
			return nil, fmt.Errorf("resolve executable for shipped workflows: %w", err)
		}
		defaultDirectory = filepath.Join(filepath.Dir(executable), "workflows")
	}
	return workflowfilesystem.ResolveDirectories(defaultDirectory, service.paths, service.projectRoot)
}

func (service *daemonAPIService) Close() error {
	var executionErr error
	if service.executions != nil {
		executionErr = service.executions.Close()
		service.executions = nil
	}
	serverErr := service.server.Close()
	var databaseErr error
	if service.database != nil {
		databaseErr = service.database.Close()
		service.database = nil
	}
	return errors.Join(executionErr, serverErr, databaseErr)
}

type daemonRunEvent struct {
	SchemaVersion int                     `json:"schemaVersion"`
	Event         string                  `json:"event"`
	Process       *daemon.ProcessIdentity `json:"process,omitempty"`
}

func runDaemonForeground(manager *daemon.Manager, jsonOutput bool, stdout, stderr io.Writer) int {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()
	server, err := localapi.NewServer(manager.RuntimeDirectory())
	if err != nil {
		return writeCommandError(stdout, stderr, jsonOutput, "darkstar daemon run", "DAEMON_START_FAILED", err.Error(), true, ExitTransientFailure)
	}
	var outputErr error
	paths, err := resolveApplicationPaths(ctx)
	if err != nil {
		return writeCommandError(stdout, stderr, jsonOutput, "darkstar daemon run", "DAEMON_START_FAILED", err.Error(), true, ExitTransientFailure)
	}
	projectRoot, err := os.Getwd()
	if err != nil {
		return writeCommandError(stdout, stderr, jsonOutput, "darkstar daemon run", "DAEMON_START_FAILED", "resolve daemon working directory: "+err.Error(), true, ExitTransientFailure)
	}
	projectRoot, err = filepath.Abs(projectRoot)
	if err != nil {
		return writeCommandError(stdout, stderr, jsonOutput, "darkstar daemon run", "DAEMON_START_FAILED", "resolve absolute daemon working directory: "+err.Error(), true, ExitTransientFailure)
	}
	service := &daemonAPIService{server: server, paths: paths, projectRoot: projectRoot}
	err = manager.RunWithService(ctx, service, func(state daemon.State) {
		if jsonOutput {
			process := state.Process
			outputErr = writeJSON(stdout, daemonRunEvent{SchemaVersion: machineSchemaVersion, Event: "running", Process: &process})
		} else {
			_, outputErr = fmt.Fprintf(stdout, "Daemon running in foreground (pid %d).\n", state.Process.PID)
		}
	})
	if outputErr != nil {
		return writeCommandError(stdout, stderr, false, "darkstar daemon run", "OUTPUT_FAILED", outputErr.Error(), false, ExitInvariantViolation)
	}
	if errors.Is(err, daemon.ErrAlreadyRunning) {
		if jsonOutput {
			if outputErr := writeJSON(stdout, daemonRunEvent{SchemaVersion: machineSchemaVersion, Event: "already_running"}); outputErr != nil {
				return writeCommandError(stdout, stderr, false, "darkstar daemon run", "OUTPUT_FAILED", outputErr.Error(), false, ExitInvariantViolation)
			}
		} else {
			_, _ = fmt.Fprintln(stdout, "Daemon is already running.")
		}
		return int(ExitSuccess)
	}
	if err != nil {
		return writeCommandError(stdout, stderr, jsonOutput, "darkstar daemon run", "DAEMON_RUN_FAILED", err.Error(), true, ExitTransientFailure)
	}
	if jsonOutput {
		if outputErr := writeJSON(stdout, daemonRunEvent{SchemaVersion: machineSchemaVersion, Event: "stopped"}); outputErr != nil {
			return writeCommandError(stdout, stderr, false, "darkstar daemon run", "OUTPUT_FAILED", outputErr.Error(), false, ExitInvariantViolation)
		}
	} else {
		_, _ = fmt.Fprintln(stdout, "Daemon stopped.")
	}
	return int(ExitSuccess)
}

func newDaemonManager(ctx context.Context) (*daemon.Manager, error) {
	paths, err := resolveApplicationPaths(ctx)
	if err != nil {
		return nil, fmt.Errorf("resolve runtime paths: %w", err)
	}
	return daemon.NewManager(paths.Runtime, windows.NewDaemonHost())
}

var resolveApplicationPaths = func(ctx context.Context) (platformport.Paths, error) {
	return windows.NewPathResolver().ResolvePaths(ctx, platformport.PathRequest{ApplicationName: "DARKSTAR"})
}

func detachedRequest(manager *daemon.Manager) (daemon.DetachedRequest, error) {
	executable, err := os.Executable()
	if err == nil {
		executable, err = filepath.Abs(executable)
	}
	if err != nil {
		return daemon.DetachedRequest{}, fmt.Errorf("resolve executable: %w", err)
	}
	return daemon.DetachedRequest{
		Executable: executable,
		Arguments:  []string{"daemon", "run"},
		LogPath:    filepath.Join(manager.RuntimeDirectory(), "daemon.log"),
	}, nil
}

func startDaemonProcess(ctx context.Context, manager *daemon.Manager) (daemon.StartResult, error) {
	request, err := detachedRequest(manager)
	if err != nil {
		return daemon.StartResult{}, err
	}
	return manager.Start(ctx, request)
}

type daemonStartOutput struct {
	SchemaVersion int                     `json:"schemaVersion"`
	Status        daemon.StartDisposition `json:"status"`
	Process       daemon.ProcessIdentity  `json:"process"`
}

func startDaemon(ctx context.Context, manager *daemon.Manager, jsonOutput bool, stdout, stderr io.Writer) int {
	result, err := startDaemonProcess(ctx, manager)
	if err != nil {
		return writeCommandError(stdout, stderr, jsonOutput, "darkstar daemon start", "DAEMON_START_FAILED", err.Error(), true, ExitTransientFailure)
	}
	if err := writeStartResult(result, jsonOutput, stdout); err != nil {
		return writeCommandError(stdout, stderr, false, "darkstar daemon start", "OUTPUT_FAILED", err.Error(), false, ExitInvariantViolation)
	}
	return int(ExitSuccess)
}

func writeStartResult(result daemon.StartResult, jsonOutput bool, stdout io.Writer) error {
	if result.Disposition != daemon.StartCreated && result.Disposition != daemon.StartAlreadyRunning {
		return fmt.Errorf("unknown daemon start disposition %q", result.Disposition)
	}
	if jsonOutput {
		return writeJSON(stdout, daemonStartOutput{SchemaVersion: machineSchemaVersion, Status: result.Disposition, Process: result.State.Process})
	} else if result.Disposition == daemon.StartAlreadyRunning {
		_, err := fmt.Fprintf(stdout, "Daemon is already running (pid %d).\n", result.State.Process.PID)
		return err
	} else {
		_, err := fmt.Fprintf(stdout, "Daemon started (pid %d).\n", result.State.Process.PID)
		return err
	}
}

type daemonStopOutput struct {
	SchemaVersion int                    `json:"schemaVersion"`
	Status        daemon.StopDisposition `json:"status"`
}

func stopDaemon(ctx context.Context, manager *daemon.Manager, jsonOutput bool, stdout, stderr io.Writer) int {
	result, err := manager.Stop(ctx)
	if err != nil {
		return writeCommandError(stdout, stderr, jsonOutput, "darkstar daemon stop", "DAEMON_STOP_FAILED", err.Error(), true, ExitTransientFailure)
	}
	if err := writeStopResult(result, jsonOutput, stdout); err != nil {
		return writeCommandError(stdout, stderr, false, "darkstar daemon stop", "OUTPUT_FAILED", err.Error(), false, ExitInvariantViolation)
	}
	return int(ExitSuccess)
}

func writeStopResult(result daemon.StopResult, jsonOutput bool, stdout io.Writer) error {
	if jsonOutput {
		return writeJSON(stdout, daemonStopOutput{SchemaVersion: machineSchemaVersion, Status: result.Disposition})
	}
	var err error
	switch result.Disposition {
	case daemon.StopAlreadyStopped:
		_, err = fmt.Fprintln(stdout, "Daemon is already stopped.")
	case daemon.StopGraceful:
		_, err = fmt.Fprintln(stdout, "Daemon stopped gracefully.")
	case daemon.StopForced:
		_, err = fmt.Fprintln(stdout, "Daemon did not stop within the grace period and was force-stopped.")
	case daemon.StopStaleCleaned:
		_, err = fmt.Fprintln(stdout, "Removed stale daemon state; daemon is stopped.")
	default:
		return fmt.Errorf("unknown daemon stop disposition %q", result.Disposition)
	}
	return err
}

type daemonRestartOutput struct {
	SchemaVersion int                    `json:"schemaVersion"`
	Status        string                 `json:"status"`
	StopStatus    daemon.StopDisposition `json:"stopStatus"`
	Process       daemon.ProcessIdentity `json:"process"`
}

func restartDaemon(ctx context.Context, manager *daemon.Manager, jsonOutput bool, stdout, stderr io.Writer) int {
	stopResult, err := manager.Stop(ctx)
	if err != nil {
		return writeCommandError(stdout, stderr, jsonOutput, "darkstar daemon restart", "DAEMON_STOP_FAILED", err.Error(), true, ExitTransientFailure)
	}
	startResult, err := startDaemonProcess(ctx, manager)
	if err != nil {
		return writeCommandError(stdout, stderr, jsonOutput, "darkstar daemon restart", "DAEMON_START_FAILED", err.Error(), true, ExitTransientFailure)
	}
	if jsonOutput {
		if err := writeJSON(stdout, daemonRestartOutput{SchemaVersion: machineSchemaVersion, Status: "restarted", StopStatus: stopResult.Disposition, Process: startResult.State.Process}); err != nil {
			return writeCommandError(stdout, stderr, false, "darkstar daemon restart", "OUTPUT_FAILED", err.Error(), false, ExitInvariantViolation)
		}
	} else {
		if err := writeStopResult(stopResult, false, stdout); err != nil {
			return writeCommandError(stdout, stderr, false, "darkstar daemon restart", "OUTPUT_FAILED", err.Error(), false, ExitInvariantViolation)
		}
		if err := writeStartResult(startResult, false, stdout); err != nil {
			return writeCommandError(stdout, stderr, false, "darkstar daemon restart", "OUTPUT_FAILED", err.Error(), false, ExitInvariantViolation)
		}
	}
	return int(ExitSuccess)
}

type statusOutput struct {
	SchemaVersion int                     `json:"schemaVersion"`
	Status        string                  `json:"status"`
	InstanceID    string                  `json:"instanceId,omitempty"`
	Process       *daemon.ProcessIdentity `json:"process,omitempty"`
	Reason        string                  `json:"reason,omitempty"`
}

func daemonStatus(manager *daemon.Manager, jsonOutput bool, stdout, stderr io.Writer) int {
	inspection, err := manager.Inspect()
	if err != nil {
		return writeCommandError(stdout, stderr, jsonOutput, "darkstar daemon status", "DAEMON_INSPECTION_FAILED", err.Error(), true, ExitTransientFailure)
	}
	output := statusOutput{SchemaVersion: machineSchemaVersion}
	switch current := inspection.(type) {
	case daemon.Stopped:
		output.Status = "stopped"
	case daemon.Running:
		process := current.State.Process
		output.Status, output.InstanceID, output.Process = "running", current.State.InstanceID, &process
	case daemon.Stale:
		process := current.State.Process
		output.Status, output.InstanceID, output.Process, output.Reason = "stale", current.State.InstanceID, &process, current.Reason
	case daemon.InvalidState:
		output.Status, output.Reason = "stale", current.Reason
	default:
		return writeCommandError(stdout, stderr, jsonOutput, "darkstar daemon status", "INTERNAL_INVARIANT_VIOLATION", fmt.Sprintf("unknown inspection outcome %T", inspection), false, ExitInvariantViolation)
	}
	if jsonOutput {
		if err := writeJSON(stdout, output); err != nil {
			return writeCommandError(stdout, stderr, false, "darkstar daemon status", "OUTPUT_FAILED", err.Error(), false, ExitInvariantViolation)
		}
		return int(ExitSuccess)
	}
	switch output.Status {
	case "running":
		_, _ = fmt.Fprintf(stdout, "Daemon is running (pid %d, started %s).\n", output.Process.PID, output.Process.StartedAt.Format(time.RFC3339Nano))
	case "stale":
		_, _ = fmt.Fprintf(stdout, "Daemon state is stale (%s).\n", output.Reason)
	default:
		_, _ = fmt.Fprintln(stdout, "Daemon is stopped.")
	}
	return int(ExitSuccess)
}

type apiStatusOutput struct {
	SchemaVersion     int              `json:"schemaVersion"`
	Status            string           `json:"status"`
	APIVersion        localapi.Version `json:"apiVersion"`
	PID               int              `json:"pid"`
	Reconciled        int              `json:"reconciled"`
	ReconcileRequired int              `json:"reconcileRequired"`
}

func runAPI(args []string, jsonOutput bool, stdout, stderr io.Writer) int {
	if len(args) != 1 || args[0] != "status" {
		return writeCommandError(stdout, stderr, jsonOutput, "darkstar api", "ARGUMENT_INVALID", "expected 'api status'", false, ExitInvalidInput)
	}
	manager, err := newDaemonManager(context.Background())
	if err != nil {
		return writeCommandError(stdout, stderr, jsonOutput, "darkstar api status", "DAEMON_RUNTIME_UNAVAILABLE", err.Error(), true, ExitTransientFailure)
	}
	client, err := clientapi.New(clientapi.Config{
		RuntimeDirectory: manager.RuntimeDirectory(),
		Autostart: func(ctx context.Context) error {
			_, startErr := startDaemonProcess(ctx, manager)
			return startErr
		},
	})
	if err != nil {
		return writeCommandError(stdout, stderr, jsonOutput, "darkstar api status", "INTERNAL_INVARIANT_VIOLATION", err.Error(), false, ExitInvariantViolation)
	}
	session, err := client.Connect(context.Background())
	if err != nil {
		return writeClientError(stdout, stderr, jsonOutput, "darkstar api status", err)
	}
	recoveryState := session.Recovery()
	status := "ready"
	if !recoveryState.SchedulingAllowed() {
		status = "reconciliation_required"
	}
	output := apiStatusOutput{
		SchemaVersion:     machineSchemaVersion,
		Status:            status,
		APIVersion:        session.Version(),
		PID:               session.Endpoint().PID,
		Reconciled:        recoveryState.Reconciled,
		ReconcileRequired: recoveryState.ReconcileRequired,
	}
	if jsonOutput {
		if err := writeJSON(stdout, output); err != nil {
			return writeCommandError(stdout, stderr, false, "darkstar api status", "OUTPUT_FAILED", err.Error(), false, ExitInvariantViolation)
		}
	} else {
		if recoveryState.SchedulingAllowed() {
			_, _ = fmt.Fprintf(stdout, "Daemon API %s is ready (pid %d); startup recovery is complete.\n", output.APIVersion, output.PID)
		} else {
			_, _ = fmt.Fprintf(stdout, "Daemon API %s is ready (pid %d); %d item(s) require reconciliation and scheduling is paused.\n", output.APIVersion, output.PID, output.ReconcileRequired)
		}
	}
	return int(ExitSuccess)
}

func runDoctor(args []string, jsonOutput bool, stdout, stderr io.Writer) int {
	if len(args) != 0 {
		return writeCommandError(stdout, stderr, jsonOutput, "darkstar doctor", "ARGUMENT_INVALID", "doctor accepts no arguments", false, ExitInvalidInput)
	}
	manager, err := newDaemonManager(context.Background())
	if err != nil {
		return writeCommandError(stdout, stderr, jsonOutput, "darkstar doctor", "DAEMON_RUNTIME_UNAVAILABLE", err.Error(), true, ExitTransientFailure)
	}
	client, err := clientapi.New(clientapi.Config{
		RuntimeDirectory: manager.RuntimeDirectory(),
		Autostart: func(ctx context.Context) error {
			_, startErr := startDaemonProcess(ctx, manager)
			return startErr
		},
	})
	if err != nil {
		return writeCommandError(stdout, stderr, jsonOutput, "darkstar doctor", "INTERNAL_INVARIANT_VIOLATION", err.Error(), false, ExitInvariantViolation)
	}
	session, err := client.Connect(context.Background())
	if err != nil {
		return writeClientError(stdout, stderr, jsonOutput, "darkstar doctor", err)
	}
	var report health.Report
	projectRoot, err := os.Getwd()
	if err != nil {
		return writeCommandError(stdout, stderr, jsonOutput, "darkstar doctor", "PROJECT_ROOT_UNAVAILABLE", err.Error(), false, ExitInvalidInput)
	}
	projectRoot, err = filepath.Abs(projectRoot)
	if err != nil {
		return writeCommandError(stdout, stderr, jsonOutput, "darkstar doctor", "PROJECT_ROOT_UNAVAILABLE", err.Error(), false, ExitInvalidInput)
	}
	resource := "doctor?projectRoot=" + url.QueryEscape(projectRoot)
	if err := session.DoJSON(context.Background(), http.MethodGet, resource, nil, &report); err != nil {
		return writeClientError(stdout, stderr, jsonOutput, "darkstar doctor", err)
	}
	if jsonOutput {
		if err := writeJSON(stdout, report); err != nil {
			return writeCommandError(stdout, stderr, false, "darkstar doctor", "OUTPUT_FAILED", err.Error(), false, ExitInvariantViolation)
		}
	} else if err := writeDoctorReport(stdout, report); err != nil {
		return writeCommandError(stdout, stderr, false, "darkstar doctor", "OUTPUT_FAILED", err.Error(), false, ExitInvariantViolation)
	}
	if report.Status() != health.StatusHealthy {
		return int(ExitValidationFailed)
	}
	return int(ExitSuccess)
}

type runExportOutput struct {
	SchemaVersion int    `json:"schemaVersion"`
	RunID         string `json:"runId"`
	Output        string `json:"output"`
	Size          int    `json:"size"`
}

func runRun(args []string, jsonOutput bool, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		return writeCommandError(stdout, stderr, jsonOutput, "darkstar run", "ARGUMENT_INVALID", "a run command is required (start, list, show, watch, pause, resume, retry, continue, cancel, export, readiness)", false, ExitInvalidInput)
	}
	command := "darkstar run " + args[0]
	switch args[0] {
	case "start":
		request, scenario, key, err := parseRunStart(args[1:])
		if err != nil {
			return writeCommandError(stdout, stderr, jsonOutput, command, "ARGUMENT_INVALID", err.Error(), false, ExitInvalidInput)
		}
		session, code := connectRunSession(command, jsonOutput, stdout, stderr)
		if session == nil {
			return code
		}
		if scenario == "" {
			var result statestore.RunProjection
			if err := session.DoJSON(context.Background(), http.MethodPost, "runs", request, &result, clientapi.WithHeader("Idempotency-Key", key)); err != nil {
				return writeClientError(stdout, stderr, jsonOutput, command, err)
			}
			return writeRunProjectionResult(result, jsonOutput, stdout, stderr, command)
		}
		var view runexecution.View
		if err := session.DoJSON(context.Background(), http.MethodPost, "runs", runexecution.StartRequest{Scenario: scenario}, &view, clientapi.WithHeader("Idempotency-Key", key)); err != nil {
			return writeClientError(stdout, stderr, jsonOutput, command, err)
		}
		return writeRunView(view, jsonOutput, stdout, stderr, command, "Started")
	case "list":
		resource, err := parseRunList(args[1:])
		if err != nil {
			return writeCommandError(stdout, stderr, jsonOutput, command, "ARGUMENT_INVALID", err.Error(), false, ExitInvalidInput)
		}
		session, code := connectRunSession(command, jsonOutput, stdout, stderr)
		if session == nil {
			return code
		}
		var page runexecution.Page
		if err := session.DoJSON(context.Background(), http.MethodGet, resource, nil, &page); err != nil {
			return writeClientError(stdout, stderr, jsonOutput, command, err)
		}
		return writeRunPage(page, jsonOutput, stdout, stderr, command)
	case "show":
		if len(args) != 2 || !runIdentityPattern.MatchString(args[1]) {
			return writeCommandError(stdout, stderr, jsonOutput, command, "ARGUMENT_INVALID", "expected 'run show <run-id>' with a canonical run_ ULID", false, ExitInvalidInput)
		}
		session, code := connectRunSession(command, jsonOutput, stdout, stderr)
		if session == nil {
			return code
		}
		var view runexecution.View
		if err := session.DoJSON(context.Background(), http.MethodGet, "runs/"+args[1], nil, &view); err != nil {
			return writeClientError(stdout, stderr, jsonOutput, command, err)
		}
		return writeRunView(view, jsonOutput, stdout, stderr, command, "Run")
	case "watch":
		if len(args) != 2 || !runIdentityPattern.MatchString(args[1]) {
			return writeCommandError(stdout, stderr, jsonOutput, command, "ARGUMENT_INVALID", "expected 'run watch <run-id>' with a canonical run_ ULID", false, ExitInvalidInput)
		}
		session, code := connectRunSession(command, jsonOutput, stdout, stderr)
		if session == nil {
			return code
		}
		runID := args[1]
		err := session.StreamEvents(context.Background(), 0, func(event statestore.Event) bool {
			if event.CorrelationID != runID {
				return true
			}
			if !jsonOutput {
				_, _ = fmt.Fprintf(stdout, "%d %s\n", event.GlobalPosition, event.Kind)
			}
			return event.Kind != "run.completed" && event.Kind != "run.failed" && event.Kind != "run.cancelled" && event.Kind != "run.reconcile_required"
		})
		if err != nil {
			return writeClientError(stdout, stderr, jsonOutput, command, err)
		}
		var view runexecution.View
		if err := session.DoJSON(context.Background(), http.MethodGet, "runs/"+runID, nil, &view); err != nil {
			return writeClientError(stdout, stderr, jsonOutput, command, err)
		}
		return writeRunView(view, jsonOutput, stdout, stderr, command, "Completed")
	case "pause", "resume", "cancel":
		runID, key, err := parseSimpleRunControl(args[1:], args[0])
		if err != nil {
			return writeCommandError(stdout, stderr, jsonOutput, command, "ARGUMENT_INVALID", err.Error(), false, ExitInvalidInput)
		}
		return runControl(command, args[0], runID, key, nil, jsonOutput, stdout, stderr)
	case "retry":
		runID, nodeID, key, err := parseRunRetry(args[1:])
		if err != nil {
			return writeCommandError(stdout, stderr, jsonOutput, command, "ARGUMENT_INVALID", err.Error(), false, ExitInvalidInput)
		}
		var body any
		if nodeID != "" {
			body = map[string]string{"nodeId": nodeID}
		}
		return runControl(command, "retry", runID, key, body, jsonOutput, stdout, stderr)
	case "continue":
		runID, until, key, err := parseRunContinue(args[1:])
		if err != nil {
			return writeCommandError(stdout, stderr, jsonOutput, command, "ARGUMENT_INVALID", err.Error(), false, ExitInvalidInput)
		}
		return runControl(command, "continue", runID, key, map[string]string{"until": until}, jsonOutput, stdout, stderr)
	case "readiness":
		return runReadiness(args[1:], jsonOutput, stdout, stderr)
	case "export":
		if len(args) != 4 || args[2] != "--output" || args[1] == "" || args[3] == "" {
			return writeCommandError(stdout, stderr, jsonOutput, command, "ARGUMENT_INVALID", "expected 'run export <run-id> --output <file>'", false, ExitInvalidInput)
		}
		return runExport(args[1], args[3], jsonOutput, stdout, stderr)
	default:
		return writeCommandError(stdout, stderr, jsonOutput, "darkstar run", "ARGUMENT_INVALID", fmt.Sprintf("unknown run command %q", args[0]), false, ExitInvalidInput)
	}
}

func runControl(command, action, runID, key string, body any, jsonOutput bool, stdout, stderr io.Writer) int {
	session, code := connectRunSession(command, jsonOutput, stdout, stderr)
	if session == nil {
		return code
	}
	var current runexecution.View
	if err := session.DoJSON(context.Background(), http.MethodGet, "runs/"+runID, nil, &current); err != nil {
		return writeClientError(stdout, stderr, jsonOutput, command, err)
	}
	var result statestore.RunProjection
	if err := session.DoJSON(context.Background(), http.MethodPost, "runs/"+runID+"/"+action, body, &result,
		clientapi.WithHeader("Idempotency-Key", key), clientapi.WithHeader("If-Match", fmt.Sprintf(`"%d"`, current.Run.ResourceVersion))); err != nil {
		return writeClientError(stdout, stderr, jsonOutput, command, err)
	}
	labels := map[string]string{"pause": "Paused", "resume": "Resumed", "retry": "Retried", "continue": "Continued", "cancel": "Cancelled"}
	return writeRunControlResult(result, labels[action], jsonOutput, stdout, stderr, command)
}

func runExport(runID, outputPath string, jsonOutput bool, stdout, stderr io.Writer) int {
	if !runIdentityPattern.MatchString(runID) {
		return writeCommandError(stdout, stderr, jsonOutput, "darkstar run export", "ARGUMENT_INVALID", "run ID must be a canonical run_ ULID", false, ExitInvalidInput)
	}
	absoluteOutput, err := filepath.Abs(outputPath)
	if err != nil {
		return writeCommandError(stdout, stderr, jsonOutput, "darkstar run export", "ARGUMENT_INVALID", "resolve output path: "+err.Error(), false, ExitInvalidInput)
	}
	session, code := connectRunSession("darkstar run export", jsonOutput, stdout, stderr)
	if session == nil {
		return code
	}
	content, err := session.Download(context.Background(), "runs/"+runID+"/export")
	if err != nil {
		return writeClientError(stdout, stderr, jsonOutput, "darkstar run export", err)
	}
	if err := writeNewFileAtomically(absoluteOutput, content); err != nil {
		return writeCommandError(stdout, stderr, jsonOutput, "darkstar run export", "OUTPUT_FAILED", err.Error(), false, ExitInvalidInput)
	}
	output := runExportOutput{SchemaVersion: machineSchemaVersion, RunID: runID, Output: absoluteOutput, Size: len(content)}
	if jsonOutput {
		if err := writeJSON(stdout, output); err != nil {
			return writeCommandError(stdout, stderr, false, "darkstar run export", "OUTPUT_FAILED", err.Error(), false, ExitInvariantViolation)
		}
	} else {
		_, _ = fmt.Fprintf(stdout, "Exported %s to %s (%d bytes).\n", runID, absoluteOutput, len(content))
	}
	return int(ExitSuccess)
}

func connectRunSession(command string, jsonOutput bool, stdout, stderr io.Writer) (*clientapi.Session, int) {
	manager, err := newDaemonManager(context.Background())
	if err != nil {
		return nil, writeCommandError(stdout, stderr, jsonOutput, command, "DAEMON_RUNTIME_UNAVAILABLE", err.Error(), true, ExitTransientFailure)
	}
	client, err := clientapi.New(clientapi.Config{
		RuntimeDirectory: manager.RuntimeDirectory(),
		Autostart: func(ctx context.Context) error {
			_, startErr := startDaemonProcess(ctx, manager)
			return startErr
		},
	})
	if err != nil {
		return nil, writeCommandError(stdout, stderr, jsonOutput, command, "INTERNAL_INVARIANT_VIOLATION", err.Error(), false, ExitInvariantViolation)
	}
	session, err := client.Connect(context.Background())
	if err != nil {
		return nil, writeClientError(stdout, stderr, jsonOutput, command, err)
	}
	return session, int(ExitSuccess)
}

func writeRunView(view runexecution.View, jsonOutput bool, stdout, stderr io.Writer, command, verb string) int {
	if jsonOutput {
		if err := writeJSON(stdout, view); err != nil {
			return writeCommandError(stdout, stderr, false, command, "OUTPUT_FAILED", err.Error(), false, ExitInvariantViolation)
		}
	} else if len(view.Attempts) > 0 {
		_, _ = fmt.Fprintf(stdout, "%s %s: %s (attempt %s: %s).\n", verb, view.Run.RunID, view.Run.Status, view.Attempts[0].AttemptID, view.Attempts[0].Status)
	} else {
		_, _ = fmt.Fprintf(stdout, "%s %s: %s.\n", verb, view.Run.RunID, view.Run.Status)
	}
	return int(ExitSuccess)
}

func newIdempotencyKey() string {
	value := make([]byte, 16)
	if _, err := rand.Read(value); err != nil {
		panic("cryptographic idempotency-key generation failed: " + err.Error())
	}
	return "cli-" + hex.EncodeToString(value)
}

func writeNewFileAtomically(destination string, content []byte) (err error) {
	if _, statErr := os.Stat(destination); statErr == nil {
		return fmt.Errorf("output file already exists: %s", destination)
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return fmt.Errorf("inspect output path: %w", statErr)
	}
	temporary, err := os.CreateTemp(filepath.Dir(destination), ".darkstar-export-*.tmp")
	if err != nil {
		return fmt.Errorf("create temporary export: %w", err)
	}
	temporaryPath := temporary.Name()
	defer func() { _ = os.Remove(temporaryPath) }()
	if err = temporary.Chmod(0o600); err == nil {
		_, err = temporary.Write(content)
	}
	if err == nil {
		err = temporary.Sync()
	}
	closeErr := temporary.Close()
	if err != nil {
		return fmt.Errorf("write temporary export: %w", err)
	}
	if closeErr != nil {
		return fmt.Errorf("close temporary export: %w", closeErr)
	}
	if err := os.Rename(temporaryPath, destination); err != nil {
		return fmt.Errorf("publish export: %w", err)
	}
	return nil
}

func writeDoctorReport(destination io.Writer, report health.Report) error {
	if _, err := fmt.Fprintf(destination, "DARKSTAR doctor: %s\n", report.Status()); err != nil {
		return err
	}
	for _, check := range report.Checks {
		if _, err := fmt.Fprintf(destination, "[%s] %s %s: %s\n", check.Status, check.Subsystem, check.Code, check.Message); err != nil {
			return err
		}
		if check.Action != "" {
			if _, err := fmt.Fprintf(destination, "  Action: %s\n", check.Action); err != nil {
				return err
			}
		}
		if check.ProviderDetails != nil {
			details := check.ProviderDetails
			if _, err := fmt.Fprintf(destination, "  Provider: %s %s\n  Executable: %s\n  Authentication: %s; usage: %s\n",
				details.Name, details.Version, details.ExecutableIdentity, details.Authentication, details.Usage); err != nil {
				return err
			}
			for _, source := range details.InstructionSources {
				if _, err := fmt.Fprintf(destination, "  Instruction source: %s\n", source); err != nil {
					return err
				}
			}
			for _, conflict := range details.ConflictingExecutables {
				if _, err := fmt.Fprintf(destination, "  Conflicting executable: %s\n", conflict); err != nil {
					return err
				}
			}
			for _, capability := range details.AvailableCapabilities {
				if _, err := fmt.Fprintf(destination, "  Capability: %s %s (available)\n", capability.Name, capability.Version); err != nil {
					return err
				}
			}
			for _, capability := range details.UnavailableCapabilities {
				if _, err := fmt.Fprintf(destination, "  Capability: %s (unavailable: %s)\n", capability.Name, capability.Reason); err != nil {
					return err
				}
			}
		}
	}
	return nil
}
