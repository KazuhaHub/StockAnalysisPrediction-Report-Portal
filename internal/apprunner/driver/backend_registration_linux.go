//go:build linux

package driver

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
	"golang.org/x/sys/unix"
)

const backendRegistrationFilename = "registration.json"

// DeliverCandidateRegistration is infrastructure-only, after a durable journal
// unknown claim. It never overwrites or repairs an existing file. The broker
// directory is already root owned and mounted read-only in the running app.
func (r *CandidateRuntime) DeliverCandidateRegistration(ctx context.Context, launch CandidateLaunch, nativeID, bindingSHA string, document []byte) (BackendRegistrationObservation, error) {
	return r.candidateRegistration(ctx, launch, nativeID, bindingSHA, document, false)
}

// ObserveCandidateRegistration is the read-only uncertain-outcome path. Missing
// or changed files fail; this method never resumes or repeats a write.
func (r *CandidateRuntime) ObserveCandidateRegistration(ctx context.Context, launch CandidateLaunch, nativeID, bindingSHA string, document []byte) (BackendRegistrationObservation, error) {
	return r.candidateRegistration(ctx, launch, nativeID, bindingSHA, document, true)
}
func (r *CandidateRuntime) candidateRegistration(ctx context.Context, launch CandidateLaunch, nativeID, bindingSHA string, document []byte, observeOnly bool) (BackendRegistrationObservation, error) {
	var zero BackendRegistrationObservation
	if r == nil || os.Geteuid() != 0 || launch.state == nil || launch.state.owner != r || !containerSHA.MatchString(nativeID) || !containerSHA.MatchString(bindingSHA) || len(document) == 0 || len(document) > 4096 {
		return zero, ErrContainerUncertain
	}
	document = bytes.Clone(document)
	defer clear(document)
	registration, err := appplatform.ParseBackendRegistration(document)
	if err != nil {
		return zero, ErrContainerUncertain
	}
	defer registration.Close()
	metadata, err := registration.Metadata()
	if err != nil {
		return zero, ErrContainerUncertain
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	s := launch.state
	s.mu.Lock()
	defer s.mu.Unlock()
	if r.closed || s.closed || s.used {
		return zero, ErrContainerUncertain
	}
	i, m := s.snapshot.Identity, metadata.Identity
	if m.DeploymentID != i.DeploymentID || m.ControllerEpoch != i.ControllerEpoch || m.InstallationID != i.InstallationID || m.Generation != i.Generation || m.Generation != "1" || m.PolicyRevision != "1" || m.ReleaseSHA256 != i.ReleaseSHA256 {
		return zero, ErrContainerUncertain
	}
	// Descriptor/application binding is checked by the signed journal dispatcher;
	// the native runtime independently checks all managed identity dimensions.
	check := func() error {
		if err := s.pins.validate(ctx, s.plan); err != nil {
			return err
		}
		if err := r.checkData(ctx, i, s.snapshot.Data); err != nil {
			return err
		}
		client, closeClient := r.client()
		defer closeClient()
		state, err := inspectCandidateContainerHTTP(ctx, client, nativeID, s.plan)
		if err != nil || state != "running" {
			return ErrContainerUncertain
		}
		return ctx.Err()
	}
	if err := check(); err != nil {
		return zero, err
	}
	path := filepath.Join(r.roots.Sockets, i.InstallationID, i.Generation, "broker")
	// Open relative to the pinned directory. Fresh path/pin checks before and after
	// IO also detect replacement, rather than writing through an untrusted path.
	var parent int = -1
	for _, pin := range s.pins.dirs {
		if pin.path == path {
			parent = int(pin.file.Fd())
			break
		}
	}
	if parent < 0 {
		return zero, ErrUnsafeSocket
	}
	dir, err := unix.Openat(parent, ".", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return zero, ErrUnsafeSocket
	}
	defer unix.Close(dir)
	var st unix.Stat_t
	if !observeOnly {
		if err := createBackendRegistrationFile(ctx, dir, document, bindingSHA, 65532, 65532); err != nil {
			return zero, ErrContainerUncertain
		}
	}
	st, err = observeBackendRegistrationFile(ctx, dir, document, bindingSHA, 65532, 65532)
	if err != nil {
		return zero, ErrContainerUncertain
	}
	if err := check(); err != nil {
		return zero, err
	}
	return BackendRegistrationObservation{snapshot: BackendRegistrationSnapshot{Launch: s.snapshot, NativeID: nativeID, BindingSHA256: bindingSHA, DocumentSHA256: containerHash(document), Device: uint64(st.Dev), Inode: st.Ino, ObservedMS: time.Now().UnixMilli()}, valid: true}, nil
}

// Anonymous temporary files keep partially written/chowned secret bytes out of
// the app's read-only directory. linkat publishes once, atomically without replace.
func createBackendRegistrationFile(ctx context.Context, parent int, document []byte, bindingSHA string, uid, gid int) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if len(document) == 0 || len(document) > 4096 || !containerSHA.MatchString(bindingSHA) {
		return ErrInvalidResponse
	}
	fd, err := unix.Openat(parent, ".", unix.O_TMPFILE|unix.O_RDWR|unix.O_CLOEXEC, 0600)
	if err != nil {
		return ErrUnsafeSocket
	}
	file := os.NewFile(uintptr(fd), "private-registration")
	defer file.Close()
	if n, err := file.Write(document); err != nil || n != len(document) {
		return ErrContainerUncertain
	}
	if unix.Fsetxattr(fd, "user.portal.registration", []byte(bindingSHA), unix.XATTR_CREATE) != nil {
		return ErrContainerUncertain
	}
	if unix.Fchown(fd, uid, gid) != nil || unix.Fchmod(fd, 0400) != nil || unix.Fsync(fd) != nil {
		return ErrContainerUncertain
	}
	var st unix.Stat_t
	if unix.Fstat(fd, &st) != nil || st.Mode&unix.S_IFMT != unix.S_IFREG || st.Mode&07777 != 0400 || st.Uid != uint32(uid) || st.Gid != uint32(gid) || st.Nlink != 0 || st.Size != int64(len(document)) {
		return ErrUnsafeSocket
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if unix.Linkat(fd, "", parent, backendRegistrationFilename, unix.AT_EMPTY_PATH) != nil {
		return ErrContainerUncertain
	}
	if unix.Fsync(parent) != nil {
		return ErrContainerUncertain
	}
	return ctx.Err()
}
func observeBackendRegistrationFile(ctx context.Context, parent int, document []byte, bindingSHA string, uid, gid int) (unix.Stat_t, error) {
	var zero unix.Stat_t
	if err := ctx.Err(); err != nil {
		return zero, err
	}
	if len(document) == 0 || len(document) > 4096 || !containerSHA.MatchString(bindingSHA) {
		return zero, ErrInvalidResponse
	}
	fd, err := unix.Openat(parent, backendRegistrationFilename, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err != nil {
		return zero, ErrUnsafeSocket
	}
	file := os.NewFile(uintptr(fd), "private-registration")
	defer file.Close()
	var before, after, path unix.Stat_t
	if unix.Fstat(fd, &before) != nil || before.Mode&unix.S_IFMT != unix.S_IFREG || before.Mode&07777 != 0400 || before.Uid != uint32(uid) || before.Gid != uint32(gid) || before.Nlink != 1 || before.Size != int64(len(document)) {
		return zero, ErrUnsafeSocket
	}
	binding := make([]byte, 65)
	n, err := unix.Fgetxattr(fd, "user.portal.registration", binding)
	if err != nil || n != 64 || string(binding[:n]) != bindingSHA {
		return zero, ErrUnsafeSocket
	}
	actual, err := io.ReadAll(io.LimitReader(file, 4097))
	defer clear(actual)
	if err != nil || !bytes.Equal(actual, document) || unix.Fstat(fd, &after) != nil || !sameBackendRegistrationStat(before, after) || unix.Fstatat(parent, backendRegistrationFilename, &path, unix.AT_SYMLINK_NOFOLLOW) != nil || !sameBackendRegistrationStat(path, after) {
		return zero, ErrUnsafeSocket
	}
	return after, ctx.Err()
}

func sameBackendRegistrationStat(a, b unix.Stat_t) bool {
	return mountStatEqual(a, b) && a.Nlink == b.Nlink && a.Size == b.Size && a.Mtim == b.Mtim && a.Ctim == b.Ctim
}
