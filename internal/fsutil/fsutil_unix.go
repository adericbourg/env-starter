//go:build !windows

package fsutil

import (
	"fmt"
	"os"
	"syscall"
)

// enforceOwnerOnly rejects a directory owned by another user and chmods a
// too-permissive one down to 0700. Ownership is checked first: chmod'ing a
// foreign directory would either fail or, worse, succeed without making its
// existing contents trustworthy.
func enforceOwnerOnly(dir string, info os.FileInfo) error {
	if st, ok := info.Sys().(*syscall.Stat_t); ok {
		if int(st.Uid) != os.Getuid() {
			return fmt.Errorf("%s: owned by uid %d, not the current user (uid %d); refusing to use it", dir, st.Uid, os.Getuid())
		}
	}
	if info.Mode().Perm() != 0o700 {
		// 0700, not 0600: directories need the execute bit for the owner to
		// traverse them; gosec's 0600-or-less rule targets regular files.
		if err := os.Chmod(dir, 0o700); err != nil { //nolint:gosec
			return fmt.Errorf("%s: tighten permissions to 0700: %w", dir, err)
		}
	}
	return nil
}
