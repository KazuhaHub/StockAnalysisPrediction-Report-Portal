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

// handleMachineSupport answers the capability query. It needs no provider: forwarding a machine
// assertion is part of the backend request path every runner build of this version has.
func (s *Server) handleMachineSupport(ctx context.Context, conn *net.UnixConn) {
	if ctx.Err() != nil {
		return
	}
	var nonce [32]byte
	if _, err := io.ReadFull(conn, nonce[:]); err != nil || networkStreamEOF(conn) != nil {
		return
	}
	raw, err := EncodeMachineSupport(NewMachineSupport())
	if err != nil {
		return
	}
	reply := make([]byte, 44)
	copy(reply, MachineSupportReplyMagic)
	copy(reply[8:40], nonce[:])
	binary.BigEndian.PutUint32(reply[40:], uint32(len(raw)))
	reply = append(reply, raw...)
	_, _ = io.Copy(conn, bytes.NewReader(reply))
}

// QueryControllerMachineSupport asks the runner whether it forwards machine assertions.
// ErrMachineSupportUnsupported means it does not answer (an older runner closes the connection at the
// unknown query) or answered something other than the closed format: the capability must not be
// offered against it. Any other error is a failure to ask. As with the network targets query, a clean
// close or reset before any reply byte is repeated once on a fresh connection before it is concluded
// unsupported; a malformed or wrong reply is a protocol answer and is not retried.
func QueryControllerMachineSupport(ctx context.Context, path string, uid, gid uint32) (MachineSupport, error) {
	if ctx == nil || !validController(uid, gid) || uint32(os.Geteuid()) != uid {
		return MachineSupport{}, ErrUnsafeEndpoint
	}
	return queryMachineSupport(ctx, path, controllerProfile(gid))
}

// machineSupportRetryDelay separates the two attempts; tests shorten it.
var machineSupportRetryDelay = 200 * time.Millisecond

var errMachineSupportDropped = errors.New("machine support query dropped before any reply")

func queryMachineSupport(parent context.Context, path string, profile endpointProfile) (MachineSupport, error) {
	got, err := queryMachineSupportOnce(parent, path, profile)
	if !errors.Is(err, errMachineSupportDropped) {
		return got, err
	}
	select {
	case <-time.After(machineSupportRetryDelay):
	case <-parent.Done():
		return MachineSupport{}, ErrRefused
	}
	got, err = queryMachineSupportOnce(parent, path, profile)
	if errors.Is(err, errMachineSupportDropped) {
		return MachineSupport{}, ErrMachineSupportUnsupported
	}
	return got, err
}

func queryMachineSupportOnce(parent context.Context, path string, profile endpointProfile) (MachineSupport, error) {
	var zero MachineSupport
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
	if _, err := io.Copy(conn, bytes.NewReader(append([]byte(MachineSupportQueryMagic), nonce[:]...))); err != nil {
		return zero, ErrRefused
	}
	if conn.CloseWrite() != nil {
		return zero, ErrRefused
	}
	var header [44]byte
	if _, err := io.ReadFull(conn, header[:]); err != nil {
		// A runner that does not know the query reads only the magic and closes with the nonce
		// unread, which the kernel reports as a reset rather than a clean end of stream.
		if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, syscall.ECONNRESET) {
			return zero, errMachineSupportDropped
		}
		return zero, fmt.Errorf("%w: %w", ErrRefused, err)
	}
	size := binary.BigEndian.Uint32(header[40:])
	if string(header[:8]) != MachineSupportReplyMagic || !bytes.Equal(header[8:40], nonce[:]) || size == 0 || size > maxMachineSupportBytes {
		return zero, ErrMachineSupportUnsupported
	}
	raw := make([]byte, int(size))
	if _, err := io.ReadFull(conn, raw); err != nil || networkStreamEOF(conn) != nil {
		return zero, ErrMachineSupportUnsupported
	}
	return ParseMachineSupport(raw)
}
