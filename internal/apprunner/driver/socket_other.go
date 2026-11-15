//go:build !linux

package driver

import (
	"context"
	"net"
)

func dockerDialer(string, ...*daemonPeer) func(context.Context, string, string) (net.Conn, error) {
	return func(context.Context, string, string) (net.Conn, error) { return nil, ErrUnsupported }
}
