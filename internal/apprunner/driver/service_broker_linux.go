//go:build linux

package driver

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sync"

	"golang.org/x/sys/unix"
)

// BindServiceBroker, BindServiceBrokerUpdateStart and BindServiceBrokerWritable
// verify, for the three kinds of registered backend, that the container the
// session names is the one running and bound exactly as registered, using the
// same physical checks as a backend request. With listen they then bind the
// broker socket inside the pinned broker directory and return it. Without listen
// they only verify. They never start, stop, repair or change the container, and
// never touch the registration file.
func (r *CandidateRuntime) BindServiceBroker(ctx context.Context, launch CandidateLaunch, binding BackendRegistrationSnapshot, listen bool) (ServiceBroker, error) {
	var zero ServiceBroker
	if r == nil || launch.state == nil || launch.state.owner != r ||
		!containerSHA.MatchString(binding.NativeID) || !containerSHA.MatchString(binding.BindingSHA256) ||
		!containerSHA.MatchString(binding.DocumentSHA256) || binding.Inode == 0 {
		return zero, ErrContainerUncertain
	}
	r.mu.Lock()
	s := launch.state
	s.mu.Lock()
	if r.closed || s.closed || s.used || binding.Launch != s.snapshot {
		s.mu.Unlock()
		r.mu.Unlock()
		return zero, ErrContainerUncertain
	}
	r.mu.Unlock()
	defer s.mu.Unlock()
	if err := r.checkBackendBindingLocked(ctx, s, binding); err != nil {
		return zero, err
	}
	var broker ServiceBroker
	if listen {
		var err error
		broker, err = bindBrokerInPins(s.pins, r.roots.Sockets, s.snapshot.Identity.InstallationID, s.snapshot.Identity.Generation)
		if err != nil {
			return zero, err
		}
	}
	if err := r.checkBackendBindingLocked(ctx, s, binding); err != nil {
		closeBroker(broker)
		return zero, err
	}
	return broker, nil
}

func (r *CandidateRuntime) BindServiceBrokerUpdateStart(ctx context.Context, request UpdateStartLaunchRequest, expected UpdateStartLaunchSnapshot, registration UpdateStartRegistrationSnapshot, listen bool) (ServiceBroker, error) {
	var zero ServiceBroker
	if r == nil || expected != registration.Launch || !containerSHA.MatchString(registration.NativeID) ||
		!containerSHA.MatchString(registration.BindingSHA256) || !containerSHA.MatchString(registration.DocumentSHA256) || registration.Inode == 0 {
		return zero, ErrContainerUncertain
	}
	var broker ServiceBroker
	_, err := r.withVerifiedUpdateStartCandidate(ctx, request, expected, registration.NativeID, func(ctx context.Context, pins *candidateMountPins) error {
		if err := checkUpdateStartBackendFile(ctx, r, pins, registration); err != nil {
			return err
		}
		if listen {
			var err error
			broker, err = bindBrokerInPins(pins, r.roots.Sockets, request.Identity.InstallationID, request.Identity.Generation)
			if err != nil {
				return err
			}
		}
		return checkUpdateStartBackendFile(ctx, r, pins, registration)
	})
	if err != nil {
		closeBroker(broker)
		return zero, err
	}
	return broker, nil
}

func (r *CandidateRuntime) BindServiceBrokerWritable(ctx context.Context, request WritableCandidateLaunchRequest, receipt WritableBackendRegistrationSnapshot, listen bool) (ServiceBroker, error) {
	var zero ServiceBroker
	if r == nil || receipt.Inode == 0 || !containerSHA.MatchString(receipt.BindingSHA256) ||
		!containerSHA.MatchString(receipt.DocumentSHA256) || !containerSHA.MatchString(receipt.NativeID) {
		return zero, ErrContainerUncertain
	}
	var broker ServiceBroker
	_, err := r.withVerifiedWritableCandidate(ctx, request, receipt.Launch, receipt.NativeID, func(ctx context.Context, pins *candidateMountPins) error {
		if err := r.checkWritableBackendFile(ctx, pins, receipt); err != nil {
			return err
		}
		if listen {
			var err error
			broker, err = bindBrokerInPins(pins, r.roots.Sockets, request.Identity.InstallationID, request.Identity.Generation)
			if err != nil {
				return err
			}
		}
		return r.checkWritableBackendFile(ctx, pins, receipt)
	})
	if err != nil {
		closeBroker(broker)
		return zero, err
	}
	return broker, nil
}

func closeBroker(b ServiceBroker) {
	if b.Listener != nil {
		_ = b.Listener.Close()
	}
}

// bindBrokerInPins finds the pinned broker directory of the generation among the
// launch's pins, as the registration delivery does, and binds in it.
func bindBrokerInPins(pins *candidateMountPins, sockets, installation, generation string) (ServiceBroker, error) {
	path := filepath.Join(sockets, installation, generation, "broker")
	parent := -1
	for _, pin := range pins.dirs {
		if pin.path == path {
			parent = int(pin.file.Fd())
			break
		}
	}
	if parent < 0 {
		return ServiceBroker{}, ErrUnsafeSocket
	}
	listener, err := bindServiceBrokerSocket(parent, 0)
	if err != nil {
		return ServiceBroker{}, err
	}
	return ServiceBroker{Listener: listener, Intact: listener.intact}, nil
}

// serviceBrokerListener is the unix listener on the broker socket. It keeps its
// own descriptor of the pinned directory, so it can check and remove the socket
// by name relative to that directory however the path above it changes.
type serviceBrokerListener struct {
	net.Listener
	dir        int
	device     uint64
	inode      uint64
	closeOnce  sync.Once
	closeError error
}

// staleSocketRemovable reports whether what sits at the socket name is a stale
// socket of ours: a socket, owned by owner, with a single link.
func staleSocketRemovable(st *unix.Stat_t, owner uint32) bool {
	return st.Mode&unix.S_IFMT == unix.S_IFSOCK && st.Uid == owner && st.Nlink == 1
}

// bindServiceBrokerSocket binds ServiceBrokerSocketName inside the directory
// open at parent. The socket is created relative to the directory descriptor (no
// path resolution of an untrusted path), then made 0666: the application runs as
// another user and needs write permission to connect, and the parent directory,
// which is root-owned, read-only in the container and not writable by the
// application, is what protects the name. A stale socket of an earlier run is
// removed only if it is a socket owned by owner (root) with a single link;
// anything else at the name is refused, never replaced. The directory itself must
// be owned by owner and not writable by group or other.
func bindServiceBrokerSocket(parent int, owner uint32) (*serviceBrokerListener, error) {
	dir, err := unix.Openat(parent, ".", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, ErrUnsafeSocket
	}
	fail := func(err error) (*serviceBrokerListener, error) {
		_ = unix.Close(dir)
		return nil, err
	}
	// The directory is what protects the name, so it must be the expected owner's
	// and closed to group and other writers before anything is bound in it.
	var dirStat unix.Stat_t
	if unix.Fstat(dir, &dirStat) != nil || dirStat.Mode&unix.S_IFMT != unix.S_IFDIR || dirStat.Uid != owner || dirStat.Mode&0022 != 0 {
		return fail(ErrUnsafeSocket)
	}
	var st unix.Stat_t
	switch err := unix.Fstatat(dir, ServiceBrokerSocketName, &st, unix.AT_SYMLINK_NOFOLLOW); {
	case err == nil:
		if !staleSocketRemovable(&st, owner) {
			return fail(ErrUnsafeSocket)
		}
		if err := unix.Unlinkat(dir, ServiceBrokerSocketName, 0); err != nil {
			return fail(ErrUnsafeSocket)
		}
	case !errors.Is(err, unix.ENOENT):
		return fail(ErrUnsafeSocket)
	}
	fd, err := unix.Socket(unix.AF_UNIX, unix.SOCK_STREAM|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		return fail(ErrUnsafeSocket)
	}
	// bind(2) takes a path; the descriptor's /proc entry resolves to the pinned
	// directory itself, whatever its path is now.
	if err := unix.Bind(fd, &unix.SockaddrUnix{Name: fmt.Sprintf("/proc/self/fd/%d/%s", dir, ServiceBrokerSocketName)}); err != nil {
		_ = unix.Close(fd)
		return fail(ErrUnsafeSocket)
	}
	cleanup := func(err error) (*serviceBrokerListener, error) {
		_ = unix.Close(fd)
		_ = unix.Unlinkat(dir, ServiceBrokerSocketName, 0)
		return fail(err)
	}
	if err := unix.Fchmodat(dir, ServiceBrokerSocketName, 0666, 0); err != nil {
		return cleanup(ErrUnsafeSocket)
	}
	if err := unix.Listen(fd, 16); err != nil {
		return cleanup(ErrUnsafeSocket)
	}
	var bound unix.Stat_t
	if unix.Fstatat(dir, ServiceBrokerSocketName, &bound, unix.AT_SYMLINK_NOFOLLOW) != nil || bound.Mode&unix.S_IFMT != unix.S_IFSOCK || bound.Mode&07777 != 0666 || bound.Uid != owner || bound.Nlink != 1 {
		return cleanup(ErrUnsafeSocket)
	}
	file := os.NewFile(uintptr(fd), ServiceBrokerSocketName)
	listener, err := net.FileListener(file)
	_ = file.Close()
	if err != nil {
		_ = unix.Unlinkat(dir, ServiceBrokerSocketName, 0)
		return fail(ErrUnsafeSocket)
	}
	if unixListener, ok := listener.(*net.UnixListener); ok {
		unixListener.SetUnlinkOnClose(false)
	}
	return &serviceBrokerListener{Listener: listener, dir: dir, device: uint64(bound.Dev), inode: bound.Ino}, nil
}

// intact reports whether the name still refers to the socket that was bound.
func (l *serviceBrokerListener) intact() error {
	var st unix.Stat_t
	if unix.Fstatat(l.dir, ServiceBrokerSocketName, &st, unix.AT_SYMLINK_NOFOLLOW) != nil ||
		st.Mode&unix.S_IFMT != unix.S_IFSOCK || uint64(st.Dev) != l.device || st.Ino != l.inode {
		return ErrUnsafeSocket
	}
	return nil
}

// Close stops the listener and removes the socket file, but only the one that
// was bound: a replacement at the name is left alone.
func (l *serviceBrokerListener) Close() error {
	l.closeOnce.Do(func() {
		l.closeError = l.Listener.Close()
		if l.intact() == nil {
			_ = unix.Unlinkat(l.dir, ServiceBrokerSocketName, 0)
		}
		_ = unix.Close(l.dir)
	})
	return l.closeError
}
