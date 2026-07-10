package source

import (
	"fmt"
	"os"
	"sync"

	"github.com/adericbourg/env-starter/internal/fsutil"
)

// pathLocks serializes Fetch operations targeting the same cache directory so
// two commands that share a repo+ref (or URL) never clone/download into the
// same dir concurrently. Distinct dirs lock independently and run in parallel.
// This only guards against races within one process; lockPath layers a
// cross-process advisory file lock on top for the (currently theoretical,
// since all fetching happens inside the single daemon) case of two processes
// sharing the cache.
var pathLocks sync.Map // map[string]*sync.Mutex

// lockPath blocks until the caller holds the lock for dir, both within this
// process (an in-memory mutex) and across processes sharing the OS cache (an
// advisory lock on "<dir>.lock", a sibling of dir in the cache root — never
// inside it, since dir itself is created and removed by clone/download logic).
// The caller must have already created filepath.Dir(dir).
//
// Typical usage:
//
//	unlock, err := lockPath(dir)
//	if err != nil {
//		return "", err
//	}
//	defer unlock()
func lockPath(dir string) (func(), error) {
	lockFile := dir + ".lock"

	actual, _ := pathLocks.LoadOrStore(lockFile, &sync.Mutex{})
	mu := actual.(*sync.Mutex)
	mu.Lock()

	f, err := os.OpenFile(lockFile, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		mu.Unlock()
		return nil, fmt.Errorf("open cache lock %s: %w", lockFile, err)
	}
	if err := fsutil.LockFileExclusive(f); err != nil {
		_ = f.Close()
		mu.Unlock()
		return nil, fmt.Errorf("acquire cache lock %s: %w", lockFile, err)
	}

	return func() {
		_ = fsutil.UnlockFile(f)
		_ = f.Close()
		mu.Unlock()
	}, nil
}
