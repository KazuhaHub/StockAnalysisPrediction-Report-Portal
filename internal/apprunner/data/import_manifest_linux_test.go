//go:build linux && (amd64 || arm64)

package data

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const (
	manifestTestCheckpointID = "00000000-0000-0000-0000-000000000001"
	manifestTestNamespaceID  = "00000000-0000-0000-0000-000000000002"
	manifestTestSourceFS     = "01234567-89ab-cdef-0123-456789abcdef"
	manifestTestOldRootFS    = "7a7a7a7a-89ab-cdef-0123-456789abcdef"
)

func manifestTestManifest() checkpointManifest {
	return checkpointManifest{
		Format: checkpointFormat, CheckpointID: manifestTestCheckpointID, NamespaceID: manifestTestNamespaceID,
		ReleaseSHA256: strings.Repeat("a", 64), ProjectID: 4201, WriterFence: 1, DataFormat: 1,
		NamespaceInode: 10, Device: 11,
		SourceFilesystemUUID: manifestTestSourceFS, SourceRootInode: 64,
		CheckpointFilesystemUUID: manifestTestOldRootFS, CheckpointRootInode: 98765,
	}
}

// manifestTestArchive writes a checkpoint-shaped tar (manifest first, then one file) and returns it
// opened, with its size and SHA-256.
func manifestTestArchive(t *testing.T, manifest checkpointManifest) (*os.File, int64, string) {
	t.Helper()
	raw, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	var buffer bytes.Buffer
	writer := tar.NewWriter(&buffer)
	for _, entry := range []struct {
		name string
		body []byte
	}{{checkpointManifestEntry, raw}, {"hello.txt", []byte("imported payload")}} {
		if err := writer.WriteHeader(&tar.Header{Name: entry.name, Mode: 0600, Size: int64(len(entry.body)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		if _, err := writer.Write(entry.body); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "archive.tar")
	if err := os.WriteFile(path, buffer.Bytes(), 0400); err != nil {
		t.Fatal(err)
	}
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { file.Close() })
	sum := sha256.Sum256(buffer.Bytes())
	return file, int64(buffer.Len()), hex.EncodeToString(sum[:])
}

func manifestTestRequest(size int64, digest string) ImportRequest {
	return ImportRequest{
		CheckpointID: manifestTestCheckpointID, NamespaceID: manifestTestNamespaceID,
		ReleaseSHA256: strings.Repeat("a", 64), ManifestSHA256: digest,
		ProjectID: 5201, DataFormat: 1, ArchiveBytes: size, MaxArchiveBytes: 1 << 20, WriterFence: 1,
		QuotaBytes: 8 << 20, QuotaInodes: 128,
		Provenance: ImportProvenance{
			ProjectID: 4201, NamespaceInode: 10, Device: 11,
			Source: RootIdentity{FilesystemUUID: manifestTestSourceFS, Inode: 64},
		},
	}
}

// The request carries no old checkpoint-root identity. The manifest's own value is authentic only
// because the archive digest Portal recorded matched, so an archive whose manifest names a root that
// nothing the caller supplied mentions is still accepted.
func TestImportVerifyArchiveAcceptsManifestOldRootWhenDigestMatches(t *testing.T) {
	archive, size, digest := manifestTestArchive(t, manifestTestManifest())
	if err := importVerifyArchive(context.Background(), archive, manifestTestRequest(size, digest)); err != nil {
		t.Fatalf("a digest-verified archive was refused: %v", err)
	}
}

// A manifest that differs from the one Portal recorded the digest of, here only in the old
// checkpoint root's inode and with the same length, is refused before any manifest field is trusted.
func TestImportVerifyArchiveRefusesTamperedManifest(t *testing.T) {
	_, size, digest := manifestTestArchive(t, manifestTestManifest())
	tampered := manifestTestManifest()
	tampered.CheckpointRootInode = 98766
	archive, tamperedSize, _ := manifestTestArchive(t, tampered)
	if tamperedSize != size {
		t.Fatalf("fixture sizes differ: %d %d", size, tamperedSize)
	}
	if err := importVerifyArchive(context.Background(), archive, manifestTestRequest(size, digest)); !errors.Is(err, ErrImportUnsafe) {
		t.Fatalf("a manifest that does not hash to the recorded digest was accepted: %v", err)
	}
}

// With a matching digest the manifest is still checked against the request and against itself.
func TestImportVerifyArchiveRefusesInconsistentManifest(t *testing.T) {
	for name, mutate := range map[string]func(*checkpointManifest){
		"old root equals source root": func(m *checkpointManifest) {
			m.CheckpointFilesystemUUID, m.CheckpointRootInode = m.SourceFilesystemUUID, m.SourceRootInode
		},
		"old root missing":       func(m *checkpointManifest) { m.CheckpointRootInode = 0 },
		"old root fs invalid":    func(m *checkpointManifest) { m.CheckpointFilesystemUUID = "not-a-uuid" },
		"old root fs all zeroes": func(m *checkpointManifest) { m.CheckpointFilesystemUUID = "00000000-0000-0000-0000-000000000000" },
		"foreign source root":    func(m *checkpointManifest) { m.SourceRootInode = 65 },
		"foreign device":         func(m *checkpointManifest) { m.Device = 12 },
		"foreign namespace":      func(m *checkpointManifest) { m.NamespaceID = "00000000-0000-0000-0000-000000000009" },
	} {
		t.Run(name, func(t *testing.T) {
			manifest := manifestTestManifest()
			mutate(&manifest)
			archive, size, digest := manifestTestArchive(t, manifest)
			if err := importVerifyArchive(context.Background(), archive, manifestTestRequest(size, digest)); !errors.Is(err, ErrImportUnsafe) {
				t.Fatalf("inconsistent manifest accepted: %v", err)
			}
		})
	}
}
