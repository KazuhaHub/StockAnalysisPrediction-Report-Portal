//go:build linux

package transport

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"io"
	"net"
	"os"
	"strconv"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/control"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/driver"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/journal"
)

const backendRegistrationQueryMagic = "PRBQ0001"
const backendRegistrationReplyMagic = "PRBS0001"

// DeliverControllerRegistration requires the configured non-root controller and
// a root-owned protected Unix endpoint. It sends private bytes only on that
// authenticated connection; cancellation/lost responses may follow native IO.
func DeliverControllerRegistration(ctx context.Context, path string, uid, gid uint32, raw, signature []byte, a control.Authority, descriptor, document []byte) (BackendRegistrationObservation, error) {
	if !validController(uid, gid) || uint32(os.Geteuid()) != uid {
		return BackendRegistrationObservation{}, ErrUnsafeEndpoint
	}
	return deliverRegistrationFrame(ctx, path, controllerProfile(gid), raw, signature, a, descriptor, document)
}
func deliverRegistrationFrame(ctx context.Context, path string, profile endpointProfile, raw, signature []byte, a control.Authority, descriptor, document []byte) (BackendRegistrationObservation, error) {
	var zero BackendRegistrationObservation
	if len(raw) == 0 || len(raw) > control.MaxCommandBytes || len(signature) != 64 || len(descriptor) == 0 || len(descriptor) > appplatform.MaxDescriptorBytes || len(document) == 0 || len(document) > 4096 {
		return zero, driver.ErrContainerUncertain
	}
	raw, signature, descriptor, document, a.PublicKey = bytes.Clone(raw), bytes.Clone(signature), bytes.Clone(descriptor), bytes.Clone(document), bytes.Clone(a.PublicKey)
	defer clear(document)
	started := time.Now()
	verified, err := control.VerifyBackendRegistration(raw, signature, a, started.UnixMilli())
	if err != nil {
		return zero, driver.ErrContainerUncertain
	}
	registration, err := control.ValidateBackendRegistrationDocument(verified, descriptor, document)
	if err != nil {
		return zero, driver.ErrContainerUncertain
	}
	registration.Close()
	ctx, cancel := context.WithTimeout(ctx, candidateExecutionTimeout)
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
	frame := make([]byte, 44, 44+len(raw)+64+4+len(descriptor)+4+len(document))
	defer func() { clear(frame) }()
	copy(frame, backendRegistrationQueryMagic)
	if _, err := rand.Read(frame[8:40]); err != nil {
		return zero, err
	}
	binary.BigEndian.PutUint32(frame[40:44], uint32(len(raw)))
	frame = append(frame, raw...)
	frame = append(frame, signature...)
	frame = append(frame, candidateDescriptorFrame(descriptor)...)
	var size [4]byte
	binary.BigEndian.PutUint32(size[:], uint32(len(document)))
	frame = append(frame, size[:]...)
	frame = append(frame, document...)
	if _, err := io.Copy(conn, bytes.NewReader(frame)); err != nil {
		return zero, driver.ErrContainerUncertain
	}
	if err := conn.CloseWrite(); err != nil {
		return zero, driver.ErrContainerUncertain
	}
	var header [44]byte
	if _, err := io.ReadFull(conn, header[:]); err != nil {
		return zero, driver.ErrContainerUncertain
	}
	digest := sha256.Sum256(frame)
	count := binary.BigEndian.Uint32(header[40:44])
	if string(header[:8]) != backendRegistrationReplyMagic || !bytes.Equal(header[8:40], digest[:]) || count == 0 || count > 4096 {
		return zero, driver.ErrContainerUncertain
	}
	body := make([]byte, int(count))
	if _, err := io.ReadFull(conn, body); err != nil {
		return zero, driver.ErrContainerUncertain
	}
	var extra [1]byte
	if n, err := conn.Read(extra[:]); n != 0 || err != io.EOF {
		return zero, driver.ErrContainerUncertain
	}
	receipt, err := journal.ParseBackendRegistrationReceipt(body)
	received := time.Now()
	settled, _ := strconv.ParseInt(receipt.SettledMS, 10, 64)
	// Historical exact replays are permitted. This observation is provenance for
	// this response, not a fresh app health/boot proof or a new delivery timestamp.
	if err != nil || !receipt.MatchesCommand(verified.Command()) || settled > received.UnixMilli() || ctx.Err() != nil || received.Before(started) {
		return zero, driver.ErrContainerUncertain
	}
	if _, err := control.VerifyBackendRegistration(raw, signature, a, received.UnixMilli()); err != nil {
		return zero, driver.ErrContainerUncertain
	}
	sum := sha256.Sum256(raw)
	return BackendRegistrationObservation{receipt: receipt, authority: a, endpoint: CandidateReceiptEndpoint{Path: path, RunnerUID: profile.owner, ControllerUID: uint32(os.Geteuid()), ControllerGID: profile.group, RootController: profile.shared}, receivedAt: received, commandSHA: hex.EncodeToString(sum[:])}, nil
}
func (s *Server) handleBackendRegistration(ctx context.Context, conn *net.UnixConn) {
	// Ordinary read-only and data-only listeners never receive the document.
	provider := s.candidateExecution
	if provider == nil || provider.store == nil || provider.native == nil {
		return
	}
	header := make([]byte, 44)
	copy(header, backendRegistrationQueryMagic)
	if _, err := io.ReadFull(conn, header[8:]); err != nil {
		return
	}
	count := binary.BigEndian.Uint32(header[40:44])
	if count == 0 || count > control.MaxCommandBytes {
		return
	}
	body := make([]byte, int(count)+64)
	if _, err := io.ReadFull(conn, body); err != nil {
		return
	}
	descriptor, err := readCandidateDescriptor(conn)
	if err != nil {
		return
	}
	// Verify current independent controller trust and public material before
	// allocating or reading the private portion of an authenticated peer's frame.
	enrollment, err := provider.store.ControllerDeployment(ctx)
	if err != nil {
		return
	}
	a := control.Authority{DeploymentID: enrollment.DeploymentID, ControllerEpoch: enrollment.ControllerEpoch, ControlRevision: enrollment.ControlRevision, PublicKey: enrollment.PublicKey, Fingerprint: enrollment.Fingerprint}
	v, err := control.VerifyBackendRegistration(body[:count], body[count:], a, time.Now().UnixMilli())
	if err != nil {
		return
	}
	parsed, err := appplatform.ParseDescriptor(descriptor)
	if err != nil || parsed.Manifest.Backend == nil || parsed.SHA256 != v.Candidate().Payload.ReleaseSHA256 {
		return
	}
	var size [4]byte
	if _, err := io.ReadFull(conn, size[:]); err != nil {
		return
	}
	documentSize := binary.BigEndian.Uint32(size[:])
	if documentSize == 0 || documentSize > 4096 {
		return
	}
	document := make([]byte, int(documentSize))
	defer clear(document)
	if _, err := io.ReadFull(conn, document); err != nil {
		return
	}
	var extra [1]byte
	if n, err := conn.Read(extra[:]); n != 0 || err != io.EOF {
		return
	}
	receipt, err := provider.store.DispatchBackendRegistration(ctx, body[:count], body[count:], descriptor, document, provider.native)
	if err != nil {
		return
	}
	encoded, err := json.Marshal(receipt)
	if err != nil || len(encoded) > 4096 || !receipt.MatchesCommand(v.Command()) {
		return
	}
	digest := sha256.New()
	digest.Write(header)
	digest.Write(body)
	digest.Write(candidateDescriptorFrame(descriptor))
	digest.Write(size[:])
	digest.Write(document)
	reply := make([]byte, 44, 44+len(encoded))
	copy(reply, backendRegistrationReplyMagic)
	copy(reply[8:40], digest.Sum(nil))
	binary.BigEndian.PutUint32(reply[40:44], uint32(len(encoded)))
	io.Copy(conn, bytes.NewReader(append(reply, encoded...)))
}
