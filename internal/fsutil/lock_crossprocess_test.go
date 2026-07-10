package fsutil

import (
	"bufio"
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// TestLockFileExclusive_crossProcess_blocksSecondProcess proves that
// LockFileExclusive is a real cross-process lock, not just an in-process
// mutex: a second process attempting to acquire the same lock file blocks
// until the first process releases it.
//
// It re-executes the test binary as a helper process (see
// TestHelperProcess_acquireAndHoldLock below) that acquires the lock, signals
// readiness over stdout, waits for a line on stdin, then releases and exits.
func TestLockFileExclusive_crossProcess_blocksSecondProcess(t *testing.T) {
	t.Helper()

	lockPath := filepath.Join(t.TempDir(), "cross-process.lock")

	cmd := exec.Command(os.Args[0], "-test.run=^TestHelperProcess_acquireAndHoldLock$")
	cmd.Env = append(os.Environ(),
		"FSUTIL_LOCK_HELPER=1",
		"FSUTIL_LOCK_PATH="+lockPath,
	)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatalf("StdinPipe: %v", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("StdoutPipe: %v", err)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	if err := cmd.Start(); err != nil {
		t.Fatalf("start helper process: %v", err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	})

	// Wait for the helper to signal that it holds the lock.
	scanner := bufio.NewScanner(stdout)
	if !scanner.Scan() {
		t.Fatalf("helper process did not signal readiness; stderr: %s", stderr.String())
	}
	if line := scanner.Text(); line != "ready" {
		t.Fatalf("unexpected readiness line %q; stderr: %s", line, stderr.String())
	}

	// The helper now holds the lock. Acquiring it ourselves must not succeed
	// until we tell the helper to release it.
	f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatalf("open lock file: %v", err)
	}
	defer f.Close()

	acquired := make(chan error, 1)
	go func() { acquired <- LockFileExclusive(f) }()

	select {
	case err := <-acquired:
		t.Fatalf("acquired the lock while the helper process still holds it (err=%v)", err)
	case <-time.After(200 * time.Millisecond):
		// Expected: still blocked by the helper's lock.
	}

	// Tell the helper to release and exit.
	if _, err := fmt.Fprintln(stdin, "release"); err != nil {
		t.Fatalf("signal helper to release: %v", err)
	}

	select {
	case err := <-acquired:
		if err != nil {
			t.Fatalf("LockFileExclusive after helper release: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting to acquire the lock after the helper released it")
	}
	_ = UnlockFile(f)

	if err := cmd.Wait(); err != nil {
		t.Fatalf("helper process exited with error: %v; stderr: %s", err, stderr.String())
	}
}

// TestHelperProcess_acquireAndHoldLock is not a real test on its own — it
// skips immediately unless FSUTIL_LOCK_HELPER=1 is set. It is re-executed as
// a subprocess by TestLockFileExclusive_crossProcess_blocksSecondProcess to
// hold an exclusive lock on FSUTIL_LOCK_PATH so the parent process can verify
// that its own acquire attempt blocks.
func TestHelperProcess_acquireAndHoldLock(t *testing.T) {
	if os.Getenv("FSUTIL_LOCK_HELPER") != "1" {
		t.Skip("not invoked as a lock helper process")
	}

	lockPath := os.Getenv("FSUTIL_LOCK_PATH")
	f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatalf("open lock file: %v", err)
	}
	defer f.Close()

	if err := LockFileExclusive(f); err != nil {
		t.Fatalf("LockFileExclusive: %v", err)
	}
	defer UnlockFile(f) //nolint:errcheck

	fmt.Println("ready")

	// Block until the parent signals us to release the lock.
	scanner := bufio.NewScanner(os.Stdin)
	scanner.Scan()
}
