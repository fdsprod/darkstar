package windows

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"darkstar/src/daemon"
	"golang.org/x/sys/windows"
)

const (
	waitTimeout      = 258
	stillActive      = 259
	stopPollInterval = 100 * time.Millisecond
)

// DaemonHost implements the Windows-specific lock, identity, event, and
// detached-process operations used by daemon.Manager.
type DaemonHost struct{}

var _ daemon.Host = (*DaemonHost)(nil)

func NewDaemonHost() *DaemonHost { return &DaemonHost{} }

func (h *DaemonHost) AcquireLock(path string) (daemon.Lock, error) {
	if !filepath.IsAbs(path) {
		return nil, fmt.Errorf("daemon lock path must be absolute: %q", path)
	}
	pathPointer, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, fmt.Errorf("encode daemon lock path: %w", err)
	}
	handle, err := windows.CreateFile(
		pathPointer,
		windows.GENERIC_READ|windows.GENERIC_WRITE,
		0,
		nil,
		windows.OPEN_ALWAYS,
		windows.FILE_ATTRIBUTE_NORMAL,
		0,
	)
	if errors.Is(err, windows.ERROR_SHARING_VIOLATION) || errors.Is(err, windows.ERROR_LOCK_VIOLATION) {
		return nil, daemon.ErrLockHeld
	}
	if err != nil {
		return nil, err
	}
	return &daemonLock{handle: handle}, nil
}

func (h *DaemonHost) CreateStopEvent(instanceID string) (daemon.StopEvent, error) {
	name, err := stopEventName(instanceID)
	if err != nil {
		return nil, err
	}
	namePointer, err := windows.UTF16PtrFromString(name)
	if err != nil {
		return nil, err
	}
	handle, err := windows.CreateEvent(nil, 1, 0, namePointer)
	if err != nil {
		return nil, err
	}
	return &daemonStopEvent{handle: handle}, nil
}

func (h *DaemonHost) CurrentProcessIdentity() (daemon.ProcessIdentity, error) {
	return processIdentity(os.Getpid())
}

func (h *DaemonHost) InspectProcess(expected daemon.ProcessIdentity) (daemon.ProcessInspection, error) {
	handle, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION|windows.SYNCHRONIZE, false, uint32(expected.PID))
	if errors.Is(err, windows.ERROR_INVALID_PARAMETER) {
		return daemon.ProcessAbsent, nil
	}
	if err != nil {
		return 0, err
	}
	defer func() {
		_ = windows.CloseHandle(handle)
	}()
	return inspectHandle(handle, expected)
}

func (h *DaemonHost) SignalStop(instanceID string) error {
	name, err := stopEventName(instanceID)
	if err != nil {
		return err
	}
	namePointer, err := windows.UTF16PtrFromString(name)
	if err != nil {
		return err
	}
	handle, err := windows.OpenEvent(windows.EVENT_MODIFY_STATE, false, namePointer)
	if err != nil {
		return err
	}
	defer func() {
		_ = windows.CloseHandle(handle)
	}()
	return windows.SetEvent(handle)
}

func (h *DaemonHost) StartDetached(ctx context.Context, request daemon.DetachedRequest) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	if !filepath.IsAbs(request.Executable) {
		return 0, fmt.Errorf("daemon executable must be absolute: %q", request.Executable)
	}
	if !filepath.IsAbs(request.LogPath) {
		return 0, fmt.Errorf("daemon log path must be absolute: %q", request.LogPath)
	}
	if err := os.MkdirAll(filepath.Dir(request.LogPath), 0o700); err != nil {
		return 0, fmt.Errorf("create daemon log directory: %w", err)
	}
	logFile, err := os.OpenFile(request.LogPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return 0, fmt.Errorf("open daemon log: %w", err)
	}
	defer func() {
		_ = logFile.Close()
	}()

	command := exec.Command(request.Executable, request.Arguments...)
	command.Stdout = logFile
	command.Stderr = logFile
	command.Stdin = nil
	command.SysProcAttr = &syscall.SysProcAttr{
		HideWindow:    true,
		CreationFlags: windows.CREATE_NEW_PROCESS_GROUP | windows.DETACHED_PROCESS,
	}
	if err := command.Start(); err != nil {
		return 0, err
	}
	pid := command.Process.Pid
	if err := command.Process.Release(); err != nil {
		return 0, fmt.Errorf("release detached daemon process: %w", err)
	}
	return pid, nil
}

func (h *DaemonHost) TerminateProcess(expected daemon.ProcessIdentity) error {
	handle, err := windows.OpenProcess(
		windows.PROCESS_QUERY_LIMITED_INFORMATION|windows.PROCESS_TERMINATE|windows.SYNCHRONIZE,
		false,
		uint32(expected.PID),
	)
	if errors.Is(err, windows.ERROR_INVALID_PARAMETER) {
		return nil
	}
	if err != nil {
		return err
	}
	defer func() {
		_ = windows.CloseHandle(handle)
	}()

	inspection, err := inspectHandle(handle, expected)
	if err != nil {
		return err
	}
	if inspection == daemon.ProcessAbsent {
		return nil
	}
	if inspection != daemon.ProcessIdentityMatches {
		return fmt.Errorf("%w: PID %d no longer matches the recorded daemon", daemon.ErrLifecycleUncertain, expected.PID)
	}
	return windows.TerminateProcess(handle, 1)
}

type daemonLock struct{ handle windows.Handle }

func (l *daemonLock) Close() error {
	if l.handle == 0 {
		return nil
	}
	err := windows.CloseHandle(l.handle)
	l.handle = 0
	return err
}

type daemonStopEvent struct{ handle windows.Handle }

func (e *daemonStopEvent) Wait(ctx context.Context) error {
	for {
		result, err := windows.WaitForSingleObject(e.handle, uint32(stopPollInterval/time.Millisecond))
		if err != nil {
			return err
		}
		switch result {
		case windows.WAIT_OBJECT_0:
			return nil
		case waitTimeout:
			if err := ctx.Err(); err != nil {
				return err
			}
			continue
		default:
			return fmt.Errorf("unexpected stop event wait result %d", result)
		}
	}
}

func (e *daemonStopEvent) Close() error {
	if e.handle == 0 {
		return nil
	}
	err := windows.CloseHandle(e.handle)
	e.handle = 0
	return err
}

func stopEventName(instanceID string) (string, error) {
	if len(instanceID) != 32 {
		return "", errors.New("daemon instance ID must be 128-bit lowercase hexadecimal")
	}
	for _, character := range instanceID {
		if (character < '0' || character > '9') && (character < 'a' || character > 'f') {
			return "", errors.New("daemon instance ID must be 128-bit lowercase hexadecimal")
		}
	}
	return `Local\DARKSTAR-Daemon-` + instanceID, nil
}

func processIdentity(pid int) (daemon.ProcessIdentity, error) {
	handle, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION|windows.SYNCHRONIZE, false, uint32(pid))
	if err != nil {
		return daemon.ProcessIdentity{}, err
	}
	defer func() {
		_ = windows.CloseHandle(handle)
	}()
	return identityFromHandle(handle, pid)
}

func inspectHandle(handle windows.Handle, expected daemon.ProcessIdentity) (daemon.ProcessInspection, error) {
	var exitCode uint32
	if err := windows.GetExitCodeProcess(handle, &exitCode); err != nil {
		return 0, err
	}
	if exitCode != stillActive {
		return daemon.ProcessAbsent, nil
	}
	observed, err := identityFromHandle(handle, expected.PID)
	if err != nil {
		return 0, err
	}
	if observed.StartedAt.UnixNano() != expected.StartedAt.UnixNano() ||
		!strings.EqualFold(filepath.Clean(observed.Executable), filepath.Clean(expected.Executable)) {
		return daemon.ProcessIdentityDiffers, nil
	}
	return daemon.ProcessIdentityMatches, nil
}

func identityFromHandle(handle windows.Handle, pid int) (daemon.ProcessIdentity, error) {
	var creationTime, exitTime, kernelTime, userTime windows.Filetime
	if err := windows.GetProcessTimes(handle, &creationTime, &exitTime, &kernelTime, &userTime); err != nil {
		return daemon.ProcessIdentity{}, err
	}
	buffer := make([]uint16, 32768)
	size := uint32(len(buffer))
	if err := windows.QueryFullProcessImageName(handle, 0, &buffer[0], &size); err != nil {
		return daemon.ProcessIdentity{}, err
	}
	executable := filepath.Clean(windows.UTF16ToString(buffer[:size]))
	return daemon.ProcessIdentity{
		PID:        pid,
		StartedAt:  time.Unix(0, creationTime.Nanoseconds()).UTC(),
		Executable: executable,
	}, nil
}
