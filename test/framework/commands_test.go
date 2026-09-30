package framework

import (
	"strings"
	"testing"
)

func TestRunningCommandWaitCapturesOutput(t *testing.T) {
	command, err := startCommand("printf", "rufio-ready")
	if err != nil {
		t.Fatalf("startCommand() error = %v", err)
	}
	if err := command.Wait(); err != nil {
		t.Fatalf("RunningCommand.Wait() error = %v", err)
	}
	if got := command.Output(); got != "rufio-ready" {
		t.Fatalf("RunningCommand.Output() = %q, want %q", got, "rufio-ready")
	}
}

func TestRunningCommandStop(t *testing.T) {
	command, err := startCommand("sleep", "30")
	if err != nil {
		t.Fatalf("startCommand() error = %v", err)
	}
	if err := command.Stop(); err == nil {
		t.Fatal("RunningCommand.Stop() error = nil, want canceled command error")
	} else if !strings.Contains(err.Error(), "killed") {
		t.Fatalf("RunningCommand.Stop() error = %q, want killed command", err)
	}
}
