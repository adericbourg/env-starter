// Package source provides implementations of the Source interface that fetch
// a working directory from a local path, a GitHub repository, or a URL.
package source

import "context"

// Source returns the local directory a command should run in.
type Source interface {
	Fetch(ctx context.Context) (dir string, err error)
}
