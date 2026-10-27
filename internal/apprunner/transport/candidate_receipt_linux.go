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

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/control"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/journal"
)

const candidateReceiptQueryMagic = "PRKR0001"
const candidateReceiptReplyMagic = "PRKP0001"
const maxCandidateReceiptBytes = 16384

type candidateReceiptReader interface {
	ReadCandidateRunningReceipt(context.Context, []byte, []byte) (journal.CandidateRunningReceipt, error)
}

func (s *Server) handleCandidateReceipt(ctx context.Context, conn *net.UnixConn) {
	s.handleCandidateResponse(ctx, conn)
}

func (s *Server) handleCandidateResponse(ctx context.Context, conn *net.UnixConn) {
	s.handleCandidateResponseMode(ctx, conn, false)
}
func (s *Server) handleCandidateResponseMode(ctx context.Context, conn *net.UnixConn, execute bool) {
	reader, ok := s.journal.(candidateReceiptReader)
	if !ok {
		return
	}
	header := make([]byte, 44)
	queryMagic, replyMagic := candidateReceiptQueryMagic, candidateReceiptReplyMagic
	if execute {
		queryMagic, replyMagic = candidateExecutionQueryMagic, candidateExecutionReplyMagic
	}
	copy(header, queryMagic)
	if _, err := io.ReadFull(conn, header[8:]); err != nil {
		return
	}
	size := binary.BigEndian.Uint32(header[40:44])
	if size == 0 || size > control.MaxCommandBytes {
		return
	}
	body := make([]byte, int(size)+64)
	if _, err := io.ReadFull(conn, body); err != nil {
		return
	}
	var descriptor []byte
	if execute {
		var err error
		descriptor, err = readCandidateDescriptor(conn)
		if err != nil || !validCandidateDescriptor(body[:size], descriptor) {
			return
		}
	}
	var extra [1]byte
	if n, err := conn.Read(extra[:]); n != 0 || err != io.EOF {
		return
	}
	var receipt journal.CandidateRunningReceipt
	var err error
	if execute {
		receipt, err = s.candidateExecution.store.DispatchCandidate(ctx, body[:size], body[size:], s.candidateExecution.native, descriptor)
	} else {
		receipt, err = reader.ReadCandidateRunningReceipt(ctx, body[:size], body[size:])
	}
	if err != nil {
		return
	}
	encoded, err := json.Marshal(receipt)
	if err != nil {
		return
	}
	if _, err := journal.ParseCandidateRunningReceipt(encoded); err != nil {
		return
	}
	digest := sha256.New()
	digest.Write(header)
	digest.Write(body)
	if execute {
		digest.Write(candidateDescriptorFrame(descriptor))
	}
	reply := make([]byte, 44, 44+len(encoded))
	copy(reply, replyMagic)
	copy(reply[8:40], digest.Sum(nil))
	binary.BigEndian.PutUint32(reply[40:44], uint32(len(encoded)))
	reply = append(reply, encoded...)
	io.Copy(conn, bytes.NewReader(reply))
}

// ReadCandidateReceipt uses the private same-user development endpoint. authority is
// independently trusted controller configuration, never supplied by app code.
func ReadCandidateReceipt(ctx context.Context, path string, runnerUID uint32, raw, signature []byte, authority control.Authority) (CandidateReceiptObservation, error) {
	return readCandidateReceipt(ctx, path, privateProfile(runnerUID), raw, signature, authority)
}

// ReadControllerCandidateReceipt uses the provisioned root endpoint and exact non-root
// controller UID/group. It performs no enrollment, dispatch, repair or startup.
func ReadControllerCandidateReceipt(ctx context.Context, path string, controllerUID, controllerGID uint32, raw, signature []byte, authority control.Authority) (CandidateReceiptObservation, error) {
	if !validController(controllerUID, controllerGID) || uint32(os.Geteuid()) != controllerUID {
		return CandidateReceiptObservation{}, ErrUnsafeEndpoint
	}
	return readCandidateReceipt(ctx, path, controllerProfile(controllerGID), raw, signature, authority)
}
func readCandidateReceipt(parent context.Context, path string, profile endpointProfile, raw, signature []byte, authority control.Authority) (CandidateReceiptObservation, error) {
	return readCandidateResponse(parent, path, profile, raw, signature, authority)
}

func readCandidateResponse(parent context.Context, path string, profile endpointProfile, raw, signature []byte, authority control.Authority) (CandidateReceiptObservation, error) {
	return readCandidateResponseMode(parent, path, profile, raw, signature, authority, nil, false)
}
func readCandidateResponseMode(parent context.Context, path string, profile endpointProfile, raw, signature []byte, authority control.Authority, descriptor []byte, execute bool) (CandidateReceiptObservation, error) {
	var zero CandidateReceiptObservation
	if len(raw) == 0 || len(raw) > control.MaxCommandBytes || len(signature) != 64 || len(authority.PublicKey) != 32 || parent.Err() != nil {
		return zero, ErrCandidateReceiptObservation
	}
	if execute && !validCandidateDescriptor(raw, descriptor) {
		return zero, ErrCandidateReceiptObservation
	}
	raw, signature, authority.PublicKey, descriptor = bytes.Clone(raw), bytes.Clone(signature), bytes.Clone(authority.PublicKey), bytes.Clone(descriptor)
	started := time.Now()
	verified, err := control.VerifyCandidate(raw, signature, authority, started.UnixMilli())
	if err != nil {
		return zero, ErrCandidateReceiptObservation
	}
	timeout := sessionTimeout
	if execute {
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
	frame := make([]byte, 44, 44+len(raw)+64)
	queryMagic, replyMagic := candidateReceiptQueryMagic, candidateReceiptReplyMagic
	if execute {
		queryMagic, replyMagic = candidateExecutionQueryMagic, candidateExecutionReplyMagic
	}
	copy(frame, queryMagic)
	if _, err := rand.Read(frame[8:40]); err != nil {
		return zero, ErrCandidateReceiptObservation
	}
	binary.BigEndian.PutUint32(frame[40:44], uint32(len(raw)))
	frame = append(frame, raw...)
	frame = append(frame, signature...)
	if execute {
		frame = append(frame, candidateDescriptorFrame(descriptor)...)
	}
	if _, err := io.Copy(conn, bytes.NewReader(frame)); err != nil {
		return zero, ErrCandidateReceiptObservation
	}
	if err := conn.CloseWrite(); err != nil {
		return zero, ErrCandidateReceiptObservation
	}
	var header [44]byte
	if _, err := io.ReadFull(conn, header[:]); err != nil {
		return zero, ErrCandidateReceiptObservation
	}
	expected := sha256.Sum256(frame)
	size := binary.BigEndian.Uint32(header[40:44])
	if string(header[:8]) != replyMagic || !bytes.Equal(header[8:40], expected[:]) || size == 0 || size > maxCandidateReceiptBytes {
		return zero, ErrCandidateReceiptObservation
	}
	body := make([]byte, int(size))
	if _, err := io.ReadFull(conn, body); err != nil {
		return zero, ErrCandidateReceiptObservation
	}
	var extra [1]byte
	if n, err := conn.Read(extra[:]); n != 0 || err != io.EOF {
		return zero, ErrCandidateReceiptObservation
	}
	receipt, err := journal.ParseCandidateRunningReceipt(body)
	if err != nil || !receipt.MatchesCommand(verified.Command()) {
		return zero, ErrCandidateReceiptObservation
	}
	received := time.Now()
	observed, _ := strconv.ParseInt(receipt.SettledMS, 10, 64)
	if ctx.Err() != nil || received.Before(started) || received.UnixMilli() < started.UnixMilli() || observed > received.UnixMilli() {
		return zero, ErrCandidateReceiptObservation
	}
	if _, err := control.VerifyCandidate(raw, signature, authority, received.UnixMilli()); err != nil {
		return zero, ErrCandidateReceiptObservation
	}
	commandDigest := sha256.Sum256(raw)
	return CandidateReceiptObservation{receiptJSON: string(body), controller: authority, requestSHA: hex.EncodeToString(expected[:]), commandSHA: hex.EncodeToString(commandDigest[:]), receivedAt: received, endpoint: CandidateReceiptEndpoint{Path: path, RunnerUID: profile.owner, ControllerUID: uint32(os.Geteuid()), ControllerGID: profile.group, RootController: profile.shared}}, nil
}
