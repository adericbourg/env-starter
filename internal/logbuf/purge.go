package logbuf

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// LogRetention is how long log files are kept before being purged (~1 month).
const LogRetention = 30 * 24 * time.Hour

// PurgeOlderThan removes *.log files in dir whose modification time is older
// than maxAge. A missing directory is not an error. Directories and non-.log
// files are left alone. Returns the base names of removed files. Individual
// removal errors are aggregated and returned together, but do not stop the
// iteration.
func PurgeOlderThan(dir string, maxAge time.Duration) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}

	var removed []string
	var errs []error

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		if !strings.HasSuffix(entry.Name(), ".log") {
			continue
		}

		info, err := entry.Info()
		if err != nil {
			errs = append(errs, err)
			continue
		}

		if time.Since(info.ModTime()) <= maxAge {
			continue
		}

		if err := os.Remove(filepath.Join(dir, entry.Name())); err != nil && !errors.Is(err, os.ErrNotExist) {
			errs = append(errs, err)
			continue
		}

		removed = append(removed, entry.Name())
	}

	return removed, errors.Join(errs...)
}
