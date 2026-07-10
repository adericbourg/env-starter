package fsutil

import (
	"os"
	"testing"
)

// lockFileExclusive_thenUnlock_succeed verifies that acquiring and releasing a
// file lock completes without error on the current platform.
func TestLockUnlock_onTempFile_succeed(t *testing.T) {
	t.Helper()

	// Given a temporary file to use as a lock target
	f, err := os.CreateTemp(t.TempDir(), "fsutil-lock-*.lock")
	if err != nil {
		t.Fatalf("create temp file: %v", err)
	}
	defer f.Close()

	// When acquiring an exclusive lock
	if err := LockFileExclusive(f); err != nil {
		t.Fatalf("LockFileExclusive: unexpected error: %v", err)
	}

	// Then releasing the lock also succeeds
	if err := UnlockFile(f); err != nil {
		t.Fatalf("UnlockFile: unexpected error: %v", err)
	}
}
