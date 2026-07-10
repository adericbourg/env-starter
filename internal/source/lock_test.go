package source

import (
	"os"
	"path/filepath"
	"testing"
)

// lockPath_acquireThenRelease_succeeds verifies that lockPath creates a
// sibling "<dir>.lock" file, that release unlocks both the in-process mutex
// and the cross-process file lock, and that a subsequent acquire for the same
// dir succeeds once released.
func TestLockPath_acquireThenRelease_succeeds(t *testing.T) {
	t.Helper()

	// Given a cache dir whose parent already exists
	parent := t.TempDir()
	dir := filepath.Join(parent, "github-owner-name-main")

	// When acquiring the lock
	unlock, err := lockPath(dir)
	if err != nil {
		t.Fatalf("lockPath: unexpected error: %v", err)
	}

	// Then the sibling lock file exists
	lockFile := dir + ".lock"
	if _, statErr := os.Stat(lockFile); statErr != nil {
		t.Fatalf("expected lock file %s to exist: %v", lockFile, statErr)
	}

	// When releasing and re-acquiring
	unlock()
	unlock2, err := lockPath(dir)
	if err != nil {
		t.Fatalf("lockPath: re-acquire after release: unexpected error: %v", err)
	}
	unlock2()
}

// lockPath_missingParent_errors verifies that lockPath surfaces an error
// rather than panicking when the cache dir's parent does not exist (the lock
// file cannot be created there).
func TestLockPath_missingParent_errors(t *testing.T) {
	t.Helper()

	dir := filepath.Join(t.TempDir(), "does-not-exist", "github-owner-name-main")

	if _, err := lockPath(dir); err == nil {
		t.Fatal("lockPath: expected error for missing parent dir, got nil")
	}
}
