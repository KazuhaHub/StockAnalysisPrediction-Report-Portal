//go:build linux

package transport

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"io"
	"net"
	"os"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/control"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/driver"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/journal"
)

const candidateExecutionQueryMagic = "PRKE0001"
const candidateExecutionReplyMagic = "PRKF0001"
const candidateExecutionTimeout = 30 * time.Second

type candidateExecutionProvider struct {
	store  *journal.Store
	native *driver.CandidateRuntime
}

// ListenCandidateController explicitly enables signed native candidate dispatch.
// The caller owns the preconfigured concrete runtime/journal until Serve stops.
// No provider, user, trust, image, socket directory or backend registration is
// provisioned here; ordinary ListenController endpoints stay read-only.
func ListenCandidateController(ctx context.Context, path string, controllerUID, controllerGID uint32, store *journal.Store, native *driver.CandidateRuntime) (*Server, error) {
	if os.Geteuid() != 0 || store == nil || native == nil || ctx.Err() != nil {
		return nil, ErrUnsafeEndpoint
	}
	if err := native.CheckConfiguration(ctx); err != nil {
		return nil, ErrUnsafeEndpoint
	}
	server, err := ListenController(path, controllerUID, controllerGID, store)
	if err != nil {
		return nil, err
	}
	server.candidateExecution = &candidateExecutionProvider{store: store, native: native}
	return server, nil
}
func (s *Server) handleCandidateExecution(ctx context.Context, conn *net.UnixConn) {
	if s.candidateExecution == nil {
		return
	}
	s.handleCandidateResponseMode(ctx, conn, true)
}

// StartControllerCandidate sends the exact signed intent and digest-bound
// descriptor. Lost responses/cancellation may follow effects: retain the same
// identity and inspect with renewed authority, never create a replacement.
func StartControllerCandidate(ctx context.Context, path string, controllerUID, controllerGID uint32, raw, signature []byte, authority control.Authority, descriptor []byte) (CandidateReceiptObservation, error) {
	if !validController(controllerUID, controllerGID) || uint32(os.Geteuid()) != controllerUID {
		return CandidateReceiptObservation{}, ErrUnsafeEndpoint
	}
	return readCandidateResponseMode(ctx, path, controllerProfile(controllerGID), raw, signature, authority, descriptor, true)
}

func candidateDescriptorFrame(descriptor []byte) []byte {
	result := make([]byte, 4, 4+len(descriptor))
	binary.BigEndian.PutUint32(result, uint32(len(descriptor)))
	return append(result, descriptor...)
}
func readCandidateDescriptor(conn *net.UnixConn) ([]byte, error) {
	var size [4]byte
	if _, err := io.ReadFull(conn, size[:]); err != nil {
		return nil, err
	}
	count := binary.BigEndian.Uint32(size[:])
	if count == 0 || count > appplatform.MaxDescriptorBytes {
		return nil, ErrRefused
	}
	descriptor := make([]byte, int(count))
	if _, err := io.ReadFull(conn, descriptor); err != nil {
		return nil, err
	}
	return descriptor, nil
}
func validCandidateDescriptor(raw, descriptor []byte) bool {
	parsed, err := appplatform.ParseDescriptor(descriptor)
	if err != nil || parsed.Manifest.Backend == nil {
		return false
	}
	// Signature validation is independently mandatory before any native dispatch.
	// The complete signed command already binds these exact bytes by digest.
	var c control.CandidateCommand
	return json.Unmarshal(raw, &c) == nil && parsed.SHA256 == c.Payload.ReleaseSHA256
}
