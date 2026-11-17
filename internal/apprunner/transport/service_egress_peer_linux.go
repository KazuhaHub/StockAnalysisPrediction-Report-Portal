//go:build linux

package transport

import "net"

// EgressAppUID is the user every managed application container runs as
// (driver: User "65532:65532"). It is the only peer a broker socket serves.
const EgressAppUID = 65532

// egressPeerUID returns a connection check that accepts only a unix-socket peer
// whose kernel-reported uid (SO_PEERCRED) is uid. The socket path and directory
// identity are the primary boundary; this is defence in depth, so that a process
// of another user that somehow reached the 0666 socket is still refused.
func egressPeerUID(uid uint32) func(net.Conn) bool {
	return func(c net.Conn) bool {
		unix, ok := c.(*net.UnixConn)
		return ok && peerUID(unix, uid)
	}
}
