//go:build linux

package transport

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/control"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/journal"
)

var ErrUnsafeEndpoint = errors.New("unsafe runner control endpoint")
var ErrRefused = errors.New("runner intent was refused or acknowledgement unavailable")

const sessionTimeout = 3 * time.Second
const requestMagic = "PRCI0001"
const replyMagic = "PRCA0001"

type admitter interface {
	AdmitControlCommand(context.Context, []byte, []byte) (journal.CommandAdmission, error)
}

// Server owns one explicitly configured local endpoint. Separate controller
// identity provisioning must be performed outside this service.
// UID authentication supplements signatures; neither makes app processes trusted.
type Server struct {
	started            atomic.Bool
	closeOnce          sync.Once
	listener           *net.UnixListener
	path               string
	inode              os.FileInfo
	controllerUID      uint32
	journal            admitter
	dataExecution      *dataExecutionProvider
	candidateExecution *candidateExecutionProvider
}

type endpointProfile struct {
	owner      uint32
	group      uint32
	parentMode os.FileMode
	socketMode os.FileMode
	shared     bool
}

func privateProfile(owner uint32) endpointProfile {
	return endpointProfile{owner: owner, parentMode: 0700, socketMode: 0600}
}
func controllerProfile(group uint32) endpointProfile {
	return endpointProfile{owner: 0, group: group, parentMode: 0750, socketMode: 0660, shared: true}
}
func validController(uid, gid uint32) bool {
	return uid != 0 && gid != 0 && uid != ^uint32(0) && gid != ^uint32(0)
}

func safeParent(path string, profile endpointProfile) bool {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path || len(path) > 107 || strings.ContainsAny(path, "\x00\r\n") {
		return false
	}
	parent := filepath.Dir(path)
	canonical, err := filepath.EvalSymlinks(parent)
	if err != nil || canonical != parent {
		return false
	}
	for dir := parent; ; dir = filepath.Dir(dir) {
		info, err := os.Lstat(dir)
		if err != nil || !info.IsDir() {
			return false
		}
		st, ok := info.Sys().(*syscall.Stat_t)
		if !ok {
			return false
		}
		if dir == parent {
			if st.Uid != profile.owner || info.Mode().Perm() != profile.parentMode || (profile.shared && st.Gid != profile.group) {
				return false
			}
		} else if st.Uid != 0 && st.Uid != profile.owner {
			return false
		}
		if info.Mode().Perm()&0022 != 0 && !(st.Uid == 0 && info.Mode()&os.ModeSticky != 0) {
			return false
		}
		if dir == "/" {
			return true
		}
	}
}
func socketInfo(path string, profile endpointProfile) (os.FileInfo, error) {
	if !safeParent(path, profile) {
		return nil, ErrUnsafeEndpoint
	}
	info, err := os.Lstat(path)
	if err != nil {
		return nil, ErrUnsafeEndpoint
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok || info.Mode()&os.ModeSocket == 0 || info.Mode().Perm() != profile.socketMode || st.Uid != profile.owner || (profile.shared && st.Gid != profile.group) || st.Nlink != 1 {
		return nil, ErrUnsafeEndpoint
	}
	return info, nil
}
func peerUID(conn *net.UnixConn, uid uint32) bool {
	raw, err := conn.SyscallConn()
	if err != nil {
		return false
	}
	var cred *syscall.Ucred
	var inner error
	err = raw.Control(func(fd uintptr) {
		cred, inner = syscall.GetsockoptUcred(int(fd), syscall.SOL_SOCKET, syscall.SO_PEERCRED)
	})
	return err == nil && inner == nil && cred != nil && cred.Uid == uid
}

// Listen refuses pre-existing paths and never changes a parent directory, process
// umask or trust record. The 0700 parent protects creation before socket chmod.
func Listen(path string, controllerUID uint32, store admitter) (*Server, error) {
	return listenProfile(path, controllerUID, store, privateProfile(uint32(os.Geteuid())))
}

// ListenController requires a root runner, a non-root controller UID and dedicated
// non-root endpoint GID. The existing parent must be root:GID 0750; only the socket
// created here is set to root:GID 0660. Group membership is never authentication:
// every connection must also match controllerUID and carry a valid signed intent.
// No user/group/directory is created and no production startup hook calls this.
func ListenController(path string, controllerUID, controllerGID uint32, store admitter) (*Server, error) {
	if os.Geteuid() != 0 || !validController(controllerUID, controllerGID) {
		return nil, ErrUnsafeEndpoint
	}
	return listenProfile(path, controllerUID, store, controllerProfile(controllerGID))
}
func listenProfile(path string, controllerUID uint32, store admitter, profile endpointProfile) (*Server, error) {
	if store == nil || !safeParent(path, profile) {
		return nil, ErrUnsafeEndpoint
	}
	if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
		return nil, ErrUnsafeEndpoint
	}
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		return nil, ErrUnsafeEndpoint
	}
	listener.SetUnlinkOnClose(false)
	// Capture identity before chmod so failure cleanup cannot remove a replacement.
	info, err := os.Lstat(path)
	cleanup := func() {
		listener.Close()
		current, e := os.Lstat(path)
		if e == nil && info != nil && os.SameFile(info, current) {
			os.Remove(path)
		}
	}
	if err != nil {
		cleanup()
		return nil, ErrUnsafeEndpoint
	}
	if profile.shared {
		if err = os.Chown(path, 0, int(profile.group)); err != nil {
			cleanup()
			return nil, ErrUnsafeEndpoint
		}
	}
	if err = os.Chmod(path, profile.socketMode); err != nil {
		cleanup()
		return nil, ErrUnsafeEndpoint
	}
	after, err := socketInfo(path, profile)
	if err != nil || !os.SameFile(info, after) {
		cleanup()
		return nil, ErrUnsafeEndpoint
	}
	return &Server{listener: listener, path: path, inode: after, controllerUID: controllerUID, journal: store}, nil
}

// Close stops acceptance and removes only the socket inode created by Listen.
// Callers should cancel Serve's context to stop in-flight journal operations too.
func (s *Server) Close() {
	s.closeOnce.Do(func() {
		s.listener.Close()
		if info, err := os.Lstat(s.path); err == nil && os.SameFile(s.inode, info) {
			os.Remove(s.path)
		}
	})
}

// Serve runs at most eight sessions at once, dropping excess connections. It must
// be called once. Cancellation closes the listener and active sessions and waits
// for journal calls to return. A lost acknowledgement is an ambiguous outcome.
func (s *Server) Serve(ctx context.Context) error {
	if !s.started.CompareAndSwap(false, true) {
		return ErrUnsafeEndpoint
	}
	stop := context.AfterFunc(ctx, s.Close)
	defer stop()
	defer s.Close()
	var workers sync.WaitGroup
	defer workers.Wait()
	slots := make(chan struct{}, 8)
	for {
		conn, err := s.listener.AcceptUnix()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return ErrUnsafeEndpoint
		}
		select {
		case slots <- struct{}{}:
		default:
			conn.Close()
			continue
		}
		workers.Add(1)
		go func() { defer workers.Done(); defer func() { <-slots }(); defer conn.Close(); s.handle(ctx, conn) }()
	}
}
func (s *Server) handle(parent context.Context, conn *net.UnixConn) {
	started := time.Now()
	// Authenticate the kernel peer before reading even the frame header.
	if !peerUID(conn, s.controllerUID) {
		return
	}
	ctx, cancel := context.WithTimeout(parent, sessionTimeout)
	defer cancel()
	deadline, _ := ctx.Deadline()
	conn.SetDeadline(deadline)
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()
	header := make([]byte, 12)
	if _, err := io.ReadFull(conn, header[:8]); err != nil {
		return
	}
	switch string(header[:8]) {
	case backendRequestQueryMagic:
		if s.candidateExecution == nil || ctx.Err() != nil || !stop() {
			return
		}
		cancel()
		executionCtx, executionCancel := context.WithDeadline(parent, started.Add(backendRequestTimeout))
		defer executionCancel()
		executionDeadline, _ := executionCtx.Deadline()
		conn.SetDeadline(executionDeadline)
		executionStop := context.AfterFunc(executionCtx, func() { conn.Close() })
		defer executionStop()
		s.handleBackendRequest(executionCtx, conn)
		return
	case writableBackendHandshakeQueryMagic:
		s.handleWritableBackendHandshake(ctx, conn)
		return
	case backendHandshakeQueryMagic:
		s.handleBackendHandshake(ctx, conn)
		return
	case backendRegistrationQueryMagic:
		if s.candidateExecution == nil || ctx.Err() != nil || !stop() {
			return
		}
		cancel()
		executionCtx, executionCancel := context.WithDeadline(parent, started.Add(candidateExecutionTimeout))
		defer executionCancel()
		executionDeadline, _ := executionCtx.Deadline()
		conn.SetDeadline(executionDeadline)
		executionStop := context.AfterFunc(executionCtx, func() { conn.Close() })
		defer executionStop()
		s.handleBackendRegistration(executionCtx, conn)
		return
	case candidateReadinessQueryMagic:
		s.handleCandidateResponseKind(ctx, conn, false, true)
		return
	case candidateExecutionQueryMagic:
		if s.candidateExecution == nil || ctx.Err() != nil || !stop() {
			return
		}
		cancel()
		executionCtx, executionCancel := context.WithDeadline(parent, started.Add(candidateExecutionTimeout))
		defer executionCancel()
		executionDeadline, _ := executionCtx.Deadline()
		conn.SetDeadline(executionDeadline)
		executionStop := context.AfterFunc(executionCtx, func() { conn.Close() })
		defer executionStop()
		s.handleCandidateExecution(executionCtx, conn)
		return

	case managedRuntimeQueryMagic:
		s.handleManagedRuntime(ctx, conn)
		return
	case enrollmentQueryMagic:
		s.handleEnrollment(ctx, conn)
		return
	case dataExecutionQueryMagic:
		s.handleDataExecution(ctx, conn)
		return
	case candidateReceiptQueryMagic:
		s.handleCandidateReceipt(ctx, conn)
		return
	case dataReceiptQueryMagic:
		s.handleDataReceipt(ctx, conn)
		return
	case requestMagic:
	default:
		return
	}
	if _, err := io.ReadFull(conn, header[8:]); err != nil {
		return
	}
	size := binary.BigEndian.Uint32(header[8:])
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
	digest := sha256.New()
	digest.Write(header)
	digest.Write(body)
	status := byte(0)
	if _, err := s.journal.AdmitControlCommand(ctx, body[:size], body[size:]); err == nil {
		status = 1
	}
	// No error details, secret, fence, ready flag or physical receipt crosses here.
	reply := append([]byte(replyMagic), digest.Sum(nil)...)
	reply = append(reply, status)
	io.Copy(conn, bytes.NewReader(reply))
}
func requestFrame(raw, sig []byte) []byte {
	frame := make([]byte, 12, 12+len(raw)+len(sig))
	copy(frame, requestMagic)
	binary.BigEndian.PutUint32(frame[8:], uint32(len(raw)))
	frame = append(frame, raw...)
	return append(frame, sig...)
}

// Exchange submits exactly one intent to a pinned private endpoint. Any error may
// follow a committed intent; callers must reconcile stable identity, not dispatch
// or blindly retry. The response is local and not an independently signed receipt.
func Exchange(parent context.Context, path string, runnerUID uint32, raw, sig []byte) error {
	return exchangeProfile(parent, path, privateProfile(runnerUID), raw, sig)
}

// ExchangeController pins a root runner endpoint with the configured dedicated
// GID and requires the caller to run as the configured non-root controller UID.
// A matching path owner/group never replaces the server SO_PEERCRED check.
func ExchangeController(parent context.Context, path string, controllerUID, controllerGID uint32, raw, sig []byte) error {
	if !validController(controllerUID, controllerGID) || uint32(os.Geteuid()) != controllerUID {
		return ErrUnsafeEndpoint
	}
	return exchangeProfile(parent, path, controllerProfile(controllerGID), raw, sig)
}
func exchangeProfile(parent context.Context, path string, profile endpointProfile, raw, sig []byte) error {
	if len(raw) == 0 || len(raw) > control.MaxCommandBytes || len(sig) != 64 {
		return ErrRefused
	}
	ctx, cancel := context.WithTimeout(parent, sessionTimeout)
	defer cancel()
	conn, err := connectEndpoint(ctx, path, profile)
	if err != nil {
		return err
	}
	defer conn.Close()
	deadline, _ := ctx.Deadline()
	conn.SetDeadline(deadline)
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()
	frame := requestFrame(raw, sig)
	if _, err := io.Copy(conn, bytes.NewReader(frame)); err != nil {
		return ErrRefused
	}
	if err := conn.CloseWrite(); err != nil {
		return ErrRefused
	}
	var reply [41]byte
	if _, err := io.ReadFull(conn, reply[:]); err != nil {
		return ErrRefused
	}
	var extra [1]byte
	if n, err := conn.Read(extra[:]); n != 0 || err != io.EOF {
		return ErrRefused
	}
	expected := sha256.Sum256(frame)
	if string(reply[:8]) != replyMagic || !bytes.Equal(reply[8:40], expected[:]) || reply[40] != 1 {
		return ErrRefused
	}
	return nil
}

func connectEndpoint(ctx context.Context, path string, profile endpointProfile) (*net.UnixConn, error) {
	before, err := socketInfo(path, profile)
	if err != nil {
		return nil, ErrUnsafeEndpoint
	}
	connection, err := (&net.Dialer{}).DialContext(ctx, "unix", path)
	if err != nil {
		return nil, ErrRefused
	}
	conn, ok := connection.(*net.UnixConn)
	if !ok {
		connection.Close()
		return nil, ErrUnsafeEndpoint
	}
	after, err := socketInfo(path, profile)
	if err != nil || !os.SameFile(before, after) || !peerUID(conn, profile.owner) {
		conn.Close()
		return nil, ErrUnsafeEndpoint
	}
	return conn, nil
}
