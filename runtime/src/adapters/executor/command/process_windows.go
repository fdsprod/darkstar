//go:build windows

package command

import (
	"errors"
	"fmt"
	"os/exec"
	"sync"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

type processOwner struct {
	command *exec.Cmd
	job     windows.Handle
	once    sync.Once
}

// Starting suspended closes the race in which a child could escape before the
// process is assigned to the kill-on-close job.
func configureOwnedProcess(command *exec.Cmd) {
	command.SysProcAttr = &syscall.SysProcAttr{
		HideWindow: true, CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP | windows.CREATE_SUSPENDED,
	}
}

func newProcessOwner(command *exec.Cmd) (*processOwner, error) {
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return nil, fmt.Errorf("create kill-on-close job: %w", err)
	}
	limits := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
	limits.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	if _, err := windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&limits)), uint32(unsafe.Sizeof(limits))); err != nil {
		_ = windows.CloseHandle(job)
		return nil, fmt.Errorf("configure kill-on-close job: %w", err)
	}
	process, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE, false, uint32(command.Process.Pid))
	if err != nil {
		_ = windows.CloseHandle(job)
		return nil, fmt.Errorf("open process for job assignment: %w", err)
	}
	defer func() { _ = windows.CloseHandle(process) }()
	if err := windows.AssignProcessToJobObject(job, process); err != nil {
		_ = windows.CloseHandle(job)
		return nil, fmt.Errorf("assign process to job: %w", err)
	}
	if err := resumeOwnedProcess(command.Process.Pid); err != nil {
		_ = windows.CloseHandle(job)
		return nil, fmt.Errorf("resume job-owned process: %w", err)
	}
	return &processOwner{command: command, job: job}, nil
}

func resumeOwnedProcess(pid int) error {
	snapshot, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPTHREAD, 0)
	if err != nil {
		return err
	}
	defer func() { _ = windows.CloseHandle(snapshot) }()
	entry := windows.ThreadEntry32{Size: uint32(unsafe.Sizeof(windows.ThreadEntry32{}))}
	if err := windows.Thread32First(snapshot, &entry); err != nil {
		return err
	}
	resumed := 0
	for {
		if entry.OwnerProcessID == uint32(pid) {
			thread, openErr := windows.OpenThread(windows.THREAD_SUSPEND_RESUME, false, entry.ThreadID)
			if openErr != nil {
				return openErr
			}
			_, resumeErr := windows.ResumeThread(thread)
			_ = windows.CloseHandle(thread)
			if resumeErr != nil {
				return resumeErr
			}
			resumed++
		}
		if err := windows.Thread32Next(snapshot, &entry); err != nil {
			if errors.Is(err, windows.ERROR_NO_MORE_FILES) {
				break
			}
			return err
		}
	}
	if resumed == 0 {
		return errors.New("started process had no resumable thread")
	}
	return nil
}

func (owner *processOwner) Wait() error {
	err := owner.command.Wait()
	owner.close()
	return err
}

// Windows does not provide a reliable non-interactive graceful signal for an
// arbitrary hidden process, so cancellation escalates directly to the job.
func (*processOwner) Terminate() (bool, error) { return false, nil }

func (owner *processOwner) Kill() error {
	err := windows.TerminateJobObject(owner.job, 1)
	owner.close()
	return err
}

func (owner *processOwner) PID() int { return owner.command.Process.Pid }

func (owner *processOwner) close() {
	owner.once.Do(func() { _ = windows.CloseHandle(owner.job) })
}
