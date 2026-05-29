package source

import (
	"os"
)

// writeFile is a small helper used by multiple test files to create a file
// with given contents.
func writeFile(path string, data []byte) error {
	return os.WriteFile(path, data, 0o600)
}

// makeDir creates a directory with the given permissions.
func makeDir(path string, perm os.FileMode) error {
	return os.MkdirAll(path, perm)
}
