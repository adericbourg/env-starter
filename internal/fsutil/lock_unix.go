//go:build !windows

package fsutil

import (
	"os"
	"syscall"
)

// LockFileExclusive blocks until it holds an exclusive advisory lock on f.
func LockFileExclusive(f *os.File) error {
	return syscall.Flock(int(f.Fd()), syscall.LOCK_EX)
}

// UnlockFile releases the advisory lock held on f.
func UnlockFile(f *os.File) error {
	return syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
}
