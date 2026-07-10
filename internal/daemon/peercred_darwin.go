//go:build darwin

package daemon

import (
	"fmt"
	"net"

	"golang.org/x/sys/unix"
)

// checkPeer verifies that the process on the other end of the unix socket
// runs as the same user as the daemon (LOCAL_PEERCRED). The socket's 0600
// mode and 0700 parent dir are the primary barrier; this check is defence in
// depth so a misconfigured directory alone is never enough to drive the
// daemon — any connected peer can start the owner's configured commands.
func checkPeer(conn net.Conn) error {
	uc, ok := conn.(*net.UnixConn)
	if !ok {
		return fmt.Errorf("unexpected connection type %T", conn)
	}
	raw, err := uc.SyscallConn()
	if err != nil {
		return fmt.Errorf("peer credentials: %w", err)
	}
	var cred *unix.Xucred
	var credErr error
	if err := raw.Control(func(fd uintptr) {
		cred, credErr = unix.GetsockoptXucred(int(fd), unix.SOL_LOCAL, unix.LOCAL_PEERCRED)
	}); err != nil {
		return fmt.Errorf("peer credentials: %w", err)
	}
	if credErr != nil {
		return fmt.Errorf("peer credentials: %w", credErr)
	}
	return checkPeerUID(int(cred.Uid))
}
