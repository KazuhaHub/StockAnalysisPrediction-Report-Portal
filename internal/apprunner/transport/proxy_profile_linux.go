//go:build linux

package transport

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"syscall"
)

// handleProxyProfile answers the PRPX0001 query. A runner built without egress
// does not answer at all, exactly like one that predates the proxy.
func (s *Server) handleProxyProfile(ctx context.Context, conn *net.UnixConn) {
	if s.egress == nil || s.network == nil || ctx.Err() != nil {
		return
	}
	var nonce [32]byte
	if _, err := io.ReadFull(conn, nonce[:]); err != nil || networkStreamEOF(conn) != nil {
		return
	}
	raw, err := encodeProxyProfile(NewProxyProfile())
	if err != nil {
		return
	}
	reply := make([]byte, 44)
	copy(reply, ProxyReplyMagic)
	copy(reply[8:40], nonce[:])
	binary.BigEndian.PutUint32(reply[40:], uint32(len(raw)))
	reply = append(reply, raw...)
	_, _ = io.Copy(conn, bytes.NewReader(reply))
}

// QueryControllerProxy asks the runner whether it serves the reviewed forward
// proxy. ErrProxyUnsupported means it does not (an older runner closes the
// connection at the unknown query) or answered something else; Portal then puts
// no proxy policy into a session. Any other error is a failure to ask.
func QueryControllerProxy(ctx context.Context, path string, uid, gid uint32) (ProxyProfile, error) {
	if ctx == nil || !validController(uid, gid) || uint32(os.Geteuid()) != uid {
		return ProxyProfile{}, ErrUnsafeEndpoint
	}
	return queryProxyProfile(ctx, path, controllerProfile(gid))
}

func queryProxyProfile(parent context.Context, path string, profile endpointProfile) (ProxyProfile, error) {
	var zero ProxyProfile
	if parent == nil || parent.Err() != nil {
		return zero, ErrRefused
	}
	ctx, cancel := context.WithTimeout(parent, sessionTimeout)
	defer cancel()
	conn, err := connectEndpoint(ctx, path, profile)
	if err != nil {
		return zero, err
	}
	defer conn.Close()
	deadline, _ := ctx.Deadline()
	_ = conn.SetDeadline(deadline)
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()
	var nonce [32]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return zero, ErrRefused
	}
	if _, err := io.Copy(conn, bytes.NewReader(append([]byte(ProxyQueryMagic), nonce[:]...))); err != nil {
		return zero, ErrRefused
	}
	if conn.CloseWrite() != nil {
		return zero, ErrRefused
	}
	var header [44]byte
	if _, err := io.ReadFull(conn, header[:]); err != nil {
		// A runner that does not know the query reads only the eight magic bytes and closes with
		// the nonce still unread, which the kernel reports as a reset rather than a clean end.
		if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, syscall.ECONNRESET) {
			return zero, ErrProxyUnsupported
		}
		return zero, fmt.Errorf("%w: %w", ErrRefused, err)
	}
	size := binary.BigEndian.Uint32(header[40:])
	if string(header[:8]) != ProxyReplyMagic || !bytes.Equal(header[8:40], nonce[:]) || size == 0 || size > maxProxyProfileBytes {
		return zero, ErrProxyUnsupported
	}
	raw := make([]byte, int(size))
	if _, err := io.ReadFull(conn, raw); err != nil || networkStreamEOF(conn) != nil {
		return zero, ErrRefused
	}
	return ParseProxyProfile(raw)
}
