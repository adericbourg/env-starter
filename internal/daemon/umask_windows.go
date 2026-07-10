//go:build windows

package daemon

// restrictUmask is a no-op on Windows, which has no umask; socket access is
// governed by ACLs inherited from the owner-only cache directory.
func restrictUmask() func() {
	return func() {}
}
