//go:build linux && (amd64 || arm64)

package data

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"os"
	"sync"
	"unsafe"

	"golang.org/x/sys/unix"
)

// Linux UAPI linux/dqblk_xfs.h and linux/fs.h. This adapter deliberately supports
// only the initial amd64/arm64 native ABI; it never invokes a shell or quota CLI.
const (
	quotaStatus        = ((uint32('X')<<8 | 8) << 8) | 2
	quotaGet           = ((uint32('X')<<8 | 3) << 8) | 2
	quotaSet           = ((uint32('X')<<8 | 4) << 8) | 2
	getAttr            = 0x801c581f
	setAttr            = 0x401c5820
	projectInherit     = 0x200
	geometryGet        = 0x80705864
	geometrySize       = 112
	geometryUUIDOffset = 64
)

type Root struct {
	mu     sync.Mutex
	file   *os.File
	device uint64
	closed bool
}

func native(file *os.File, call func(uintptr) error) error {
	if file == nil {
		return ErrUnsafe
	}
	connection, err := file.SyscallConn()
	if err != nil {
		return err
	}
	var result error
	if err := connection.Control(func(fd uintptr) { result = call(fd) }); err != nil {
		return err
	}
	return result
}
func ioctl(file *os.File, request uintptr, buffer []byte) error {
	return native(file, func(fd uintptr) error {
		_, _, errno := unix.Syscall(unix.SYS_IOCTL, fd, request, uintptr(unsafe.Pointer(&buffer[0])))
		if errno != 0 {
			return errno
		}
		return nil
	})
}
func quota(file *os.File, command uint32, id uint32, buffer []byte) error {
	return native(file, func(fd uintptr) error {
		_, _, errno := unix.Syscall6(unix.SYS_QUOTACTL_FD, fd, uintptr(command), uintptr(id), uintptr(unsafe.Pointer(&buffer[0])), 0, 0)
		if errno != 0 {
			return errno
		}
		return nil
	})
}
func attributes(file *os.File) ([]byte, error) {
	buffer := make([]byte, 28)
	if err := ioctl(file, getAttr, buffer); err != nil {
		return nil, err
	}
	return buffer, nil
}
func enforcement(file *os.File) error {
	buffer := make([]byte, 160)
	buffer[0] = 1
	if err := quota(file, quotaStatus, 0, buffer); err != nil {
		return err
	}
	if buffer[0] != 1 || binary.NativeEndian.Uint16(buffer[2:4])&0x30 != 0x30 {
		return ErrUnsupported
	}
	return nil
}
func openRoot(input *os.File) (*Root, error) {
	if os.Geteuid() != 0 {
		return nil, ErrUnsupported
	}
	fd := -1
	err := native(input, func(parent uintptr) error {
		var err error
		fd, err = unix.Openat(int(parent), ".", unix.O_DIRECTORY|unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		return err
	})
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), "runner-data-root")
	ok := false
	defer func() {
		if !ok {
			file.Close()
		}
	}()
	var stat unix.Stat_t
	var fs unix.Statfs_t
	if unix.Fstat(fd, &stat) != nil || stat.Uid != 0 || stat.Mode&07777 != 0700 || unix.Fstatfs(fd, &fs) != nil || fs.Type != unix.XFS_SUPER_MAGIC || fs.Flags&(unix.ST_NODEV|unix.ST_NOSUID|unix.ST_NOEXEC) != (unix.ST_NODEV|unix.ST_NOSUID|unix.ST_NOEXEC) {
		return nil, ErrUnsafe
	}
	if err := unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB); err != nil {
		return nil, ErrConflict
	}
	attr, err := attributes(file)
	if err != nil {
		return nil, err
	}
	if binary.NativeEndian.Uint32(attr[12:16]) != 0 || binary.NativeEndian.Uint32(attr[:4])&projectInherit != 0 {
		return nil, ErrUnsafe
	}
	if err := enforcement(file); err != nil {
		return nil, err
	}
	root := &Root{file: file, device: uint64(stat.Dev)}
	if _, err := root.identity(context.Background()); err != nil {
		return nil, err
	}
	ok = true
	return root, nil
}
func (r *Root) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return nil
	}
	r.closed = true
	return r.file.Close()
}

// Identity reads from the pinned descriptor, never from a pathname lookup.
func (r *Root) Identity(ctx context.Context) (RootIdentity, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.identity(ctx)
}
func (r *Root) identity(ctx context.Context) (RootIdentity, error) {
	var zero RootIdentity
	if err := ctx.Err(); err != nil {
		return zero, err
	}
	if r.closed {
		return zero, ErrUnsafe
	}
	var stat unix.Stat_t
	var fs unix.Statfs_t
	if err := native(r.file, func(fd uintptr) error {
		if err := unix.Fstat(int(fd), &stat); err != nil {
			return err
		}
		return unix.Fstatfs(int(fd), &fs)
	}); err != nil {
		return zero, err
	}
	if uint64(stat.Dev) != r.device || stat.Ino == 0 || stat.Uid != 0 || stat.Mode&unix.S_IFMT != unix.S_IFDIR || stat.Mode&07777 != 0700 || fs.Type != unix.XFS_SUPER_MAGIC || fs.Flags&(unix.ST_NODEV|unix.ST_NOSUID|unix.ST_NOEXEC) != (unix.ST_NODEV|unix.ST_NOSUID|unix.ST_NOEXEC) {
		return zero, ErrUnsafe
	}
	attr, err := attributes(r.file)
	if err != nil {
		return zero, err
	}
	if binary.NativeEndian.Uint32(attr[12:16]) != 0 || binary.NativeEndian.Uint32(attr[:4])&projectInherit != 0 {
		return zero, ErrUnsafe
	}
	if err := enforcement(r.file); err != nil {
		return zero, err
	}
	geometry := make([]byte, geometrySize)
	if err := ioctl(r.file, geometryGet, geometry); err != nil {
		return zero, err
	}
	id := geometry[geometryUUIDOffset : geometryUUIDOffset+16]
	result := RootIdentity{FilesystemUUID: fmt.Sprintf("%x-%x-%x-%x-%x", id[:4], id[4:6], id[6:8], id[8:10], id[10:]), Inode: stat.Ino}
	if !result.valid() {
		return zero, ErrUnsafe
	}
	if err := ctx.Err(); err != nil {
		return zero, err
	}
	return result, nil
}
func (r *Root) requireIdentity(ctx context.Context, expected RootIdentity) error {
	if !expected.valid() {
		return ErrUnsafe
	}
	actual, err := r.identity(ctx)
	if err != nil {
		return err
	}
	if actual != expected {
		return ErrUnsafe
	}
	return nil
}

type limits struct{ bytes, inodes, usedBytes, usedInodes int64 }

func readLimits(file *os.File, project uint32) (limits, error) {
	buffer := make([]byte, 112)
	err := quota(file, quotaGet, project, buffer)
	if errors.Is(err, unix.ENOENT) || errors.Is(err, unix.ESRCH) {
		if err := enforcement(file); err != nil {
			return limits{}, err
		}
		return limits{}, nil
	}
	if err != nil {
		return limits{}, err
	}
	if buffer[0] != 1 || buffer[1]&2 == 0 || binary.NativeEndian.Uint32(buffer[4:8]) != project {
		return limits{}, ErrUnsafe
	}
	number := func(offset int) uint64 { return binary.NativeEndian.Uint64(buffer[offset : offset+8]) }
	for _, offset := range []int{8, 16, 40} {
		if number(offset) > math.MaxInt64/512 {
			return limits{}, ErrUnsafe
		}
	}
	for _, offset := range []int{24, 32, 48} {
		if number(offset) > math.MaxInt64 {
			return limits{}, ErrUnsafe
		}
	}
	// Soft limits are not part of this hard-limit-only initial profile.
	if number(16) != 0 || number(32) != 0 {
		return limits{}, ErrConflict
	}
	return limits{int64(number(8)) * 512, int64(number(24)), int64(number(40)) * 512, int64(number(48))}, nil
}
func (r *Root) openNamespace(id string) (*os.File, error) {
	fd := -1
	err := native(r.file, func(parent uintptr) error {
		var err error
		fd, err = unix.Openat(int(parent), id, unix.O_DIRECTORY|unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		return err
	})
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(fd), "runner-data-namespace"), nil
}
func (r *Root) inspect(ctx context.Context, id string, project uint32) (Observation, error) {
	var zero Observation
	if err := ctx.Err(); err != nil {
		return zero, err
	}
	if err := enforcement(r.file); err != nil {
		return zero, err
	}
	folder, err := r.openNamespace(id)
	if err != nil {
		return zero, err
	}
	defer folder.Close()
	var stat unix.Stat_t
	if err := native(folder, func(fd uintptr) error { return unix.Fstat(int(fd), &stat) }); err != nil {
		return zero, err
	}
	if uint64(stat.Dev) != r.device || stat.Uid != 0 || stat.Mode&07777 != 0700 {
		return zero, ErrUnsafe
	}
	attr, err := attributes(folder)
	if err != nil {
		return zero, err
	}
	if binary.NativeEndian.Uint32(attr[12:16]) != project || binary.NativeEndian.Uint32(attr[:4])&projectInherit == 0 {
		return zero, ErrConflict
	}
	limit, err := readLimits(r.file, project)
	if err != nil {
		return zero, err
	}
	if limit.bytes <= 0 || limit.inodes <= 0 {
		return zero, ErrUnsafe
	}
	if err := ctx.Err(); err != nil {
		return zero, err
	}
	return Observation{id, project, uint64(stat.Dev), stat.Ino, limit.bytes, limit.inodes, limit.usedBytes, limit.usedInodes, true}, nil
}
func (r *Root) Inspect(ctx context.Context, expected RootIdentity, id string, project uint32) (Observation, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed || !namespaceID.MatchString(id) || project == 0 {
		return Observation{}, ErrUnsafe
	}
	if err := r.requireIdentity(ctx, expected); err != nil {
		return Observation{}, err
	}
	observed, err := r.inspect(ctx, id, project)
	if err != nil {
		return Observation{}, err
	}
	if err := r.requireIdentity(ctx, expected); err != nil {
		return Observation{}, err
	}
	return observed, nil
}

// Prepare accepts only a fresh externally reserved project/namespace. Any failure
// after creation is uncertain and must be reconciled, never blindly retried or
// recursively removed. This primitive grants no writer UID or mount access.
func (r *Root) Prepare(ctx context.Context, expected RootIdentity, request Request) (Observation, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var zero Observation
	if r.closed || !request.valid() {
		return zero, ErrUnsafe
	}
	if err := ctx.Err(); err != nil {
		return zero, err
	}
	if err := enforcement(r.file); err != nil {
		return zero, err
	}
	if err := r.requireIdentity(ctx, expected); err != nil {
		return zero, err
	}
	limit, err := readLimits(r.file, request.ProjectID)
	if err != nil {
		return zero, err
	}
	if limit != (limits{}) {
		return zero, ErrConflict
	}
	if err := ctx.Err(); err != nil {
		return zero, err
	}
	if err := native(r.file, func(fd uintptr) error { return unix.Mkdirat(int(fd), request.NamespaceID, 0700) }); err != nil {
		return zero, err
	}
	incomplete := func(err error) (Observation, error) { return zero, errors.Join(ErrIncomplete, err) }
	if err := ctx.Err(); err != nil {
		return incomplete(err)
	}
	folder, err := r.openNamespace(request.NamespaceID)
	if err != nil {
		return incomplete(err)
	}
	defer folder.Close()
	// The root remains inaccessible to applications. No file data or startup hook
	// runs before quotas and inherited project ownership are observed.
	buffer := make([]byte, 112)
	buffer[0] = 1
	buffer[1] = 2
	binary.NativeEndian.PutUint16(buffer[2:4], 10)
	binary.NativeEndian.PutUint32(buffer[4:8], request.ProjectID)
	binary.NativeEndian.PutUint64(buffer[8:16], uint64(request.Bytes/512))
	binary.NativeEndian.PutUint64(buffer[24:32], uint64(request.Inodes))
	if err := quota(r.file, quotaSet, request.ProjectID, buffer); err != nil {
		return incomplete(err)
	}
	if err := ctx.Err(); err != nil {
		return incomplete(err)
	}
	attr, err := attributes(folder)
	if err != nil {
		return incomplete(err)
	}
	if binary.NativeEndian.Uint32(attr[12:16]) != 0 {
		return incomplete(ErrConflict)
	}
	binary.NativeEndian.PutUint32(attr[12:16], request.ProjectID)
	binary.NativeEndian.PutUint32(attr[:4], binary.NativeEndian.Uint32(attr[:4])|projectInherit)
	if err := ioctl(folder, setAttr, attr); err != nil {
		return incomplete(err)
	}
	if err := folder.Sync(); err != nil {
		return incomplete(err)
	}
	if err := r.file.Sync(); err != nil {
		return incomplete(err)
	}
	observed, err := r.inspect(ctx, request.NamespaceID, request.ProjectID)
	if err != nil {
		return incomplete(err)
	}
	if err := r.requireIdentity(ctx, expected); err != nil {
		return incomplete(err)
	}
	if observed.ByteLimit != request.Bytes || observed.InodeLimit != request.Inodes {
		return incomplete(ErrConflict)
	}
	return observed, nil
}
