//go:build linux && (amd64 || arm64)

package data

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestNativeImportCheckpoint models a lost host: a namespace is checkpointed,
// then deleted, and the retained archive is imported by content. It shares the
// disposable fixture and refusal conventions of TestNativeDataPrimitive.
func TestNativeImportCheckpoint(t *testing.T) {
	rootPath := os.Getenv("APP_DATA_NATIVE_ROOT")
	checkpointPath := os.Getenv("APP_DATA_CHECKPOINT_ROOT")
	if rootPath == "" || checkpointPath == "" {
		t.Skip("explicit disposable native fixture only")
	}
	if os.Getenv("GITHUB_ACTIONS") != "true" || os.Getenv("RUNNER_ENVIRONMENT") != "github-hosted" || os.Geteuid() != 0 || !filepath.IsAbs(rootPath) || !filepath.IsAbs(checkpointPath) {
		t.Fatal("native fixture requires disposable Linux root")
	}
	if os.Getenv("APP_DATA_NATIVE_PHASE") != "" {
		t.Skip("import runs in the primary phase only")
	}
	ctx := context.Background()
	rootInput, err := os.Open(rootPath)
	if err != nil {
		t.Fatal(err)
	}
	defer rootInput.Close()
	root, err := Open(rootInput)
	if err != nil {
		t.Fatal("native root", err)
	}
	defer root.Close()
	checkpointInput, err := os.Open(checkpointPath)
	if err != nil {
		t.Fatal(err)
	}
	defer checkpointInput.Close()
	checkpointRoot, err := Open(checkpointInput)
	if err != nil {
		t.Fatal("checkpoint root", err)
	}
	defer checkpointRoot.Close()
	rootIdentity, err := root.Identity(ctx)
	if err != nil {
		t.Fatal(err)
	}
	checkpointIdentity, err := checkpointRoot.Identity(ctx)
	if err != nil || checkpointIdentity == rootIdentity {
		t.Fatal("checkpoint root identity", checkpointIdentity, err)
	}

	const project = 4260
	namespaceName := nativeNamespace(60)
	namespacePath := filepath.Join(rootPath, namespaceName)
	prepared, err := root.Prepare(ctx, rootIdentity, Request{namespaceName, project, 8 << 20, 128})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(namespacePath, "nested"), 0750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(namespacePath, "hello.txt"), []byte("imported payload"), 0640); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(namespacePath, "nested", "value.json"), []byte(`{"v":2}`), 0600); err != nil {
		t.Fatal(err)
	}
	checkpointRequest := CheckpointRequest{
		CheckpointID: nativeNamespace(600), NamespaceID: namespaceName, ReleaseSHA256: strings.Repeat("a", 64),
		ProjectID: project, DataFormat: 1, MaxArchiveBytes: 1 << 20, ExpectedDevice: prepared.Device,
		ExpectedNamespaceInode: prepared.Inode, WriterFence: 1,
	}
	checkpoint, err := checkpointRoot.Checkpoint(ctx, checkpointIdentity, root, rootIdentity, checkpointRequest)
	snapshot, snapshotErr := checkpoint.Snapshot()
	if err != nil || snapshotErr != nil {
		t.Fatal("checkpoint fixture", errors.Join(err, snapshotErr))
	}
	// The old host is gone: remove the namespace and its quota so the import is
	// the only thing that can bring the data back.
	if _, err := root.Delete(ctx, rootIdentity, DeleteRequest{
		NamespaceID: namespaceName, ReleaseSHA256: checkpointRequest.ReleaseSHA256, ProjectID: project,
		DataFormat: 1, QuotaBytes: prepared.ByteLimit, QuotaInodes: prepared.InodeLimit,
		ExpectedDevice: prepared.Device, ExpectedNamespaceInode: prepared.Inode, WriterFence: 1,
	}); err != nil {
		t.Fatal("delete fixture", err)
	}

	good := ImportRequest{
		CheckpointID: checkpointRequest.CheckpointID, NamespaceID: namespaceName,
		ReleaseSHA256: checkpointRequest.ReleaseSHA256, ManifestSHA256: snapshot.ManifestSHA256,
		ProjectID: project, DataFormat: 1, ArchiveBytes: snapshot.ArchiveBytes,
		MaxArchiveBytes: 1 << 20, WriterFence: 1, QuotaBytes: 8 << 20, QuotaInodes: 128,
		Provenance: ImportProvenance{
			ProjectID: project, NamespaceInode: prepared.Inode, Device: prepared.Device,
			Source: rootIdentity, Checkpoint: checkpointIdentity,
		},
	}
	refuse := func(name string, request ImportRequest) {
		t.Helper()
		if _, err := root.ImportCheckpoint(ctx, rootIdentity, checkpointRoot, checkpointIdentity, request); !errors.Is(err, ErrImportUnsafe) {
			t.Fatalf("%s: import not refused: %v", name, err)
		}
		for _, name := range []string{request.NamespaceID, namespaceName} {
			if _, err := os.Lstat(filepath.Join(rootPath, name)); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("%s: refusal created namespace %s", name, name)
			}
		}
		if limit, err := readLimits(root.file, request.ProjectID); err != nil || limit != (limits{}) {
			t.Fatalf("%s: refusal reserved quota %+v %v", name, limit, err)
		}
	}

	tampered := good
	tampered.CheckpointID = nativeNamespace(601)
	raw, err := os.ReadFile(filepath.Join(checkpointPath, checkpointDirectory, checkpointRequest.CheckpointID+".tar"))
	if err != nil {
		t.Fatal(err)
	}
	raw[len(raw)/2] ^= 1
	if err := os.WriteFile(filepath.Join(checkpointPath, checkpointDirectory, tampered.CheckpointID+".tar"), raw, 0400); err != nil {
		t.Fatal(err)
	}
	refuse("tampered archive", tampered)

	wrongCheckpoint := good
	wrongCheckpoint.CheckpointID = nativeNamespace(602)
	refuse("missing checkpoint", wrongCheckpoint)

	wrongNamespace := good
	wrongNamespace.NamespaceID = nativeNamespace(61)
	refuse("foreign namespace", wrongNamespace)

	wrongProvenance := good
	wrongProvenance.Provenance.Device++
	refuse("foreign device", wrongProvenance)
	wrongProvenance = good
	wrongProvenance.Provenance.Source.Inode++
	refuse("foreign source root", wrongProvenance)

	small := good
	small.QuotaBytes = 4096
	refuse("quota smaller than the archive", small)
	few := good
	few.QuotaInodes = 2
	refuse("inode quota smaller than the archive", few)

	wrongRoot := RootIdentity{FilesystemUUID: rootIdentity.FilesystemUUID, Inode: rootIdentity.Inode + 1}
	if _, err := root.ImportCheckpoint(ctx, wrongRoot, checkpointRoot, checkpointIdentity, good); !errors.Is(err, ErrImportUnsafe) {
		t.Fatalf("wrong receiving root accepted: %v", err)
	}
	if _, err := os.Lstat(namespacePath); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("root mismatch created namespace")
	}

	observed, err := root.ImportCheckpoint(ctx, rootIdentity, checkpointRoot, checkpointIdentity, good)
	imported, importedErr := observed.Snapshot()
	if err != nil || importedErr != nil || imported.NamespaceID != namespaceName || imported.ManifestSHA256 != snapshot.ManifestSHA256 ||
		imported.Entries != snapshot.Entries || imported.ByteLimit != good.QuotaBytes || imported.InodeLimit != good.QuotaInodes {
		t.Fatalf("native import: %+v %v", imported, errors.Join(err, importedErr))
	}
	if content, err := os.ReadFile(filepath.Join(namespacePath, "hello.txt")); err != nil || string(content) != "imported payload" {
		t.Fatal("imported file content", string(content), err)
	}
	if content, err := os.ReadFile(filepath.Join(namespacePath, "nested", "value.json")); err != nil || string(content) != `{"v":2}` {
		t.Fatal("imported nested content", string(content), err)
	}
	if err := os.WriteFile(filepath.Join(namespacePath, "hello.txt"), []byte("changed after import"), 0640); err != nil {
		t.Fatal(err)
	}
	if _, err := root.ImportCheckpoint(ctx, rootIdentity, checkpointRoot, checkpointIdentity, good); !errors.Is(err, ErrConflict) {
		t.Fatalf("second import over an existing namespace: %v", err)
	}
	if content, err := os.ReadFile(filepath.Join(namespacePath, "hello.txt")); err != nil || string(content) != "changed after import" {
		t.Fatal("refused import changed the existing namespace", string(content), err)
	}
	t.Log("NATIVE_DATA_IMPORT_VERIFIED")
}
