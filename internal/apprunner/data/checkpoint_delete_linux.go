//go:build linux && (amd64 || arm64)

package data

import (
	"context"
	"errors"
	"os"

	"golang.org/x/sys/unix"
)

// InspectCheckpointArchive reports whether the retained archive of one checkpoint exists, and its size. It changes
// nothing, not even a missing checkpoint directory. A file that is not a regular, singly linked file is refused.
func (r *Root) InspectCheckpointArchive(ctx context.Context, expected RootIdentity, checkpointID string) (CheckpointArchive, error) {
	return r.checkpointArchive(ctx, expected, checkpointID, -1)
}

// RemoveCheckpointArchive deletes the retained archive of one checkpoint: the file is opened relative to the
// verified checkpoint directory without following links, must be a regular file with one link and exactly
// archiveBytes, and is then unlinked, the directory synced and the absence confirmed. A file that is already gone
// is reported with Found false, which is how an interrupted removal resolves. The archive is not read: its name is
// the checkpoint's identifier and re-reading it would only cost I/O.
func (r *Root) RemoveCheckpointArchive(ctx context.Context, expected RootIdentity, checkpointID string, archiveBytes int64) (CheckpointArchive, error) {
	if archiveBytes <= 0 {
		return CheckpointArchive{}, ErrArchiveMismatch
	}
	return r.checkpointArchive(ctx, expected, checkpointID, archiveBytes)
}

// checkpointArchive inspects (remove < 0) or removes (remove is the expected size) the archive.
func (r *Root) checkpointArchive(ctx context.Context, expected RootIdentity, checkpointID string, remove int64) (CheckpointArchive, error) {
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
	fd, err := unix.Openat(int(directory.Fd()), name, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if errors.Is(err, unix.ENOENT) {
		if remove >= 0 {
			// An interrupted removal may have moved the archive to its trash name and not unlinked it yet.
			var leftover unix.Stat_t
			if unix.Fstatat(int(directory.Fd()), name+".deleting", &leftover, unix.AT_SYMLINK_NOFOLLOW) == nil {
				if leftover.Mode&unix.S_IFMT != unix.S_IFREG || leftover.Nlink != 1 || leftover.Size != remove {
					return zero, ErrArchiveMismatch
				}
				if err := r.finishTrash(directory, name+".deleting", leftover, name); err != nil {
					return zero, err
				}
			}
		}
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
	// Unlink only the file that was verified. A name can be swapped between the checks above and an unlink by name,
	// so the file is first moved to a fixed trash name (without replacing anything), identified again by device and
	// inode against the verified descriptor, and only then unlinked. A different file is moved back, never deleted.
	trash := name + ".deleting"
	if err := unix.Renameat2(int(directory.Fd()), name, int(directory.Fd()), trash, unix.RENAME_NOREPLACE); err != nil {
		if errors.Is(err, unix.ENOENT) {
			return found, r.finishTrash(directory, trash, stat, name)
		}
		return zero, err
	}
	if err := r.finishTrash(directory, trash, stat, name); err != nil {
		return zero, err
	}
	return found, nil
}

// finishTrash unlinks the trash name when it is the verified file (same device and inode, a regular file with one
// link), syncs the directory and confirms that neither name remains. A trash entry that is another file is moved back
// to the archive name when that is free and reported as a mismatch. A missing trash name is an interrupted earlier
// attempt that already finished the unlink.
func (r *Root) finishTrash(directory *os.File, trash string, verified unix.Stat_t, name string) error {
	var current unix.Stat_t
	err := unix.Fstatat(int(directory.Fd()), trash, &current, unix.AT_SYMLINK_NOFOLLOW)
	if err == nil {
		if current.Mode&unix.S_IFMT != unix.S_IFREG || current.Nlink != 1 || current.Dev != verified.Dev || current.Ino != verified.Ino {
			_ = unix.Renameat2(int(directory.Fd()), trash, int(directory.Fd()), name, unix.RENAME_NOREPLACE)
			return ErrArchiveMismatch
		}
		if err := unix.Unlinkat(int(directory.Fd()), trash, 0); err != nil && !errors.Is(err, unix.ENOENT) {
			return err
		}
	} else if !errors.Is(err, unix.ENOENT) {
		return err
	}
	if err := directory.Sync(); err != nil {
		return err
	}
	for _, n := range []string{name, trash} {
		var after unix.Stat_t
		if err := unix.Fstatat(int(directory.Fd()), n, &after, unix.AT_SYMLINK_NOFOLLOW); !errors.Is(err, unix.ENOENT) {
			return ErrArchiveRemovalIncomplete
		}
	}
	return nil
}
