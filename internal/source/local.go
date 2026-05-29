package source

import (
	"context"
	"fmt"
	"os"
)

// Local is a Source that returns an existing local directory as-is, with no caching.
type Local struct {
	Path string
}

// Fetch verifies that Path exists and is a directory, then returns it.
func (l Local) Fetch(_ context.Context) (string, error) {
	info, err := os.Stat(l.Path)
	if err != nil {
		if os.IsNotExist(err) {
			return "", fmt.Errorf("local path does not exist: %s", l.Path)
		}
		return "", fmt.Errorf("cannot stat local path %s: %w", l.Path, err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("local path is not a directory: %s", l.Path)
	}
	return l.Path, nil
}
