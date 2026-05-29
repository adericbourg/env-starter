//go:build !windows

package update

import (
	"fmt"
	"os"
	"syscall"
)

// ReExec replaces the current process image with the freshly installed binary
// using execve(2). On success this function never returns; any error it does
// return means the exec failed before the process image was replaced.
func ReExec() error {
	self, err := os.Executable()
	if err != nil {
		return fmt.Errorf("resolving executable path: %w", err)
	}
	return syscall.Exec(self, os.Args, os.Environ())
}
