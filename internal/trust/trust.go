// Package trust implements a trust-on-first-use (TOFU) approval gate for
// env-starter config files.
//
// A command's run/setup/teardown/readiness.shell fields are executed
// verbatim as shell scripts by design (see SECURITY.md) — this package does
// not sanitize them, and never will. What it defends against is a config or
// overlay file that was tampered with or slipped in without the operator's
// knowledge: it hashes each config file's raw bytes and refuses to load a
// config unless every watched file's current hash has been explicitly
// approved (via the `allow` subcommand). Approval is invalidated the moment
// a file's content changes, so a later edit — malicious or not — always
// requires a fresh review.
package trust

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/adericbourg/env-starter/internal/fsutil"
	"github.com/adericbourg/env-starter/internal/source"
)

// storeVersion identifies the trust store's on-disk format.
const storeVersion = 1

// storeFileName is the trust store's file name under source.CacheDir().
const storeFileName = "trust.json"

// Reason distinguishes why a path failed Check.
type Reason int

const (
	// ReasonUnknown means the path's current hash has never been approved.
	ReasonUnknown Reason = iota
	// ReasonChanged means the path was approved before, but its content has
	// since changed and no longer matches the approved hash.
	ReasonChanged
)

// NotApprovedError reports that Path has not been approved, or has changed
// since it was approved, and must be reviewed via `env-starter allow`.
type NotApprovedError struct {
	Path   string
	Reason Reason
}

func (e *NotApprovedError) Error() string {
	if e.Reason == ReasonChanged {
		return fmt.Sprintf("config %q has changed since it was approved; run `env-starter allow` to review and approve it", e.Path)
	}
	return fmt.Sprintf("config %q has not been approved; run `env-starter allow` to review and approve it", e.Path)
}

// Hash returns the hex-encoded sha256 digest of path's raw bytes.
func Hash(path string) (string, error) {
	abs, err := normalize(path)
	if err != nil {
		return "", err
	}
	return hashAbs(abs)
}

// normalize resolves path to an absolute, symlink-free form so the same file
// is keyed identically in the trust store regardless of how it was
// referenced (relative vs absolute, or via a symlink).
func normalize(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("trust: resolve %q: %w", path, err)
	}
	if resolved, err := filepath.EvalSymlinks(abs); err == nil {
		return resolved, nil
	}
	// EvalSymlinks fails when the path (or a parent) does not exist yet — fall
	// back to the plain absolute path so a not-yet-existing file still gets a
	// stable, reportable key; the subsequent read will surface the real error.
	return abs, nil
}

func hashAbs(abs string) (string, error) {
	data, err := os.ReadFile(abs)
	if err != nil {
		return "", fmt.Errorf("trust: hash %q: %w", abs, err)
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

// store is the on-disk trust store format: absolute path -> approved sha256.
type store struct {
	Version   int               `json:"version"`
	Approvals map[string]string `json:"approvals"`
}

// storePath returns <source.CacheDir()>/trust.json.
func storePath() (string, error) {
	dir, err := source.CacheDir()
	if err != nil {
		return "", fmt.Errorf("trust: store path: %w", err)
	}
	return filepath.Join(dir, storeFileName), nil
}

// loadStore reads the trust store, returning an empty store if it doesn't
// exist yet (nothing has been approved so far).
func loadStore() (*store, error) {
	path, err := storePath()
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return &store{Version: storeVersion, Approvals: map[string]string{}}, nil
		}
		return nil, fmt.Errorf("trust: read store %q: %w", path, err)
	}
	var s store
	if err := json.Unmarshal(data, &s); err != nil {
		return nil, fmt.Errorf("trust: parse store %q: %w", path, err)
	}
	if s.Approvals == nil {
		s.Approvals = map[string]string{}
	}
	return &s, nil
}

// saveStore writes s atomically — a temp file in the same directory is
// written first, then renamed into place, so a crash mid-write cannot leave a
// corrupt trust store. The store lives under the owner-only cache dir and the
// file itself is 0600: it is security-sensitive, since anyone who can write
// to it can grant themselves arbitrary command execution.
func saveStore(s *store) error {
	path, err := storePath()
	if err != nil {
		return err
	}
	if err := fsutil.EnsureOwnerOnlyDir(filepath.Dir(path)); err != nil {
		return fmt.Errorf("trust: ensure store dir: %w", err)
	}
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return fmt.Errorf("trust: encode store: %w", err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return fmt.Errorf("trust: write store: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("trust: rename store into place: %w", err)
	}
	return nil
}

// PathStatus reports the approval state of a single config path, used to
// render the `allow` subcommand's preview.
type PathStatus struct {
	Path         string
	CurrentHash  string
	ApprovedHash string // "" if never approved
	Approved     bool   // true when CurrentHash == ApprovedHash
}

// Status reports the current approval state of each non-empty path in
// paths. It fails outright if any path cannot be hashed (e.g. it does not
// exist) — that is a "no such file" error, not an approval decision.
func Status(paths []string) ([]PathStatus, error) {
	s, err := loadStore()
	if err != nil {
		return nil, err
	}
	out := make([]PathStatus, 0, len(paths))
	for _, p := range paths {
		if p == "" {
			continue
		}
		abs, err := normalize(p)
		if err != nil {
			return nil, err
		}
		hash, err := hashAbs(abs)
		if err != nil {
			return nil, err
		}
		approvedHash := s.Approvals[abs]
		out = append(out, PathStatus{
			Path:         abs,
			CurrentHash:  hash,
			ApprovedHash: approvedHash,
			Approved:     approvedHash == hash,
		})
	}
	return out, nil
}

// Check verifies that every non-empty path in paths has been approved at its
// current content hash. It returns a *NotApprovedError for the first path
// that is unapproved or has changed since approval. A path that cannot be
// read (e.g. it does not exist) fails with the underlying error instead —
// callers may want to handle that case differently (e.g. cmd/env-starter's
// "no config file found" hint on first run).
func Check(paths []string) error {
	statuses, err := Status(paths)
	if err != nil {
		return err
	}
	for _, st := range statuses {
		if st.Approved {
			continue
		}
		reason := ReasonUnknown
		if st.ApprovedHash != "" {
			reason = ReasonChanged
		}
		return &NotApprovedError{Path: st.Path, Reason: reason}
	}
	return nil
}

// Approve hashes each non-empty path in paths and records it as approved.
// Call this only after the operator has reviewed what the config will
// execute (see the `allow` subcommand) — Approve itself does not show or
// check anything, it just records trust.
func Approve(paths []string) error {
	s, err := loadStore()
	if err != nil {
		return err
	}
	for _, p := range paths {
		if p == "" {
			continue
		}
		abs, err := normalize(p)
		if err != nil {
			return err
		}
		hash, err := hashAbs(abs)
		if err != nil {
			return err
		}
		s.Approvals[abs] = hash
	}
	return saveStore(s)
}
