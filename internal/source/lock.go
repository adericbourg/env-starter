package source

import "sync"

// pathLocks serializes Fetch operations targeting the same cache directory so
// two commands that share a repo+ref (or URL) never clone/download into the
// same dir concurrently. Distinct dirs lock independently and run in parallel.
var pathLocks sync.Map // map[string]*sync.Mutex

// lockPath blocks until the caller holds the lock for dir and returns the
// unlock function. Typical usage: defer lockPath(dir)().
func lockPath(dir string) func() {
	actual, _ := pathLocks.LoadOrStore(dir, &sync.Mutex{})
	mu := actual.(*sync.Mutex)
	mu.Lock()
	return mu.Unlock
}
