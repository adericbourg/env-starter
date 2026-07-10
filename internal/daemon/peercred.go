package daemon

import (
	"fmt"
	"os"
)

// checkPeerUID accepts a peer only when it runs as the same uid as the
// daemon. Kept separate from the platform-specific credential lookup so the
// policy is unit-testable everywhere.
func checkPeerUID(uid int) error {
	if uid != os.Getuid() {
		return fmt.Errorf("rejected connection from uid %d (daemon runs as uid %d)", uid, os.Getuid())
	}
	return nil
}
