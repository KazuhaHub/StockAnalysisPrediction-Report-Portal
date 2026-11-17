//go:build linux

package transport

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"syscall"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/control"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/driver"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/journal"
)

// egressOpenTimeout bounds reading an open frame from the control endpoint.
const egressOpenTimeout = 5 * time.Second

// egressBindTimeout bounds verifying and binding one session.
const egressBindTimeout = 30 * time.Second

// newJournalEgressBinder is the production EgressBinder: the journal checks the
// session against its own rows and the native runtime checks the running
// container and binds the broker socket; the listener it returns serves only the
// application's own user.
func newJournalEgressBinder(store *journal.Store, native *driver.CandidateRuntime) EgressBinder {
	return func(ctx context.Context, _ control.VerifiedServiceEgressSession, open EgressOpen, listen bool) (EgressListener, error) {
		broker, err := store.OpenServiceEgressBinding(ctx, open.Raw, open.Signature, open.Descriptor, native, listen)
		if err != nil || !listen {
			return EgressListener{}, err
		}
		return EgressListener{Listener: broker.Listener, PeerOK: egressPeerUID(EgressAppUID), Intact: broker.Intact}, nil
	}
}

// handleServiceEgressOpen reads an open frame and hands the connection to the
// registry. It reports whether the connection was handed off: from then on the
// registry owns it, and the caller must neither close it nor keep holding a
// control-endpoint slot for it.
func (s *Server) handleServiceEgressOpen(ctx context.Context, conn *net.UnixConn) (detached bool) {
	if s.egress == nil || ctx.Err() != nil {
		return false
	}
	_ = conn.SetReadDeadline(time.Now().Add(egressOpenTimeout))
	open, err := ReadEgressOpen(conn, true)
	if err != nil {
		return false
	}
	_ = conn.SetDeadline(time.Time{})
	// open owns conn from here, and closes it when it refuses. Verifying and
	// binding are bounded, so a stuck native call cannot hold an endpoint slot.
	bound, cancel := context.WithTimeout(ctx, egressBindTimeout)
	defer cancel()
	_ = s.egress.open(bound, conn, open)
	return true
}

// handleServiceEgressProfile answers the capability query. A runner built
// without egress does not answer at all, exactly like one that predates it.
func (s *Server) handleServiceEgressProfile(ctx context.Context, conn *net.UnixConn) {
	if s.egress == nil || s.network == nil {
		return
	}
	var nonce [32]byte
	if _, err := io.ReadFull(conn, nonce[:]); err != nil || networkStreamEOF(conn) != nil {
		return
	}
	raw, err := encodeServiceEgressProfile(NewServiceEgressProfile(s.network.activeLimit))
	if err != nil {
		return
	}
	reply := make([]byte, 44)
	copy(reply, ServiceEgressProfileReplyMagic)
	copy(reply[8:40], nonce[:])
	binary.BigEndian.PutUint32(reply[40:], uint32(len(raw)))
	reply = append(reply, raw...)
	_, _ = io.Copy(conn, bytes.NewReader(reply))
}

// QueryControllerServiceEgress asks the runner whether it hosts egress sessions
// and with what capacity. ErrServiceEgressUnsupported means it does not (an older
// runner closes the connection at the unknown query); any other error is a
// failure to ask, which says nothing about the runner.
func QueryControllerServiceEgress(ctx context.Context, path string, uid, gid uint32) (ServiceEgressProfile, error) {
	if ctx == nil || !validController(uid, gid) || uint32(os.Geteuid()) != uid {
		return ServiceEgressProfile{}, ErrUnsafeEndpoint
	}
	return queryServiceEgress(ctx, path, controllerProfile(gid))
}

func queryServiceEgress(parent context.Context, path string, profile endpointProfile) (ServiceEgressProfile, error) {
	var zero ServiceEgressProfile
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
	if _, err := io.Copy(conn, bytes.NewReader(append([]byte(ServiceEgressProfileQueryMagic), nonce[:]...))); err != nil {
		return zero, ErrRefused
	}
	if conn.CloseWrite() != nil {
		return zero, ErrRefused
	}
	var header [44]byte
	if _, err := io.ReadFull(conn, header[:]); err != nil {
		// A runner that does not know the query reads only the eight magic bytes and
		// closes with the nonce still unread, which the kernel reports as a reset
		// rather than a clean end of stream.
		if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, syscall.ECONNRESET) {
			return zero, ErrServiceEgressUnsupported
		}
		return zero, fmt.Errorf("%w: %w", ErrRefused, err)
	}
	size := binary.BigEndian.Uint32(header[40:])
	if string(header[:8]) != ServiceEgressProfileReplyMagic || !bytes.Equal(header[8:40], nonce[:]) || size == 0 || size > maxServiceEgressProfileBytes {
		return zero, ErrServiceEgressUnsupported
	}
	raw := make([]byte, int(size))
	if _, err := io.ReadFull(conn, raw); err != nil || networkStreamEOF(conn) != nil {
		return zero, ErrRefused
	}
	return ParseServiceEgressProfile(raw)
}

// OpenControllerServiceEgress sends one signed session command and the
// release's descriptor to the runner and, once the runner acknowledges it,
// returns the multiplexed session. The caller runs the returned mux (Run) and
// owns it: closing it ends the session. The acknowledgement is bound to the exact
// bytes sent, and the command is verified here first, so a command the runner
// would refuse for a reason knowable locally is never sent.
//
// ErrRefused means the runner refused or could not open the session (a stale or
// drifted command, a container that is not the registered one, no capacity).
func OpenControllerServiceEgress(ctx context.Context, path string, uid, gid uint32, raw, signature []byte, authority control.Authority, descriptor []byte) (*EgressMux, error) {
	if ctx == nil || !validController(uid, gid) || uint32(os.Geteuid()) != uid {
		return nil, ErrUnsafeEndpoint
	}
	return openServiceEgress(ctx, path, controllerProfile(gid), raw, signature, authority, descriptor)
}

func openServiceEgress(parent context.Context, path string, profile endpointProfile, raw, signature []byte, authority control.Authority, descriptor []byte) (*EgressMux, error) {
	if parent == nil || parent.Err() != nil || len(descriptor) == 0 || len(descriptor) > appplatform.MaxDescriptorBytes {
		return nil, ErrRefused
	}
	raw, signature, descriptor = bytes.Clone(raw), bytes.Clone(signature), bytes.Clone(descriptor)
	authority.PublicKey = bytes.Clone(authority.PublicKey)
	verified, err := control.VerifyServiceEgressSession(raw, signature, authority, time.Now().UnixMilli())
	if err != nil {
		return nil, err
	}
	if parsed, err := appplatform.ParseDescriptor(descriptor); err != nil || parsed.SHA256 != verified.Command().ReleaseSHA256 {
		return nil, ErrRefused
	}
	frame, err := EncodeEgressOpen(raw, signature, descriptor)
	if err != nil {
		return nil, ErrRefused
	}
	ctx, cancel := context.WithTimeout(parent, 3*sessionTimeout)
	defer cancel()
	conn, err := connectEndpoint(ctx, path, profile)
	if err != nil {
		return nil, err
	}
	deadline, _ := ctx.Deadline()
	_ = conn.SetDeadline(deadline)
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	fail := func() (*EgressMux, error) {
		stop()
		conn.Close()
		return nil, ErrRefused
	}
	if _, err := io.Copy(conn, bytes.NewReader(frame)); err != nil {
		return fail()
	}
	var ack [41]byte
	if _, err := io.ReadFull(conn, ack[:]); err != nil {
		return fail()
	}
	status, err := ParseEgressAck(ack[:], sha256.Sum256(frame))
	if err != nil || status != EgressAckOpen {
		return fail()
	}
	// stop() reports whether the context already closed the connection.
	if !stop() || ctx.Err() != nil {
		conn.Close()
		return nil, ErrRefused
	}
	// The session outlives this call's budget: only per-write deadlines apply from
	// here on.
	_ = conn.SetDeadline(time.Time{})
	return NewEgressMux(conn, EgressPortal), nil
}
