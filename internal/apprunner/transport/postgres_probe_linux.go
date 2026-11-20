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

// handlePostgresProbeQuery answers the capability query. A runner built without the probe does not
// answer at all, exactly like one that predates it.
func (s *Server) handlePostgresProbeQuery(ctx context.Context, conn *net.UnixConn) {
	if s.network == nil || s.network.postgres == nil || ctx.Err() != nil {
		return
	}
	var nonce [32]byte
	if _, err := io.ReadFull(conn, nonce[:]); err != nil || networkStreamEOF(conn) != nil {
		return
	}
	raw, err := EncodePostgresProbeProfile(NewPostgresProbeProfile())
	if err != nil {
		return
	}
	reply := make([]byte, 44)
	copy(reply, PostgresProbeReplyMagic)
	copy(reply[8:40], nonce[:])
	binary.BigEndian.PutUint32(reply[40:], uint32(len(raw)))
	reply = append(reply, raw...)
	_, _ = io.Copy(conn, bytes.NewReader(reply))
}

// QueryControllerPostgresProbe asks the runner whether it serves the PostgreSQL probe.
// ErrPostgresProbeUnsupported means it does not (an older runner closes the connection at the
// unknown query) or answered something other than the closed format; any other error is a failure
// to ask, which says nothing about the runner.
func QueryControllerPostgresProbe(ctx context.Context, path string, uid, gid uint32) (PostgresProbeProfile, error) {
	if ctx == nil || !validController(uid, gid) || uint32(os.Geteuid()) != uid {
		return PostgresProbeProfile{}, ErrUnsafeEndpoint
	}
	return queryPostgresProbe(ctx, path, controllerProfile(gid))
}

func queryPostgresProbe(parent context.Context, path string, profile endpointProfile) (PostgresProbeProfile, error) {
	var zero PostgresProbeProfile
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
	if _, err := io.Copy(conn, bytes.NewReader(append([]byte(PostgresProbeQueryMagic), nonce[:]...))); err != nil {
		return zero, ErrRefused
	}
	if conn.CloseWrite() != nil {
		return zero, ErrRefused
	}
	var header [44]byte
	if _, err := io.ReadFull(conn, header[:]); err != nil {
		// A runner that does not know the query reads only the eight magic bytes and closes with the
		// nonce still unread, which the kernel reports as a reset rather than a clean end of stream.
		if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, syscall.ECONNRESET) {
			return zero, ErrPostgresProbeUnsupported
		}
		return zero, fmt.Errorf("%w: %w", ErrRefused, err)
	}
	size := binary.BigEndian.Uint32(header[40:])
	if string(header[:8]) != PostgresProbeReplyMagic || !bytes.Equal(header[8:40], nonce[:]) || size == 0 || size > maxPostgresProbeProfile {
		return zero, ErrPostgresProbeUnsupported
	}
	raw := make([]byte, int(size))
	if _, err := io.ReadFull(conn, raw); err != nil || networkStreamEOF(conn) != nil {
		return zero, ErrRefused
	}
	return ParsePostgresProbeProfile(raw)
}
