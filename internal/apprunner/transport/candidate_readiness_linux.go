//go:build linux

package transport

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"io"
	"os"
	"strconv"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/control"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/driver"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/journal"
)

const candidateReadinessQueryMagic = "PRKQ0001"
const candidateReadinessReplyMagic = "PRKS0001"

func ProbeControllerCandidate(ctx context.Context, path string, uid, gid uint32, raw, signature []byte, a control.Authority, descriptor []byte) (CandidateReadinessObservation, error) {
	var zero CandidateReadinessObservation
	if !validController(uid, gid) || uint32(os.Geteuid()) != uid {
		return zero, driver.ErrCandidateNotReady
	}
	return probeCandidateReadinessFrame(ctx, path, controllerProfile(gid), raw, signature, a, descriptor)
}
func probeCandidateReadinessFrame(ctx context.Context, path string, profile endpointProfile, raw, signature []byte, a control.Authority, descriptor []byte) (CandidateReadinessObservation, error) {
	var zero CandidateReadinessObservation
	if !validCandidateDescriptor(raw, descriptor) || len(signature) != 64 {
		return zero, driver.ErrCandidateNotReady
	}
	raw, signature, descriptor, a.PublicKey = bytes.Clone(raw), bytes.Clone(signature), bytes.Clone(descriptor), bytes.Clone(a.PublicKey)
	started := time.Now()
	verified, err := control.VerifyCandidate(raw, signature, a, started.UnixMilli())
	if err != nil {
		return zero, driver.ErrCandidateNotReady
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
	copy(frame, candidateReadinessQueryMagic)
	if _, err := rand.Read(frame[8:40]); err != nil {
		return zero, err
	}
	binary.BigEndian.PutUint32(frame[40:44], uint32(len(raw)))
	frame = append(frame, raw...)
	frame = append(frame, signature...)
	frame = append(frame, candidateDescriptorFrame(descriptor)...)
	if _, err := io.Copy(conn, bytes.NewReader(frame)); err != nil {
		return zero, driver.ErrCandidateNotReady
	}
	if err := conn.CloseWrite(); err != nil {
		return zero, driver.ErrCandidateNotReady
	}
	var header [44]byte
	if _, err := io.ReadFull(conn, header[:]); err != nil {
		return zero, driver.ErrCandidateNotReady
	}
	digest := sha256.Sum256(frame)
	size := binary.BigEndian.Uint32(header[40:44])
	if string(header[:8]) != candidateReadinessReplyMagic || !bytes.Equal(header[8:40], digest[:]) || size == 0 || size > 4096 {
		return zero, driver.ErrCandidateNotReady
	}
	body := make([]byte, int(size))
	if _, err := io.ReadFull(conn, body); err != nil {
		return zero, driver.ErrCandidateNotReady
	}
	var extra [1]byte
	if n, err := conn.Read(extra[:]); n != 0 || err != io.EOF {
		return zero, driver.ErrCandidateNotReady
	}
	r, err := journal.ParseCandidateReadiness(body)
	received := time.Now()
	observed, _ := strconv.ParseInt(r.ObservedMS, 10, 64)
	if err != nil || r.PayloadSHA256 != verified.Command().PayloadSHA256 || r.DataFormat != verified.Command().Payload.DataFormat || ctx.Err() != nil || observed < started.UnixMilli() || observed > received.UnixMilli() || received.Before(started) {
		return zero, driver.ErrCandidateNotReady
	}
	if _, err := control.VerifyCandidate(raw, signature, a, received.UnixMilli()); err != nil {
		return zero, driver.ErrCandidateNotReady
	}
	commandSHA := sha256.Sum256(raw)
	return CandidateReadinessObservation{readiness: r, authority: a, endpoint: CandidateReceiptEndpoint{Path: path, RunnerUID: profile.owner, ControllerUID: uint32(os.Geteuid()), ControllerGID: profile.group, RootController: profile.shared}, receivedAt: received, commandSHA: hex.EncodeToString(commandSHA[:])}, nil
}
