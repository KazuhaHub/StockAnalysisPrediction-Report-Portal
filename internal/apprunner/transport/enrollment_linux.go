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

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/journal"
)

const enrollmentQueryMagic = "PRCQ0001"
const enrollmentReplyMagic = "PRCO0001"

type enrollmentReader interface {
	ControllerDeployment(context.Context) (journal.Deployment, error)
}

func (s *Server) handleEnrollment(ctx context.Context, conn *net.UnixConn) {
	var nonce [32]byte
	if _, err := io.ReadFull(conn, nonce[:]); err != nil {
		return
	}
	var extra [1]byte
	if n, err := conn.Read(extra[:]); n != 0 || err != io.EOF {
		return
	}
	reader, ok := s.journal.(enrollmentReader)
	if !ok {
		return
	}
	deployment, err := reader.ControllerDeployment(ctx)
	if err != nil {
		return
	}
	raw, err := encodeEnrollment(deployment.ControllerEnrollment)
	if err != nil {
		return
	}
	reply := make([]byte, 44, 44+len(raw))
	copy(reply, enrollmentReplyMagic)
	copy(reply[8:40], nonce[:])
	binary.BigEndian.PutUint32(reply[40:44], uint32(len(raw)))
	reply = append(reply, raw...)
	io.Copy(conn, bytes.NewReader(reply))
}

// ObserveEnrollment is the private same-user development profile. It never
// enrolls a key or interprets a journal identity as native compatibility proof.
func ObserveEnrollment(ctx context.Context, path string, runnerUID uint32) (EnrollmentObservation, error) {
	return observeEnrollment(ctx, path, privateProfile(runnerUID))
}

// ObserveControllerEnrollment requires the separately provisioned root endpoint
// and exact non-root local controller UID/group profile. The OS server UID is
// verified before sending the nonce; no browser-selected endpoint is supported.
func ObserveControllerEnrollment(ctx context.Context, path string, controllerUID, controllerGID uint32) (EnrollmentObservation, error) {
	if !validController(controllerUID, controllerGID) || uint32(os.Geteuid()) != controllerUID {
		return EnrollmentObservation{}, ErrUnsafeEndpoint
	}
	return observeEnrollment(ctx, path, controllerProfile(controllerGID))
}
func observeEnrollment(parent context.Context, path string, profile endpointProfile) (EnrollmentObservation, error) {
	var zero EnrollmentObservation
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
		return zero, errEnrollmentObservation
	}
	request := append([]byte(enrollmentQueryMagic), nonce[:]...)
	if _, err := io.Copy(conn, bytes.NewReader(request)); err != nil {
		return zero, errEnrollmentObservation
	}
	if err := conn.CloseWrite(); err != nil {
		return zero, errEnrollmentObservation
	}
	var header [44]byte
	if _, err := io.ReadFull(conn, header[:]); err != nil || string(header[:8]) != enrollmentReplyMagic || !bytes.Equal(header[8:40], nonce[:]) {
		return zero, errEnrollmentObservation
	}
	size := binary.BigEndian.Uint32(header[40:44])
	if size == 0 || size > maxEnrollmentBytes {
		return zero, errEnrollmentObservation
	}
	raw := make([]byte, int(size))
	if _, err := io.ReadFull(conn, raw); err != nil {
		return zero, errEnrollmentObservation
	}
	var extra [1]byte
	if n, err := conn.Read(extra[:]); n != 0 || err != io.EOF {
		return zero, errEnrollmentObservation
	}
	controller, err := decodeEnrollment(raw)
	if err != nil {
		return zero, errEnrollmentObservation
	}
	if ctx.Err() != nil {
		return zero, errEnrollmentObservation
	}
	return EnrollmentObservation{controller: controller, receivedAt: time.Now()}, nil
}
