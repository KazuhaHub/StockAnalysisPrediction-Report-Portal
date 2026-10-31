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
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/control"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/driver"
)

const writableLifecycleQueryMagic = "PWLQ0001"
const writableLifecycleReplyMagic = "PWLR0001"
const maxWritableLifecycleReply = 16384

// RunControllerWritableLifecycle sends one bounded action to an explicitly
// provisioned root candidate/managed endpoint. Only delivery accepts private
// document bytes. A timeout/lost reply may follow effects; retain the same
// logical identity and use read or separately authorized reconciliation.
func RunControllerWritableLifecycle(ctx context.Context, path string, uid, gid uint32, action WritableLifecycleAction, raw, sig []byte, a control.Authority, descriptor, document []byte) (WritableLifecycleObservation, error) {
	if !validController(uid, gid) || uint32(os.Geteuid()) != uid {
		return WritableLifecycleObservation{}, ErrUnsafeEndpoint
	}
	return writableLifecycleFrame(ctx, path, controllerProfile(gid), action, raw, sig, a, descriptor, document)
}
func writableLifecycleFrame(parent context.Context, path string, profile endpointProfile, action WritableLifecycleAction, raw, sig []byte, a control.Authority, descriptor, document []byte) (WritableLifecycleObservation, error) {
	var zero WritableLifecycleObservation
	if !action.valid() || len(raw) == 0 || len(raw) > control.MaxCommandBytes || len(sig) != 64 || len(descriptor) == 0 || len(descriptor) > appplatform.MaxDescriptorBytes || len(document) > 4096 || (action == WritableRegistrationDeliver) != (len(document) > 0) {
		return zero, driver.ErrContainerUncertain
	}
	raw, sig, descriptor, document, a.PublicKey = bytes.Clone(raw), bytes.Clone(sig), bytes.Clone(descriptor), bytes.Clone(document), bytes.Clone(a.PublicKey)
	defer clear(document)
	started := time.Now()
	ref, err := verifyWritableLifecycle(action, raw, sig, descriptor, a, started.UnixMilli())
	if err != nil {
		return zero, driver.ErrContainerUncertain
	}
	if action == WritableRegistrationDeliver {
		v, err := control.VerifyWritableBackendRegistration(raw, sig, a, started.UnixMilli())
		if err != nil {
			return zero, driver.ErrContainerUncertain
		}
		private, err := control.ValidateWritableBackendRegistrationDocument(v, descriptor, document)
		if err != nil {
			return zero, driver.ErrContainerUncertain
		}
		private.Close()
	}
	timeout := sessionTimeout
	if action.mutating() {
		timeout = candidateExecutionTimeout
	}
	ctx, cancel := context.WithTimeout(parent, timeout)
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
	frame := make([]byte, 45, 45+len(raw)+64+4+len(descriptor)+4+len(document))
	defer func() { clear(frame) }()
	copy(frame, writableLifecycleQueryMagic)
	if _, err := rand.Read(frame[8:40]); err != nil {
		return zero, err
	}
	binary.BigEndian.PutUint32(frame[40:44], uint32(len(raw)))
	frame[44] = byte(action)
	frame = append(frame, raw...)
	frame = append(frame, sig...)
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
	sum := sha256.Sum256(frame)
	count := binary.BigEndian.Uint32(header[40:])
	if string(header[:8]) != writableLifecycleReplyMagic || !bytes.Equal(header[8:40], sum[:]) || count == 0 || count > maxWritableLifecycleReply {
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
	received := time.Now()
	if ctx.Err() != nil || !validWritableLifecycleReply(action, body, ref, started, received) {
		return zero, driver.ErrContainerUncertain
	}
	if _, err := verifyWritableLifecycle(action, raw, sig, descriptor, a, received.UnixMilli()); err != nil {
		return zero, driver.ErrContainerUncertain
	}
	command := sha256.Sum256(raw)
	return WritableLifecycleObservation{action: action, body: string(body), authority: a, endpoint: CandidateReceiptEndpoint{Path: path, RunnerUID: profile.owner, ControllerUID: uint32(os.Geteuid()), ControllerGID: profile.group, RootController: profile.shared}, receivedAt: received, commandSHA: hex.EncodeToString(command[:])}, nil
}
func (s *Server) handleWritableLifecycle(ctx context.Context, conn *net.UnixConn) {
	provider := s.candidateExecution
	if provider == nil || provider.store == nil || provider.native == nil {
		return
	}
	header := make([]byte, 45)
	copy(header, writableLifecycleQueryMagic)
	if _, err := io.ReadFull(conn, header[8:]); err != nil {
		return
	}
	count := binary.BigEndian.Uint32(header[40:44])
	action := WritableLifecycleAction(header[44])
	if count == 0 || count > control.MaxCommandBytes || !action.valid() {
		return
	}
	// The entire lifecycle frame and execution share the bounded session. Signed
	// deadlines remain independently enforced by concrete journal/native operations.
	body := make([]byte, int(count)+64)
	if _, err := io.ReadFull(conn, body); err != nil {
		return
	}
	descriptor, err := readCandidateDescriptor(conn)
	if err != nil {
		return
	}
	enrollment, err := provider.store.ControllerDeployment(ctx)
	if err != nil {
		return
	}
	a := control.Authority{DeploymentID: enrollment.DeploymentID, ControllerEpoch: enrollment.ControllerEpoch, ControlRevision: enrollment.ControlRevision, PublicKey: enrollment.PublicKey, Fingerprint: enrollment.Fingerprint}
	ref, err := verifyWritableLifecycle(action, body[:count], body[count:], descriptor, a, time.Now().UnixMilli())
	if err != nil {
		return
	}
	// Authenticate the exact signed public reference before reading private bytes.
	var size [4]byte
	if _, err := io.ReadFull(conn, size[:]); err != nil {
		return
	}
	n := binary.BigEndian.Uint32(size[:])
	if n > 4096 || (action == WritableRegistrationDeliver) != (n > 0) {
		return
	}
	document := make([]byte, int(n))
	defer clear(document)
	if _, err := io.ReadFull(conn, document); err != nil {
		return
	}
	var extra [1]byte
	if n, err := conn.Read(extra[:]); n != 0 || err != io.EOF {
		return
	}
	started := time.Now()
	var result any
	raw, sig := body[:count], body[count:]
	switch action {
	case WritableWriterExecute:
		result, err = provider.store.DispatchDataWriter(ctx, raw, sig, descriptor, provider.native)
	case WritableWriterRead:
		result, err = provider.store.ReadDataWriterReceipt(ctx, raw, sig)
	case WritableWriterReconcile:
		result, err = provider.store.ReconcileDataWriter(ctx, raw, sig, descriptor, provider.native)
	case WritableCandidateStart:
		result, err = provider.store.DispatchWritableCandidate(ctx, raw, sig, descriptor, provider.native)
	case WritableCandidateRead:
		result, err = provider.store.ReadWritableCandidateRunningReceipt(ctx, raw, sig)
	case WritableCandidateReconcile:
		result, err = provider.store.ReconcileWritableCandidate(ctx, raw, sig, descriptor, provider.native)
	case WritableCandidateProbe:
		result, err = provider.store.ObserveWritableCandidateReadiness(ctx, raw, sig, provider.native, descriptor)
	case WritableRegistrationDeliver:
		result, err = provider.store.DispatchWritableBackendRegistration(ctx, raw, sig, descriptor, document, provider.native)
	case WritableRegistrationRead:
		result, err = provider.store.ReadWritableBackendRegistrationReceipt(ctx, raw, sig)
	case ManagedRemovalStop, ManagedRemovalRemoveCode:
		result, err = provider.store.DispatchManagedRemoval(ctx, raw, sig, descriptor, provider.native)
	case ManagedRemovalRead:
		result, err = provider.store.ReadManagedRemovalReceipt(ctx, raw, sig)
	default:
		return
	}
	if err != nil || ctx.Err() != nil {
		return
	}
	encoded, err := json.Marshal(result)
	if err != nil || len(encoded) > maxWritableLifecycleReply || !validWritableLifecycleReply(action, encoded, ref, started, time.Now()) {
		return
	}
	digest := sha256.New()
	digest.Write(header)
	digest.Write(body)
	digest.Write(candidateDescriptorFrame(descriptor))
	digest.Write(size[:])
	digest.Write(document)
	reply := make([]byte, 44, 44+len(encoded))
	copy(reply, writableLifecycleReplyMagic)
	copy(reply[8:40], digest.Sum(nil))
	binary.BigEndian.PutUint32(reply[40:], uint32(len(encoded)))
	io.Copy(conn, bytes.NewReader(append(reply, encoded...)))
}
