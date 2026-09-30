//go:build !windows

package framework

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestRunningCommandStopKillsChildProcesses(t *testing.T) {
	tempDir := t.TempDir()
	scriptPath := filepath.Join(tempDir, "spawn-child.sh")
	pidPath := filepath.Join(tempDir, "child.pid")
	script := "#!/bin/sh\nsleep 30 &\necho $! > \"$1\"\nwait\n"
	if err := os.WriteFile(scriptPath, []byte(script), 0o755); err != nil {
		t.Fatalf("writing helper script: %v", err)
	}

	command, err := startCommand(scriptPath, pidPath)
	if err != nil {
		t.Fatalf("startCommand() error = %v", err)
	}

	var childPID int
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		data, readErr := os.ReadFile(pidPath)
		if readErr == nil {
			childPID, err = strconv.Atoi(strings.TrimSpace(string(data)))
			if err != nil {
				t.Fatalf("parsing child PID: %v", err)
			}
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if childPID == 0 {
		_ = command.Stop()
		t.Fatal("helper command did not report its child PID")
	}

	if err := command.Stop(); err == nil {
		t.Fatal("RunningCommand.Stop() error = nil, want canceled command error")
	}

	deadline = time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if !processIsActive(childPID) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("child process %d remained active after stopping command", childPID)
}

func processIsActive(pid int) bool {
	if err := syscall.Kill(pid, 0); err != nil {
		return !errors.Is(err, syscall.ESRCH)
	}
	state, err := exec.Command("ps", "-o", "stat=", "-p", strconv.Itoa(pid)).Output()
	if err != nil {
		return false
	}
	return !strings.HasPrefix(strings.TrimSpace(string(state)), "Z")
}
