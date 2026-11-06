//go:build !linux

package driver

import (
	"context"
	"net"
)

func dockerDialer(string) func(context.Context, string, string) (net.Conn, error) {
	return func(context.Context, string, string) (net.Conn, error) { return nil, ErrUnsupported }
}
