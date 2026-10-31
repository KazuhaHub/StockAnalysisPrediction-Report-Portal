//go:build linux && (amd64 || arm64)

package data

import (
	"archive/tar"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"golang.org/x/sys/unix"
)

type restoreEntry struct {
	name, digest string
	mode         uint32
	uid, gid     uint32
	size         int64
	directory    bool
}

// Restore replaces only the bound namespace's contents. The namespace inode,
// project ID, inheritance and quota remain unchanged. The caller must prove the
// process is stopped and durably journal the effect before calling this method.
// Any failure after clearing begins is incomplete, never safe to assume reverted.
func (r *Root) Restore(ctx context.Context, checkpointExpected RootIdentity, source *Root, sourceExpected RootIdentity, request RestoreRequest) (RestoreObservation, error) {
	var zero RestoreObservation
	if r == nil || source == nil || r == source || !request.valid() {
		return zero, ErrRestoreUnsafe
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	source.mu.Lock()
	defer source.mu.Unlock()
	if r.closed || source.closed || checkpointExpected == sourceExpected {
		return zero, ErrRestoreUnsafe
	}
	if err := ctx.Err(); err != nil {
		return zero, ErrRestoreUnsafe
	}
	checkpointIdentity, err := r.identity(ctx)
	if err != nil || checkpointIdentity != checkpointExpected {
		return zero, ErrRestoreUnsafe
	}
	sourceIdentity, err := source.identity(ctx)
	if err != nil || sourceIdentity != sourceExpected {
		return zero, ErrRestoreUnsafe
	}
	checkpointRequest := CheckpointRequest{
		CheckpointID: request.CheckpointID, NamespaceID: request.NamespaceID,
		ReleaseSHA256: request.ReleaseSHA256, ProjectID: request.ProjectID,
		DataFormat: request.DataFormat, MaxArchiveBytes: request.MaxArchiveBytes,
		ExpectedDevice: request.ExpectedDevice, ExpectedNamespaceInode: request.ExpectedNamespaceInode,
		WriterFence: request.WriterFence,
	}
	verified, err := r.verifyCheckpointLocked(ctx, checkpointExpected, sourceExpected, checkpointRequest, request.ManifestSHA256)
	if err != nil || verified.ArchiveBytes != request.ArchiveBytes {
		return zero, ErrRestoreUnsafe
	}
	namespace, err := source.openNamespace(request.NamespaceID)
	if err != nil {
		return zero, ErrRestoreUnsafe
	}
	defer namespace.Close()
	namespaceStat, err := checkpointNamespaceStat(source, namespace, checkpointRequest)
	if err != nil {
		return zero, ErrRestoreUnsafe
	}
	archive, err := r.openRestoreArchive(ctx, request)
	if err != nil {
		return zero, ErrRestoreUnsafe
	}
	defer archive.Close()
	entries, err := restoreArchiveEntries(ctx, archive, request)
	if err != nil {
		return zero, ErrRestoreUnsafe
	}
	if _, err := archive.Seek(0, io.SeekStart); err != nil {
		return zero, ErrRestoreUnsafe
	}
	if err := ctx.Err(); err != nil {
		return zero, ErrRestoreUnsafe
	}

	incomplete := func(err error) (RestoreObservation, error) {
		return zero, errors.Join(ErrRestoreIncomplete, err)
	}
	count := 0
	if err := restoreClearNamespace(ctx, namespace, source.device, 1, &count); err != nil {
		return incomplete(err)
	}
	if err := namespace.Sync(); err != nil {
		return incomplete(err)
	}
	if err := restoreExtract(ctx, archive, namespace, source.device, request); err != nil {
		return incomplete(err)
	}
	if err := restoreApplyDirectoryMetadata(ctx, namespace, source.device, entries); err != nil {
		return incomplete(err)
	}
	if err := restoreSyncNamespace(ctx, namespace, source.device); err != nil {
		return incomplete(err)
	}
	if err := restoreArchiveDigest(ctx, archive, request); err != nil {
		return incomplete(err)
	}
	restored, err := restoreReadNamespace(ctx, namespace, source.device, request)
	if err != nil || !sameRestoreEntries(entries, restored) {
		return incomplete(ErrRestoreUnsafe)
	}
	currentStat := unix.Stat_t{}
	if err := unix.Fstat(int(namespace.Fd()), &currentStat); err != nil ||
		uint64(currentStat.Dev) != namespaceStat.Dev || currentStat.Ino != namespaceStat.Ino {
		return incomplete(ErrRestoreUnsafe)
	}
	limits, err := readLimits(source.file, request.ProjectID)
	if err != nil || limits.bytes <= 0 || limits.inodes <= 0 ||
		limits.usedBytes < 0 || limits.usedBytes > limits.bytes ||
		limits.usedInodes < 0 || limits.usedInodes > limits.inodes {
		return incomplete(ErrRestoreUnsafe)
	}
	if err := source.requireIdentity(ctx, sourceExpected); err != nil {
		return incomplete(err)
	}
	if err := r.requireIdentity(ctx, checkpointExpected); err != nil {
		return incomplete(err)
	}
	snapshot := RestoreSnapshot{
		NamespaceID: request.NamespaceID, ReleaseSHA256: request.ReleaseSHA256,
		ManifestSHA256: request.ManifestSHA256, ProjectID: request.ProjectID,
		DataFormat: request.DataFormat, Entries: int64(len(entries)),
		UsedBytes: limits.usedBytes, ObservedMS: time.Now().UTC().UnixMilli(),
		NamespaceDevice: uint64(currentStat.Dev), NamespaceInode: currentStat.Ino,
	}
	return RestoreObservation{snapshot: snapshot, valid: true}, nil
}

func (r *Root) openRestoreArchive(ctx context.Context, request RestoreRequest) (*os.File, error) {
	directory, err := r.openCheckpointDirectory()
	if err != nil {
		return nil, err
	}
	defer directory.Close()
	fd, err := unix.Openat(int(directory.Fd()), request.CheckpointID+".tar", unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, ErrRestoreUnsafe
	}
	archive := os.NewFile(uintptr(fd), "runner-data-restore")
	if archive == nil {
		return nil, ErrRestoreUnsafe
	}
	stat, err := archive.Stat()
	if err != nil || !stat.Mode().IsRegular() || stat.Mode().Perm() != 0400 ||
		stat.Size() != request.ArchiveBytes || stat.Size() > request.MaxArchiveBytes {
		archive.Close()
		return nil, ErrRestoreUnsafe
	}
	if err := restoreArchiveDigest(ctx, archive, request); err != nil {
		archive.Close()
		return nil, err
	}
	return archive, nil
}

func restoreArchiveDigest(ctx context.Context, archive *os.File, request RestoreRequest) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if _, err := archive.Seek(0, io.SeekStart); err != nil {
		return err
	}
	hash := sha256.New()
	copied, err := io.Copy(hash, io.LimitReader(archive, request.ArchiveBytes+1))
	if err != nil || copied != request.ArchiveBytes || hex.EncodeToString(hash.Sum(nil)) != request.ManifestSHA256 {
		return ErrRestoreUnsafe
	}
	return ctx.Err()
}

func restoreArchiveEntries(ctx context.Context, archive *os.File, request RestoreRequest) ([]restoreEntry, error) {
	if _, err := archive.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	reader := tar.NewReader(archive)
	manifest, err := reader.Next()
	if err != nil || manifest.Name != checkpointManifestEntry || manifest.Typeflag != tar.TypeReg ||
		manifest.Size <= 0 || manifest.Size > 4096 {
		return nil, ErrRestoreUnsafe
	}
	if _, err := io.CopyN(io.Discard, reader, manifest.Size); err != nil {
		return nil, ErrRestoreUnsafe
	}
	entries := make([]restoreEntry, 0)
	last := ""
	seen := map[string]bool{}
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		header, err := reader.Next()
		if err == io.EOF {
			break
		}
		if err != nil || header.Name == "" || header.Name == checkpointManifestEntry ||
			seen[header.Name] || header.Name < last || !validRestorePath(header.Name) ||
			(header.Typeflag != tar.TypeDir && header.Typeflag != tar.TypeReg) ||
			header.Uid < 0 || header.Gid < 0 ||
			!checkpointOwnerAllowed(uint32(header.Uid), uint32(header.Gid)) ||
			len(entries) >= maxCheckpointEntries {
			return nil, ErrRestoreUnsafe
		}
		if header.Typeflag == tar.TypeDir {
			if header.Mode&(unix.S_ISUID|unix.S_ISVTX) != 0 ||
				(header.Mode&unix.S_ISGID != 0 && header.Gid != int(writeAccessGID)) {
				return nil, ErrRestoreUnsafe
			}
		} else if header.Mode&07000 != 0 {
			return nil, ErrRestoreUnsafe
		}
		mode := uint32(header.Mode & 0777)
		if header.Typeflag == tar.TypeDir && header.Mode&unix.S_ISGID != 0 {
			mode |= unix.S_ISGID
		}
		entry := restoreEntry{
			name: header.Name, mode: mode,
			uid: uint32(header.Uid), gid: uint32(header.Gid),
			directory: header.Typeflag == tar.TypeDir,
		}
		if entry.directory {
			if header.Size != 0 || !strings.HasSuffix(entry.name, "/") {
				return nil, ErrRestoreUnsafe
			}
		} else {
			if strings.HasSuffix(entry.name, "/") || header.Size < 0 ||
				header.Size > request.ArchiveBytes {
				return nil, ErrRestoreUnsafe
			}
			entry.size = header.Size
			hash := sha256.New()
			copied, err := io.CopyN(hash, reader, header.Size)
			if err != nil || copied != header.Size {
				return nil, ErrRestoreUnsafe
			}
			entry.digest = hex.EncodeToString(hash.Sum(nil))
		}
		seen[entry.name], last = true, entry.name
		entries = append(entries, entry)
	}
	return entries, nil
}

func validRestorePath(name string) bool {
	clean := strings.TrimSuffix(name, "/")
	if clean == "" || clean == "." || clean == ".." || strings.HasPrefix(clean, "/") ||
		strings.Contains(clean, "\\") || path.Clean(clean) != clean {
		return false
	}
	parts := strings.Split(clean, "/")
	if len(parts) > maxCheckpointDepth {
		return false
	}
	for _, part := range parts {
		if part == "" || part == "." || part == ".." || len(part) > maxCheckpointNameBytes ||
			!utf8.ValidString(part) || strings.ContainsAny(part, "\x00\r\n") {
			return false
		}
	}
	return true
}

func restoreClearNamespace(ctx context.Context, parent *os.File, device uint64, depth int, count *int) error {
	if depth > maxCheckpointDepth {
		return ErrRestoreUnsafe
	}
	entries, err := parent.ReadDir(-1)
	if err != nil {
		return err
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return err
		}
		if entry.Name() == "" || entry.Name() == "." || entry.Name() == ".." ||
			strings.ContainsAny(entry.Name(), "/\x00\r\n") || *count >= maxCheckpointEntries {
			return ErrRestoreUnsafe
		}
		var stat unix.Stat_t
		if err := unix.Fstatat(int(parent.Fd()), entry.Name(), &stat, unix.AT_SYMLINK_NOFOLLOW); err != nil {
			return err
		}
		*count++
		if stat.Mode&unix.S_IFMT != unix.S_IFDIR {
			if uint64(stat.Dev) != device {
				return ErrRestoreUnsafe
			}
			if err := unix.Unlinkat(int(parent.Fd()), entry.Name(), 0); err != nil {
				return err
			}
			continue
		}
		fd, err := unix.Openat(int(parent.Fd()), entry.Name(), unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		if err != nil {
			return err
		}
		folder := os.NewFile(uintptr(fd), "runner-data-restore-clear")
		if folder == nil {
			return ErrRestoreUnsafe
		}
		var folderStat unix.Stat_t
		if err := unix.Fstat(fd, &folderStat); err != nil || uint64(folderStat.Dev) != device {
			folder.Close()
			return ErrRestoreUnsafe
		}
		err = restoreClearNamespace(ctx, folder, device, depth+1, count)
		closeErr := folder.Close()
		if err != nil {
			return err
		}
		if closeErr != nil {
			return closeErr
		}
		if err := unix.Unlinkat(int(parent.Fd()), entry.Name(), unix.AT_REMOVEDIR); err != nil {
			return err
		}
	}
	return nil
}

func restoreExtract(ctx context.Context, archive *os.File, namespace *os.File, device uint64, request RestoreRequest) error {
	if _, err := archive.Seek(0, io.SeekStart); err != nil {
		return err
	}
	reader := tar.NewReader(archive)
	manifest, err := reader.Next()
	if err != nil || manifest.Name != checkpointManifestEntry || manifest.Typeflag != tar.TypeReg {
		return ErrRestoreUnsafe
	}
	if _, err := io.CopyN(io.Discard, reader, manifest.Size); err != nil {
		return ErrRestoreUnsafe
	}
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		header, err := reader.Next()
		if err == io.EOF {
			break
		}
		if err != nil || !validRestorePath(header.Name) ||
			header.Uid < 0 || header.Gid < 0 ||
			!checkpointOwnerAllowed(uint32(header.Uid), uint32(header.Gid)) {
			return ErrRestoreUnsafe
		}
		if header.Typeflag == tar.TypeDir {
			if header.Mode&(unix.S_ISUID|unix.S_ISVTX) != 0 ||
				(header.Mode&unix.S_ISGID != 0 && header.Gid != int(writeAccessGID)) {
				return ErrRestoreUnsafe
			}
		} else if header.Typeflag != tar.TypeReg || header.Mode&07000 != 0 {
			return ErrRestoreUnsafe
		}
		parent, base, err := restoreOpenParent(namespace, header.Name, device)
		if err != nil {
			return err
		}
		switch header.Typeflag {
		case tar.TypeDir:
			if header.Size != 0 || !strings.HasSuffix(header.Name, "/") {
				parent.Close()
				return ErrRestoreUnsafe
			}
			if err := unix.Mkdirat(int(parent.Fd()), base, 0700); err != nil {
				parent.Close()
				return err
			}
		case tar.TypeReg:
			if strings.HasSuffix(header.Name, "/") || header.Size < 0 || header.Size > request.ArchiveBytes {
				parent.Close()
				return ErrRestoreUnsafe
			}
			fd, err := unix.Openat(int(parent.Fd()), base, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0600)
			if err != nil {
				parent.Close()
				return err
			}
			file := os.NewFile(uintptr(fd), "runner-data-restore-file")
			if file == nil {
				parent.Close()
				return ErrRestoreUnsafe
			}
			copied, copyErr := io.CopyN(file, reader, header.Size)
			if copyErr == nil {
				copyErr = file.Chown(header.Uid, header.Gid)
			}
			if copyErr == nil {
				copyErr = unix.Fchmod(int(file.Fd()), uint32(header.Mode&0777))
			}
			if copyErr == nil {
				copyErr = file.Sync()
			}
			closeErr := file.Close()
			if copyErr != nil || closeErr != nil || copied != header.Size {
				return errors.Join(copyErr, closeErr)
			}
		default:
			parent.Close()
			return ErrRestoreUnsafe
		}
		if err := parent.Close(); err != nil {
			return err
		}
	}
	return nil
}

func restoreOpenParent(namespace *os.File, name string, device uint64) (*os.File, string, error) {
	clean := strings.TrimSuffix(name, "/")
	parts := strings.Split(clean, "/")
	base := parts[len(parts)-1]
	fd, err := unix.FcntlInt(namespace.Fd(), unix.F_DUPFD_CLOEXEC, 0)
	if err != nil {
		return nil, "", err
	}
	current := os.NewFile(uintptr(fd), "runner-data-restore-root")
	if current == nil {
		return nil, "", ErrRestoreUnsafe
	}
	for _, part := range parts[:len(parts)-1] {
		var stat unix.Stat_t
		if err := unix.Fstatat(int(current.Fd()), part, &stat, unix.AT_SYMLINK_NOFOLLOW); err != nil ||
			stat.Mode&unix.S_IFMT != unix.S_IFDIR || uint64(stat.Dev) != device {
			current.Close()
			return nil, "", ErrRestoreUnsafe
		}
		fd, err := unix.Openat(int(current.Fd()), part, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		current.Close()
		if err != nil {
			return nil, "", err
		}
		current = os.NewFile(uintptr(fd), "runner-data-restore-dir")
		if current == nil {
			return nil, "", ErrRestoreUnsafe
		}
	}
	return current, base, nil
}

func restoreApplyDirectoryMetadata(ctx context.Context, namespace *os.File, device uint64, entries []restoreEntry) error {
	for index := len(entries) - 1; index >= 0; index-- {
		entry := entries[index]
		if !entry.directory {
			continue
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		parent, base, err := restoreOpenParent(namespace, entry.name, device)
		if err != nil {
			return err
		}
		fd, err := unix.Openat(int(parent.Fd()), base, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		parent.Close()
		if err != nil {
			return err
		}
		folder := os.NewFile(uintptr(fd), "runner-data-restore-metadata")
		if folder == nil {
			return ErrRestoreUnsafe
		}
		err = folder.Chown(int(entry.uid), int(entry.gid))
		if err == nil {
			err = unix.Fchmod(int(folder.Fd()), entry.mode)
		}
		if err == nil {
			err = folder.Sync()
		}
		closeErr := folder.Close()
		if err != nil || closeErr != nil {
			return errors.Join(err, closeErr)
		}
	}
	return nil
}

func restoreSyncNamespace(ctx context.Context, namespace *os.File, device uint64) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	entries, err := namespace.ReadDir(-1)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		var stat unix.Stat_t
		if err := unix.Fstatat(int(namespace.Fd()), entry.Name(), &stat, unix.AT_SYMLINK_NOFOLLOW); err != nil {
			return err
		}
		if stat.Mode&unix.S_IFMT == unix.S_IFDIR {
			fd, err := unix.Openat(int(namespace.Fd()), entry.Name(), unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
			if err != nil {
				return err
			}
			folder := os.NewFile(uintptr(fd), "runner-data-restore-sync")
			if folder == nil {
				return ErrRestoreUnsafe
			}
			err = restoreSyncNamespace(ctx, folder, device)
			if closeErr := folder.Close(); err == nil {
				err = closeErr
			}
			if err != nil {
				return err
			}
		}
	}
	return namespace.Sync()
}

func restoreReadNamespace(ctx context.Context, namespace *os.File, device uint64, request RestoreRequest) ([]restoreEntry, error) {
	result := make([]restoreEntry, 0)
	if err := restoreReadEntries(ctx, namespace, device, request.MaxArchiveBytes, "", 1, &result); err != nil {
		return nil, err
	}
	if len(result) > maxCheckpointEntries {
		return nil, ErrRestoreUnsafe
	}
	return result, nil
}

func restoreReadEntries(ctx context.Context, parent *os.File, device uint64, maxArchiveBytes int64, prefix string, depth int, result *[]restoreEntry) error {
	if depth > maxCheckpointDepth || len(*result) >= maxCheckpointEntries {
		return ErrRestoreUnsafe
	}
	entries, err := parent.ReadDir(-1)
	if err != nil {
		return err
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	for _, item := range entries {
		if err := ctx.Err(); err != nil {
			return err
		}
		name := item.Name()
		if name == "" || name == "." || name == ".." || strings.ContainsAny(name, "/\x00\r\n") {
			return ErrRestoreUnsafe
		}
		relative := name
		if prefix != "" {
			relative = path.Join(prefix, name)
		}
		var stat unix.Stat_t
		if err := unix.Fstatat(int(parent.Fd()), name, &stat, unix.AT_SYMLINK_NOFOLLOW); err != nil ||
			uint64(stat.Dev) != device || !checkpointModeAllowed(stat.Mode, stat.Gid) ||
			!checkpointOwnerAllowed(stat.Uid, stat.Gid) {
			return ErrRestoreUnsafe
		}
		switch stat.Mode & unix.S_IFMT {
		case unix.S_IFDIR:
			entry := restoreEntry{
				name: relative + "/", mode: stat.Mode & 07777, uid: stat.Uid, gid: stat.Gid,
				directory: true,
			}
			*result = append(*result, entry)
			fd, err := unix.Openat(int(parent.Fd()), name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
			if err != nil {
				return err
			}
			folder := os.NewFile(uintptr(fd), "runner-data-restore-read")
			if folder == nil {
				return ErrRestoreUnsafe
			}
			err = restoreReadEntries(ctx, folder, device, maxArchiveBytes, relative, depth+1, result)
			if closeErr := folder.Close(); err == nil {
				err = closeErr
			}
			if err != nil {
				return err
			}
		case unix.S_IFREG:
			if stat.Nlink != 1 || stat.Size < 0 || stat.Size > maxArchiveBytes {
				return ErrRestoreUnsafe
			}
			fd, err := unix.Openat(int(parent.Fd()), name, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
			if err != nil {
				return err
			}
			file := os.NewFile(uintptr(fd), "runner-data-restore-read-file")
			if file == nil {
				return ErrRestoreUnsafe
			}
			hash := sha256.New()
			copied, copyErr := io.CopyN(hash, file, stat.Size)
			closeErr := file.Close()
			if copyErr != nil || closeErr != nil || copied != stat.Size {
				return errors.Join(copyErr, closeErr)
			}
			*result = append(*result, restoreEntry{
				name: relative, digest: hex.EncodeToString(hash.Sum(nil)),
				mode: stat.Mode & 07777, uid: stat.Uid, gid: stat.Gid,
				size: stat.Size,
			})
		default:
			return ErrRestoreUnsafe
		}
	}
	return nil
}

func sameRestoreEntries(expected, actual []restoreEntry) bool {
	if len(expected) != len(actual) {
		return false
	}
	for index := range expected {
		if expected[index] != actual[index] {
			return false
		}
	}
	return true
}
