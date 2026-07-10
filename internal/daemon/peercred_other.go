//go:build !linux && !darwin

package daemon

import "net"

// checkPeer is a no-op on platforms without a peer-credential API for unix
// sockets (e.g. Windows); access control relies on the owner-only socket
// directory alone there.
func checkPeer(_ net.Conn) error {
	return nil
}
