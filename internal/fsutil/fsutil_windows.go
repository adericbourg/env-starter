//go:build windows

package fsutil

import "os"

// enforceOwnerOnly is a no-op on Windows: Unix permission bits are not
// meaningful there, and directories under the user profile inherit ACLs that
// already restrict access to the owner.
func enforceOwnerOnly(_ string, _ os.FileInfo) error {
	return nil
}
