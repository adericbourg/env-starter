//go:build windows

package update

import (
	"fmt"
	"os"
	"os/exec"
)

// ReExec starts a new process from the freshly installed binary (inheriting
// stdin/stdout/stderr and all original arguments) and exits the current process.
// The new process takes over the terminal session.
func ReExec() error {
	self, err := os.Executable()
	if err != nil {
		return fmt.Errorf("resolving executable path: %w", err)
	}
	cmd := exec.Command(self, os.Args[1:]...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("starting updated binary: %w", err)
	}
	os.Exit(0)
	return nil // unreachable
}
