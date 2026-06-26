//go:build !windows

package daemon

import (
	"os"
	"os/exec"
	"syscall"
)

// lockFileExclusive blocks until it holds an exclusive advisory lock on f.
func lockFileExclusive(f *os.File) error {
	return syscall.Flock(int(f.Fd()), syscall.LOCK_EX)
}

// unlockFile releases the advisory lock held on f.
func unlockFile(f *os.File) error {
	return syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
}

// configureDetached detaches the child from the controlling terminal so it
// survives the parent process exiting.
func configureDetached(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
}
