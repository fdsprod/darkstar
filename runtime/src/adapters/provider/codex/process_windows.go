//go:build windows

package codex

import (
	"errors"
	"fmt"
	"os/exec"
	"sync"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

type commandOwner struct {
	command *exec.Cmd
	job     windows.Handle
	once    sync.Once
}

func configureAppServerProcess(command *exec.Cmd) {
	command.SysProcAttr = &syscall.SysProcAttr{
		HideWindow: true, CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP | windows.CREATE_SUSPENDED,
	}
}

func newCommandOwner(command *exec.Cmd) (*commandOwner, error) {
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return nil, fmt.Errorf("create kill-on-close job: %w", err)
	}
	limits := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
	limits.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	if _, err := windows.SetInformationJobObject(
		job,
		windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&limits)),
		uint32(unsafe.Sizeof(limits)),
	); err != nil {
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
		return nil, fmt.Errorf("assign process to kill-on-close job: %w", err)
	}
	if err := resumeProcess(command.Process.Pid); err != nil {
		_ = windows.CloseHandle(job)
		return nil, fmt.Errorf("resume job-owned process: %w", err)
	}
	return &commandOwner{command: command, job: job}, nil
}

func resumeProcess(pid int) error {
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

func (owner *commandOwner) Wait() error {
	err := owner.command.Wait()
	owner.closeJob()
	return err
}

func (owner *commandOwner) Kill() error {
	err := windows.TerminateJobObject(owner.job, 1)
	owner.closeJob()
	if err == nil {
		_ = owner.command.Wait()
	}
	return err
}

func (owner *commandOwner) PID() int { return owner.command.Process.Pid }

func (owner *commandOwner) closeJob() {
	owner.once.Do(func() { _ = windows.CloseHandle(owner.job) })
}
