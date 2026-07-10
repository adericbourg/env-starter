//go:build !windows

package daemon

import (
	"os/exec"
	"syscall"
)

// configureDetached detaches the child from the controlling terminal so it
// survives the parent process exiting.
func configureDetached(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
}
