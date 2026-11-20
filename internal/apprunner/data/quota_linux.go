//go:build linux && (amd64 || arm64)

package data

import (
	"context"
	"encoding/binary"
	"errors"
	"time"

	"golang.org/x/sys/unix"
)

// RaiseQuota applies a larger XFS project quota to an existing namespace, online: the namespace stays in place and
// a running backend is not touched (doc 185 section 3.2). It only raises. The live limits must equal the old limits
// the caller recorded, so a hand-edited limit is never raised over, and the project, device and inode must be the
// namespace's own.
//
// It is convergent: when the live limits already equal the new ones (an earlier call applied the raise and its
// answer was lost) it returns the observation with AlreadyApplied set. Any failure after the quota call started is
// incomplete, never assumed reverted.
func (r *Root) RaiseQuota(ctx context.Context, expected RootIdentity, request QuotaRaiseRequest) (QuotaRaiseObservation, error) {
	var zero QuotaRaiseObservation
	if r == nil || !request.valid() {
		return zero, ErrQuotaRaiseUnsafe
	}
	if request.NewBytes < request.OldBytes || request.NewInodes < request.OldInodes {
		return zero, ErrQuotaLower
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return zero, ErrQuotaRaiseUnsafe
	}
	if err := ctx.Err(); err != nil {
		return zero, errors.Join(ErrQuotaRaiseUnsafe, err)
	}
	if err := enforcement(r.file); err != nil {
		return zero, errors.Join(ErrQuotaRaiseUnsafe, err)
	}
	if err := r.requireIdentity(ctx, expected); err != nil {
		return zero, errors.Join(ErrQuotaRaiseUnsafe, err)
	}
	folder, err := r.openNamespace(request.NamespaceID)
	if err != nil {
		return zero, errors.Join(ErrQuotaRaiseUnsafe, err)
	}
	defer folder.Close()
	var stat unix.Stat_t
	if err := unix.Fstat(int(folder.Fd()), &stat); err != nil ||
		uint64(stat.Dev) != request.ExpectedDevice || stat.Ino != request.ExpectedNamespaceInode ||
		uint64(stat.Dev) != r.device || stat.Mode&unix.S_IFMT != unix.S_IFDIR {
		return zero, ErrQuotaRaiseUnsafe
	}
	attr, err := attributes(folder)
	if err != nil || binary.NativeEndian.Uint32(attr[12:16]) != request.ProjectID ||
		binary.NativeEndian.Uint32(attr[:4])&projectInherit == 0 {
		return zero, ErrQuotaRaiseUnsafe
	}
	live, err := readLimits(r.file, request.ProjectID)
	if err != nil {
		return zero, errors.Join(ErrQuotaRaiseUnsafe, err)
	}
	applied := live.bytes == request.NewBytes && live.inodes == request.NewInodes
	if !applied {
		if live.bytes != request.OldBytes || live.inodes != request.OldInodes {
			return zero, ErrQuotaRaiseUnsafe
		}
		buffer := make([]byte, 112)
		buffer[0] = 1
		buffer[1] = 2
		binary.NativeEndian.PutUint16(buffer[2:4], 10)
		binary.NativeEndian.PutUint32(buffer[4:8], request.ProjectID)
		binary.NativeEndian.PutUint64(buffer[8:16], uint64(request.NewBytes/512))
		binary.NativeEndian.PutUint64(buffer[24:32], uint64(request.NewInodes))
		if err := quota(r.file, quotaSet, request.ProjectID, buffer); err != nil {
			return zero, errors.Join(ErrQuotaRaiseIncomplete, err)
		}
		if err := r.file.Sync(); err != nil {
			return zero, errors.Join(ErrQuotaRaiseIncomplete, err)
		}
	}
	after, err := readLimits(r.file, request.ProjectID)
	if err != nil || after.bytes != request.NewBytes || after.inodes != request.NewInodes ||
		after.usedBytes < 0 || after.usedBytes > after.bytes || after.usedInodes < 0 || after.usedInodes > after.inodes {
		return zero, errors.Join(ErrQuotaRaiseIncomplete, err)
	}
	if err := r.requireIdentity(ctx, expected); err != nil {
		return zero, errors.Join(ErrQuotaRaiseIncomplete, err)
	}
	return QuotaRaiseObservation{valid: true, snapshot: QuotaRaiseSnapshot{
		NamespaceID: request.NamespaceID, ProjectID: request.ProjectID,
		ByteLimit: after.bytes, InodeLimit: after.inodes, UsedBytes: after.usedBytes, UsedInodes: after.usedInodes,
		AlreadyApplied: applied, ObservedMS: time.Now().UTC().UnixMilli(),
		NamespaceDevice: uint64(stat.Dev), NamespaceInode: stat.Ino,
	}}, nil
}
