//go:build linux && (amd64 || arm64)

package data

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"time"

	"golang.org/x/sys/unix"
)

const (
	maxDeleteEntries = 100000
	maxDeleteDepth   = 64
)

// Delete removes exactly one retained namespace after its writer/container
// authority has been durably revoked. The root identity, project identity,
// quota allocation and DAC profile are rechecked before the first mutation.
func (r *Root) Delete(ctx context.Context, expected RootIdentity, request DeleteRequest) (DeleteObservation, error) {
	var zero DeleteObservation
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed || !request.valid() {
		return zero, ErrDeleteUnsafe
	}
	if err := r.requireIdentity(ctx, expected); err != nil {
		return zero, err
	}
	now := time.Now().UTC().UnixMilli()
	if now <= 0 {
		return zero, ErrDeleteUnsafe
	}
	folder, err := r.openNamespace(request.NamespaceID)
	if errors.Is(err, unix.ENOENT) {
		if err := settleDeleteDataQuota(r.file, request); err != nil {
			return zero, errors.Join(ErrDeleteIncomplete, err)
		}
		if err := r.requireIdentity(ctx, expected); err != nil {
			return zero, err
		}
		return DeleteObservation{
			valid: true,
			snapshot: DeleteSnapshot{
				NamespaceID:   request.NamespaceID,
				ReleaseSHA256: request.ReleaseSHA256,
				ProjectID:     request.ProjectID,
				DataFormat:    request.DataFormat,
				WriterFence:   request.WriterFence,
				AlreadyAbsent: true, ObservedMS: now,
			},
		}, nil
	}
	if err != nil {
		return zero, ErrDeleteUnsafe
	}
	folderClosed := false
	defer func() {
		if !folderClosed {
			_ = folder.Close()
		}
	}()
	var stat unix.Stat_t
	if err := unix.Fstat(int(folder.Fd()), &stat); err != nil {
		return zero, ErrDeleteUnsafe
	}
	if uint64(stat.Dev) != request.ExpectedDevice ||
		uint64(stat.Ino) != request.ExpectedNamespaceInode ||
		stat.Uid != 0 ||
		!((stat.Gid == 0 && stat.Mode&07777 == 0700) ||
			(stat.Gid == writeAccessGID && stat.Mode&07777 == writeAccessMode)) ||
		stat.Mode&unix.S_IFMT != unix.S_IFDIR {
		return zero, ErrDeleteUnsafe
	}
	attr, err := attributes(folder)
	if err != nil ||
		binary.NativeEndian.Uint32(attr[12:16]) != request.ProjectID ||
		binary.NativeEndian.Uint32(attr[:4])&projectInherit == 0 {
		return zero, ErrDeleteUnsafe
	}
	limit, err := readLimits(r.file, request.ProjectID)
	if err != nil || limit.bytes != request.QuotaBytes ||
		limit.inodes != request.QuotaInodes ||
		limit.usedBytes < 0 || limit.usedBytes > limit.bytes ||
		limit.usedInodes < 0 || limit.usedInodes > limit.inodes {
		return zero, ErrDeleteUnsafe
	}
	result := DeleteSnapshot{
		NamespaceID:   request.NamespaceID,
		ReleaseSHA256: request.ReleaseSHA256,
		ProjectID:     request.ProjectID,
		DataFormat:    request.DataFormat,
		UsedBytes:     limit.usedBytes, UsedInodes: limit.usedInodes,
		NamespaceDevice: uint64(stat.Dev), NamespaceInode: uint64(stat.Ino),
		WriterFence: request.WriterFence,
	}
	count := 0
	if err := deleteNamespaceChildren(
		ctx, folder, r.device, 0, &count); err != nil {
		return zero, errors.Join(ErrDeleteIncomplete, err)
	}
	if err := ctx.Err(); err != nil {
		return zero, errors.Join(ErrDeleteIncomplete, err)
	}
	if err := folder.Sync(); err != nil {
		return zero, errors.Join(ErrDeleteIncomplete, err)
	}
	remaining, err := folder.ReadDir(-1)
	if err != nil {
		return zero, errors.Join(ErrDeleteIncomplete, err,
			ErrDeleteUnsafe)
	}
	if len(remaining) != 0 {
		return zero, errors.Join(
			ErrDeleteIncomplete,
			fmt.Errorf("namespace directory is not empty: %w", ErrDeleteUnsafe))
	}
	if err := unix.Unlinkat(
		int(r.file.Fd()), request.NamespaceID, unix.AT_REMOVEDIR); err != nil {
		return zero, errors.Join(
			ErrDeleteIncomplete,
			fmt.Errorf("remove namespace directory: %w", err))
	}
	closeErr := folder.Close()
	folderClosed = true
	if closeErr != nil {
		return zero, errors.Join(ErrDeleteIncomplete, closeErr)
	}
	if err := r.file.Sync(); err != nil {
		return zero, errors.Join(ErrDeleteIncomplete, err)
	}
	if err := settleDeleteDataQuota(r.file, request); err != nil {
		return zero, errors.Join(
			ErrDeleteIncomplete,
			fmt.Errorf("settle namespace quota: %w", err))
	}
	if err := r.requireIdentity(ctx, expected); err != nil {
		return zero, errors.Join(ErrDeleteIncomplete, err)
	}
	result.Entries = int64(count)
	result.ObservedMS = time.Now().UTC().UnixMilli()
	return DeleteObservation{snapshot: result, valid: true}, nil
}

func settleDeleteDataQuota(file *os.File, request DeleteRequest) error {
	limit, err := readLimits(file, request.ProjectID)
	if err != nil {
		return fmt.Errorf("read namespace quota: %w", err)
	}
	if limit == (limits{}) {
		return nil
	}
	if limit.usedBytes != 0 || limit.usedInodes != 0 {
		return fmt.Errorf(
			"namespace quota is still in use: %w", ErrDeleteIncomplete)
	}
	if limit.bytes != request.QuotaBytes ||
		limit.inodes != request.QuotaInodes {
		return fmt.Errorf(
			"namespace quota binding changed: %w", ErrDeleteUnsafe)
	}
	buffer := make([]byte, 112)
	buffer[0] = 1
	buffer[1] = 2
	binary.NativeEndian.PutUint16(buffer[2:4], 10)
	binary.NativeEndian.PutUint32(buffer[4:8], request.ProjectID)
	if err := quota(file, quotaSet, request.ProjectID, buffer); err != nil {
		return fmt.Errorf("clear namespace quota: %w", err)
	}
	cleared, err := readLimits(file, request.ProjectID)
	if err != nil {
		return fmt.Errorf("verify cleared namespace quota: %w", err)
	}
	if cleared != (limits{}) {
		return fmt.Errorf(
			"namespace quota remains after clear: %w", ErrDeleteIncomplete)
	}
	return nil
}

func deleteNamespaceChildren(ctx context.Context, folder *os.File, device uint64, depth int, count *int) error {
	if depth > maxDeleteDepth {
		return ErrDeleteUnsafe
	}
	entries, err := folder.ReadDir(-1)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return err
		}
		*count++
		if *count > maxDeleteEntries {
			return ErrDeleteUnsafe
		}
		name := entry.Name()
		if name == "" || name == "." || name == ".." {
			return ErrDeleteUnsafe
		}
		var stat unix.Stat_t
		if err := unix.Fstatat(
			int(folder.Fd()), name, &stat,
			unix.AT_SYMLINK_NOFOLLOW); err != nil {
			return err
		}
		if uint64(stat.Dev) != device {
			return ErrDeleteUnsafe
		}
		if stat.Mode&unix.S_IFMT != unix.S_IFDIR {
			if err := unix.Unlinkat(
				int(folder.Fd()), name, 0); err != nil {
				return err
			}
			continue
		}
		childFD, err := unix.Openat(
			int(folder.Fd()), name,
			unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		if err != nil {
			return err
		}
		child := os.NewFile(uintptr(childFD), "runner-delete-directory")
		if child == nil {
			unix.Close(childFD)
			return ErrDeleteUnsafe
		}
		childErr := deleteNamespaceChildren(
			ctx, child, device, depth+1, count)
		closeErr := child.Close()
		if childErr != nil || closeErr != nil {
			return errors.Join(childErr, closeErr)
		}
		if err := unix.Unlinkat(
			int(folder.Fd()), name, unix.AT_REMOVEDIR); err != nil {
			return err
		}
	}
	return nil
}
