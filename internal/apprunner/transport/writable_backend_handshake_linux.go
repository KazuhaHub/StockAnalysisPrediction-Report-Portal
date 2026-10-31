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

const writableBackendHandshakeQueryMagic = "PWHQ0001"
const writableBackendHandshakeReplyMagic = "PWHS0001"

func HandshakeWritableControllerBackend(ctx context.Context, path string, uid, gid uint32, raw, signature []byte, a control.Authority, descriptor []byte, expected journal.WritableBackendRegistrationReceipt, challenge string) (WritableBackendHandshakeObservation, error) {
	if !validController(uid, gid) || uint32(os.Geteuid()) != uid {
		return WritableBackendHandshakeObservation{}, ErrUnsafeEndpoint
	}
	return handshakeWritableBackendFrame(ctx, path, controllerProfile(gid), raw, signature, a, descriptor, expected, challenge)
}
func handshakeWritableBackendFrame(ctx context.Context, path string, profile endpointProfile, raw, signature []byte, a control.Authority, descriptor []byte, expected journal.WritableBackendRegistrationReceipt, challenge string) (WritableBackendHandshakeObservation, error) {
	var zero WritableBackendHandshakeObservation
	if len(raw) == 0 || len(raw) > control.MaxCommandBytes || len(signature) != 64 || len(descriptor) == 0 || len(descriptor) > appplatform.MaxDescriptorBytes || !appplatform.ValidBackendHandshakeToken(challenge) {
		return zero, driver.ErrContainerUncertain
	}
	raw, signature, descriptor, a.PublicKey = bytes.Clone(raw), bytes.Clone(signature), bytes.Clone(descriptor), bytes.Clone(a.PublicKey)
	started := time.Now()
	v, err := control.VerifyWritableBackendRegistration(raw, signature, a, started.UnixMilli())
	if err != nil {
		return zero, driver.ErrContainerUncertain
	}
	expectedRaw, err := json.Marshal(expected)
	if err != nil {
		return zero, driver.ErrContainerUncertain
	}
	if _, err := journal.ParseWritableBackendRegistrationReceipt(expectedRaw); err != nil {
		return zero, driver.ErrContainerUncertain
	}
	expectedDigest := sha256.Sum256(expectedRaw)
	c := v.Command()
	if expected.Claim.EffectID != c.EffectID || expected.Claim.PayloadSHA256 != c.PayloadSHA256 || expected.Claim.SourceSHA256 != c.RunningReceiptSHA256 || expected.Claim.DocumentSHA256 != c.DocumentSHA256 {
		return zero, driver.ErrContainerUncertain
	}
	parsed, err := appplatform.ParseDescriptor(descriptor)
	if err != nil || parsed.Manifest.Backend == nil || parsed.SHA256 != v.Candidate().Index().Payload.ReleaseSHA256 {
		return zero, driver.ErrContainerUncertain
	}
	ctx, cancel := context.WithTimeout(ctx, sessionTimeout)
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
	frame := make([]byte, 44)
	copy(frame, writableBackendHandshakeQueryMagic)
	if _, err := rand.Read(frame[8:40]); err != nil {
		return zero, err
	}
	binary.BigEndian.PutUint32(frame[40:44], uint32(len(raw)))
	frame = append(frame, raw...)
	frame = append(frame, signature...)
	frame = append(frame, candidateDescriptorFrame(descriptor)...)
	var size [4]byte
	binary.BigEndian.PutUint32(size[:], uint32(len(challenge)))
	frame = append(frame, size[:]...)
	frame = append(frame, challenge...)
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
	if string(header[:8]) != writableBackendHandshakeReplyMagic || !bytes.Equal(header[8:40], digest[:]) || count == 0 || count > 8192 {
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
	evidence, err := journal.ParseWritableBackendHandshakeEvidence(body)
	received := time.Now()
	observed, _ := strconv.ParseInt(evidence.ObservedMS, 10, 64)
	challengeSHA := sha256.Sum256([]byte(challenge))
	if err != nil || evidence.PayloadSHA256 != v.Command().PayloadSHA256 || evidence.NativeID != expected.Claim.NativeID || evidence.DeliveryReceiptSHA256 != hex.EncodeToString(expectedDigest[:]) || evidence.ChallengeSHA256 != hex.EncodeToString(challengeSHA[:]) || observed < started.UnixMilli() || observed > received.UnixMilli() || received.Before(started) || ctx.Err() != nil {
		return zero, driver.ErrContainerUncertain
	}
	if _, err := control.VerifyWritableBackendRegistration(raw, signature, a, received.UnixMilli()); err != nil {
		return zero, driver.ErrContainerUncertain
	}
	commandSHA := sha256.Sum256(raw)
	return WritableBackendHandshakeObservation{evidence: evidence, authority: a, endpoint: CandidateReceiptEndpoint{Path: path, RunnerUID: profile.owner, ControllerUID: uint32(os.Geteuid()), ControllerGID: profile.group, RootController: profile.shared}, receivedAt: received, commandSHA: hex.EncodeToString(commandSHA[:])}, nil
}
func (s *Server) handleWritableBackendHandshake(ctx context.Context, conn *net.UnixConn) {
	provider := s.candidateExecution
	if provider == nil || provider.store == nil || provider.native == nil {
		return
	}
	header := make([]byte, 44)
	copy(header, writableBackendHandshakeQueryMagic)
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
	var size [4]byte
	if _, err := io.ReadFull(conn, size[:]); err != nil {
		return
	}
	n := binary.BigEndian.Uint32(size[:])
	if n == 0 || n > 4096 {
		return
	}
	challenge := make([]byte, int(n))
	if _, err := io.ReadFull(conn, challenge); err != nil || !appplatform.ValidBackendHandshakeToken(string(challenge)) {
		return
	}
	var extra [1]byte
	if n, err := conn.Read(extra[:]); n != 0 || err != io.EOF {
		return
	}
	evidence, err := provider.store.ExchangeWritableBackendHandshake(ctx, body[:count], body[count:], descriptor, string(challenge), provider.native)
	if err != nil {
		return
	}
	encoded, err := json.Marshal(evidence)
	if err != nil || len(encoded) > 8192 {
		return
	}
	digest := sha256.New()
	digest.Write(header)
	digest.Write(body)
	digest.Write(candidateDescriptorFrame(descriptor))
	digest.Write(size[:])
	digest.Write(challenge)
	reply := make([]byte, 44, 44+len(encoded))
	copy(reply, writableBackendHandshakeReplyMagic)
	copy(reply[8:40], digest.Sum(nil))
	binary.BigEndian.PutUint32(reply[40:44], uint32(len(encoded)))
	io.Copy(conn, bytes.NewReader(append(reply, encoded...)))
}
