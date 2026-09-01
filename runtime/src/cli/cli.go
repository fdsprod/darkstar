// Package cli implements the runtime's human- and automation-facing command-line boundary.
package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"time"

	"github.com/fdsprod/darkstar/runtime/src/adapters/statestore/sqlite"
	localapi "github.com/fdsprod/darkstar/runtime/src/api"
	clientapi "github.com/fdsprod/darkstar/runtime/src/api/client"
	"github.com/fdsprod/darkstar/runtime/src/core/recovery"
	"github.com/fdsprod/darkstar/runtime/src/daemon"
	"github.com/fdsprod/darkstar/runtime/src/platform/windows"
	platformport "github.com/fdsprod/darkstar/runtime/src/ports/platform"
)

const usage = `DARKSTAR

Usage:
  darkstar [command] [--json]

Commands:
  api        Inspect the autostarted local API
  daemon     Run and control the per-user daemon
  help       Show this help
  version    Show version information

API commands:
  api status [--json]      Discover or autostart the daemon and verify its API

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
			fmt.Fprint(stdout, usage)
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
			fmt.Fprintf(stdout, "darkstar %s\n", Version)
		}
		return int(ExitSuccess)
	case "daemon":
		return runDaemon(cleanArgs[1:], jsonOutput, stdout, stderr)
	case "api":
		return runAPI(cleanArgs[1:], jsonOutput, stdout, stderr)
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
	server        *localapi.Server
	dataDirectory string
	database      *sqlite.Database
}

func (service *daemonAPIService) Start(ctx context.Context, state daemon.State) error {
	if err := os.MkdirAll(service.dataDirectory, 0o700); err != nil {
		return fmt.Errorf("create daemon data directory: %w", err)
	}
	database, err := sqlite.Open(ctx, filepath.Join(service.dataDirectory, "darkstar.db"), sqlite.Options{})
	if err != nil {
		return fmt.Errorf("open daemon state: %w", err)
	}
	service.database = database
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
	if err := service.server.Start(ctx, state.Process.PID, state.Process.StartedAt); err != nil {
		_ = database.Close()
		service.database = nil
		return err
	}
	return nil
}

func (service *daemonAPIService) Close() error {
	serverErr := service.server.Close()
	var databaseErr error
	if service.database != nil {
		databaseErr = service.database.Close()
		service.database = nil
	}
	return errors.Join(serverErr, databaseErr)
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
	service := &daemonAPIService{server: server, dataDirectory: paths.Data}
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
			fmt.Fprintln(stdout, "Daemon is already running.")
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
		fmt.Fprintln(stdout, "Daemon stopped.")
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

func resolveApplicationPaths(ctx context.Context) (platformport.Paths, error) {
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
		fmt.Fprintf(stdout, "Daemon is running (pid %d, started %s).\n", output.Process.PID, output.Process.StartedAt.Format(time.RFC3339Nano))
	case "stale":
		fmt.Fprintf(stdout, "Daemon state is stale (%s).\n", output.Reason)
	default:
		fmt.Fprintln(stdout, "Daemon is stopped.")
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
			fmt.Fprintf(stdout, "Daemon API %s is ready (pid %d); startup recovery is complete.\n", output.APIVersion, output.PID)
		} else {
			fmt.Fprintf(stdout, "Daemon API %s is ready (pid %d); %d item(s) require reconciliation and scheduling is paused.\n", output.APIVersion, output.PID, output.ReconcileRequired)
		}
	}
	return int(ExitSuccess)
}
