package source

import (
	"fmt"
	"os"
	"path/filepath"
)

// baseCacheDir can be overridden in tests to avoid writing to the real OS cache.
var baseCacheDir = ""

// CacheDir returns the cache directory used by all source implementations:
// <os.UserCacheDir()>/env-starter, or the overridden base when set.
func CacheDir() (string, error) {
	base := baseCacheDir
	if base == "" {
		userCache, err := os.UserCacheDir()
		if err != nil {
			return "", fmt.Errorf("cannot determine user cache dir: %w", err)
		}
		base = userCache
	}
	return filepath.Join(base, "env-starter"), nil
}
