// Package cli implements the runtime's human- and automation-facing command-line boundary.
package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"

	localapi "github.com/fdsprod/darkstar/runtime/src/api"
	"github.com/fdsprod/darkstar/runtime/src/daemon"
	"github.com/fdsprod/darkstar/runtime/src/platform/windows"
	platformport "github.com/fdsprod/darkstar/runtime/src/ports/platform"
)

const usage = `DARKSTAR

Usage:
  darkstar [command]

Commands:
  daemon     Run and control the per-user daemon
  help       Show this help
  version    Show version information

Daemon commands:
  daemon run          Run in the foreground
  daemon start        Start in the background
  daemon stop         Stop gracefully, then force if required
  daemon restart      Stop and start the daemon
  daemon status       Show daemon status
  daemon status --json
`

// Version is replaced by release builds through -ldflags.
var Version = "dev"

// Run executes the command line and returns a process exit code.
func Run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 || args[0] == "help" || args[0] == "--help" || args[0] == "-h" {
		fmt.Fprint(stdout, usage)
		return 0
	}

	if args[0] == "version" || args[0] == "--version" {
		fmt.Fprintf(stdout, "darkstar %s\n", Version)
		return 0
	}
	if args[0] == "daemon" {
		return runDaemon(args[1:], stdout, stderr)
	}

	fmt.Fprintf(stderr, "darkstar: unknown command %q\n", args[0])
	fmt.Fprintln(stderr, "Run 'darkstar help' for usage.")
	return 2
}

func runDaemon(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "darkstar daemon: a command is required (run, start, stop, restart, status)")
		return 2
	}
	if !validDaemonArguments(args) {
		fmt.Fprintf(stderr, "darkstar daemon: invalid arguments %q\n", args)
		return 2
	}

	manager, err := newDaemonManager(context.Background())
	if err != nil {
		fmt.Fprintf(stderr, "darkstar daemon: %v\n", err)
		return 8
	}

	switch args[0] {
	case "run":
		ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
		defer cancel()
		server, serverErr := localapi.NewServer(manager.RuntimeDirectory())
		if serverErr != nil {
			fmt.Fprintf(stderr, "darkstar daemon run: %v\n", serverErr)
			return 8
		}
		err := manager.RunWithService(ctx, daemonAPIService{server: server}, func(state daemon.State) {
			fmt.Fprintf(stdout, "Daemon running in foreground (pid %d).\n", state.Process.PID)
		})
		if errors.Is(err, daemon.ErrAlreadyRunning) {
			fmt.Fprintln(stdout, "Daemon is already running.")
			return 0
		}
		if err != nil {
			fmt.Fprintf(stderr, "darkstar daemon run: %v\n", err)
			return 8
		}
		fmt.Fprintln(stdout, "Daemon stopped.")
		return 0
	case "start":
		return startDaemon(context.Background(), manager, stdout, stderr)
	case "stop":
		return stopDaemon(context.Background(), manager, stdout, stderr)
	case "restart":
		if code := stopDaemon(context.Background(), manager, stdout, stderr); code != 0 {
			return code
		}
		return startDaemon(context.Background(), manager, stdout, stderr)
	case "status":
		return daemonStatus(manager, len(args) == 2, stdout, stderr)
	default:
		panic("validDaemonArguments accepted an unknown command")
	}
}

type daemonAPIService struct{ server *localapi.Server }

func (service daemonAPIService) Start(ctx context.Context, identity daemon.ProcessIdentity) error {
	return service.server.Start(ctx, identity.PID, identity.StartedAt)
}

func (service daemonAPIService) Close() error { return service.server.Close() }

func validDaemonArguments(args []string) bool {
	switch args[0] {
	case "run", "start", "stop", "restart":
		return len(args) == 1
	case "status":
		return len(args) == 1 || len(args) == 2 && args[1] == "--json"
	default:
		return false
	}
}

func newDaemonManager(ctx context.Context) (*daemon.Manager, error) {
	paths, err := windows.NewPathResolver().ResolvePaths(ctx, platformport.PathRequest{ApplicationName: "DARKSTAR"})
	if err != nil {
		return nil, fmt.Errorf("resolve runtime paths: %w", err)
	}
	return daemon.NewManager(paths.Runtime, windows.NewDaemonHost())
}

func startDaemon(ctx context.Context, manager *daemon.Manager, stdout, stderr io.Writer) int {
	executable, err := os.Executable()
	if err == nil {
		executable, err = filepath.Abs(executable)
	}
	if err != nil {
		fmt.Fprintf(stderr, "darkstar daemon start: resolve executable: %v\n", err)
		return 8
	}
	result, err := manager.Start(ctx, daemon.DetachedRequest{
		Executable: executable,
		Arguments:  []string{"daemon", "run"},
		LogPath:    filepath.Join(manager.RuntimeDirectory(), "daemon.log"),
	})
	if err != nil {
		fmt.Fprintf(stderr, "darkstar daemon start: %v\n", err)
		return 8
	}
	if result.Disposition == daemon.StartAlreadyRunning {
		fmt.Fprintf(stdout, "Daemon is already running (pid %d).\n", result.State.Process.PID)
	} else {
		fmt.Fprintf(stdout, "Daemon started (pid %d).\n", result.State.Process.PID)
	}
	return 0
}

func stopDaemon(ctx context.Context, manager *daemon.Manager, stdout, stderr io.Writer) int {
	result, err := manager.Stop(ctx)
	if err != nil {
		fmt.Fprintf(stderr, "darkstar daemon stop: %v\n", err)
		return 8
	}
	switch result.Disposition {
	case daemon.StopAlreadyStopped:
		fmt.Fprintln(stdout, "Daemon is already stopped.")
	case daemon.StopGraceful:
		fmt.Fprintln(stdout, "Daemon stopped gracefully.")
	case daemon.StopForced:
		fmt.Fprintln(stdout, "Daemon did not stop within the grace period and was force-stopped.")
	case daemon.StopStaleCleaned:
		fmt.Fprintln(stdout, "Removed stale daemon state; daemon is stopped.")
	}
	return 0
}

type statusOutput struct {
	Status     string                  `json:"status"`
	InstanceID string                  `json:"instanceId,omitempty"`
	Process    *daemon.ProcessIdentity `json:"process,omitempty"`
	Reason     string                  `json:"reason,omitempty"`
}

func daemonStatus(manager *daemon.Manager, jsonOutput bool, stdout, stderr io.Writer) int {
	inspection, err := manager.Inspect()
	if err != nil {
		fmt.Fprintf(stderr, "darkstar daemon status: %v\n", err)
		return 8
	}
	var output statusOutput
	switch current := inspection.(type) {
	case daemon.Stopped:
		output.Status = "stopped"
	case daemon.Running:
		process := current.State.Process
		output = statusOutput{Status: "running", InstanceID: current.State.InstanceID, Process: &process}
	case daemon.Stale:
		process := current.State.Process
		output = statusOutput{Status: "stale", InstanceID: current.State.InstanceID, Process: &process, Reason: current.Reason}
	case daemon.InvalidState:
		output = statusOutput{Status: "stale", Reason: current.Reason}
	default:
		fmt.Fprintf(stderr, "darkstar daemon status: unknown inspection outcome %T\n", inspection)
		return 8
	}
	if jsonOutput {
		encoder := json.NewEncoder(stdout)
		encoder.SetEscapeHTML(false)
		if err := encoder.Encode(output); err != nil {
			fmt.Fprintf(stderr, "darkstar daemon status: encode output: %v\n", err)
			return 8
		}
		return 0
	}
	switch output.Status {
	case "running":
		fmt.Fprintf(stdout, "Daemon is running (pid %d, started %s).\n", output.Process.PID, output.Process.StartedAt.Format("2006-01-02T15:04:05.999999999Z07:00"))
	case "stale":
		fmt.Fprintf(stdout, "Daemon state is stale (%s).\n", output.Reason)
	default:
		fmt.Fprintln(stdout, "Daemon is stopped.")
	}
	return 0
}
