//go:build linux

package driver

import (
	"context"
	"errors"
	"os"
	"runtime"
	"strings"

	"golang.org/x/sys/unix"
)

// ProvisionCandidateSockets is invoked only after durable candidate claiming.
// It creates fixed platform-owned names below the protected runtime root. It
// never accepts paths from the app or repairs unexpected existing ownership.
func (r *CandidateRuntime) ProvisionCandidateSockets(ctx context.Context, request CandidateLaunchRequest) error {
	if r == nil || os.Geteuid() != 0 {
		return ErrUnsupported
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed || !candidateRequestValid(request) || request.Platform != "linux/"+runtime.GOARCH {
		return ErrInvalidResponse
	}
	if err := r.checkData(ctx, request.Identity, request.Data); err != nil {
		return err
	}
	image, err := InspectBackendImage(ctx, r.socket, request.Descriptor, request.Identity.ReleaseSHA256)
	if err != nil {
		return err
	}
	if _, err := planCandidateContainer(request.Descriptor, image, candidateOwner(request.Identity), r.roots); err != nil {
		return err
	}
	pins, err := openMountDirectory(r.roots.Sockets, 0, 0, 0700)
	if err != nil {
		return err
	}
	defer func() {
		for _, pin := range pins {
			pin.file.Close()
		}
	}()
	parent, err := unix.Openat(int(pins[len(pins)-1].file.Fd()), ".", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return err
	}
	defer unix.Close(parent)
	installation, err := ensureCandidateSocketDirectory(ctx, parent, request.Identity.InstallationID, 0, 0, 0700)
	if err != nil {
		return err
	}
	defer unix.Close(installation)
	generation, err := ensureCandidateSocketDirectory(ctx, installation, request.Identity.Generation, 0, 0, 0700)
	if err != nil {
		return err
	}
	defer unix.Close(generation)
	for _, leaf := range []struct {
		name     string
		uid, gid int
		mode     uint32
	}{{"http", 65532, 65532, 0700}, {"broker", 0, 0, 0755}} {
		fd, err := ensureCandidateSocketDirectory(ctx, generation, leaf.name, leaf.uid, leaf.gid, leaf.mode)
		if err != nil {
			return err
		}
		unix.Close(fd)
	}
	return ctx.Err()
}

func ensureCandidateSocketDirectory(ctx context.Context, parent int, name string, uid, gid int, mode uint32) (int, error) {
	if name == "" || name == "." || name == ".." || strings.ContainsAny(name, "/\\") {
		return -1, ErrUnsafeSocket
	}
	if err := ctx.Err(); err != nil {
		return -1, err
	}
	created := false
	if err := unix.Mkdirat(parent, name, mode); err == nil {
		created = true
	} else if !errors.Is(err, unix.EEXIST) {
		return -1, err
	}
	fd, err := unix.Openat(parent, name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return -1, ErrUnsafeSocket
	}
	fail := func() (int, error) { unix.Close(fd); return -1, ErrUnsafeSocket }
	if created {
		if unix.Fchown(fd, uid, gid) != nil || unix.Fchmod(fd, mode) != nil || unix.Fsync(fd) != nil || unix.Fsync(parent) != nil {
			return fail()
		}
	}
	var st unix.Stat_t
	if unix.Fstat(fd, &st) != nil || !safeMountDirectory(st, true, uint32(uid), uint32(gid), mode) {
		return fail()
	}
	return fd, nil
}
