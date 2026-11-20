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
	"time"
)

// handleNetworkTargets answers the capability query. A runner built without a
// network provider does not answer at all, exactly like one that predates it.
func (s *Server) handleNetworkTargets(ctx context.Context, conn *net.UnixConn) {
	if s.network == nil || ctx.Err() != nil {
		return
	}
	var nonce [32]byte
	if _, err := io.ReadFull(conn, nonce[:]); err != nil || networkStreamEOF(conn) != nil {
		return
	}
	raw, err := EncodeNetworkTargets(s.network.targets)
	if err != nil {
		return
	}
	reply := make([]byte, 44)
	copy(reply, NetworkTargetsReplyMagic)
	copy(reply[8:40], nonce[:])
	binary.BigEndian.PutUint32(reply[40:], uint32(len(raw)))
	reply = append(reply, raw...)
	_, _ = io.Copy(conn, bytes.NewReader(reply))
}

// QueryControllerNetworkTargets asks the runner which policy formats it serves
// and which private networks its operator allowed. ErrNetworkTargetsUnsupported
// means it does not answer (an older runner closes the connection at the unknown
// query) or answered something other than the closed format: no v2 policy may be
// granted against it. Any other error is a failure to ask.
//
// A clean close or reset before any reply byte is how an old runner answers, but
// a busy or restarting new runner can look the same on one connection. So the
// query is repeated once on a fresh connection after networkTargetsRetryDelay,
// and only a second such close is concluded unsupported. A malformed or wrong
// reply is a protocol answer and is not retried.
func QueryControllerNetworkTargets(ctx context.Context, path string, uid, gid uint32) (NetworkTargets, error) {
	if ctx == nil || !validController(uid, gid) || uint32(os.Geteuid()) != uid {
		return NetworkTargets{}, ErrUnsafeEndpoint
	}
	return queryNetworkTargets(ctx, path, controllerProfile(gid))
}

// networkTargetsRetryDelay separates the two attempts; tests shorten it.
var networkTargetsRetryDelay = 200 * time.Millisecond

var errNetworkTargetsDropped = errors.New("network targets query dropped before any reply")

func queryNetworkTargets(parent context.Context, path string, profile endpointProfile) (NetworkTargets, error) {
	got, err := queryNetworkTargetsOnce(parent, path, profile)
	if !errors.Is(err, errNetworkTargetsDropped) {
		return got, err
	}
	select {
	case <-time.After(networkTargetsRetryDelay):
	case <-parent.Done():
		return NetworkTargets{}, ErrRefused
	}
	got, err = queryNetworkTargetsOnce(parent, path, profile)
	if errors.Is(err, errNetworkTargetsDropped) {
		return NetworkTargets{}, ErrNetworkTargetsUnsupported
	}
	return got, err
}

func queryNetworkTargetsOnce(parent context.Context, path string, profile endpointProfile) (NetworkTargets, error) {
	var zero NetworkTargets
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
	if _, err := io.Copy(conn, bytes.NewReader(append([]byte(NetworkTargetsQueryMagic), nonce[:]...))); err != nil {
		return zero, ErrRefused
	}
	if conn.CloseWrite() != nil {
		return zero, ErrRefused
	}
	var header [44]byte
	if _, err := io.ReadFull(conn, header[:]); err != nil {
		// A drop before a complete reply header (nothing at all, or a partial
		// header) is retried by the caller and only a second one is concluded
		// unsupported. A runner that does not know the query reads only the eight
		// magic bytes and closes with the nonce still unread, which the kernel
		// reports as a reset rather than a clean end of stream.
		if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, syscall.ECONNRESET) {
			return zero, errNetworkTargetsDropped
		}
		return zero, fmt.Errorf("%w: %w", ErrRefused, err)
	}
	size := binary.BigEndian.Uint32(header[40:])
	if string(header[:8]) != NetworkTargetsReplyMagic || !bytes.Equal(header[8:40], nonce[:]) || size == 0 || size > maxNetworkTargetsBytes {
		return zero, ErrNetworkTargetsUnsupported
	}
	raw := make([]byte, int(size))
	if _, err := io.ReadFull(conn, raw); err != nil || networkStreamEOF(conn) != nil {
		return zero, ErrNetworkTargetsUnsupported
	}
	return ParseNetworkTargets(raw)
}
