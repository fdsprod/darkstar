package daemon

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestInspectClassifiesCompleteIdentity(t *testing.T) {
	t.Parallel()

	manager, host := newTestManager(t)
	state := testState(101)
	writeTestState(t, manager, state)

	host.inspection = ProcessIdentityMatches
	inspection, err := manager.Inspect()
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := inspection.(Running); !ok {
		t.Fatalf("Inspect() = %T, want Running", inspection)
	}

	host.inspection = ProcessIdentityDiffers
	inspection, err = manager.Inspect()
	if err != nil {
		t.Fatal(err)
	}
	stale, ok := inspection.(Stale)
	if !ok || stale.Reason != "identity_mismatch" {
		t.Fatalf("Inspect() = %#v, want identity-mismatch Stale", inspection)
	}
}

func TestRunReplacesStaleStateAndCleansOwnedState(t *testing.T) {
	t.Parallel()

	manager, host := newTestManager(t)
	writeTestState(t, manager, testState(101))
	host.inspection = ProcessAbsent
	host.current = testState(202).Process
	manager.newInstanceID = func() (string, error) { return "22222222222222222222222222222222", nil }

	ctx, cancel := context.WithCancel(context.Background())
	var ready State
	err := manager.Run(ctx, func(state State) {
		ready = state
		cancel()
	})
	if err != nil {
		t.Fatal(err)
	}
	if ready.InstanceID != "22222222222222222222222222222222" || ready.Process.PID != 202 {
		t.Fatalf("ready state = %#v", ready)
	}
	if _, err := os.Stat(manager.statePath()); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("state after Run = %v, want absent", err)
	}
	if host.lockHeld {
		t.Fatal("Run left daemon lock held")
	}
}

func TestStartIsIdempotentForRunningDaemon(t *testing.T) {
	t.Parallel()

	manager, host := newTestManager(t)
	state := testState(101)
	writeTestState(t, manager, state)
	host.inspection = ProcessIdentityMatches

	result, err := manager.Start(context.Background(), DetachedRequest{
		Executable: state.Process.Executable,
		LogPath:    filepath.Join(manager.runtimeDirectory, "daemon.log"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Disposition != StartAlreadyRunning || result.State.Process.PID != 101 {
		t.Fatalf("Start() = %#v", result)
	}
	if host.startCalls != 0 {
		t.Fatalf("StartDetached calls = %d, want 0", host.startCalls)
	}
}

func TestStartCleansStaleStateAndWaitsForChildIdentity(t *testing.T) {
	t.Parallel()

	manager, host := newTestManager(t)
	writeTestState(t, manager, testState(101))
	host.inspection = ProcessAbsent
	host.startPID = 303
	host.onStart = func() {
		writeTestState(t, manager, testState(303))
		host.inspection = ProcessIdentityMatches
	}

	result, err := manager.Start(context.Background(), DetachedRequest{
		Executable: testState(303).Process.Executable,
		LogPath:    filepath.Join(manager.runtimeDirectory, "daemon.log"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Disposition != StartCreated || result.State.Process.PID != 303 {
		t.Fatalf("Start() = %#v", result)
	}
}

func TestStopUsesGracefulSignalBeforeTermination(t *testing.T) {
	t.Parallel()

	manager, host := newTestManager(t)
	state := testState(101)
	writeTestState(t, manager, state)
	host.inspection = ProcessIdentityMatches
	host.onSignal = func() { host.inspection = ProcessAbsent }

	result, err := manager.Stop(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if result.Disposition != StopGraceful {
		t.Fatalf("Stop() = %#v, want graceful", result)
	}
	if host.signals != 1 || host.terminations != 0 {
		t.Fatalf("signals = %d, terminations = %d", host.signals, host.terminations)
	}
}

func TestStopForceTerminatesOnlyAfterGraceExpires(t *testing.T) {
	t.Parallel()

	manager, host := newTestManager(t)
	state := testState(101)
	writeTestState(t, manager, state)
	host.inspection = ProcessIdentityMatches
	manager.stopTimeout = 0
	host.onTerminate = func(identity ProcessIdentity) {
		if identity != state.Process {
			t.Fatalf("TerminateProcess identity = %#v, want %#v", identity, state.Process)
		}
		host.inspection = ProcessAbsent
	}

	result, err := manager.Stop(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if result.Disposition != StopForced {
		t.Fatalf("Stop() = %#v, want forced", result)
	}
	if host.signals != 1 || host.terminations != 1 {
		t.Fatalf("signals = %d, terminations = %d", host.signals, host.terminations)
	}
}

func TestInvalidStateCannotBeCleanedWhileLockIsHeld(t *testing.T) {
	t.Parallel()

	manager, host := newTestManager(t)
	if err := os.WriteFile(manager.statePath(), []byte("not-json"), 0o600); err != nil {
		t.Fatal(err)
	}
	host.lockHeld = true

	_, err := manager.Stop(context.Background())
	if !errors.Is(err, ErrLifecycleUncertain) {
		t.Fatalf("Stop() error = %v, want ErrLifecycleUncertain", err)
	}
}

func newTestManager(t *testing.T) (*Manager, *fakeHost) {
	t.Helper()
	host := &fakeHost{inspection: ProcessAbsent, current: testState(999).Process, startPID: 999}
	manager, err := NewManager(t.TempDir(), host)
	if err != nil {
		t.Fatal(err)
	}
	manager.pollInterval = time.Millisecond
	return manager, host
}

func testState(pid int) State {
	return State{
		SchemaVersion: stateSchemaVersion,
		InstanceID:    "11111111111111111111111111111111",
		Process: ProcessIdentity{
			PID:        pid,
			StartedAt:  time.Date(2026, 8, 31, 20, 0, 0, 123400000, time.UTC),
			Executable: `C:\Program Files\DARKSTAR\darkstar.exe`,
		},
	}
}

func writeTestState(t *testing.T, manager *Manager, state State) {
	t.Helper()
	if err := os.MkdirAll(manager.runtimeDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := manager.writeState(state); err != nil {
		t.Fatal(err)
	}
}

type fakeHost struct {
	lockHeld     bool
	inspection   ProcessInspection
	current      ProcessIdentity
	startPID     int
	startCalls   int
	signals      int
	terminations int
	onStart      func()
	onSignal     func()
	onTerminate  func(ProcessIdentity)
}

func (h *fakeHost) AcquireLock(string) (Lock, error) {
	if h.lockHeld {
		return nil, ErrLockHeld
	}
	h.lockHeld = true
	return fakeLock{close: func() { h.lockHeld = false }}, nil
}

func (h *fakeHost) CreateStopEvent(string) (StopEvent, error) { return fakeStopEvent{}, nil }

func (h *fakeHost) CurrentProcessIdentity() (ProcessIdentity, error) { return h.current, nil }

func (h *fakeHost) InspectProcess(ProcessIdentity) (ProcessInspection, error) {
	return h.inspection, nil
}

func (h *fakeHost) SignalStop(string) error {
	h.signals++
	if h.onSignal != nil {
		h.onSignal()
	}
	return nil
}

func (h *fakeHost) StartDetached(context.Context, DetachedRequest) (int, error) {
	h.startCalls++
	if h.onStart != nil {
		h.onStart()
	}
	return h.startPID, nil
}

func (h *fakeHost) TerminateProcess(identity ProcessIdentity) error {
	h.terminations++
	if h.onTerminate != nil {
		h.onTerminate(identity)
	}
	return nil
}

type fakeLock struct{ close func() }

func (l fakeLock) Close() error {
	l.close()
	return nil
}

type fakeStopEvent struct{}

func (fakeStopEvent) Wait(ctx context.Context) error {
	<-ctx.Done()
	return ctx.Err()
}

func (fakeStopEvent) Close() error { return nil }
