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
)

// InspectImportArchive reads an open import archive and reports its manifest and data schema marker. It checks the
// first entry, the body digest against the manifest and every entry with the restore reader's rules, so a file it
// accepts is one a restore-style extraction accepts. maxEntries bounds the walk (zero keeps the legacy cap).
func InspectImportArchive(ctx context.Context, file *os.File, maxEntries int64) (ImportInboxArchive, error) {
	var zero ImportInboxArchive
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() <= ImportManifestBlockBytes {
		return zero, ErrImportArchiveInvalid
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return zero, err
	}
	reader := tar.NewReader(file)
	first, err := reader.Next()
	if err != nil || first.Name != ArchiveManifestEntry || first.Typeflag != tar.TypeReg ||
		first.Size <= 0 || first.Size > MaxImportManifestBytes {
		return zero, ErrImportArchiveInvalid
	}
	raw, err := io.ReadAll(io.LimitReader(reader, MaxImportManifestBytes+1))
	if err != nil || int64(len(raw)) != first.Size {
		return zero, ErrImportArchiveInvalid
	}
	manifest, err := ParseImportManifest(raw)
	if err != nil {
		return zero, err
	}
	if _, err := file.Seek(ImportManifestBlockBytes, io.SeekStart); err != nil {
		return zero, err
	}
	sum := sha256.New()
	if _, err := io.Copy(sum, file); err != nil || hex.EncodeToString(sum.Sum(nil)) != manifest.BodySHA256 {
		return zero, errors.Join(ErrImportArchiveInvalid, errors.New("body digest differs from the manifest"))
	}
	entries, err := restoreArchiveEntries(ctx, file, RestoreRequest{ArchiveBytes: info.Size(), MaxEntries: maxEntries})
	if err != nil {
		return zero, errors.Join(ErrImportArchiveInvalid, err)
	}
	var content int64
	for _, entry := range entries {
		content += entry.size
	}
	if int64(len(entries)) != manifest.Entries || content != manifest.Bytes {
		return zero, errors.Join(ErrImportArchiveInvalid, errors.New("entry counts differ from the manifest"))
	}
	marker, err := importSchemaMarker(file)
	if err != nil {
		return zero, errors.Join(ErrImportArchiveInvalid, err)
	}
	return ImportInboxArchive{Found: true, Bytes: info.Size(), Manifest: manifest, SchemaMarker: marker}, nil
}

// importSchemaMarker returns the value of the archive's root .portal-data-schema entry, zero when it has none.
func importSchemaMarker(file *os.File) (int, error) {
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return 0, err
	}
	reader := tar.NewReader(file)
	for {
		header, err := reader.Next()
		if err == io.EOF {
			return 0, nil
		}
		if err != nil {
			return 0, err
		}
		if header.Name != SchemaMarkerName {
			continue
		}
		if header.Typeflag != tar.TypeReg || header.Size > MaxSchemaMarkerBytes {
			return 0, ErrMigrationUnsafe
		}
		raw, err := io.ReadAll(reader)
		if err != nil {
			return 0, err
		}
		return parseSchemaMarker(raw)
	}
}
