//go:build linux && (amd64 || arm64)

package data

import (
	"archive/tar"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"io"
	"os"
	"path"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"golang.org/x/sys/unix"
)

const (
	checkpointDirectory     = ".portal-checkpoints"
	checkpointManifestEntry = ".portal-checkpoint-manifest.json"
	checkpointFormat        = "portal.runner.data-checkpoint/1-dev.1"
	maxCheckpointEntries    = 100000
	maxCheckpointDepth      = 64
	maxCheckpointNameBytes  = 255
)

type checkpointManifest struct {
	Format                   string `json:"format"`
	CheckpointID             string `json:"checkpoint_id"`
	NamespaceID              string `json:"namespace_id"`
	ReleaseSHA256            string `json:"release_sha256"`
	ProjectID                uint32 `json:"project_id"`
	WriterFence              int64  `json:"writer_fence"`
	DataFormat               int64  `json:"data_format"`
	NamespaceInode           uint64 `json:"namespace_inode"`
	Device                   uint64 `json:"device"`
	SourceFilesystemUUID     string `json:"source_filesystem_uuid"`
	SourceRootInode          uint64 `json:"source_root_inode"`
	CheckpointFilesystemUUID string `json:"checkpoint_filesystem_uuid"`
	CheckpointRootInode      uint64 `json:"checkpoint_root_inode"`
}

type checkpointArchiveHash struct {
	hash  hash.Hash
	bytes int64
}

func (h *checkpointArchiveHash) Write(raw []byte) (int, error) {
	h.bytes += int64(len(raw))
	return h.hash.Write(raw)
}

func newCheckpointArchiveHash() *checkpointArchiveHash {
	return &checkpointArchiveHash{hash: sha256.New()}
}

// Checkpoint serializes one stopped namespace into an exclusive archive under
// the distinct checkpoint root. It does not decide whether the source process is
// stopped or whether a Portal operation is admitted.
func (r *Root) Checkpoint(ctx context.Context, expected RootIdentity, source *Root, sourceExpected RootIdentity, request CheckpointRequest) (CheckpointObservation, error) {
	var zero CheckpointObservation
	if r == nil || source == nil || r == source || !request.valid() {
		return zero, ErrUnsafe
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	source.mu.Lock()
	defer source.mu.Unlock()
	if r.closed || source.closed || expected == sourceExpected {
		return zero, ErrUnsafe
	}
	checkpointIdentity, err := r.identity(ctx)
	if err != nil || checkpointIdentity != expected {
		return zero, ErrUnsafe
	}
	sourceIdentity, err := source.identity(ctx)
	if err != nil || sourceIdentity != sourceExpected {
		return zero, ErrUnsafe
	}
	namespace, err := source.openNamespace(request.NamespaceID)
	if err != nil {
		return zero, err
	}
	defer namespace.Close()
	namespaceStat, err := checkpointNamespaceStat(source, namespace, request)
	if err != nil {
		return zero, err
	}
	directory, err := r.openCheckpointDirectory()
	if err != nil {
		return zero, fmt.Errorf("verify checkpoint directory: %w", err)
	}
	defer directory.Close()
	name := request.CheckpointID + ".tar"
	fd, err := unix.Openat(int(directory.Fd()), name, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0600)
	if err != nil {
		if errors.Is(err, unix.EEXIST) {
			return zero, ErrConflict
		}
		return zero, err
	}
	archive := os.NewFile(uintptr(fd), "runner-data-checkpoint")
	if archive == nil {
		return zero, ErrUnsafe
	}
	ok := false
	defer func() {
		if !ok {
			_ = archive.Sync()
		}
		_ = archive.Close()
	}()
	limited := &checkpointLimitedWriter{writer: archive, limit: request.MaxArchiveBytes}
	hasher := newCheckpointArchiveHash()
	tarWriter := tar.NewWriter(io.MultiWriter(limited, hasher))
	manifest := checkpointManifest{
		Format: checkpointFormat, CheckpointID: request.CheckpointID, NamespaceID: request.NamespaceID,
		ReleaseSHA256: request.ReleaseSHA256, ProjectID: request.ProjectID, WriterFence: request.WriterFence,
		DataFormat: request.DataFormat, NamespaceInode: uint64(namespaceStat.Ino), Device: uint64(namespaceStat.Dev),
		SourceFilesystemUUID: sourceIdentity.FilesystemUUID, SourceRootInode: sourceIdentity.Inode,
		CheckpointFilesystemUUID: checkpointIdentity.FilesystemUUID, CheckpointRootInode: checkpointIdentity.Inode,
	}
	manifestRaw, err := json.Marshal(manifest)
	if err != nil || len(manifestRaw) > 4096 {
		return zero, ErrCheckpointUnsafe
	}
	if err := tarWriter.WriteHeader(&tar.Header{
		Format: tar.FormatPAX, Name: checkpointManifestEntry, Mode: 0600,
		Size: int64(len(manifestRaw)), Typeflag: tar.TypeReg,
	}); err != nil {
		return zero, checkpointIncomplete(err)
	}
	if _, err := tarWriter.Write(manifestRaw); err != nil {
		return zero, checkpointIncomplete(err)
	}
	walker := checkpointWalker{ctx: ctx, tar: tarWriter, request: request}
	entries, err := namespace.ReadDir(-1)
	if err != nil {
		return zero, checkpointIncomplete(err)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	for _, entry := range entries {
		if err := walker.add(namespace, entry.Name(), entry.Name(), 1); err != nil {
			return zero, err
		}
	}
	if err := checkpointAfter(namespace, namespaceStat); err != nil {
		return zero, err
	}
	if err := ctx.Err(); err != nil {
		return zero, checkpointIncomplete(err)
	}
	if err := tarWriter.Close(); err != nil {
		return zero, checkpointIncomplete(err)
	}
	if limited.exceeded {
		return zero, checkpointIncomplete(ErrCheckpointUnsafe)
	}
	if err := archive.Sync(); err != nil {
		return zero, checkpointIncomplete(err)
	}
	if err := archive.Chmod(0400); err != nil {
		return zero, checkpointIncomplete(err)
	}
	currentSource, err := source.identity(ctx)
	if err != nil || currentSource != sourceExpected {
		return zero, checkpointIncomplete(ErrCheckpointUnsafe)
	}
	currentCheckpoint, err := r.identity(ctx)
	if err != nil || currentCheckpoint != expected {
		return zero, checkpointIncomplete(ErrCheckpointUnsafe)
	}
	archiveStat, err := archive.Stat()
	if err != nil || archiveStat.Size() <= 0 || archiveStat.Size() != hasher.bytes || archiveStat.Size() > request.MaxArchiveBytes {
		return zero, checkpointIncomplete(ErrCheckpointUnsafe)
	}
	if err := unix.Fsync(int(directory.Fd())); err != nil {
		return zero, checkpointIncomplete(err)
	}
	digest := hex.EncodeToString(hasher.hash.Sum(nil))
	verified, err := r.verifyCheckpointLocked(ctx, expected, sourceExpected, request, digest)
	if err != nil {
		return zero, checkpointIncomplete(err)
	}
	ok = true
	verified.ObservedMS = time.Now().UTC().UnixMilli()
	return CheckpointObservation{snapshot: verified, valid: true}, nil
}

func checkpointIncomplete(err error) error {
	return errors.Join(ErrCheckpointIncomplete, err)
}

func checkpointNamespaceStat(source *Root, namespace *os.File, request CheckpointRequest) (unix.Stat_t, error) {
	var stat unix.Stat_t
	if err := unix.Fstat(int(namespace.Fd()), &stat); err != nil {
		return unix.Stat_t{}, err
	}
	if uint64(stat.Dev) != request.ExpectedDevice || uint64(stat.Ino) != request.ExpectedNamespaceInode || stat.Uid != 0 || stat.Mode&unix.S_IFMT != unix.S_IFDIR || uint64(stat.Dev) != source.device {
		return unix.Stat_t{}, ErrCheckpointUnsafe
	}
	attr, err := attributes(namespace)
	if err != nil {
		return unix.Stat_t{}, err
	}
	if binary.NativeEndian.Uint32(attr[12:16]) != request.ProjectID || binary.NativeEndian.Uint32(attr[:4])&projectInherit == 0 {
		return unix.Stat_t{}, ErrCheckpointUnsafe
	}
	limits, err := readLimits(source.file, request.ProjectID)
	if err != nil || limits.bytes <= 0 || limits.inodes <= 0 || limits.usedBytes < 0 || limits.usedInodes < 0 {
		return unix.Stat_t{}, ErrCheckpointUnsafe
	}
	return stat, nil
}

func (r *Root) openCheckpointDirectory() (*os.File, error) {
	fd, err := unix.Openat(int(r.file.Fd()), checkpointDirectory, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if errors.Is(err, unix.ENOENT) {
		if err := unix.Mkdirat(int(r.file.Fd()), checkpointDirectory, 0700); err != nil && !errors.Is(err, unix.EEXIST) {
			return nil, err
		}
		fd, err = unix.Openat(int(r.file.Fd()), checkpointDirectory, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	}
	if err != nil {
		return nil, err
	}
	result := os.NewFile(uintptr(fd), "runner-data-checkpoints")
	if result == nil {
		return nil, ErrUnsafe
	}
	ok := false
	defer func() {
		if !ok {
			result.Close()
		}
	}()
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil || uint64(stat.Dev) != r.device || stat.Uid != 0 || stat.Mode&unix.S_IFMT != unix.S_IFDIR || stat.Mode&07777 != 0700 {
		return nil, ErrUnsafe
	}
	attr, err := attributes(result)
	if err != nil || binary.NativeEndian.Uint32(attr[12:16]) != 0 || binary.NativeEndian.Uint32(attr[:4])&projectInherit != 0 {
		return nil, ErrUnsafe
	}
	ok = true
	return result, nil
}

type checkpointLimitedWriter struct {
	writer   io.Writer
	limit    int64
	written  int64
	exceeded bool
}

func (w *checkpointLimitedWriter) Write(raw []byte) (int, error) {
	if w.exceeded || int64(len(raw)) > w.limit-w.written {
		w.exceeded = true
		return 0, ErrCheckpointUnsafe
	}
	n, err := w.writer.Write(raw)
	w.written += int64(n)
	return n, err
}

type checkpointWalker struct {
	ctx     context.Context
	tar     *tar.Writer
	request CheckpointRequest
	entries int64
	bytes   int64
}

func (w *checkpointWalker) add(parent *os.File, name, relative string, depth int) error {
	if err := w.ctx.Err(); err != nil {
		return checkpointIncomplete(err)
	}
	if depth > maxCheckpointDepth || w.entries >= maxCheckpointEntries || name == "" || name == "." || name == ".." || len(name) > maxCheckpointNameBytes || len(relative) > 4096 || !utf8.ValidString(name) || strings.ContainsAny(name, "/\x00\r\n") || relative == checkpointManifestEntry {
		return ErrCheckpointUnsafe
	}
	var before unix.Stat_t
	if err := unix.Fstatat(int(parent.Fd()), name, &before, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return checkpointIncomplete(err)
	}
	if !checkpointModeAllowed(before.Mode, before.Gid) {
		return ErrCheckpointUnsafe
	}
	if !checkpointOwnerAllowed(before.Uid, before.Gid) {
		return ErrCheckpointUnsafe
	}
	switch before.Mode & unix.S_IFMT {
	case unix.S_IFDIR:
		if err := w.tar.WriteHeader(checkpointHeader(relative+"/", before, 0, tar.TypeDir)); err != nil {
			return checkpointIncomplete(err)
		}
		fd, err := unix.Openat(int(parent.Fd()), name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		if err != nil {
			return checkpointIncomplete(err)
		}
		folder := os.NewFile(uintptr(fd), "runner-data-checkpoint-dir")
		if folder == nil {
			return checkpointIncomplete(ErrCheckpointUnsafe)
		}
		defer folder.Close()
		w.entries++
		entries, err := folder.ReadDir(-1)
		if err != nil {
			return checkpointIncomplete(err)
		}
		sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
		for _, entry := range entries {
			if err := w.add(folder, entry.Name(), path.Join(relative, entry.Name()), depth+1); err != nil {
				return err
			}
		}
		if err := checkpointAfter(folder, before); err != nil {
			return err
		}
		return nil
	case unix.S_IFREG:
		if before.Nlink > 1 || before.Size < 0 || before.Size > w.request.MaxArchiveBytes-w.bytes {
			return ErrCheckpointUnsafe
		}
		fd, err := unix.Openat(int(parent.Fd()), name, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		if err != nil {
			return checkpointIncomplete(err)
		}
		file := os.NewFile(uintptr(fd), "runner-data-checkpoint-file")
		if file == nil {
			return checkpointIncomplete(ErrCheckpointUnsafe)
		}
		defer file.Close()
		if err := w.tar.WriteHeader(checkpointHeader(relative, before, before.Size, tar.TypeReg)); err != nil {
			return checkpointIncomplete(err)
		}
		copied, err := io.CopyN(w.tar, file, before.Size)
		if err != nil || copied != before.Size {
			return checkpointIncomplete(ErrCheckpointUnsafe)
		}
		var extra [1]byte
		if n, err := file.Read(extra[:]); n != 0 || err != io.EOF {
			return ErrCheckpointUnsafe
		}
		w.entries++
		w.bytes += before.Size
		return checkpointAfter(file, before)
	default:
		return ErrCheckpointUnsafe
	}
}

func checkpointHeader(name string, stat unix.Stat_t, size int64, kind byte) *tar.Header {
	mode := stat.Mode & 0777
	if kind == tar.TypeDir && stat.Mode&unix.S_ISGID != 0 && stat.Gid == writeAccessGID {
		mode |= unix.S_ISGID
	}
	return &tar.Header{
		Format: tar.FormatPAX, Name: name, Mode: int64(mode), Uid: int(stat.Uid), Gid: int(stat.Gid),
		Size: size, ModTime: time.Unix(0, 0).UTC(), Typeflag: kind,
	}
}

func checkpointAfter(file *os.File, before unix.Stat_t) error {
	var after unix.Stat_t
	if err := unix.Fstat(int(file.Fd()), &after); err != nil {
		return checkpointIncomplete(err)
	}
	if before.Dev != after.Dev || before.Ino != after.Ino || before.Mode != after.Mode || before.Uid != after.Uid || before.Gid != after.Gid || before.Size != after.Size || before.Mtim != after.Mtim || before.Ctim != after.Ctim {
		return ErrCheckpointUnsafe
	}
	return nil
}

func checkpointOwnerAllowed(uid, gid uint32) bool {
	return (uid == 0 && (gid == 0 || gid == writeAccessGID)) ||
		(uid == writeAccessGID && gid == writeAccessGID)
}

func checkpointModeAllowed(mode uint32, gid uint32) bool {
	if mode&(unix.S_ISUID|unix.S_ISVTX) != 0 {
		return false
	}
	if mode&unix.S_ISGID != 0 {
		return mode&unix.S_IFMT == unix.S_IFDIR && gid == writeAccessGID
	}
	return true
}

// VerifyCheckpoint recomputes the archive hash and validates its bound manifest
// without changing the checkpoint or source namespace.
func (r *Root) VerifyCheckpoint(ctx context.Context, expected RootIdentity, sourceExpected RootIdentity, request CheckpointRequest, manifestSHA256 string) (CheckpointObservation, error) {
	var zero CheckpointObservation
	if r == nil || !request.valid() || !sha256Value(manifestSHA256) {
		return zero, ErrUnsafe
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed || expected == sourceExpected {
		return zero, ErrUnsafe
	}
	identity, err := r.identity(ctx)
	if err != nil || identity != expected {
		return zero, ErrUnsafe
	}
	snapshot, err := r.verifyCheckpointLocked(ctx, expected, sourceExpected, request, manifestSHA256)
	if err != nil {
		return zero, err
	}
	snapshot.ObservedMS = time.Now().UTC().UnixMilli()
	return CheckpointObservation{snapshot: snapshot, valid: true}, nil
}

func (r *Root) verifyCheckpointLocked(ctx context.Context, expected, sourceExpected RootIdentity, request CheckpointRequest, manifestSHA256 string) (CheckpointSnapshot, error) {
	var zero CheckpointSnapshot
	directory, err := r.openCheckpointDirectory()
	if err != nil {
		return zero, err
	}
	defer directory.Close()
	fd, err := unix.Openat(int(directory.Fd()), request.CheckpointID+".tar", unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return zero, fmt.Errorf("%w: verify checkpoint archive open", ErrCheckpointUnsafe)
	}
	archive := os.NewFile(uintptr(fd), "runner-data-checkpoint-read")
	if archive == nil {
		return zero, fmt.Errorf("%w: verify checkpoint archive handle", ErrCheckpointUnsafe)
	}
	defer archive.Close()
	stat, err := archive.Stat()
	if err != nil || stat.Size() <= 0 || stat.Size() > request.MaxArchiveBytes || !stat.Mode().IsRegular() || stat.Mode().Perm() != 0400 {
		return zero, fmt.Errorf("%w: verify checkpoint archive stat", ErrCheckpointUnsafe)
	}
	hasher := sha256.New()
	copied, err := io.Copy(hasher, archive)
	if err != nil || copied != stat.Size() || hex.EncodeToString(hasher.Sum(nil)) != manifestSHA256 {
		return zero, fmt.Errorf("%w: verify checkpoint archive digest", ErrCheckpointUnsafe)
	}
	if _, err := archive.Seek(0, io.SeekStart); err != nil {
		return zero, fmt.Errorf("%w: verify checkpoint archive seek", ErrCheckpointUnsafe)
	}
	reader := tar.NewReader(archive)
	first, err := reader.Next()
	if err != nil || first.Name != checkpointManifestEntry || first.Typeflag != tar.TypeReg || first.Size <= 0 || first.Size > 4096 {
		return zero, fmt.Errorf("%w: verify checkpoint manifest entry", ErrCheckpointUnsafe)
	}
	manifestRaw, err := io.ReadAll(io.LimitReader(reader, 4097))
	if err != nil || int64(len(manifestRaw)) != first.Size {
		return zero, fmt.Errorf("%w: verify checkpoint manifest read", ErrCheckpointUnsafe)
	}
	var manifest checkpointManifest
	if json.Unmarshal(manifestRaw, &manifest) != nil || manifest.Format != checkpointFormat ||
		manifest.CheckpointID != request.CheckpointID || manifest.NamespaceID != request.NamespaceID ||
		manifest.ReleaseSHA256 != request.ReleaseSHA256 || manifest.ProjectID != request.ProjectID ||
		manifest.WriterFence != request.WriterFence || manifest.DataFormat != request.DataFormat ||
		manifest.NamespaceInode != request.ExpectedNamespaceInode || manifest.Device != request.ExpectedDevice ||
		manifest.SourceFilesystemUUID != sourceExpected.FilesystemUUID || manifest.SourceRootInode != sourceExpected.Inode ||
		manifest.CheckpointFilesystemUUID != expected.FilesystemUUID || manifest.CheckpointRootInode != expected.Inode {
		return zero, fmt.Errorf("%w: verify checkpoint manifest binding", ErrCheckpointUnsafe)
	}
	canonicalManifest, err := json.Marshal(manifest)
	if err != nil || string(canonicalManifest) != string(manifestRaw) {
		return zero, fmt.Errorf("%w: verify checkpoint manifest canonical", ErrCheckpointUnsafe)
	}
	seen := map[string]bool{}
	last := ""
	var entries, bytes int64
	for {
		if err := ctx.Err(); err != nil {
			return zero, err
		}
		header, err := reader.Next()
		if err == io.EOF {
			break
		}
		if err != nil || header.Name == "" || header.Name == checkpointManifestEntry || seen[header.Name] || header.Name < last ||
			header.Name == "." || header.Name == ".." || strings.HasPrefix(header.Name, "/") || strings.Contains(header.Name, "\\") || path.Clean(strings.TrimSuffix(header.Name, "/")) != strings.TrimSuffix(header.Name, "/") ||
			(header.Typeflag != tar.TypeDir && header.Typeflag != tar.TypeReg) || entries >= maxCheckpointEntries {
			return zero, fmt.Errorf("%w: verify checkpoint entry path", ErrCheckpointUnsafe)
		}
		if header.Typeflag == tar.TypeDir {
			if header.Mode&(unix.S_ISUID|unix.S_ISVTX) != 0 ||
				(header.Mode&unix.S_ISGID != 0 && header.Gid != int(writeAccessGID)) {
				return zero, ErrCheckpointUnsafe
			}
		} else if header.Mode&07000 != 0 {
			return zero, fmt.Errorf("%w: verify checkpoint entry mode", ErrCheckpointUnsafe)
		}
		if header.Uid < 0 || header.Gid < 0 || !checkpointOwnerAllowed(uint32(header.Uid), uint32(header.Gid)) {
			return zero, fmt.Errorf("%w: verify checkpoint entry owner", ErrCheckpointUnsafe)
		}
		if header.Typeflag == tar.TypeReg {
			if header.Size < 0 || header.Size > request.MaxArchiveBytes-bytes {
				return zero, fmt.Errorf("%w: verify checkpoint entry size", ErrCheckpointUnsafe)
			}
			copied, err := io.CopyN(io.Discard, reader, header.Size)
			if err != nil || copied != header.Size {
				return zero, fmt.Errorf("%w: verify checkpoint entry content", ErrCheckpointUnsafe)
			}
			bytes += copied
		} else if header.Size != 0 {
			return zero, fmt.Errorf("%w: verify checkpoint entry directory size", ErrCheckpointUnsafe)
		}
		seen[header.Name] = true
		last = header.Name
		entries++
	}
	if stat.Size() > request.MaxArchiveBytes {
		return zero, fmt.Errorf("%w: verify checkpoint archive size", ErrCheckpointUnsafe)
	}
	return CheckpointSnapshot{
		NamespaceID: request.NamespaceID, ReleaseSHA256: request.ReleaseSHA256, ProjectID: request.ProjectID,
		DataFormat: request.DataFormat, ArchiveBytes: stat.Size(), Entries: entries, ManifestSHA256: manifestSHA256,
	}, nil
}
