//go:build linux

package transport

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/json"
	"io"
	"net"
	"os"
	"strconv"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/control"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/journal"
)

const managedRuntimeQueryMagic = "PRMQ0001"
const managedRuntimeReplyMagic = "PRMP0001"

func (s *Server) handleManagedRuntime(ctx context.Context, conn *net.UnixConn) {
	d, c := s.dataExecution, s.candidateExecution
	if d == nil || c == nil || d.store != c.store || s.journal != d.store {
		return
	}
	var nonce [32]byte
	if _, err := io.ReadFull(conn, nonce[:]); err != nil {
		return
	}
	var extra [1]byte
	if n, err := conn.Read(extra[:]); n != 0 || err != io.EOF {
		return
	}
	before, err := d.store.ControllerDeployment(ctx)
	if err != nil {
		return
	}
	config, err := c.native.ObserveSharedConfiguration(ctx, d.root, d.pin)
	if err != nil {
		return
	}
	after, err := d.store.ControllerDeployment(ctx)
	if err != nil {
		return
	}
	first, err := encodeEnrollment(before.ControllerEnrollment)
	if err != nil {
		return
	}
	second, err := encodeEnrollment(after.ControllerEnrollment)
	if err != nil || !bytes.Equal(first, second) || before.Revision != after.Revision || before.UpdatedMS != after.UpdatedMS {
		return
	}
	var controller enrollmentWire
	if json.Unmarshal(second, &controller) != nil {
		return
	}
	w := managedRuntimeWire{Format: "portal.runner.managed-profile/1-dev.1", Controller: controller, Profile: ManagedRuntimeProfile{Protocol: ManagedRunnerProtocol, Platform: config.Platform, BackendProtocol: ManagedBackendProtocol, SandboxProfile: config.SandboxProfile, WritableSandboxProfile: control.WritableCandidateSandboxProfile, RootFilesystemUUID: config.Root.FilesystemUUID, RootInode: strconv.FormatUint(config.Root.Inode, 10)}, ObservedMS: strconv.FormatInt(time.Now().UnixMilli(), 10)}
	raw, err := json.Marshal(w)
	if err != nil || ctx.Err() != nil {
		return
	}
	if _, _, err := decodeManagedRuntime(raw); err != nil {
		return
	}
	reply, err := buildReplyFrame(managedRuntimeReplyMagic, nonce[:], raw)
	if err != nil {
		return
	}
	io.Copy(conn, bytes.NewReader(reply))
}

// ObserveControllerManagedRuntime requires configured public authority and the
// protected root/non-root-controller endpoint. It reads no app data and executes
// no command; the resulting profile explicitly does not grant execution readiness.
func ObserveControllerManagedRuntime(ctx context.Context, path string, uid, gid uint32, authority control.Authority) (ManagedRuntimeObservation, error) {
	if !validController(uid, gid) || uint32(os.Geteuid()) != uid {
		return ManagedRuntimeObservation{}, ErrUnsafeEndpoint
	}
	return observeManagedRuntime(ctx, path, controllerProfile(gid), authority)
}
func observeManagedRuntime(parent context.Context, path string, profile endpointProfile, authority control.Authority) (ManagedRuntimeObservation, error) {
	var zero ManagedRuntimeObservation
	if parent.Err() != nil {
		return zero, ErrManagedRuntimeObservation
	}
	authority.PublicKey = bytes.Clone(authority.PublicKey)
	if _, err := encodeEnrollment(journal.ControllerEnrollment{DeploymentID: authority.DeploymentID, ControllerEpoch: authority.ControllerEpoch, ControlRevision: authority.ControlRevision, PublicKey: authority.PublicKey, Fingerprint: authority.Fingerprint}); err != nil {
		return zero, ErrManagedRuntimeObservation
	}
	started := time.Now()
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
	frame := make([]byte, 40)
	copy(frame, managedRuntimeQueryMagic)
	if _, err := rand.Read(frame[8:]); err != nil {
		return zero, ErrManagedRuntimeObservation
	}
	if _, err := io.Copy(conn, bytes.NewReader(frame)); err != nil {
		return zero, ErrManagedRuntimeObservation
	}
	if err := conn.CloseWrite(); err != nil {
		return zero, ErrManagedRuntimeObservation
	}
	var header [44]byte
	if _, err := io.ReadFull(conn, header[:]); err != nil {
		return zero, ErrManagedRuntimeObservation
	}
	size := binary.BigEndian.Uint32(header[40:])
	if string(header[:8]) != managedRuntimeReplyMagic || !bytes.Equal(header[8:40], frame[8:]) || size == 0 || size > 4096 {
		return zero, ErrManagedRuntimeObservation
	}
	raw := make([]byte, int(size))
	if _, err := io.ReadFull(conn, raw); err != nil {
		return zero, ErrManagedRuntimeObservation
	}
	var extra [1]byte
	if n, err := conn.Read(extra[:]); n != 0 || err != io.EOF {
		return zero, ErrManagedRuntimeObservation
	}
	wire, current, err := decodeManagedRuntime(raw)
	if err != nil || !sameManagedAuthority(authority, current) {
		return zero, ErrManagedRuntimeObservation
	}
	observed, _ := strconv.ParseInt(wire.ObservedMS, 10, 64)
	received := time.Now()
	if ctx.Err() != nil || received.Before(started) || observed < started.UnixMilli() || observed > received.UnixMilli() {
		return zero, ErrManagedRuntimeObservation
	}
	return ManagedRuntimeObservation{profile: wire.Profile, authority: current, receivedAt: received, endpoint: CandidateReceiptEndpoint{Path: path, RunnerUID: profile.owner, ControllerUID: uint32(os.Geteuid()), ControllerGID: profile.group, RootController: profile.shared}}, nil
}
