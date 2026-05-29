// Package openfile provides a cross-platform helper to open a file using
// the operating system's default application.
package openfile

import (
	"fmt"
	"os"
	"os/exec"
	"runtime"
)

// Open opens path using the OS default application. The call is non-blocking:
// it launches the opener and returns immediately, leaving the process running
// in the background. An error is returned if the file cannot be stat'd (i.e. it
// does not exist yet) or if the opener process cannot be started.
func Open(path string) error {
	if _, err := os.Stat(path); err != nil {
		return fmt.Errorf("log file not available: %w", err)
	}
	name, args := command(runtime.GOOS, path)
	return exec.Command(name, args...).Start()
}

// command returns the OS-appropriate executable and arguments to open path
// in the system default application.
func command(goos, path string) (string, []string) {
	switch goos {
	case "darwin":
		return "open", []string{path}
	case "windows":
		// cmd /c start "" <path> — the empty title arg prevents start from
		// misinterpreting the first quoted string as a window title.
		return "cmd", []string{"/c", "start", "", path}
	default:
		return "xdg-open", []string{path}
	}
}
