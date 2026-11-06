//go:build linux && (amd64 || arm64)

package data

import (
	"archive/tar"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"golang.org/x/sys/unix"
)

// ImportCheckpoint creates the namespace named by request on r and fills it
// from a retained checkpoint archive that was produced on another host. Nothing
// about the old host is trusted: the archive digest, canonical manifest, every
// entry and the quota bound are verified before the first create, and the
// manifest's old source-root fields are compared with the provenance values
// carried in the request, never with r or checkpointRoot, and its old
// checkpoint-root fields are accepted only as part of the digest-verified
// manifest.
//
// The caller must durably claim the effect before calling this method. Any
// failure after the namespace exists is incomplete, never safe to assume
// reverted, and is never retried under a new identity.
func (r *Root) ImportCheckpoint(ctx context.Context, receivingExpected RootIdentity, checkpointRoot *Root, checkpointExpected RootIdentity, request ImportRequest) (ImportObservation, error) {
	var zero ImportObservation
	if r == nil || checkpointRoot == nil || r == checkpointRoot || !request.valid() {
		return zero, ErrImportUnsafe
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	checkpointRoot.mu.Lock()
	defer checkpointRoot.mu.Unlock()
	if r.closed || checkpointRoot.closed || receivingExpected == checkpointExpected {
		return zero, ErrImportUnsafe
	}
	if err := ctx.Err(); err != nil {
		return zero, errors.Join(ErrImportUnsafe, err)
	}
	// Verification order: both root identities, the archive file and digest, the
	// canonical manifest, the entry walk, then the quota bound. No step before
	// the last may create anything.
	if err := r.requireIdentity(ctx, receivingExpected); err != nil {
		return zero, errors.Join(ErrImportUnsafe, err)
	}
	if err := checkpointRoot.requireIdentity(ctx, checkpointExpected); err != nil {
		return zero, errors.Join(ErrImportUnsafe, err)
	}
	restoreRequest := request.restoreRequest()
	archive, entries, err := checkpointRoot.openImportArchive(ctx, request, restoreRequest)
	if err != nil {
		return zero, err
	}
	defer archive.Close()
	if err := r.importFits(entries, request); err != nil {
		return zero, err
	}
	var existing unix.Stat_t
	err = native(r.file, func(fd uintptr) error {
		return unix.Fstatat(int(fd), request.NamespaceID, &existing, unix.AT_SYMLINK_NOFOLLOW)
	})
	if err == nil {
		return zero, ErrConflict
	}
	if !errors.Is(err, unix.ENOENT) {
		return zero, errors.Join(ErrImportUnsafe, err)
	}
	if _, err := archive.Seek(0, io.SeekStart); err != nil {
		return zero, errors.Join(ErrImportUnsafe, err)
	}
	allocation, err := r.prepareLocked(ctx, receivingExpected, Request{
		NamespaceID: request.NamespaceID, ProjectID: request.ProjectID,
		Bytes: request.QuotaBytes, Inodes: request.QuotaInodes,
	})
	if err != nil {
		if errors.Is(err, ErrIncomplete) {
			return zero, errors.Join(ErrImportIncomplete, err)
		}
		return zero, errors.Join(ErrImportUnsafe, err)
	}
	incomplete := func(err error) (ImportObservation, error) {
		return zero, errors.Join(ErrImportIncomplete, err)
	}
	namespace, err := r.openNamespace(request.NamespaceID)
	if err != nil {
		return incomplete(fmt.Errorf("open imported namespace: %w", err))
	}
	defer namespace.Close()
	if err := restoreExtract(ctx, archive, namespace, r.device, restoreRequest); err != nil {
		return incomplete(fmt.Errorf("extract archive: %w", err))
	}
	if err := restoreApplyDirectoryMetadata(ctx, namespace, r.device, entries); err != nil {
		return incomplete(fmt.Errorf("apply directory metadata: %w", err))
	}
	if err := restoreSyncNamespace(ctx, namespace, r.device); err != nil {
		return incomplete(fmt.Errorf("sync imported namespace: %w", err))
	}
	if err := r.file.Sync(); err != nil {
		return incomplete(fmt.Errorf("sync data root: %w", err))
	}
	if err := restoreArchiveDigest(ctx, archive, restoreRequest); err != nil {
		return incomplete(fmt.Errorf("recheck archive: %w", err))
	}
	observed, err := r.importReadBack(ctx, namespace, receivingExpected, entries, allocation, request)
	if err != nil {
		return incomplete(err)
	}
	if err := checkpointRoot.requireIdentity(ctx, checkpointExpected); err != nil {
		return incomplete(err)
	}
	return observed, nil
}

func (r ImportRequest) restoreRequest() RestoreRequest {
	return RestoreRequest{
		CheckpointID: r.CheckpointID, NamespaceID: r.NamespaceID,
		ReleaseSHA256: r.ReleaseSHA256, ManifestSHA256: r.ManifestSHA256,
		ProjectID: r.ProjectID, DataFormat: r.DataFormat, ArchiveBytes: r.ArchiveBytes,
		MaxArchiveBytes: r.MaxArchiveBytes, ExpectedDevice: r.Provenance.Device,
		ExpectedNamespaceInode: r.Provenance.NamespaceInode, WriterFence: r.WriterFence,
	}
}

// openImportArchive opens the retained archive under the checkpoint root and
// verifies its file profile, digest, canonical manifest and entry walk.
func (r *Root) openImportArchive(ctx context.Context, request ImportRequest, restoreRequest RestoreRequest) (*os.File, []restoreEntry, error) {
	archive, err := r.openRestoreArchiveFile(restoreRequest)
	if err != nil {
		return nil, nil, errors.Join(ErrImportUnsafe, fmt.Errorf("open archive: %w", err))
	}
	ok := false
	defer func() {
		if !ok {
			archive.Close()
		}
	}()
	if err := importVerifyArchive(ctx, archive, request); err != nil {
		return nil, nil, err
	}
	entries, err := restoreArchiveEntries(ctx, archive, restoreRequest)
	if err != nil {
		return nil, nil, errors.Join(ErrImportUnsafe, fmt.Errorf("read archive entries: %w", err))
	}
	ok = true
	return archive, entries, nil
}

// importVerifyArchive proves the archive is the one Portal recorded, then reads
// its manifest. The order is the invariant: the digest is checked first, so
// every manifest field, including the old host's checkpoint-root identity that
// the request does not carry, is authentic by the time it is compared.
func importVerifyArchive(ctx context.Context, archive *os.File, request ImportRequest) error {
	if err := restoreArchiveDigest(ctx, archive, request.restoreRequest()); err != nil {
		return errors.Join(ErrImportUnsafe, fmt.Errorf("verify archive digest: %w", err))
	}
	return importVerifyManifest(archive, request)
}

// importVerifyManifest must only run on an archive whose digest has matched.
func importVerifyManifest(archive *os.File, request ImportRequest) error {
	fail := func(reason string) error {
		return fmt.Errorf("%w: import manifest %s", ErrImportUnsafe, reason)
	}
	if _, err := archive.Seek(0, io.SeekStart); err != nil {
		return fail("seek")
	}
	reader := tar.NewReader(archive)
	first, err := reader.Next()
	if err != nil || first.Name != checkpointManifestEntry || first.Typeflag != tar.TypeReg || first.Size <= 0 || first.Size > 4096 {
		return fail("entry")
	}
	raw, err := io.ReadAll(io.LimitReader(reader, 4097))
	if err != nil || int64(len(raw)) != first.Size {
		return fail("read")
	}
	var manifest checkpointManifest
	if json.Unmarshal(raw, &manifest) != nil {
		return fail("decode")
	}
	canonical, err := json.Marshal(manifest)
	if err != nil || string(canonical) != string(raw) {
		return fail("canonical form")
	}
	p := request.Provenance
	if manifest.Format != checkpointFormat ||
		manifest.CheckpointID != request.CheckpointID || manifest.NamespaceID != request.NamespaceID ||
		manifest.ReleaseSHA256 != request.ReleaseSHA256 || manifest.WriterFence != request.WriterFence ||
		manifest.DataFormat != request.DataFormat || manifest.ProjectID != p.ProjectID ||
		manifest.NamespaceInode != p.NamespaceInode || manifest.Device != p.Device ||
		manifest.SourceFilesystemUUID != p.Source.FilesystemUUID || manifest.SourceRootInode != p.Source.Inode {
		return fail("binding")
	}
	// The old checkpoint root is taken from the verified manifest. The old and
	// new roots of one checkpoint are different physical roots by construction.
	oldCheckpoint := RootIdentity{FilesystemUUID: manifest.CheckpointFilesystemUUID, Inode: manifest.CheckpointRootInode}
	if !oldCheckpoint.valid() || oldCheckpoint == p.Source {
		return fail("old checkpoint root")
	}
	return nil
}

// importFits refuses an archive whose rounded footprint exceeds the requested
// quota, before any namespace or quota record exists.
func (r *Root) importFits(entries []restoreEntry, request ImportRequest) error {
	var fs unix.Statfs_t
	if err := native(r.file, func(fd uintptr) error { return unix.Fstatfs(int(fd), &fs) }); err != nil {
		return errors.Join(ErrImportUnsafe, err)
	}
	footprint := newImportFootprint(int64(fs.Bsize))
	for _, entry := range entries {
		if entry.directory {
			footprint.addDirectory()
		} else {
			footprint.addFile(entry.size)
		}
	}
	if err := footprint.fits(request.QuotaBytes, request.QuotaInodes); err != nil {
		return fmt.Errorf("%w: archive footprint exceeds the requested quota", err)
	}
	return nil
}

// importReadBack compares the whole namespace, entry by entry with SHA-256
// digests, against the verified archive and re-observes the allocation.
func (r *Root) importReadBack(ctx context.Context, namespace *os.File, receivingExpected RootIdentity, entries []restoreEntry, allocation Observation, request ImportRequest) (ImportObservation, error) {
	var zero ImportObservation
	restored, err := restoreReadNamespace(ctx, namespace, r.device, request.restoreRequest())
	if err != nil {
		return zero, fmt.Errorf("read back imported namespace: %w", err)
	}
	if !sameRestoreEntries(entries, restored) {
		return zero, fmt.Errorf("read back imported namespace: %s", restoreEntryDifference(entries, restored))
	}
	var stat unix.Stat_t
	if err := unix.Fstat(int(namespace.Fd()), &stat); err != nil ||
		uint64(stat.Dev) != allocation.Device || stat.Ino != allocation.Inode {
		return zero, ErrImportUnsafe
	}
	limit, err := readLimits(r.file, request.ProjectID)
	if err != nil || limit.bytes != request.QuotaBytes || limit.inodes != request.QuotaInodes ||
		limit.usedBytes < 0 || limit.usedBytes > limit.bytes || limit.usedInodes < 0 || limit.usedInodes > limit.inodes {
		return zero, errors.Join(ErrImportUnsafe, err)
	}
	if err := r.requireIdentity(ctx, receivingExpected); err != nil {
		return zero, err
	}
	return ImportObservation{valid: true, snapshot: ImportSnapshot{
		NamespaceID: request.NamespaceID, ReleaseSHA256: request.ReleaseSHA256,
		ManifestSHA256: request.ManifestSHA256, ProjectID: request.ProjectID,
		DataFormat: request.DataFormat, Entries: int64(len(entries)),
		UsedBytes: limit.usedBytes, UsedInodes: limit.usedInodes,
		ByteLimit: limit.bytes, InodeLimit: limit.inodes,
		ObservedMS:      time.Now().UTC().UnixMilli(),
		NamespaceDevice: uint64(stat.Dev), NamespaceInode: stat.Ino,
	}}, nil
}

// VerifyImportedCheckpoint is the read-only reconciliation of an import whose
// outcome is unknown. It never creates, repairs or removes anything: it proves
// that the namespace now on r equals the verified archive under the requested
// allocation, or it refuses. An import that cannot be proven is not retried.
func (r *Root) VerifyImportedCheckpoint(ctx context.Context, receivingExpected RootIdentity, checkpointRoot *Root, checkpointExpected RootIdentity, request ImportRequest) (ImportObservation, error) {
	var zero ImportObservation
	if r == nil || checkpointRoot == nil || r == checkpointRoot || !request.valid() {
		return zero, ErrImportUnsafe
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	checkpointRoot.mu.Lock()
	defer checkpointRoot.mu.Unlock()
	if r.closed || checkpointRoot.closed || receivingExpected == checkpointExpected {
		return zero, ErrImportUnsafe
	}
	if err := r.requireIdentity(ctx, receivingExpected); err != nil {
		return zero, errors.Join(ErrImportUnsafe, err)
	}
	if err := checkpointRoot.requireIdentity(ctx, checkpointExpected); err != nil {
		return zero, errors.Join(ErrImportUnsafe, err)
	}
	archive, entries, err := checkpointRoot.openImportArchive(ctx, request, request.restoreRequest())
	if err != nil {
		return zero, err
	}
	defer archive.Close()
	allocation, err := r.inspect(ctx, request.NamespaceID, request.ProjectID)
	if err != nil {
		return zero, errors.Join(ErrImportUnsafe, err)
	}
	if allocation.ByteLimit != request.QuotaBytes || allocation.InodeLimit != request.QuotaInodes {
		return zero, ErrImportUnsafe
	}
	namespace, err := r.openNamespace(request.NamespaceID)
	if err != nil {
		return zero, errors.Join(ErrImportUnsafe, err)
	}
	defer namespace.Close()
	observed, err := r.importReadBack(ctx, namespace, receivingExpected, entries, allocation, request)
	if err != nil {
		return zero, errors.Join(ErrImportUnsafe, err)
	}
	if err := checkpointRoot.requireIdentity(ctx, checkpointExpected); err != nil {
		return zero, errors.Join(ErrImportUnsafe, err)
	}
	return observed, nil
}
