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

const writableBackendRequestQueryMagic = "PWRQ0001"
const writableBackendRequestReplyMagic = "PWRS0001"

func RequestWritableControllerBackend(ctx context.Context, path string, uid, gid uint32, raw, signature []byte, a control.Authority, descriptor []byte, expected journal.WritableBackendRegistrationReceipt, body []byte, assertion string) (WritableBackendRequestObservation, error) {
	if !validController(uid, gid) || uint32(os.Geteuid()) != uid {
		return WritableBackendRequestObservation{}, ErrUnsafeEndpoint
	}
	return requestWritableBackendFrame(ctx, path, controllerProfile(gid), raw, signature, a, descriptor, expected, body, assertion)
}
func requestWritableBackendFrame(ctx context.Context, path string, profile endpointProfile, raw, signature []byte, a control.Authority, descriptor []byte, expected journal.WritableBackendRegistrationReceipt, body []byte, assertion string) (WritableBackendRequestObservation, error) {
	var zero WritableBackendRequestObservation
	if len(raw) == 0 || len(raw) > control.MaxCommandBytes || len(signature) != 64 || len(descriptor) == 0 || len(descriptor) > appplatform.MaxDescriptorBytes || len(body) > appplatform.MaxBackendRequestBody || len(assertion) > appplatform.MaxBackendAssertion {
		return zero, driver.ErrContainerUncertain
	}
	raw, signature, descriptor, body, a.PublicKey = bytes.Clone(raw), bytes.Clone(signature), bytes.Clone(descriptor), bytes.Clone(body), bytes.Clone(a.PublicKey)
	defer clear(body)
	started := time.Now()
	v, err := control.VerifyWritableBackendRequest(raw, signature, a, started.UnixMilli())
	if err != nil {
		return zero, err
	}
	if err := control.ValidateWritableBackendRequestData(v, body, assertion); err != nil {
		return zero, err
	}
	expectedRaw, err := json.Marshal(expected)
	if err != nil {
		return zero, driver.ErrContainerUncertain
	}
	if _, err := journal.ParseWritableBackendRegistrationReceipt(expectedRaw); err != nil {
		return zero, driver.ErrContainerUncertain
	}
	reg := v.Registration().Command()
	if expected.Claim.EffectID != reg.EffectID || expected.Claim.PayloadSHA256 != reg.PayloadSHA256 || expected.Claim.SourceSHA256 != reg.RunningReceiptSHA256 || expected.Claim.DocumentSHA256 != reg.DocumentSHA256 {
		return zero, driver.ErrContainerUncertain
	}
	expectedSHA := sha256.Sum256(expectedRaw)
	parsed, err := appplatform.ParseDescriptor(descriptor)
	if err != nil || parsed.Manifest.Backend == nil || parsed.SHA256 != v.Registration().Candidate().Index().Payload.ReleaseSHA256 {
		return zero, driver.ErrContainerUncertain
	}
	ctx, cancel := context.WithTimeout(ctx, backendRequestTimeout)
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
	frame := make([]byte, 44, 44+len(raw)+64+4+len(descriptor)+4+len(body)+4+len(assertion))
	defer func() { clear(frame) }()
	copy(frame, writableBackendRequestQueryMagic)
	if _, err := rand.Read(frame[8:40]); err != nil {
		return zero, err
	}
	binary.BigEndian.PutUint32(frame[40:44], uint32(len(raw)))
	frame = append(frame, raw...)
	frame = append(frame, signature...)
	frame = append(frame, candidateDescriptorFrame(descriptor)...)
	var size [4]byte
	binary.BigEndian.PutUint32(size[:], uint32(len(body)))
	frame = append(frame, size[:]...)
	frame = append(frame, body...)
	binary.BigEndian.PutUint32(size[:], uint32(len(assertion)))
	frame = append(frame, size[:]...)
	frame = append(frame, assertion...)
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
	if string(header[:8]) != writableBackendRequestReplyMagic || !bytes.Equal(header[8:40], digest[:]) || count == 0 || count > journal.MaxBackendRequestResultBytes {
		return zero, driver.ErrContainerUncertain
	}
	response := make([]byte, int(count))
	defer clear(response)
	if _, err := io.ReadFull(conn, response); err != nil {
		return zero, driver.ErrContainerUncertain
	}
	var extra [1]byte
	if n, err := conn.Read(extra[:]); n != 0 || err != io.EOF {
		return zero, driver.ErrContainerUncertain
	}
	result, err := journal.ParseWritableBackendRequestResult(response)
	received := time.Now()
	observed, _ := strconv.ParseInt(result.ObservedMS, 10, 64)
	commandSHA := sha256.Sum256(raw)
	if err != nil || result.NativeID != expected.Claim.NativeID || result.DeliveryReceiptSHA256 != hex.EncodeToString(expectedSHA[:]) || result.RequestID != v.Command().RequestID || result.BootID != v.Command().BootID || result.RequestSHA256 != hex.EncodeToString(commandSHA[:]) || observed < started.UnixMilli() || observed > received.UnixMilli() || received.Before(started) || ctx.Err() != nil {
		return zero, driver.ErrContainerUncertain
	}
	if _, err := control.VerifyWritableBackendRequest(raw, signature, a, received.UnixMilli()); err != nil {
		return zero, err
	}
	return WritableBackendRequestObservation{result: result, authority: a, endpoint: CandidateReceiptEndpoint{Path: path, RunnerUID: profile.owner, ControllerUID: uint32(os.Geteuid()), ControllerGID: profile.group, RootController: profile.shared}, receivedAt: received, commandSHA: hex.EncodeToString(commandSHA[:])}, nil
}
func (s *Server) handleWritableBackendRequest(ctx context.Context, conn *net.UnixConn) {
	provider := s.candidateExecution
	if provider == nil || provider.store == nil || provider.native == nil {
		return
	}
	header := make([]byte, 44)
	copy(header, writableBackendRequestQueryMagic)
	if _, err := io.ReadFull(conn, header[8:]); err != nil {
		return
	}
	count := binary.BigEndian.Uint32(header[40:44])
	if count == 0 || count > control.MaxCommandBytes {
		return
	}
	command := make([]byte, int(count)+64)
	if _, err := io.ReadFull(conn, command); err != nil {
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
	if _, err := control.VerifyWritableBackendRequest(command[:count], command[count:], a, time.Now().UnixMilli()); err != nil {
		return
	}
	var bodySize, assertionSize [4]byte
	if _, err := io.ReadFull(conn, bodySize[:]); err != nil {
		return
	}
	n := binary.BigEndian.Uint32(bodySize[:])
	if n > appplatform.MaxBackendRequestBody {
		return
	}
	body := make([]byte, int(n))
	defer clear(body)
	if _, err := io.ReadFull(conn, body); err != nil {
		return
	}
	if _, err := io.ReadFull(conn, assertionSize[:]); err != nil {
		return
	}
	n = binary.BigEndian.Uint32(assertionSize[:])
	if n == 0 || n > appplatform.MaxBackendAssertion {
		return
	}
	assertion := make([]byte, int(n))
	defer clear(assertion)
	if _, err := io.ReadFull(conn, assertion); err != nil {
		return
	}
	var extra [1]byte
	if n, err := conn.Read(extra[:]); n != 0 || err != io.EOF {
		return
	}
	result, err := provider.store.ForwardWritableBackendRequest(ctx, command[:count], command[count:], descriptor, body, string(assertion), provider.native)
	if err != nil {
		return
	}
	encoded, err := json.Marshal(result)
	if err != nil || len(encoded) > journal.MaxBackendRequestResultBytes {
		return
	}
	defer clear(encoded)
	digest := sha256.New()
	digest.Write(header)
	digest.Write(command)
	digest.Write(candidateDescriptorFrame(descriptor))
	digest.Write(bodySize[:])
	digest.Write(body)
	digest.Write(assertionSize[:])
	digest.Write(assertion)
	reply := make([]byte, 44, 44+len(encoded))
	defer func() { clear(reply) }()
	copy(reply, writableBackendRequestReplyMagic)
	copy(reply[8:40], digest.Sum(nil))
	binary.BigEndian.PutUint32(reply[40:44], uint32(len(encoded)))
	reply = append(reply, encoded...)
	io.Copy(conn, bytes.NewReader(reply))
}
