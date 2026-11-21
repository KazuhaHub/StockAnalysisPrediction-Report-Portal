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
	"math"
	"net"
	"os"
	"syscall"
)

// handleProxyProfile answers the PRPX0001 query. A runner built without egress
// does not answer at all, exactly like one that predates the proxy. The answer is
// the frozen 1-dev.1 profile whatever the runner's pins hold (doc 186).
func (s *Server) handleProxyProfile(ctx context.Context, conn *net.UnixConn) {
	if s.egress == nil || s.network == nil || ctx.Err() != nil {
		return
	}
	raw, err := encodeProxyProfile(NewProxyProfile())
	if err != nil {
		return
	}
	answerProxyQuery(conn, ProxyReplyMagic, raw)
}

// handleProxyPinsProfile answers the PRPX0002 query: the fixed profile and the operator's allowlist of pins.
func (s *Server) handleProxyPinsProfile(ctx context.Context, conn *net.UnixConn) {
	if s.egress == nil || s.network == nil || ctx.Err() != nil {
		return
	}
	profile, err := NewProxyPinsProfile(s.network.proxyPins)
	if err != nil {
		return
	}
	raw, err := encodeProxyPinsProfile(profile)
	if err != nil {
		return
	}
	answerProxyQuery(conn, ProxyPinsReplyMagic, raw)
}

// answerProxyQuery reads the 32-byte nonce and writes the reply magic, the nonce, a u32 length and raw.
func answerProxyQuery(conn *net.UnixConn, replyMagic string, raw []byte) {
	var nonce [32]byte
	if _, err := io.ReadFull(conn, nonce[:]); err != nil || networkStreamEOF(conn) != nil {
		return
	}
	if len(raw) == 0 || uint64(len(raw)) > math.MaxUint32 {
		return
	}
	reply := make([]byte, 44)
	copy(reply, replyMagic)
	copy(reply[8:40], nonce[:])
	binary.BigEndian.PutUint32(reply[40:], uint32(len(raw)))
	reply = append(reply, raw...)
	_, _ = io.Copy(conn, bytes.NewReader(reply))
}

// QueryControllerProxy asks the runner whether it serves the reviewed forward
// proxy, with the frozen 1-dev.1 query. ErrProxyUnsupported means it does not (an
// older runner closes the connection at the unknown query) or answered something
// else; Portal then puts no proxy policy into a session. Any other error is a
// failure to ask.
func QueryControllerProxy(ctx context.Context, path string, uid, gid uint32) (ProxyProfile, error) {
	if ctx == nil || !validController(uid, gid) || uint32(os.Geteuid()) != uid {
		return ProxyProfile{}, ErrUnsafeEndpoint
	}
	return queryProxyProfile(ctx, path, controllerProfile(gid))
}

// QueryControllerProxyState asks PRPX0002 first, as a Portal that knows pinned destinations does (doc 186). A
// runner that closes at the unknown magic predates pins: PRPX0001 is asked and the state says pins are not
// supported. Any other failure is returned as it is and is never read as "unsupported".
func QueryControllerProxyState(ctx context.Context, path string, uid, gid uint32) (ProxyRunnerState, error) {
	if ctx == nil || !validController(uid, gid) || uint32(os.Geteuid()) != uid {
		return ProxyRunnerState{}, ErrUnsafeEndpoint
	}
	return queryProxyState(ctx, func(ctx context.Context) (*net.UnixConn, error) {
		return connectEndpoint(ctx, path, controllerProfile(gid))
	})
}

func queryProxyProfile(parent context.Context, path string, profile endpointProfile) (ProxyProfile, error) {
	raw, err := proxyQuery(parent, func(ctx context.Context) (*net.UnixConn, error) {
		return connectEndpoint(ctx, path, profile)
	}, ProxyQueryMagic, ProxyReplyMagic, maxProxyProfileBytes)
	if err != nil {
		if errors.Is(err, errProxyQueryClosed) {
			return ProxyProfile{}, ErrProxyUnsupported
		}
		return ProxyProfile{}, err
	}
	return ParseProxyProfile(raw)
}

func queryProxyState(parent context.Context, dial func(context.Context) (*net.UnixConn, error)) (ProxyRunnerState, error) {
	raw, err := proxyQuery(parent, dial, ProxyPinsQueryMagic, ProxyPinsReplyMagic, maxProxyPinsProfileBytes)
	if err == nil {
		profile, err := ParseProxyPinsProfile(raw)
		if err != nil {
			return ProxyRunnerState{}, err
		}
		return ProxyRunnerState{Profile: profile.Fixed, PinsSupported: true, Pins: profile.Pins}, nil
	}
	if !errors.Is(err, errProxyQueryClosed) {
		return ProxyRunnerState{}, err
	}
	// The runner closed at the unknown magic: it predates pins. The 1-dev.1 query is frozen and answers.
	raw, err = proxyQuery(parent, dial, ProxyQueryMagic, ProxyReplyMagic, maxProxyProfileBytes)
	if err != nil {
		if errors.Is(err, errProxyQueryClosed) {
			return ProxyRunnerState{}, ErrProxyUnsupported
		}
		return ProxyRunnerState{}, err
	}
	profile, err := ParseProxyProfile(raw)
	if err != nil {
		return ProxyRunnerState{}, err
	}
	return ProxyRunnerState{Profile: profile}, nil
}

// errProxyQueryClosed is a runner closing the connection at a query magic it does not know (or does not answer).
var errProxyQueryClosed = errors.New("runner closed the proxy query")

// proxyQuery sends one query on a fresh connection and returns the raw reply body. A close or reset before the
// reply header is errProxyQueryClosed; a reply with the wrong magic, nonce or a size of zero or over max is
// ErrProxyUnsupported; anything else is a failure to ask.
func proxyQuery(parent context.Context, dial func(context.Context) (*net.UnixConn, error), queryMagic, replyMagic string, max uint32) ([]byte, error) {
	if parent == nil || parent.Err() != nil {
		return nil, ErrRefused
	}
	ctx, cancel := context.WithTimeout(parent, sessionTimeout)
	defer cancel()
	conn, err := dial(ctx)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	deadline, _ := ctx.Deadline()
	_ = conn.SetDeadline(deadline)
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()
	return proxyQueryExchange(conn, queryMagic, replyMagic, max)
}

// proxyQueryExchange runs the query on an open connection.
func proxyQueryExchange(conn *net.UnixConn, queryMagic, replyMagic string, max uint32) ([]byte, error) {
	var nonce [32]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return nil, ErrRefused
	}
	if _, err := io.Copy(conn, bytes.NewReader(append([]byte(queryMagic), nonce[:]...))); err != nil {
		return nil, ErrRefused
	}
	if conn.CloseWrite() != nil {
		return nil, ErrRefused
	}
	var header [44]byte
	if _, err := io.ReadFull(conn, header[:]); err != nil {
		// A runner that does not know the query reads only the eight magic bytes and closes with
		// the nonce still unread, which the kernel reports as a reset rather than a clean end.
		if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, syscall.ECONNRESET) {
			return nil, errProxyQueryClosed
		}
		return nil, fmt.Errorf("%w: %w", ErrRefused, err)
	}
	size := binary.BigEndian.Uint32(header[40:])
	if string(header[:8]) != replyMagic || !bytes.Equal(header[8:40], nonce[:]) || size == 0 || size > max {
		return nil, ErrProxyUnsupported
	}
	raw := make([]byte, int(size))
	if _, err := io.ReadFull(conn, raw); err != nil || networkStreamEOF(conn) != nil {
		return nil, ErrRefused
	}
	return raw, nil
}
