//go:build linux && (amd64 || arm64)

package data

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// TestNativeRaiseQuotaAndImportData runs on the disposable XFS fixture only. It raises a namespace's quota online,
// refuses lowering, then imports an inbox archive into the same namespace inode and removes the archive. It has
// not been run outside that fixture.
func TestNativeRaiseQuotaAndImportData(t *testing.T) {
	rootPath, checkpointPath := os.Getenv("APP_DATA_NATIVE_ROOT"), os.Getenv("APP_DATA_CHECKPOINT_ROOT")
	if rootPath == "" || checkpointPath == "" {
		t.Skip("explicit disposable native fixture only")
	}
	if os.Getenv("GITHUB_ACTIONS") != "true" || os.Getenv("RUNNER_ENVIRONMENT") != "github-hosted" || os.Geteuid() != 0 {
		t.Fatal("native fixture requires disposable Linux root")
	}
	if os.Getenv("APP_DATA_NATIVE_PHASE") != "" {
		t.Skip("runs in the primary phase only")
	}
	ctx := context.Background()
	open := func(path string) *Root {
		input, err := os.Open(path)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { input.Close() })
		root, err := Open(input)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { root.Close() })
		return root
	}
	root, checkpointRoot := open(rootPath), open(checkpointPath)
	rootID, err := root.Identity(ctx)
	checkpointID, err2 := checkpointRoot.Identity(ctx)
	if err != nil || err2 != nil {
		t.Fatal(errors.Join(err, err2))
	}
	const project = 4280
	name := nativeNamespace(80)
	prepared, err := root.Prepare(ctx, rootID, Request{name, project, 8 << 20, 128})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(rootPath, name, "old.txt"), []byte("old"), 0640); err != nil {
		t.Fatal(err)
	}

	raise := QuotaRaiseRequest{NamespaceID: name, ProjectID: project, OldBytes: 8 << 20, OldInodes: 128,
		NewBytes: 16 << 20, NewInodes: 256, ExpectedDevice: prepared.Device, ExpectedNamespaceInode: prepared.Inode}
	lower := raise
	lower.NewBytes = 4 << 20
	if _, err := root.RaiseQuota(ctx, rootID, lower); !errors.Is(err, ErrQuotaLower) {
		t.Fatal("lowering accepted", err)
	}
	wrongOld := raise
	wrongOld.OldBytes = 4 << 20
	if _, err := root.RaiseQuota(ctx, rootID, wrongOld); !errors.Is(err, ErrQuotaRaiseUnsafe) {
		t.Fatal("a raise over a hand-edited limit was accepted", err)
	}
	observed, err := root.RaiseQuota(ctx, rootID, raise)
	snapshot, snapErr := observed.Snapshot()
	if err != nil || snapErr != nil || snapshot.ByteLimit != 16<<20 || snapshot.InodeLimit != 256 || snapshot.AlreadyApplied {
		t.Fatal("raise", err, snapErr, snapshot)
	}
	again, err := root.RaiseQuota(ctx, rootID, raise)
	if againSnap, snapErr := again.Snapshot(); err != nil || snapErr != nil || !againSnap.AlreadyApplied {
		t.Fatal("a repeated raise is not convergent", err, snapErr)
	}

	inbox := filepath.Join(checkpointPath, ImportInboxDirectory)
	if err := os.MkdirAll(inbox, 0700); err != nil {
		t.Fatal(err)
	}
	archive := importArchiveBytes(t, map[string]string{SchemaMarkerName: "1\n", "seed/a.bin": "alpha", "seed/b.bin": "beta"}, "")
	if err := os.WriteFile(filepath.Join(inbox, "seed.tar"), archive, 0400); err != nil {
		t.Fatal(err)
	}
	inspected, err := checkpointRoot.InspectImportInbox(ctx, checkpointID, "", 256)
	if err == nil {
		t.Fatalf("an empty digest was accepted: %+v", inspected)
	}
	probe := importArchiveBytes(t, map[string]string{SchemaMarkerName: "1\n", "seed/a.bin": "alpha", "seed/b.bin": "beta"}, "")
	_ = probe
	// The digest is the manifest's: read it back through the inspector on the file itself.
	file, err := os.Open(filepath.Join(inbox, "seed.tar"))
	if err != nil {
		t.Fatal(err)
	}
	info, err := InspectImportArchive(ctx, file, 256)
	file.Close()
	if err != nil {
		t.Fatal(err)
	}
	digest := info.Manifest.BodySHA256
	if found, err := checkpointRoot.InspectImportInbox(ctx, checkpointID, digest, 256); err != nil || !found.Found {
		t.Fatal("inbox inspection", found, err)
	}
	request := ImportDataRequest{
		NamespaceID: name, ProjectID: project, ArchiveSHA256: digest, ArchiveBytes: info.Bytes,
		Entries: info.Manifest.Entries, Bytes: info.Manifest.Bytes, QuotaBytes: 16 << 20, QuotaInodes: 256,
		DataFormat: 2, MaxArchiveBytes: 1 << 20, ExpectedDevice: prepared.Device, ExpectedNamespaceInode: prepared.Inode, WriterFence: 1,
	}
	if _, err := checkpointRoot.ImportData(ctx, checkpointID, root, rootID, request); !errors.Is(err, ErrImportFormatMismatch) {
		t.Fatal("a data format mismatch was accepted", err)
	}
	request.DataFormat = 1
	result, err := checkpointRoot.ImportData(ctx, checkpointID, root, rootID, request)
	done, snapErr := result.Snapshot()
	if err != nil || snapErr != nil || done.Entries != 4 || done.UsedInodes < 4 || done.UsedBytes <= 0 {
		t.Fatal("import", err, snapErr, done)
	}
	if _, err := os.Stat(filepath.Join(rootPath, name, "old.txt")); !os.IsNotExist(err) {
		t.Fatal("the previous contents were not replaced", err)
	}
	if raw, err := os.ReadFile(filepath.Join(rootPath, name, "seed", "a.bin")); err != nil || string(raw) != "alpha" {
		t.Fatal("imported content", err)
	}
	if err := checkpointRoot.RemoveImportArchive(ctx, checkpointID, digest); err != nil {
		t.Fatal(err)
	}
	if _, err := checkpointRoot.InspectImportInbox(ctx, checkpointID, digest, 256); !errors.Is(err, ErrImportArchiveMissing) {
		t.Fatal("the archive was not removed", err)
	}
}
