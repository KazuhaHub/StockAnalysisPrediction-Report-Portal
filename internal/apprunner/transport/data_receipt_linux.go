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

const dataReceiptQueryMagic = "PRCR0001"
const dataReceiptReplyMagic = "PRCP0001"
const maxDataReceiptBytes = 16384

type dataReceiptReader interface {
	ReadDataPreparationReceipt(context.Context, []byte, []byte) (journal.DataPreparationReceipt, error)
}

func (s *Server) handleDataReceipt(ctx context.Context, conn *net.UnixConn) {
	reader, ok := s.journal.(dataReceiptReader)
	if !ok {
		return
	}
	header := make([]byte, 44)
	copy(header, dataReceiptQueryMagic)
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
	var extra [1]byte
	if n, err := conn.Read(extra[:]); n != 0 || err != io.EOF {
		return
	}
	receipt, err := reader.ReadDataPreparationReceipt(ctx, body[:size], body[size:])
	if err != nil {
		return
	}
	encoded, err := json.Marshal(receipt)
	if err != nil {
		return
	}
	if _, err := journal.ParseDataPreparationReceipt(encoded); err != nil {
		return
	}
	digest := sha256.New()
	digest.Write(header)
	digest.Write(body)
	reply := make([]byte, 44, 44+len(encoded))
	copy(reply, dataReceiptReplyMagic)
	copy(reply[8:40], digest.Sum(nil))
	binary.BigEndian.PutUint32(reply[40:44], uint32(len(encoded)))
	reply = append(reply, encoded...)
	io.Copy(conn, bytes.NewReader(reply))
}

// ReadDataReceipt uses the private same-user development endpoint. authority is
// independently trusted controller configuration, never supplied by app code.
func ReadDataReceipt(ctx context.Context, path string, runnerUID uint32, raw, signature []byte, authority control.Authority) (DataReceiptObservation, error) {
	return readDataReceipt(ctx, path, privateProfile(runnerUID), raw, signature, authority)
}

// ReadControllerDataReceipt uses the provisioned root endpoint and exact non-root
// controller UID/group. It performs no enrollment, dispatch, repair or startup.
func ReadControllerDataReceipt(ctx context.Context, path string, controllerUID, controllerGID uint32, raw, signature []byte, authority control.Authority) (DataReceiptObservation, error) {
	if !validController(controllerUID, controllerGID) || uint32(os.Geteuid()) != controllerUID {
		return DataReceiptObservation{}, ErrUnsafeEndpoint
	}
	return readDataReceipt(ctx, path, controllerProfile(controllerGID), raw, signature, authority)
}
func readDataReceipt(parent context.Context, path string, profile endpointProfile, raw, signature []byte, authority control.Authority) (DataReceiptObservation, error) {
	var zero DataReceiptObservation
	if len(raw) == 0 || len(raw) > control.MaxCommandBytes || len(signature) != 64 || len(authority.PublicKey) != 32 || parent.Err() != nil {
		return zero, ErrDataReceiptObservation
	}
	raw, signature, authority.PublicKey = bytes.Clone(raw), bytes.Clone(signature), bytes.Clone(authority.PublicKey)
	started := time.Now()
	verified, err := control.Verify(raw, signature, authority, started.UnixMilli())
	if err != nil {
		return zero, ErrDataReceiptObservation
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
	frame := make([]byte, 44, 44+len(raw)+64)
	copy(frame, dataReceiptQueryMagic)
	if _, err := rand.Read(frame[8:40]); err != nil {
		return zero, ErrDataReceiptObservation
	}
	binary.BigEndian.PutUint32(frame[40:44], uint32(len(raw)))
	frame = append(frame, raw...)
	frame = append(frame, signature...)
	if _, err := io.Copy(conn, bytes.NewReader(frame)); err != nil {
		return zero, ErrDataReceiptObservation
	}
	if err := conn.CloseWrite(); err != nil {
		return zero, ErrDataReceiptObservation
	}
	var header [44]byte
	if _, err := io.ReadFull(conn, header[:]); err != nil {
		return zero, ErrDataReceiptObservation
	}
	expected := sha256.Sum256(frame)
	size := binary.BigEndian.Uint32(header[40:44])
	if string(header[:8]) != dataReceiptReplyMagic || !bytes.Equal(header[8:40], expected[:]) || size == 0 || size > maxDataReceiptBytes {
		return zero, ErrDataReceiptObservation
	}
	body := make([]byte, int(size))
	if _, err := io.ReadFull(conn, body); err != nil {
		return zero, ErrDataReceiptObservation
	}
	var extra [1]byte
	if n, err := conn.Read(extra[:]); n != 0 || err != io.EOF {
		return zero, ErrDataReceiptObservation
	}
	receipt, err := journal.ParseDataPreparationReceipt(body)
	if err != nil || !receipt.MatchesCommand(verified.Command()) {
		return zero, ErrDataReceiptObservation
	}
	received := time.Now()
	observed, _ := strconv.ParseInt(receipt.ObservedMS, 10, 64)
	if ctx.Err() != nil || received.Before(started) || received.UnixMilli() < started.UnixMilli() || observed > received.UnixMilli() {
		return zero, ErrDataReceiptObservation
	}
	if _, err := control.Verify(raw, signature, authority, received.UnixMilli()); err != nil {
		return zero, ErrDataReceiptObservation
	}
	commandDigest := sha256.Sum256(raw)
	return DataReceiptObservation{receipt: receipt, controller: authority, requestSHA: hex.EncodeToString(expected[:]), commandSHA: hex.EncodeToString(commandDigest[:]), receivedAt: received, endpoint: DataReceiptEndpoint{Path: path, RunnerUID: profile.owner, ControllerUID: uint32(os.Geteuid()), ControllerGID: profile.group, RootController: profile.shared}}, nil
}
