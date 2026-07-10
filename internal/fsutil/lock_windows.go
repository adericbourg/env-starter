//go:build windows

package fsutil

import (
	"os"

	"golang.org/x/sys/windows"
)

// LockFileExclusive blocks until it holds an exclusive lock on the first byte
// of f. LockFileEx without LOCKFILE_FAIL_IMMEDIATELY blocks, matching the
// behaviour of flock(LOCK_EX) on Unix.
func LockFileExclusive(f *os.File) error {
	return windows.LockFileEx(
		windows.Handle(f.Fd()),
		windows.LOCKFILE_EXCLUSIVE_LOCK, // blocking exclusive lock
		0, 1, 0,
		&windows.Overlapped{},
	)
}

// UnlockFile releases the lock held on the first byte of f.
func UnlockFile(f *os.File) error {
	return windows.UnlockFileEx(
		windows.Handle(f.Fd()),
		0, 1, 0,
		&windows.Overlapped{},
	)
}
