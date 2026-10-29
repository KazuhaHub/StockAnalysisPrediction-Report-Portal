//go:build linux

package transport

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/binary"
	"io"
	"net"
	"os"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/driver"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/journal"
)

const networkRuntimeQueryMagic = "PRNV0001"
const networkRuntimeReplyMagic = "PRNW0001"

func (s *Server) handleNetworkRuntime(ctx context.Context, conn *net.UnixConn) {
	if s.network == nil {
		return
	}
	var nonce [32]byte
	if _, err := io.ReadFull(conn, nonce[:]); err != nil || networkStreamEOF(conn) != nil {
		return
	}
	d, err := s.network.store.ControllerDeployment(ctx)
	if err != nil {
		return
	}
	writable := s.candidateExecution != nil && s.candidateExecution.store != nil && s.candidateExecution.native != nil
	raw, err := encodeNetworkRuntime(d.ControllerEnrollment, s.network.activeLimit, s.network.cacheLimit, writable)
	if err != nil {
		return
	}
	reply := make([]byte, 44)
	copy(reply, networkRuntimeReplyMagic)
	copy(reply[8:40], nonce[:])
	binary.BigEndian.PutUint32(reply[40:], uint32(len(raw)))
	reply = append(reply, raw...)
	io.Copy(conn, bytes.NewReader(reply))
}

// ObserveControllerNetwork verifies the exact root endpoint and observes enabled
// network protocol/limits only. It does not grant a request or attest app health.
func ObserveControllerNetwork(ctx context.Context, path string, uid, gid uint32) (NetworkRuntimeObservation, error) {
	if ctx == nil || !validController(uid, gid) || uint32(os.Geteuid()) != uid {
		return NetworkRuntimeObservation{}, ErrUnsafeEndpoint
	}
	return observeNetworkRuntime(ctx, path, controllerProfile(gid))
}
func observeNetworkRuntime(parent context.Context, path string, profile endpointProfile) (NetworkRuntimeObservation, error) {
	var zero NetworkRuntimeObservation
	if parent == nil || parent.Err() != nil {
		return zero, ErrNetworkRuntimeUnavailable
	}
	ctx, cancel := context.WithTimeout(parent, sessionTimeout)
	defer cancel()
	conn, err := connectEndpoint(ctx, path, profile)
	if err != nil {
		return zero, err
	}
	defer conn.Close()
	deadline, _ := ctx.Deadline()
	conn.SetDeadline(deadline)
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()
	var nonce [32]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return zero, ErrNetworkRuntimeUnavailable
	}
	request := append([]byte(networkRuntimeQueryMagic), nonce[:]...)
	if _, err := io.Copy(conn, bytes.NewReader(request)); err != nil {
		return zero, ErrNetworkRuntimeUnavailable
	}
	if conn.CloseWrite() != nil {
		return zero, ErrNetworkRuntimeUnavailable
	}
	var header [44]byte
	if _, err := io.ReadFull(conn, header[:]); err != nil || string(header[:8]) != networkRuntimeReplyMagic || !bytes.Equal(header[8:40], nonce[:]) {
		return zero, ErrNetworkRuntimeUnavailable
	}
	size := binary.BigEndian.Uint32(header[40:])
	if size == 0 || size > maxNetworkRuntimeBytes {
		return zero, ErrNetworkRuntimeIncompatible
	}
	raw := make([]byte, int(size))
	if _, err := io.ReadFull(conn, raw); err != nil || networkStreamEOF(conn) != nil {
		return zero, ErrNetworkRuntimeUnavailable
	}
	controller, info, err := decodeNetworkRuntime(raw)
	if err != nil {
		return zero, err
	}
	if ctx.Err() != nil {
		return zero, ErrNetworkRuntimeUnavailable
	}
	return NetworkRuntimeObservation{controller: controller, profile: info, endpoint: CandidateReceiptEndpoint{Path: path, RunnerUID: profile.owner, ControllerUID: uint32(os.Geteuid()), ControllerGID: profile.group, RootController: profile.shared}, receivedAt: time.Now()}, nil
}

// ListenNetworkCandidateController composes the independently checked candidate
// runtime and one network pool at the same operator-owned root endpoint. It is
// opt-in development bootstrap only; it creates no grants or production startup.
func ListenNetworkCandidateController(ctx context.Context, path string, uid, gid uint32, store *journal.Store, native *driver.CandidateRuntime, active, entries int) (*Server, error) {
	if ctx == nil || ctx.Err() != nil || os.Geteuid() != 0 || store == nil || native == nil {
		return nil, ErrUnsafeEndpoint
	}
	if _, err := store.ControllerDeployment(ctx); err != nil {
		return nil, ErrUnsafeEndpoint
	}
	network, err := newNetworkProvider(store, active, entries)
	if err != nil {
		return nil, ErrUnsafeEndpoint
	}
	server, err := ListenCandidateController(ctx, path, uid, gid, store, native)
	if err != nil {
		return nil, err
	}
	server.network = network
	return server, nil
}
