//go:build linux && (amd64 || arm64)

package data

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"log"
	"os"
	"strings"

	"golang.org/x/sys/unix"
)

// InspectCheckpointArchive reports whether the retained archive of one checkpoint exists, and its size. It changes
// nothing, not even a missing checkpoint directory. A file that is not a regular, singly linked file is refused.
func (r *Root) InspectCheckpointArchive(ctx context.Context, expected RootIdentity, checkpointID string) (CheckpointArchive, error) {
	return r.checkpointArchive(ctx, expected, checkpointID, -1, "")
}

// RemoveCheckpointArchive deletes the retained archive of one checkpoint: the file is opened relative to the
// verified checkpoint directory without following links, must be a regular file with one link and exactly
// archiveBytes, and is then removed through a trash name (see checkpointArchive), the directory synced and the
// absence confirmed. A file that is already gone is reported with Found false, which is how an interrupted removal
// resolves. manifestSHA256 is the SHA-256 of the whole archive file as the checkpoint row records it; it identifies
// a leftover trash entry of an interrupted removal, which is hashed once, and is not used on the normal path, where
// re-reading the archive would only cost I/O.
func (r *Root) RemoveCheckpointArchive(ctx context.Context, expected RootIdentity, checkpointID string, archiveBytes int64, manifestSHA256 string) (CheckpointArchive, error) {
	if archiveBytes <= 0 || !sha256Value(manifestSHA256) {
		return CheckpointArchive{}, ErrArchiveMismatch
	}
	return r.checkpointArchive(ctx, expected, checkpointID, archiveBytes, manifestSHA256)
}

const archiveTrashSuffix = ".deleting"

// checkpointArchive inspects (remove < 0) or removes (remove is the expected size) the archive.
//
// Removal never unlinks by the archive's own name, which could be swapped between the checks and the unlink: the
// verified file is first renamed to a fixed trash name without replacing anything, identified again by device and
// inode against the verified descriptor, and only then unlinked. A different file is moved back, never deleted.
// A trash entry that already exists is the leftover of an interrupted removal or something else: it is unlinked only
// when it is a regular, singly linked file of the claimed size whose digest is the checkpoint's recorded digest, and
// is otherwise moved aside under a unique quarantine name and logged, so it can neither be deleted wrongly nor block
// this removal for ever. Without a no-replace rename the removal is refused with ErrArchiveRenameUnsupported.
func (r *Root) checkpointArchive(ctx context.Context, expected RootIdentity, checkpointID string, remove int64, manifest string) (CheckpointArchive, error) {
	var zero CheckpointArchive
	if r == nil || !namespaceID.MatchString(checkpointID) || !expected.valid() {
		return zero, ErrUnsafe
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return zero, ErrUnsafe
	}
	identity, err := r.identity(ctx)
	if err != nil || identity != expected {
		return zero, ErrUnsafe
	}
	directory, err := r.openCheckpointDirectoryMode(false)
	if errors.Is(err, unix.ENOENT) {
		return zero, nil
	}
	if err != nil {
		return zero, err
	}
	defer directory.Close()
	name := checkpointID + ".tar"
	trash := name + archiveTrashSuffix
	if remove >= 0 {
		if err := settleLeftoverTrash(ctx, directory, trash, remove, manifest); err != nil {
			return zero, err
		}
	}
	fd, err := unix.Openat(int(directory.Fd()), name, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if errors.Is(err, unix.ENOENT) {
		return zero, nil
	}
	if err != nil {
		return zero, ErrArchiveMismatch
	}
	file := os.NewFile(uintptr(fd), "runner-checkpoint-archive")
	if file == nil {
		return zero, ErrUnsafe
	}
	defer file.Close()
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil || stat.Mode&unix.S_IFMT != unix.S_IFREG || stat.Nlink != 1 || stat.Size < 0 || (remove >= 0 && stat.Size != remove) {
		return zero, ErrArchiveMismatch
	}
	found := CheckpointArchive{Found: true, Bytes: stat.Size}
	if remove < 0 {
		return found, nil
	}
	if err := ctx.Err(); err != nil {
		return zero, err
	}
	if err := renameNoReplace(directory, name, trash); err != nil {
		return zero, err
	}
	var moved unix.Stat_t
	if err := unix.Fstatat(int(directory.Fd()), trash, &moved, unix.AT_SYMLINK_NOFOLLOW); err != nil ||
		moved.Mode&unix.S_IFMT != unix.S_IFREG || moved.Nlink != 1 || moved.Dev != stat.Dev || moved.Ino != stat.Ino {
		// Not the file that was verified: put whatever was moved back and refuse.
		_ = renameNoReplace(directory, trash, name)
		return zero, ErrArchiveMismatch
	}
	if err := unix.Unlinkat(int(directory.Fd()), trash, 0); err != nil && !errors.Is(err, unix.ENOENT) {
		return zero, err
	}
	if err := confirmArchiveGone(directory, name, trash); err != nil {
		return zero, err
	}
	return found, nil
}

func renameNoReplace(directory *os.File, from, to string) error {
	err := unix.Renameat2(int(directory.Fd()), from, int(directory.Fd()), to, unix.RENAME_NOREPLACE)
	if errors.Is(err, unix.EINVAL) || errors.Is(err, unix.ENOSYS) {
		return ErrArchiveRenameUnsupported
	}
	return err
}

// settleLeftoverTrash deals with an existing trash entry before a removal starts: unlink it when it is this
// checkpoint's archive, quarantine it when it is anything else.
func settleLeftoverTrash(ctx context.Context, directory *os.File, trash string, bytes int64, manifest string) error {
	fd, err := unix.Openat(int(directory.Fd()), trash, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if errors.Is(err, unix.ENOENT) {
		return nil
	}
	if err != nil {
		return quarantineTrash(directory, trash)
	}
	file := os.NewFile(uintptr(fd), "runner-checkpoint-leftover")
	if file == nil {
		return ErrUnsafe
	}
	defer file.Close()
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil || stat.Mode&unix.S_IFMT != unix.S_IFREG || stat.Nlink != 1 || stat.Size != bytes {
		return quarantineTrash(directory, trash)
	}
	sum := sha256.New()
	if n, err := io.Copy(sum, io.LimitReader(file, bytes+1)); err != nil || n != bytes {
		return quarantineTrash(directory, trash)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if hex.EncodeToString(sum.Sum(nil)) != manifest {
		return quarantineTrash(directory, trash)
	}
	// The digest matched this descriptor, but a name can still be swapped: unlink only if the name still is that file.
	var current unix.Stat_t
	if err := unix.Fstatat(int(directory.Fd()), trash, &current, unix.AT_SYMLINK_NOFOLLOW); err != nil || current.Dev != stat.Dev || current.Ino != stat.Ino {
		return quarantineTrash(directory, trash)
	}
	if err := unix.Unlinkat(int(directory.Fd()), trash, 0); err != nil && !errors.Is(err, unix.ENOENT) {
		return err
	}
	return directory.Sync()
}

// quarantineTrash moves an unrecognised trash entry to a unique name, never deleting it.
func quarantineTrash(directory *os.File, trash string) error {
	var suffix [8]byte
	if _, err := rand.Read(suffix[:]); err != nil {
		return err
	}
	to := trash + ".quarantine-" + hex.EncodeToString(suffix[:])
	if err := renameNoReplace(directory, trash, to); err != nil {
		return err
	}
	log.Printf("checkpoint archive removal: an unrecognised %s was moved aside to %s", trash, to)
	return directory.Sync()
}

// confirmArchiveGone syncs the directory and confirms that neither name remains.
func confirmArchiveGone(directory *os.File, names ...string) error {
	if err := directory.Sync(); err != nil {
		return err
	}
	for _, n := range names {
		var after unix.Stat_t
		if err := unix.Fstatat(int(directory.Fd()), n, &after, unix.AT_SYMLINK_NOFOLLOW); !errors.Is(err, unix.ENOENT) {
			return ErrArchiveRemovalIncomplete
		}
	}
	return nil
}

// QuarantineSummary counts the files a checkpoint removal moved aside as unrecognised (see checkpointArchive) and sums
// their sizes. They are never deleted by the runner; the operator decides. It changes nothing.
func (r *Root) QuarantineSummary(ctx context.Context, expected RootIdentity) (QuarantineReport, error) {
	var zero QuarantineReport
	if r == nil || !expected.valid() {
		return zero, ErrUnsafe
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return zero, ErrUnsafe
	}
	identity, err := r.identity(ctx)
	if err != nil || identity != expected {
		return zero, ErrUnsafe
	}
	directory, err := r.openCheckpointDirectoryMode(false)
	if errors.Is(err, unix.ENOENT) {
		return zero, nil
	}
	if err != nil {
		return zero, err
	}
	defer directory.Close()
	entries, err := directory.ReadDir(-1)
	if err != nil {
		return zero, err
	}
	for _, entry := range entries {
		if !strings.Contains(entry.Name(), archiveTrashSuffix+".quarantine-") {
			continue
		}
		var stat unix.Stat_t
		if err := unix.Fstatat(int(directory.Fd()), entry.Name(), &stat, unix.AT_SYMLINK_NOFOLLOW); err != nil {
			continue
		}
		zero.Files++
		zero.Bytes += stat.Size
	}
	return zero, nil
}
