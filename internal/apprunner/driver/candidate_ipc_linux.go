//go:build linux

package driver

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

const candidateIPCBytes int64 = 16 << 20
const candidateIPCInodes uint64 = 1024
const candidateIPCMarker = "trusted.portal.ipc"

func candidateIPCRecord(identity CandidateIdentity, kind string) []byte {
	raw, _ := json.Marshal(struct {
		Format   string            `json:"format"`
		Profile  string            `json:"profile"`
		Kind     string            `json:"kind"`
		Identity CandidateIdentity `json:"identity"`
	}{"portal.runner.ipc-storage/1-dev.1", candidateIPCProfile, kind, identity})
	return raw
}
func candidateIPCMarkerMatches(fd int, identity CandidateIdentity, kind string) bool {
	value := make([]byte, 2048)
	n, err := unix.Fgetxattr(fd, candidateIPCMarker, value)
	return err == nil && bytes.Equal(value[:n], candidateIPCRecord(identity, kind))
}

// Observe a pinned directory, never an app-supplied path. Trusted xattrs cannot
// be changed by the app owning the tmpfs root. Kernel data-block and inode limits bound file content and entry counts,
// independently of data writer grants. Docker memory limits remain separately required.
func validateCandidateHTTPStorage(fd int, identity CandidateIdentity, strictMode bool) error {
	if !validContainerOwner(candidateOwner(identity)) {
		return ErrUnsafeSocket
	}
	readable, err := unix.Openat(fd, ".", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return ErrUnsafeSocket
	}
	defer unix.Close(readable)
	var st unix.Stat_t
	var fs unix.Statfs_t
	if unix.Fstat(readable, &st) != nil || st.Mode&unix.S_IFMT != unix.S_IFDIR || st.Uid != 65532 || st.Gid != 65532 || (strictMode && st.Mode&07777 != 0700) || unix.Fstatfs(readable, &fs) != nil {
		return ErrUnsafeSocket
	}
	const required = unix.ST_NODEV | unix.ST_NOSUID | unix.ST_NOEXEC
	if fs.Type != unix.TMPFS_MAGIC || fs.Bsize <= 0 || candidateIPCBytes%fs.Bsize != 0 || fs.Blocks != uint64(candidateIPCBytes/fs.Bsize) || fs.Files != candidateIPCInodes || fs.Flags&required != required || fs.Flags&unix.ST_RDONLY != 0 || !candidateIPCMarkerMatches(readable, identity, "mount") {
		return ErrUnsafeSocket
	}
	return nil
}

// Called after durable candidate claiming. Only a fresh directory is mounted;
// an existing one must already have the exact bounded mount and trusted identity.
// A partial or foreign directory is never repaired under an uncertain effect.
func provisionCandidateHTTPStorage(ctx context.Context, parent int, identity CandidateIdentity) error {
	if os.Geteuid() != 0 || !validContainerOwner(candidateOwner(identity)) {
		return ErrUnsupported
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	err := unix.Mkdirat(parent, "http", 0700)
	if errors.Is(err, unix.EEXIST) {
		fd, err := unix.Openat(parent, "http", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		if err != nil {
			return ErrUnsafeSocket
		}
		defer unix.Close(fd)
		return validateCandidateHTTPStorage(fd, identity, true)
	}
	if err != nil {
		return err
	}
	base, err := unix.Openat(parent, "http", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return errors.Join(ErrContainerUncertain, err)
	}
	defer unix.Close(base)
	// Normalize only this freshly created underlay, never an existing directory.
	if unix.Fchown(base, 0, 0) != nil || unix.Fchmod(base, 0700) != nil {
		return ErrContainerUncertain
	}
	var st unix.Stat_t
	if unix.Fstat(base, &st) != nil || !safeMountDirectory(st, true, 0, 0, 0700) {
		return ErrUnsafeSocket
	}
	if err := unix.Fsetxattr(base, candidateIPCMarker, candidateIPCRecord(identity, "base"), unix.XATTR_CREATE); err != nil {
		return errors.Join(ErrContainerUncertain, err)
	}
	if unix.Fsync(base) != nil || unix.Fsync(parent) != nil {
		return ErrContainerUncertain
	}
	if err := ctx.Err(); err != nil {
		return errors.Join(ErrContainerUncertain, err)
	}
	// Kernel-owned proc FD aliases keep the effect attached to the protected,
	// already opened directory instead of resolving mutable caller paths.
	target := fmt.Sprintf("/proc/self/fd/%d", base)
	if err := unix.Mount("portal-app-ipc", target, "tmpfs", unix.MS_NODEV|unix.MS_NOSUID|unix.MS_NOEXEC, "size=16777216,nr_inodes=1024,mode=0700,uid=65532,gid=65532"); err != nil {
		return errors.Join(ErrContainerUncertain, err)
	}
	mounted, err := unix.Openat(parent, "http", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return errors.Join(ErrContainerUncertain, err)
	}
	defer unix.Close(mounted)
	if err := unix.Mount("", fmt.Sprintf("/proc/self/fd/%d", mounted), "", unix.MS_PRIVATE, ""); err != nil {
		return errors.Join(ErrContainerUncertain, err)
	}
	if err := unix.Fsetxattr(mounted, candidateIPCMarker, candidateIPCRecord(identity, "mount"), unix.XATTR_CREATE); err != nil {
		return errors.Join(ErrContainerUncertain, err)
	}
	if err := validateCandidateHTTPStorage(mounted, identity, true); err != nil {
		return errors.Join(ErrContainerUncertain, err)
	}
	return ctx.Err()
}

// Caller holds lifecycle exclusion, closes live pins and proves no Docker mount
// references remain. Never lazy-unmount or recursively remove unknown storage.
func releaseCandidateHTTPStorage(ctx context.Context, parent int, path string, identity CandidateIdentity) error {
	if os.Geteuid() != 0 || !validContainerOwner(candidateOwner(identity)) {
		return ErrUnsupported
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	fd, err := unix.Openat(parent, "http", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if errors.Is(err, unix.ENOENT) {
		return nil
	}
	if err != nil {
		return ErrUnsafeSocket
	}
	if !candidateIPCMarkerMatches(fd, identity, "base") {
		err = validateCandidateHTTPStorage(fd, identity, false)
		unix.Close(fd)
		if err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		// The complete root-owned ancestor chain is pinned by the caller. Close the
		// final mount FD first so ordinary unmount detects genuine outstanding users.
		if err := unix.Unmount(path, 0); err != nil {
			return errors.Join(ErrContainerUncertain, err)
		}
		fd, err = unix.Openat(parent, "http", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		if err != nil {
			return errors.Join(ErrContainerUncertain, err)
		}
	}
	defer unix.Close(fd)
	var st unix.Stat_t
	if unix.Fstat(fd, &st) != nil || !safeMountDirectory(st, true, 0, 0, 0700) || !candidateIPCMarkerMatches(fd, identity, "base") {
		return ErrUnsafeSocket
	}
	if err := unix.Unlinkat(parent, "http", unix.AT_REMOVEDIR); err != nil {
		return errors.Join(ErrContainerUncertain, err)
	}
	return unix.Fsync(parent)
}

// Explicit quiescent cleanup, not a lifecycle authorization. Remove the owned
// container under its own journal authority and close launch pins first. Runtime
// Close alone deliberately keeps live app communication storage mounted.
func (r *CandidateRuntime) ReleaseCandidateHTTPStorage(ctx context.Context, identity CandidateIdentity) error {
	if r == nil || os.Geteuid() != 0 || !validContainerOwner(candidateOwner(identity)) {
		return ErrUnsupported
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for launch := range r.launches {
		if launch.snapshot.Identity.InstallationID == identity.InstallationID && launch.snapshot.Identity.Generation == identity.Generation {
			return ErrContainerConflict
		}
	}
	path := filepath.Join(r.roots.Sockets, identity.InstallationID, identity.Generation, "http")
	client, closeClient := r.client()
	defer closeClient()
	var containers []struct {
		ID     string `json:"Id"`
		Mounts []struct {
			Source string `json:"Source"`
		} `json:"Mounts"`
	}
	if err := readJSON(ctx, client, "/v"+APIVersion+"/containers/json?all=1", &containers); err != nil {
		return err
	}
	if containers == nil {
		return ErrInvalidResponse
	}
	for _, container := range containers {
		if !containerSHA.MatchString(container.ID) {
			return ErrInvalidResponse
		}
		for _, mount := range container.Mounts {
			if mount.Source == path || (mount.Source != "" && (strings.HasPrefix(path, mount.Source+"/") || strings.HasPrefix(mount.Source, path+"/") || mount.Source == "/")) {
				return ErrContainerConflict
			}
		}
	}
	pins, err := openMountDirectory(filepath.Dir(path), 0, 0, 0700)
	if err != nil {
		return err
	}
	defer func() {
		for _, p := range pins {
			p.file.Close()
		}
	}()
	parent, err := unix.Openat(int(pins[len(pins)-1].file.Fd()), ".", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return err
	}
	defer unix.Close(parent)
	return releaseCandidateHTTPStorage(ctx, parent, path, identity)
}
