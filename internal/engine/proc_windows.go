//go:build windows

package engine

import (
	"fmt"
	"os/exec"
)

// setSysProcAttr is a no-op on Windows; process-group handling is not used.
func setSysProcAttr(cmd *exec.Cmd) {}

// interruptProcess kills the process; Windows has no SIGINT-to-group equivalent.
func interruptProcess(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return fmt.Errorf("process not started")
	}
	return cmd.Process.Kill()
}

// killProcess kills the process.
func killProcess(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return fmt.Errorf("process not started")
	}
	return cmd.Process.Kill()
}
