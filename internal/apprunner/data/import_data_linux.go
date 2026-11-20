//go:build linux && (amd64 || arm64)

package data

import (
	"archive/tar"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

// openImportInbox opens the import inbox under the checkpoint root without creating it: a missing inbox is
// unix.ENOENT, so a read-only caller never changes the root.
func (r *Root) openImportInbox() (*os.File, error) {
	fd, err := unix.Openat(int(r.file.Fd()), ImportInboxDirectory, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	dir := os.NewFile(uintptr(fd), "runner-import-inbox")
	if dir == nil {
		return nil, ErrUnsafe
	}
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil || uint64(stat.Dev) != r.device || stat.Uid != 0 ||
		stat.Mode&unix.S_IFMT != unix.S_IFDIR || stat.Mode&07777 != 0700 {
		dir.Close()
		return nil, ErrUnsafe
	}
	return dir, nil
}

// findImportArchive returns the one inbox archive whose manifest carries the digest, opened read-only with its
// file profile checked (a regular, root-owned, single-link 0400 file). Only manifests are read here; the body
// digest is verified by InspectImportArchive before the content is trusted.
func (r *Root) findImportArchive(digest string) (*os.File, string, error) {
	dir, err := r.openImportInbox()
	if errors.Is(err, unix.ENOENT) {
		return nil, "", ErrImportArchiveMissing
	}
	if err != nil {
		return nil, "", err
	}
	defer dir.Close()
	entries, err := dir.ReadDir(-1)
	if err != nil {
		return nil, "", err
	}
	var found *os.File
	var name string
	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), ".tar") || strings.HasPrefix(entry.Name(), ".") {
			continue
		}
		fd, err := unix.Openat(int(dir.Fd()), entry.Name(), unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
		if err != nil {
			continue
		}
		file := os.NewFile(uintptr(fd), "runner-import-archive")
		var stat unix.Stat_t
		if file == nil || unix.Fstat(fd, &stat) != nil || stat.Mode&unix.S_IFMT != unix.S_IFREG ||
			stat.Mode&07777 != 0400 || stat.Uid != 0 || stat.Nlink != 1 || stat.Size <= ImportManifestBlockBytes {
			if file != nil {
				file.Close()
			}
			continue
		}
		reader := tar.NewReader(file)
		first, err := reader.Next()
		if err == nil && first.Name == ArchiveManifestEntry && first.Typeflag == tar.TypeReg &&
			first.Size > 0 && first.Size <= MaxImportManifestBytes {
			raw, readErr := io.ReadAll(io.LimitReader(reader, MaxImportManifestBytes+1))
			if manifest, parseErr := ParseImportManifest(raw); readErr == nil && parseErr == nil && manifest.BodySHA256 == digest {
				if found != nil {
					found.Close()
					file.Close()
					return nil, "", ErrImportArchiveAmbiguous
				}
				found, name = file, entry.Name()
				continue
			}
		}
		file.Close()
	}
	if found == nil {
		return nil, "", ErrImportArchiveMissing
	}
	return found, name, nil
}

// InspectImportInbox confirms that exactly one inbox archive has the digest and fully validates it: file profile,
// body digest, manifest counts and every entry. It changes nothing. maxEntries bounds the walk.
func (r *Root) InspectImportInbox(ctx context.Context, expected RootIdentity, digest string, maxEntries int64) (ImportInboxArchive, error) {
	var zero ImportInboxArchive
	if r == nil || !sha256Value(digest) {
		return zero, ErrImportUnsafe
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return zero, ErrImportUnsafe
	}
	if err := r.requireIdentity(ctx, expected); err != nil {
		return zero, errors.Join(ErrImportUnsafe, err)
	}
	file, _, err := r.findImportArchive(digest)
	if err != nil {
		return zero, err
	}
	defer file.Close()
	result, err := InspectImportArchive(ctx, file, maxEntries)
	if err != nil {
		return zero, err
	}
	if err := r.requireIdentity(ctx, expected); err != nil {
		return zero, errors.Join(ErrImportUnsafe, err)
	}
	return result, nil
}

// RemoveImportArchive deletes the one inbox archive with the digest. The caller does so only once the import has
// completed; a failed import keeps its archive for another try. An archive already gone is not an error.
func (r *Root) RemoveImportArchive(ctx context.Context, expected RootIdentity, digest string) error {
	if r == nil || !sha256Value(digest) {
		return ErrImportUnsafe
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return ErrImportUnsafe
	}
	if err := r.requireIdentity(ctx, expected); err != nil {
		return errors.Join(ErrImportUnsafe, err)
	}
	file, name, err := r.findImportArchive(digest)
	if errors.Is(err, ErrImportArchiveMissing) {
		return nil
	}
	if err != nil {
		return err
	}
	file.Close()
	dir, err := r.openImportInbox()
	if err != nil {
		return err
	}
	defer dir.Close()
	if err := unix.Unlinkat(int(dir.Fd()), name, 0); err != nil && !errors.Is(err, unix.ENOENT) {
		return err
	}
	return dir.Sync()
}

func (r ImportDataRequest) restoreRequest() RestoreRequest {
	return RestoreRequest{
		CheckpointID: r.NamespaceID, NamespaceID: r.NamespaceID, ProjectID: r.ProjectID, DataFormat: max(r.DataFormat, 1),
		ArchiveBytes: r.ArchiveBytes, MaxArchiveBytes: r.MaxArchiveBytes, MaxEntries: r.QuotaInodes,
		ExpectedDevice: r.ExpectedDevice, ExpectedNamespaceInode: r.ExpectedNamespaceInode, WriterFence: r.WriterFence,
	}
}

// ImportData replaces the contents of an existing namespace with an inbox archive. The receiver is the checkpoint
// root, which holds the inbox; source is the data root. The namespace inode, project, inheritance and quota stay
// as they are: the namespace's children are cleared and the archive is extracted into the same inode, exactly as
// a restore does, reusing its extraction and read-back.
//
// Before anything is cleared the archive is found by digest, verified, checked against the namespace quota and its
// schema marker is compared with the release's data format. Any failure after clearing begins is incomplete, never
// safe to assume reverted: the caller restores the pre-import checkpoint. The call is convergent, so repeating it
// over a partly imported namespace reaches the same verified end.
func (r *Root) ImportData(ctx context.Context, checkpointExpected RootIdentity, source *Root, sourceExpected RootIdentity, request ImportDataRequest) (ImportDataObservation, error) {
	var zero ImportDataObservation
	if r == nil || source == nil || r == source || !request.valid() {
		return zero, ErrImportUnsafe
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	source.mu.Lock()
	defer source.mu.Unlock()
	if r.closed || source.closed || checkpointExpected == sourceExpected {
		return zero, ErrImportUnsafe
	}
	if err := ctx.Err(); err != nil {
		return zero, errors.Join(ErrImportUnsafe, err)
	}
	if err := r.requireIdentity(ctx, checkpointExpected); err != nil {
		return zero, errors.Join(ErrImportUnsafe, err)
	}
	if err := source.requireIdentity(ctx, sourceExpected); err != nil {
		return zero, errors.Join(ErrImportUnsafe, err)
	}
	restoreRequest := request.restoreRequest()
	archive, _, err := r.findImportArchive(request.ArchiveSHA256)
	if err != nil {
		return zero, err
	}
	defer archive.Close()
	inspected, err := InspectImportArchive(ctx, archive, request.QuotaInodes)
	if err != nil {
		return zero, err
	}
	if inspected.Bytes != request.ArchiveBytes || inspected.Manifest.Entries != request.Entries || inspected.Manifest.Bytes != request.Bytes {
		return zero, errors.Join(ErrImportUnsafe, errors.New("archive differs from the reviewed size or counts"))
	}
	if int64(inspected.SchemaMarker) != request.DataFormat {
		return zero, ErrImportFormatMismatch
	}
	entries, err := restoreArchiveEntries(ctx, archive, restoreRequest)
	if err != nil {
		return zero, errors.Join(ErrImportUnsafe, fmt.Errorf("read archive entries: %w", err))
	}
	if err := source.importFitsQuota(entries, request.QuotaBytes, request.QuotaInodes); err != nil {
		return zero, ErrImportTooLarge
	}
	namespace, err := source.openNamespace(request.NamespaceID)
	if err != nil {
		return zero, ErrImportUnsafe
	}
	defer namespace.Close()
	before, err := checkpointNamespaceStat(source, namespace, restoreRequest.checkpointRequest())
	if err != nil {
		return zero, ErrImportUnsafe
	}
	limit, err := readLimits(source.file, request.ProjectID)
	if err != nil || limit.bytes != request.QuotaBytes || limit.inodes != request.QuotaInodes {
		return zero, errors.Join(ErrImportUnsafe, errors.New("namespace quota differs from the reviewed quota"))
	}
	if _, err := archive.Seek(0, io.SeekStart); err != nil {
		return zero, ErrImportUnsafe
	}

	incomplete := func(err error) (ImportDataObservation, error) {
		return zero, errors.Join(ErrImportIncomplete, err)
	}
	count := 0
	if err := restoreClearNamespace(ctx, namespace, source.device, 1, &count, restoreRequest.pathLimit()); err != nil {
		return incomplete(fmt.Errorf("clear namespace: %w", err))
	}
	if err := namespace.Sync(); err != nil {
		return incomplete(fmt.Errorf("sync cleared namespace: %w", err))
	}
	if err := restoreExtract(ctx, archive, namespace, source.device, restoreRequest); err != nil {
		return incomplete(fmt.Errorf("extract archive: %w", err))
	}
	if err := restoreApplyDirectoryMetadata(ctx, namespace, source.device, entries); err != nil {
		return incomplete(fmt.Errorf("apply directory metadata: %w", err))
	}
	if err := restoreSyncNamespace(ctx, namespace, source.device); err != nil {
		return incomplete(fmt.Errorf("sync imported namespace: %w", err))
	}
	if err := verifyImportBody(archive, request); err != nil {
		return incomplete(fmt.Errorf("recheck archive: %w", err))
	}
	read, err := restoreReadNamespace(ctx, namespace, source.device, restoreRequest)
	if err != nil {
		return incomplete(fmt.Errorf("verify imported namespace: %w", err))
	}
	if !sameRestoreEntries(entries, read) {
		return incomplete(fmt.Errorf("verify imported namespace: %s", restoreEntryDifference(entries, read)))
	}
	var after unix.Stat_t
	if err := unix.Fstat(int(namespace.Fd()), &after); err != nil || uint64(after.Dev) != uint64(before.Dev) || after.Ino != before.Ino {
		return incomplete(ErrImportUnsafe)
	}
	used, err := readLimits(source.file, request.ProjectID)
	if err != nil || used.bytes != request.QuotaBytes || used.inodes != request.QuotaInodes ||
		used.usedBytes < 0 || used.usedBytes > used.bytes || used.usedInodes < 0 || used.usedInodes > used.inodes {
		return incomplete(errors.Join(ErrImportUnsafe, err))
	}
	if err := source.requireIdentity(ctx, sourceExpected); err != nil {
		return incomplete(err)
	}
	if err := r.requireIdentity(ctx, checkpointExpected); err != nil {
		return incomplete(err)
	}
	return ImportDataObservation{valid: true, snapshot: ImportDataSnapshot{
		NamespaceID: request.NamespaceID, ArchiveSHA256: request.ArchiveSHA256, ProjectID: request.ProjectID,
		DataFormat: request.DataFormat, Entries: int64(len(entries)),
		UsedBytes: used.usedBytes, UsedInodes: used.usedInodes, ByteLimit: used.bytes, InodeLimit: used.inodes,
		ObservedMS: time.Now().UTC().UnixMilli(), NamespaceDevice: uint64(after.Dev), NamespaceInode: after.Ino,
	}}, nil
}

// verifyImportBody re-hashes the archive body, so a file replaced during the extraction is caught.
func verifyImportBody(archive *os.File, request ImportDataRequest) error {
	info, err := archive.Stat()
	if err != nil || info.Size() != request.ArchiveBytes {
		return ErrImportUnsafe
	}
	if _, err := archive.Seek(ImportManifestBlockBytes, io.SeekStart); err != nil {
		return err
	}
	sum := sha256.New()
	if _, err := io.Copy(sum, archive); err != nil || hex.EncodeToString(sum.Sum(nil)) != request.ArchiveSHA256 {
		return ErrImportUnsafe
	}
	return nil
}
