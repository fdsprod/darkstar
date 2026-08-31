package daemon

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"time"
)

const stateSchemaVersion = 1

var instanceIDPattern = regexp.MustCompile(`^[0-9a-f]{32}$`)

// ErrLockHeld reports that another daemon owns the per-user lifecycle lock.
var ErrLockHeld = errors.New("daemon lock is held")

// ErrAlreadyRunning reports a verified live daemon instance.
var ErrAlreadyRunning = errors.New("daemon is already running")

// ErrLifecycleUncertain reports state that cannot be changed without risking a
// different process. Callers must fail closed rather than terminate by PID.
var ErrLifecycleUncertain = errors.New("daemon lifecycle is uncertain")

// ProcessIdentity is the minimum proof required before DARKSTAR treats a PID as
// its daemon. StartedAt and Executable prevent PID reuse from proving ownership.
type ProcessIdentity struct {
	PID        int       `json:"pid"`
	StartedAt  time.Time `json:"startedAt"`
	Executable string    `json:"executable"`
}

// State is the durable daemon identity record. Liveness is deliberately not
// persisted; it is derived by inspecting Process on every command.
type State struct {
	SchemaVersion int             `json:"schemaVersion"`
	InstanceID    string          `json:"instanceId"`
	Process       ProcessIdentity `json:"process"`
}

// Inspection is a closed set of daemon discovery outcomes.
type Inspection interface{ isInspection() }

// Stopped means no lifecycle state exists.
type Stopped struct{}

func (Stopped) isInspection() {}

// Running contains a state record whose complete process identity still
// matches a live process.
type Running struct{ State State }

func (Running) isInspection() {}

// Stale contains a valid state record whose process is absent or has a
// different identity.
type Stale struct {
	State  State
	Reason string
}

func (Stale) isInspection() {}

// InvalidState means a state file exists but cannot represent a valid daemon.
type InvalidState struct{ Reason string }

func (InvalidState) isInspection() {}

// ProcessInspection is the platform's exhaustive identity comparison result.
type ProcessInspection int

const (
	ProcessIdentityMatches ProcessInspection = iota + 1
	ProcessAbsent
	ProcessIdentityDiffers
)

// Lock is the held-open, exclusive daemon lock.
type Lock interface{ Close() error }

// StopEvent is a per-instance graceful shutdown signal.
type StopEvent interface {
	Wait(context.Context) error
	Close() error
}

// RuntimeService is an independently testable transport or worker that must be
// ready before daemon state becomes discoverable. The daemon owns its lifetime.
type RuntimeService interface {
	Start(context.Context, ProcessIdentity) error
	Close() error
}

// DetachedRequest describes the background daemon process. Arguments exclude
// the executable itself.
type DetachedRequest struct {
	Executable string
	Arguments  []string
	LogPath    string
}

// Host owns operations whose identity, locking, signaling, or process
// semantics differ by operating system.
type Host interface {
	AcquireLock(string) (Lock, error)
	CreateStopEvent(string) (StopEvent, error)
	CurrentProcessIdentity() (ProcessIdentity, error)
	InspectProcess(ProcessIdentity) (ProcessInspection, error)
	SignalStop(string) error
	StartDetached(context.Context, DetachedRequest) (int, error)
	TerminateProcess(ProcessIdentity) error
}

// StartDisposition distinguishes a new process from an idempotent duplicate.
type StartDisposition string

const (
	StartCreated        StartDisposition = "started"
	StartAlreadyRunning StartDisposition = "already_running"
)

type StartResult struct {
	Disposition StartDisposition
	State       State
}

// StopDisposition records whether shutdown was unnecessary, graceful, forced,
// or consisted only of removing stale state.
type StopDisposition string

const (
	StopAlreadyStopped StopDisposition = "already_stopped"
	StopGraceful       StopDisposition = "graceful"
	StopForced         StopDisposition = "forced"
	StopStaleCleaned   StopDisposition = "stale_cleaned"
)

type StopResult struct{ Disposition StopDisposition }

// Manager coordinates lifecycle state without embedding Windows conditionals.
type Manager struct {
	runtimeDirectory string
	host             Host
	startupTimeout   time.Duration
	stopTimeout      time.Duration
	pollInterval     time.Duration
	now              func() time.Time
	newInstanceID    func() (string, error)
}

// NewManager creates a daemon lifecycle manager rooted in an absolute,
// platform-resolved runtime directory.
func NewManager(runtimeDirectory string, host Host) (*Manager, error) {
	if !filepath.IsAbs(runtimeDirectory) {
		return nil, fmt.Errorf("daemon runtime directory must be absolute: %q", runtimeDirectory)
	}
	if host == nil {
		return nil, errors.New("daemon lifecycle host is required")
	}
	return &Manager{
		runtimeDirectory: filepath.Clean(runtimeDirectory),
		host:             host,
		startupTimeout:   10 * time.Second,
		stopTimeout:      5 * time.Second,
		pollInterval:     50 * time.Millisecond,
		now:              time.Now,
		newInstanceID:    randomInstanceID,
	}, nil
}

func (m *Manager) RuntimeDirectory() string { return m.runtimeDirectory }

func (m *Manager) statePath() string { return filepath.Join(m.runtimeDirectory, "daemon.json") }

func (m *Manager) lockPath() string { return filepath.Join(m.runtimeDirectory, "daemon.lock") }

// Inspect derives daemon liveness from the state record and complete process
// identity. It never treats a PID alone as ownership proof.
func (m *Manager) Inspect() (Inspection, error) {
	state, found, err := m.readState()
	if errors.Is(err, errInvalidState) {
		return InvalidState{Reason: err.Error()}, nil
	}
	if err != nil {
		return nil, err
	}
	if !found {
		return Stopped{}, nil
	}

	observed, err := m.host.InspectProcess(state.Process)
	if err != nil {
		return nil, fmt.Errorf("inspect daemon process: %w", err)
	}
	switch observed {
	case ProcessIdentityMatches:
		return Running{State: state}, nil
	case ProcessAbsent:
		return Stale{State: state, Reason: "process_absent"}, nil
	case ProcessIdentityDiffers:
		return Stale{State: state, Reason: "identity_mismatch"}, nil
	default:
		return nil, fmt.Errorf("inspect daemon process returned unknown outcome %d", observed)
	}
}

// Run owns the foreground daemon lifetime without an attached runtime service.
func (m *Manager) Run(ctx context.Context, ready func(State)) (err error) {
	return m.RunWithService(ctx, nil, ready)
}

// RunWithService owns the foreground daemon and service lifetime. ready is
// called only after the lock, stop event, service, and durable state are ready.
// Service startup precedes daemon.json publication so detached starts cannot
// observe a running daemon whose public API is not yet accepting requests.
func (m *Manager) RunWithService(ctx context.Context, service RuntimeService, ready func(State)) (err error) {
	if err := os.MkdirAll(m.runtimeDirectory, 0o700); err != nil {
		return fmt.Errorf("create daemon runtime directory: %w", err)
	}
	lock, err := m.host.AcquireLock(m.lockPath())
	if errors.Is(err, ErrLockHeld) {
		return ErrAlreadyRunning
	}
	if err != nil {
		return fmt.Errorf("acquire daemon lock: %w", err)
	}
	defer func() {
		if closeErr := lock.Close(); err == nil && closeErr != nil {
			err = fmt.Errorf("release daemon lock: %w", closeErr)
		}
	}()

	if err := m.prepareStateWhileLocked(); err != nil {
		return err
	}
	instanceID, err := m.newInstanceID()
	if err != nil {
		return fmt.Errorf("create daemon instance ID: %w", err)
	}
	event, err := m.host.CreateStopEvent(instanceID)
	if err != nil {
		return fmt.Errorf("create daemon stop event: %w", err)
	}
	defer event.Close()

	identity, err := m.host.CurrentProcessIdentity()
	if err != nil {
		return fmt.Errorf("inspect current daemon process: %w", err)
	}
	state := State{SchemaVersion: stateSchemaVersion, InstanceID: instanceID, Process: identity}
	if err := validateState(state); err != nil {
		return fmt.Errorf("validate current daemon state: %w", err)
	}
	serviceStarted := false
	stateWritten := false
	defer func() {
		if serviceStarted {
			if closeErr := service.Close(); err == nil && closeErr != nil {
				err = fmt.Errorf("close daemon runtime service: %w", closeErr)
			}
		}
		if stateWritten {
			m.removeStateIfOwned(state.InstanceID)
		}
	}()
	if service != nil {
		if err := service.Start(ctx, identity); err != nil {
			return fmt.Errorf("start daemon runtime service: %w", err)
		}
		serviceStarted = true
	}
	if err := m.writeState(state); err != nil {
		return err
	}
	stateWritten = true
	if ready != nil {
		ready(state)
	}

	if waitErr := event.Wait(ctx); waitErr != nil && !errors.Is(waitErr, context.Canceled) && !errors.Is(waitErr, context.DeadlineExceeded) {
		return fmt.Errorf("wait for daemon stop: %w", waitErr)
	}
	return nil
}

// Start launches a detached copy of the current executable and waits until its
// identity is durably discoverable. Concurrent starts converge on one daemon.
func (m *Manager) Start(ctx context.Context, request DetachedRequest) (StartResult, error) {
	inspection, err := m.Inspect()
	if err != nil {
		return StartResult{}, err
	}
	if running, ok := inspection.(Running); ok {
		return StartResult{Disposition: StartAlreadyRunning, State: running.State}, nil
	}
	if _, ok := inspection.(Stopped); !ok {
		if err := m.cleanStaleState(); err != nil {
			return StartResult{}, err
		}
	}

	pid, err := m.host.StartDetached(ctx, request)
	if err != nil {
		return StartResult{}, fmt.Errorf("start detached daemon: %w", err)
	}
	deadline := m.now().Add(m.startupTimeout)
	for {
		inspection, err = m.Inspect()
		if err != nil {
			return StartResult{}, err
		}
		if running, ok := inspection.(Running); ok {
			disposition := StartAlreadyRunning
			if running.State.Process.PID == pid {
				disposition = StartCreated
			}
			return StartResult{Disposition: disposition, State: running.State}, nil
		}
		if !m.now().Before(deadline) {
			return StartResult{}, fmt.Errorf("daemon did not become ready within %s", m.startupTimeout)
		}
		if err := waitForPoll(ctx, m.pollInterval); err != nil {
			return StartResult{}, err
		}
	}
}

// Stop requests graceful shutdown, waits the configured grace period, then
// terminates only the still-matching process identity.
func (m *Manager) Stop(ctx context.Context) (StopResult, error) {
	inspection, err := m.Inspect()
	if err != nil {
		return StopResult{}, err
	}
	switch current := inspection.(type) {
	case Stopped:
		return StopResult{Disposition: StopAlreadyStopped}, nil
	case Stale, InvalidState:
		if err := m.cleanStaleState(); err != nil {
			return StopResult{}, err
		}
		return StopResult{Disposition: StopStaleCleaned}, nil
	case Running:
		_ = m.host.SignalStop(current.State.InstanceID)
		if m.waitForExit(ctx, current.State, m.stopTimeout) {
			m.removeStateIfOwned(current.State.InstanceID)
			return StopResult{Disposition: StopGraceful}, nil
		}
		if err := m.host.TerminateProcess(current.State.Process); err != nil {
			return StopResult{}, fmt.Errorf("force stop daemon: %w", err)
		}
		if !m.waitForExit(ctx, current.State, m.stopTimeout) {
			return StopResult{}, errors.New("daemon remained alive after forced stop")
		}
		m.removeStateIfOwned(current.State.InstanceID)
		return StopResult{Disposition: StopForced}, nil
	default:
		return StopResult{}, fmt.Errorf("unknown daemon inspection outcome %T", inspection)
	}
}

func (m *Manager) waitForExit(ctx context.Context, state State, timeout time.Duration) bool {
	deadline := m.now().Add(timeout)
	for {
		observed, err := m.host.InspectProcess(state.Process)
		if err == nil && observed != ProcessIdentityMatches {
			return true
		}
		if !m.now().Before(deadline) {
			return false
		}
		if waitForPoll(ctx, m.pollInterval) != nil {
			return false
		}
	}
}

func (m *Manager) prepareStateWhileLocked() error {
	inspection, err := m.Inspect()
	if err != nil {
		return err
	}
	switch inspection.(type) {
	case Stopped:
		return nil
	case Stale, InvalidState:
		if err := os.Remove(m.statePath()); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("remove stale daemon state: %w", err)
		}
		return nil
	case Running:
		return fmt.Errorf("%w: live identity exists without the daemon lock", ErrLifecycleUncertain)
	default:
		return fmt.Errorf("unknown daemon inspection outcome %T", inspection)
	}
}

func (m *Manager) cleanStaleState() (err error) {
	if err := os.MkdirAll(m.runtimeDirectory, 0o700); err != nil {
		return fmt.Errorf("create daemon runtime directory: %w", err)
	}
	lock, err := m.host.AcquireLock(m.lockPath())
	if errors.Is(err, ErrLockHeld) {
		return fmt.Errorf("%w: daemon lock is held but lifecycle state is not usable", ErrLifecycleUncertain)
	}
	if err != nil {
		return fmt.Errorf("acquire daemon lock for stale cleanup: %w", err)
	}
	defer func() {
		if closeErr := lock.Close(); err == nil && closeErr != nil {
			err = fmt.Errorf("release daemon cleanup lock: %w", closeErr)
		}
	}()

	inspection, err := m.Inspect()
	if err != nil {
		return err
	}
	if _, ok := inspection.(Running); ok {
		return ErrAlreadyRunning
	}
	if err := os.Remove(m.statePath()); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove stale daemon state: %w", err)
	}
	return nil
}

var errInvalidState = errors.New("invalid daemon state")

func (m *Manager) readState() (State, bool, error) {
	content, err := os.ReadFile(m.statePath())
	if errors.Is(err, os.ErrNotExist) {
		return State{}, false, nil
	}
	if err != nil {
		return State{}, false, fmt.Errorf("read daemon state: %w", err)
	}
	var state State
	if err := json.Unmarshal(content, &state); err != nil {
		return State{}, false, fmt.Errorf("%w: decode daemon state: %v", errInvalidState, err)
	}
	if err := validateState(state); err != nil {
		return State{}, false, fmt.Errorf("%w: %v", errInvalidState, err)
	}
	return state, true, nil
}

func (m *Manager) writeState(state State) error {
	content, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return fmt.Errorf("encode daemon state: %w", err)
	}
	content = append(content, '\n')
	temporary, err := os.CreateTemp(m.runtimeDirectory, ".daemon-*.tmp")
	if err != nil {
		return fmt.Errorf("create temporary daemon state: %w", err)
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("restrict temporary daemon state: %w", err)
	}
	if _, err := temporary.Write(content); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("write temporary daemon state: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("flush temporary daemon state: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close temporary daemon state: %w", err)
	}
	if err := os.Rename(temporaryPath, m.statePath()); err != nil {
		return fmt.Errorf("publish daemon state: %w", err)
	}
	return nil
}

func (m *Manager) removeStateIfOwned(instanceID string) {
	state, found, err := m.readState()
	if err == nil && found && state.InstanceID == instanceID {
		_ = os.Remove(m.statePath())
	}
}

func validateState(state State) error {
	if state.SchemaVersion != stateSchemaVersion {
		return fmt.Errorf("unsupported schemaVersion %d", state.SchemaVersion)
	}
	if !instanceIDPattern.MatchString(state.InstanceID) {
		return errors.New("instanceId must be 128-bit lowercase hexadecimal")
	}
	if state.Process.PID <= 0 {
		return errors.New("process PID must be positive")
	}
	if state.Process.StartedAt.IsZero() {
		return errors.New("process startedAt is required")
	}
	if !filepath.IsAbs(state.Process.Executable) {
		return errors.New("process executable must be absolute")
	}
	return nil
}

func randomInstanceID() (string, error) {
	bytes := make([]byte, 16)
	if _, err := rand.Read(bytes); err != nil {
		return "", err
	}
	return hex.EncodeToString(bytes), nil
}

func waitForPoll(ctx context.Context, interval time.Duration) error {
	timer := time.NewTimer(interval)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
