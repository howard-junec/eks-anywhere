//go:build windows

package framework

import "os/exec"

func configureCommandCancellation(_ *exec.Cmd) {}
