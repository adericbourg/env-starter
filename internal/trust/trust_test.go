package trust

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// isolateCacheDir redirects os.UserCacheDir() (and therefore source.CacheDir,
// and therefore the trust store) to a throwaway temp dir, so tests never
// read or write the developer's real trust store. Mirrors the pattern
// already used by internal/engine and cmd/env-starter tests.
func isolateCacheDir(t *testing.T) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", "")
}

func writeTempFile(t *testing.T, dir, name, content string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("writeTempFile: %v", err)
	}
	return path
}

// ── Hash ──────────────────────────────────────────────────────────────────

func TestHash_ofKnownBytes_returnsKnownSha256(t *testing.T) {
	// Given a file with known content ("hello\n" sha256 is well-known).
	dir := t.TempDir()
	path := writeTempFile(t, dir, "config.yaml", "hello\n")

	// When
	got, err := Hash(path)

	// Then
	if err != nil {
		t.Fatalf("Hash: unexpected error: %v", err)
	}
	want := "5891b5b522d5df086d0ff0b110fbd9d21bb4fc7163af34d08286a2e846f6be03"
	if got != want {
		t.Errorf("Hash(%q) = %q; want %q", path, got, want)
	}
}

func TestHash_ofMissingFile_errors(t *testing.T) {
	// Given a path that does not exist.
	path := filepath.Join(t.TempDir(), "nonexistent.yaml")

	// When
	_, err := Hash(path)

	// Then
	if err == nil {
		t.Fatal("Hash: expected an error for a missing file, got nil")
	}
	if !errors.Is(err, os.ErrNotExist) {
		t.Errorf("Hash: expected an ErrNotExist-wrapping error, got: %v", err)
	}
}

func TestHash_ofRelativeAndAbsolutePath_returnsSameDigest(t *testing.T) {
	// Given the same file referenced by an absolute and a relative path.
	dir := t.TempDir()
	abs := writeTempFile(t, dir, "config.yaml", "content\n")

	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	defer func() { _ = os.Chdir(cwd) }()
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("Chdir: %v", err)
	}

	// When
	fromAbs, err := Hash(abs)
	if err != nil {
		t.Fatalf("Hash(abs): %v", err)
	}
	fromRel, err := Hash("config.yaml")
	if err != nil {
		t.Fatalf("Hash(rel): %v", err)
	}

	// Then
	if fromAbs != fromRel {
		t.Errorf("Hash: absolute (%q) and relative (%q) paths produced different digests", fromAbs, fromRel)
	}
}

// ── Check / Approve round-trip ────────────────────────────────────────────

func TestCheck_ofUnapprovedPath_returnsNotApprovedErrorUnknown(t *testing.T) {
	// Given a config file that has never been approved.
	isolateCacheDir(t)
	dir := t.TempDir()
	path := writeTempFile(t, dir, "config.yaml", "commands: []\n")

	// When
	err := Check([]string{path})

	// Then
	var notApproved *NotApprovedError
	if !errors.As(err, &notApproved) {
		t.Fatalf("Check: expected a *NotApprovedError, got: %v", err)
	}
	if notApproved.Reason != ReasonUnknown {
		t.Errorf("Check: expected Reason=ReasonUnknown, got %v", notApproved.Reason)
	}
}

func TestApprove_thenCheck_succeeds(t *testing.T) {
	// Given an approved config file.
	isolateCacheDir(t)
	dir := t.TempDir()
	path := writeTempFile(t, dir, "config.yaml", "commands: []\n")

	if err := Approve([]string{path}); err != nil {
		t.Fatalf("Approve: unexpected error: %v", err)
	}

	// When
	err := Check([]string{path})

	// Then
	if err != nil {
		t.Errorf("Check: expected no error for an approved, unchanged file, got: %v", err)
	}
}

func TestCheck_ofChangedApprovedPath_returnsNotApprovedErrorChanged(t *testing.T) {
	// Given a config file approved once, then edited afterwards.
	isolateCacheDir(t)
	dir := t.TempDir()
	path := writeTempFile(t, dir, "config.yaml", "commands: []\n")

	if err := Approve([]string{path}); err != nil {
		t.Fatalf("Approve: unexpected error: %v", err)
	}
	if err := os.WriteFile(path, []byte("commands: [tampered]\n"), 0o600); err != nil {
		t.Fatalf("editing config: %v", err)
	}

	// When
	err := Check([]string{path})

	// Then
	var notApproved *NotApprovedError
	if !errors.As(err, &notApproved) {
		t.Fatalf("Check: expected a *NotApprovedError, got: %v", err)
	}
	if notApproved.Reason != ReasonChanged {
		t.Errorf("Check: expected Reason=ReasonChanged, got %v", notApproved.Reason)
	}
}

func TestApprove_afterEdit_updatesStoredHash(t *testing.T) {
	// Given a config approved, then edited and re-approved.
	isolateCacheDir(t)
	dir := t.TempDir()
	path := writeTempFile(t, dir, "config.yaml", "commands: []\n")
	if err := Approve([]string{path}); err != nil {
		t.Fatalf("Approve (first): unexpected error: %v", err)
	}
	if err := os.WriteFile(path, []byte("commands: [added]\n"), 0o600); err != nil {
		t.Fatalf("editing config: %v", err)
	}

	// When re-approved after the edit.
	if err := Approve([]string{path}); err != nil {
		t.Fatalf("Approve (second): unexpected error: %v", err)
	}

	// Then Check against the new content succeeds.
	if err := Check([]string{path}); err != nil {
		t.Errorf("Check: expected no error after re-approval, got: %v", err)
	}
}

func TestCheck_ofMultiplePaths_failsIfAnyIsUnapproved(t *testing.T) {
	// Given a base config approved, and an overlay never approved.
	isolateCacheDir(t)
	dir := t.TempDir()
	base := writeTempFile(t, dir, "base.yaml", "commands: []\n")
	overlay := writeTempFile(t, dir, "overlay.yaml", "commands: []\n")
	if err := Approve([]string{base}); err != nil {
		t.Fatalf("Approve(base): unexpected error: %v", err)
	}

	// When
	err := Check([]string{base, overlay})

	// Then — the unapproved overlay fails the whole check (AND semantics).
	var notApproved *NotApprovedError
	if !errors.As(err, &notApproved) {
		t.Fatalf("Check: expected a *NotApprovedError, got: %v", err)
	}
	wantPath, err2 := normalize(overlay)
	if err2 != nil {
		t.Fatalf("normalize(overlay): %v", err2)
	}
	if notApproved.Path != wantPath {
		t.Errorf("Check: expected the failing path to be the overlay %q, got %q", wantPath, notApproved.Path)
	}
}

func TestCheck_ofEmptyPath_isSkipped(t *testing.T) {
	// Given an approved base config and no overlay (empty string, as
	// resolveConfig passes when --config-overlay is unset).
	isolateCacheDir(t)
	dir := t.TempDir()
	base := writeTempFile(t, dir, "base.yaml", "commands: []\n")
	if err := Approve([]string{base}); err != nil {
		t.Fatalf("Approve: unexpected error: %v", err)
	}

	// When
	err := Check([]string{base, ""})

	// Then
	if err != nil {
		t.Errorf("Check: expected the empty path to be skipped, got: %v", err)
	}
}

// ── Store persistence ─────────────────────────────────────────────────────

func TestApprove_writesOwnerOnlyStoreFile(t *testing.T) {
	// Given a config file to approve, with the cache dir redirected to a temp dir.
	isolateCacheDir(t)
	dir := t.TempDir()
	path := writeTempFile(t, dir, "config.yaml", "commands: []\n")

	// When
	if err := Approve([]string{path}); err != nil {
		t.Fatalf("Approve: unexpected error: %v", err)
	}

	// Then the store file exists with 0600 permissions.
	storeFile, err := storePath()
	if err != nil {
		t.Fatalf("storePath: %v", err)
	}
	info, err := os.Stat(storeFile)
	if err != nil {
		t.Fatalf("expected store file to exist at %q: %v", storeFile, err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("expected store file mode 0600, got %v", info.Mode().Perm())
	}
	if _, err := os.Stat(storeFile + ".tmp"); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("expected the temp file to be renamed away, but it still exists (err=%v)", err)
	}
}

func TestCheck_whenStoreFileMissing_treatsAsNoApprovals(t *testing.T) {
	// Given a config file, and no trust store has ever been written.
	isolateCacheDir(t)
	dir := t.TempDir()
	path := writeTempFile(t, dir, "config.yaml", "commands: []\n")

	// When
	err := Check([]string{path})

	// Then — unapproved, not a fatal "store missing" error.
	var notApproved *NotApprovedError
	if !errors.As(err, &notApproved) {
		t.Fatalf("Check: expected a *NotApprovedError for a first-ever run, got: %v", err)
	}
}
