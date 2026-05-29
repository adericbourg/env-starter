//go:build !windows

package engine

import (
	"fmt"
	"os/exec"
	"syscall"
)

// setSysProcAttr configures the command to run in its own process group so the
// whole group can be signalled at once.
func setSysProcAttr(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// interruptProcess sends SIGINT to the command's process group.
func interruptProcess(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return fmt.Errorf("process not started")
	}
	// Negative pid targets the whole process group (set up via Setpgid).
	return syscall.Kill(-cmd.Process.Pid, syscall.SIGINT)
}

// killProcess sends SIGKILL to the command's process group.
func killProcess(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return fmt.Errorf("process not started")
	}
	return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
}
