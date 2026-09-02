//go:build windows

package codex

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

func TestCommandOwnerTerminatesWindowsProcessTree(t *testing.T) {
	mode := os.Getenv("DARKSTAR_PROCESS_OWNER_HELPER")
	switch mode {
	case "parent":
		runProcessOwnerParent(t)
		return
	case "child":
		select {}
	}

	root := t.TempDir()
	gate := filepath.Join(root, "gate")
	childPID := filepath.Join(root, "child.pid")
	command := exec.Command(os.Args[0], "-test.run=TestCommandOwnerTerminatesWindowsProcessTree")
	command.Env = append(os.Environ(),
		"DARKSTAR_PROCESS_OWNER_HELPER=parent",
		"DARKSTAR_PROCESS_OWNER_GATE="+gate,
		"DARKSTAR_PROCESS_OWNER_CHILD_PID="+childPID,
	)
	configureAppServerProcess(command)
	if err := command.Start(); err != nil {
		t.Fatalf("start parent helper: %v", err)
	}
	owner, err := newCommandOwner(command)
	if err != nil {
		_ = command.Process.Kill()
		_ = command.Wait()
		t.Fatalf("newCommandOwner() error = %v", err)
	}
	if err := os.WriteFile(gate, []byte("go"), 0o600); err != nil {
		t.Fatalf("release helper gate: %v", err)
	}
	grandchild := waitForChildPID(t, childPID)
	if err := owner.Kill(); err != nil {
		t.Fatalf("Kill() error = %v", err)
	}
	assertProcessExited(t, uint32(command.Process.Pid))
	assertProcessExited(t, grandchild)
}

func runProcessOwnerParent(t *testing.T) {
	gate := os.Getenv("DARKSTAR_PROCESS_OWNER_GATE")
	childPID := os.Getenv("DARKSTAR_PROCESS_OWNER_CHILD_PID")
	for deadline := time.Now().Add(10 * time.Second); ; {
		if _, err := os.Stat(gate); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for process-owner gate")
		}
		time.Sleep(10 * time.Millisecond)
	}
	child := exec.Command(os.Args[0], "-test.run=TestCommandOwnerTerminatesWindowsProcessTree")
	child.Env = append(os.Environ(), "DARKSTAR_PROCESS_OWNER_HELPER=child")
	configureAppServerProcess(child)
	if err := child.Start(); err != nil {
		t.Fatalf("start child helper: %v", err)
	}
	if err := os.WriteFile(childPID, []byte(strconv.Itoa(child.Process.Pid)), 0o600); err != nil {
		t.Fatalf("write child PID: %v", err)
	}
	_ = child.Wait()
}

func waitForChildPID(t *testing.T, path string) uint32 {
	t.Helper()
	for deadline := time.Now().Add(10 * time.Second); ; {
		payload, err := os.ReadFile(path)
		if err == nil {
			pid, parseErr := strconv.ParseUint(strings.TrimSpace(string(payload)), 10, 32)
			if parseErr != nil {
				t.Fatalf("parse child PID: %v", parseErr)
			}
			return uint32(pid)
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for child PID: %v", err)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func assertProcessExited(t *testing.T, pid uint32) {
	t.Helper()
	process, err := windows.OpenProcess(windows.SYNCHRONIZE, false, pid)
	if err != nil {
		if err == windows.ERROR_INVALID_PARAMETER {
			return
		}
		t.Fatalf("open process %d: %v", pid, err)
	}
	defer func() { _ = windows.CloseHandle(process) }()
	status, err := windows.WaitForSingleObject(process, 5_000)
	if err != nil {
		t.Fatalf("wait for process %d: %v", pid, err)
	}
	if status != windows.WAIT_OBJECT_0 {
		t.Fatalf("process %d survived owned job termination (wait status %d)", pid, status)
	}
}
