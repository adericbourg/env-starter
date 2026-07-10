//go:build windows

package daemon

import (
	"os/exec"
	"syscall"

	"golang.org/x/sys/windows"
)

// configureDetached starts the child in its own process group, detached from
// the console, so it survives the parent process exiting.
func configureDetached(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{
		CreationFlags: windows.CREATE_NEW_PROCESS_GROUP | windows.DETACHED_PROCESS,
	}
}
