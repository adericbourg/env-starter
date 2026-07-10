// Package fsutil provides small filesystem helpers shared by the daemon,
// the source cache, and the log writer.
package fsutil

import (
	"fmt"
	"os"
)

// EnsureOwnerOnlyDir creates dir (and any parents) and guarantees that dir
// itself is private to the current user (mode 0700). Unlike a bare
// os.MkdirAll, it also tightens the permissions of a pre-existing directory —
// MkdirAll applies its mode only on creation — so callers can rely on the
// owner-only guarantee even when the directory already existed with a looser
// mode (old version, restored backup, permissive umask). On Unix it
// additionally fails when the directory is owned by another user (e.g.
// pre-created on a shared cache location) rather than silently trusting its
// contents.
func EnsureOwnerOnlyDir(dir string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	info, err := os.Stat(dir)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("%s: not a directory", dir)
	}
	return enforceOwnerOnly(dir, info)
}
