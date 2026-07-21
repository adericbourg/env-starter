package daemon

import (
	"os"
	"testing"
)

// socketTempDir returns a directory for holding a unix socket in tests, rooted
// directly under /tmp. t.TempDir() lives under $TMPDIR, which on macOS is a long
// /var/folders/... path that overflows sockaddr_un's ~104-byte sun_path limit and
// fails net.Listen/net.Dial with "bind: invalid argument". The dir is removed when
// the test finishes. (Same rationale as the WORK dir in e2e/e2e.sh.)
func socketTempDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "es")
	if err != nil {
		t.Fatalf("create socket temp dir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}
