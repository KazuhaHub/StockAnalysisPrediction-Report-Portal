//go:build !linux

package driver

import "net"

// verify refuses: the peer of a connection is identified with sock_diag and /proc, which only Linux has.
func (*daemonPeer) verify(net.Conn) error { return ErrUnsupported }
