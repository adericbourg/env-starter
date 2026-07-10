package fsutil

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestEnsureOwnerOnlyDirCreates(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "a", "b")

	if err := EnsureOwnerOnlyDir(dir); err != nil {
		t.Fatalf("EnsureOwnerOnlyDir: %v", err)
	}

	info, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if !info.IsDir() {
		t.Fatalf("expected directory at %s", dir)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o700 {
		t.Fatalf("expected mode 0700, got %o", info.Mode().Perm())
	}
}

func TestEnsureOwnerOnlyDirTightensExisting(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix permission bits are not meaningful on windows")
	}
	dir := filepath.Join(t.TempDir(), "loose")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	// Mkdir applies the umask; force the loose mode we want to start from.
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatalf("chmod: %v", err)
	}

	if err := EnsureOwnerOnlyDir(dir); err != nil {
		t.Fatalf("EnsureOwnerOnlyDir: %v", err)
	}

	info, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if info.Mode().Perm() != 0o700 {
		t.Fatalf("expected pre-existing dir tightened to 0700, got %o", info.Mode().Perm())
	}
}

func TestEnsureOwnerOnlyDirIdempotent(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "d")

	if err := EnsureOwnerOnlyDir(dir); err != nil {
		t.Fatalf("first call: %v", err)
	}
	if err := EnsureOwnerOnlyDir(dir); err != nil {
		t.Fatalf("second call: %v", err)
	}
}

func TestEnsureOwnerOnlyDirRejectsFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	if err := EnsureOwnerOnlyDir(path); err == nil {
		t.Fatal("expected an error for a regular file, got nil")
	}
}
