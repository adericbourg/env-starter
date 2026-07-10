//go:build !windows

package daemon

import "syscall"

// restrictUmask temporarily sets the process umask to 0177 so files created
// until the returned restore function is called — in particular the unix
// socket created by net.Listen — are born owner-only instead of 0777&^umask.
// The returned function restores the previous umask; call it immediately
// after the file is created (the umask is process-wide).
func restrictUmask() func() {
	old := syscall.Umask(0o177)
	return func() { syscall.Umask(old) }
}
