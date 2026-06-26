//go:build windows

package daemon

import (
	"os"
	"os/exec"
	"syscall"

	"golang.org/x/sys/windows"
)

// lockFileExclusive blocks until it holds an exclusive lock on the first byte
// of f. LockFileEx without LOCKFILE_FAIL_IMMEDIATELY blocks, matching the
// behaviour of flock(LOCK_EX) on Unix.
func lockFileExclusive(f *os.File) error {
	return windows.LockFileEx(
		windows.Handle(f.Fd()),
		windows.LOCKFILE_EXCLUSIVE_LOCK, // blocking exclusive lock
		0, 1, 0,
		&windows.Overlapped{},
	)
}

// unlockFile releases the lock held on the first byte of f.
func unlockFile(f *os.File) error {
	return windows.UnlockFileEx(
		windows.Handle(f.Fd()),
		0, 1, 0,
		&windows.Overlapped{},
	)
}

// configureDetached starts the child in its own process group, detached from
// the console, so it survives the parent process exiting.
func configureDetached(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{
		CreationFlags: windows.CREATE_NEW_PROCESS_GROUP | windows.DETACHED_PROCESS,
	}
}
