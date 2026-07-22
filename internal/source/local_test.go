package source

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
)

// snapshotTree walks dir and returns the contents of every regular file,
// keyed by path relative to dir, so a test can assert nothing changed.
func snapshotTree(t *testing.T, dir string) map[string][]byte {
	t.Helper()
	snapshot := map[string][]byte{}
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		snapshot[rel] = data
		return nil
	})
	if err != nil {
		t.Fatalf("failed to snapshot %s: %v", dir, err)
	}
	return snapshot
}

func TestLocal_Fetch_ofExistingDir_returnsPath(t *testing.T) {
	// Given
	dir := t.TempDir()
	l := Local{Path: dir}

	// When
	got, err := l.Fetch(context.Background())

	// Then
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != dir {
		t.Errorf("got %q, want %q", got, dir)
	}
}

func TestLocal_Fetch_ofMissingPath_returnsError(t *testing.T) {
	// Given
	l := Local{Path: filepath.Join(t.TempDir(), "nonexistent")}

	// When
	_, err := l.Fetch(context.Background())

	// Then
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestLocal_Fetch_ofFilePath_returnsError(t *testing.T) {
	// Given
	dir := t.TempDir()
	// Create a file inside the temp dir.
	filePath := filepath.Join(dir, "afile.txt")
	if err := writeFile(filePath, []byte("hello")); err != nil {
		t.Fatal(err)
	}
	l := Local{Path: filePath}

	// When
	_, err := l.Fetch(context.Background())

	// Then
	if err == nil {
		t.Fatal("expected error for file path, got nil")
	}
}

// TestLocal_Fetch_whenPathIsGitRepo_leavesRepoUntouched pins the invariant that
// a local source is never mutated, even when its path happens to be a git
// working copy: unlike the github source, Local.Fetch must never run git
// against it.
func TestLocal_Fetch_whenPathIsGitRepo_leavesRepoUntouched(t *testing.T) {
	// Given a directory shaped like a git working copy (a .git dir plus a
	// tracked-looking file), snapshotted before Fetch runs.
	dir := t.TempDir()
	if err := makeDir(filepath.Join(dir, ".git"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := writeFile(filepath.Join(dir, ".git", "HEAD"), []byte("ref: refs/heads/main\n")); err != nil {
		t.Fatal(err)
	}
	if err := writeFile(filepath.Join(dir, "app.go"), []byte("package app\n")); err != nil {
		t.Fatal(err)
	}
	before := snapshotTree(t, dir)

	l := Local{Path: dir}

	// When
	got, err := l.Fetch(context.Background())

	// Then
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != dir {
		t.Errorf("got %q, want %q", got, dir)
	}
	after := snapshotTree(t, dir)
	if len(before) != len(after) {
		t.Fatalf("file count changed: before %d, after %d", len(before), len(after))
	}
	for path, want := range before {
		got, ok := after[path]
		if !ok {
			t.Errorf("%s: file disappeared after Fetch", path)
			continue
		}
		if string(got) != string(want) {
			t.Errorf("%s: content changed after Fetch: before %q, after %q", path, want, got)
		}
	}
}
