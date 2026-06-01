package logbuf

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestPurgeOlderThan_removesFilesOlderThanMaxAge(t *testing.T) {
	// Given
	dir := t.TempDir()
	old := filepath.Join(dir, "old.log")
	recent := filepath.Join(dir, "recent.log")

	if err := os.WriteFile(old, []byte("stale"), 0o644); err != nil {
		t.Fatalf("setup old.log: %v", err)
	}
	if err := os.WriteFile(recent, []byte("fresh"), 0o644); err != nil {
		t.Fatalf("setup recent.log: %v", err)
	}

	// Back-date old.log to 40 days ago.
	past := time.Now().Add(-40 * 24 * time.Hour)
	if err := os.Chtimes(old, past, past); err != nil {
		t.Fatalf("Chtimes: %v", err)
	}

	// When
	removed, err := PurgeOlderThan(dir, 30*24*time.Hour)

	// Then
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(removed) != 1 || removed[0] != "old.log" {
		t.Errorf("removed = %v, want [old.log]", removed)
	}
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Error("old.log still exists after purge")
	}
	if _, err := os.Stat(recent); err != nil {
		t.Errorf("recent.log was unexpectedly removed: %v", err)
	}
}

func TestPurgeOlderThan_ignoresNonLogFiles(t *testing.T) {
	// Given
	dir := t.TempDir()
	notALog := filepath.Join(dir, "notes.txt")

	if err := os.WriteFile(notALog, []byte("data"), 0o644); err != nil {
		t.Fatalf("setup notes.txt: %v", err)
	}
	// Back-date the file so it would be purged if it matched.
	past := time.Now().Add(-40 * 24 * time.Hour)
	if err := os.Chtimes(notALog, past, past); err != nil {
		t.Fatalf("Chtimes: %v", err)
	}

	// When
	removed, err := PurgeOlderThan(dir, 30*24*time.Hour)

	// Then
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(removed) != 0 {
		t.Errorf("removed = %v, want empty", removed)
	}
	if _, err := os.Stat(notALog); err != nil {
		t.Errorf("notes.txt was unexpectedly removed: %v", err)
	}
}

func TestPurgeOlderThan_skipsDirectories(t *testing.T) {
	// Given — a subdirectory whose name ends in .log and is old.
	dir := t.TempDir()
	subdir := filepath.Join(dir, "fake.log")
	if err := os.Mkdir(subdir, 0o755); err != nil {
		t.Fatalf("mkdir fake.log: %v", err)
	}
	past := time.Now().Add(-40 * 24 * time.Hour)
	if err := os.Chtimes(subdir, past, past); err != nil {
		t.Fatalf("Chtimes: %v", err)
	}

	// When
	removed, err := PurgeOlderThan(dir, 30*24*time.Hour)

	// Then
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(removed) != 0 {
		t.Errorf("removed = %v, want empty (directory must be skipped)", removed)
	}
	if _, err := os.Stat(subdir); err != nil {
		t.Errorf("fake.log/ directory was unexpectedly removed: %v", err)
	}
}

func TestPurgeOlderThan_missingDir_returnsNoError(t *testing.T) {
	// Given — a directory that does not exist.
	dir := filepath.Join(t.TempDir(), "nonexistent")

	// When
	removed, err := PurgeOlderThan(dir, 30*24*time.Hour)

	// Then
	if err != nil {
		t.Fatalf("expected no error for missing dir, got: %v", err)
	}
	if len(removed) != 0 {
		t.Errorf("removed = %v, want empty", removed)
	}
}
