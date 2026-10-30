//go:build linux && (amd64 || arm64)

package data

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"golang.org/x/sys/unix"
)

func nativeNamespace(i int) string { return fmt.Sprintf("00000000-0000-0000-0000-%012d", i) }
func TestNativeDataPrimitive(t *testing.T) {
	rootPath := os.Getenv("APP_DATA_NATIVE_ROOT")
	if rootPath == "" {
		t.Skip("explicit disposable native fixture only")
	}
	if os.Getenv("GITHUB_ACTIONS") != "true" || os.Getenv("RUNNER_ENVIRONMENT") != "github-hosted" || os.Geteuid() != 0 || !filepath.IsAbs(rootPath) {
		t.Fatal("native fixture requires disposable Linux root")
	}
	raw, err := os.ReadFile(os.Getenv("APP_DATA_NATIVE_ABI"))
	if err != nil {
		t.Fatal(err)
	}
	var actual map[string]uint64
	if json.Unmarshal(raw, &actual) != nil {
		t.Fatal("invalid native ABI observation")
	}
	want := map[string]uint64{"geometry_size": geometrySize, "geometry_uuid": geometryUUIDOffset, "geometry_cmd": geometryGet, "status_size": 160, "quota_size": 112, "attr_size": 28, "status_flags": 2, "block_hard": 8, "inode_hard": 24, "used_blocks": 40, "used_inodes": 48, "project": 12, "get_attr": getAttr, "set_attr": setAttr, "status_cmd": uint64(quotaStatus), "get_cmd": uint64(quotaGet), "set_cmd": uint64(quotaSet)}
	for key, value := range want {
		if actual[key] != value {
			t.Fatalf("native UAPI mismatch %s: %d != %d", key, actual[key], value)
		}
	}
	input, err := os.Open(rootPath)
	if err != nil {
		t.Fatal(err)
	}
	defer input.Close()
	root, err := Open(input)
	if os.Getenv("APP_DATA_NATIVE_PHASE") == "disabled" {
		if !errors.Is(err, ErrUnsupported) {
			if root != nil {
				root.Close()
			}
			t.Fatal("disabled enforcement accepted", err)
		}
		return
	}
	if err != nil {
		t.Fatal("native root", err)
	}
	defer root.Close()

	expectedInode, err := strconv.ParseUint(os.Getenv("APP_DATA_NATIVE_INODE"), 10, 64)
	if err != nil {
		t.Fatal("missing independent root inode", err)
	}
	expected := RootIdentity{FilesystemUUID: os.Getenv("APP_DATA_NATIVE_UUID"), Inode: expectedInode}
	identity, err := root.Identity(context.Background())
	if err != nil {
		t.Fatal("native root identity", err)
	}
	if os.Getenv("APP_DATA_NATIVE_PHASE") == "replaced" {
		if identity == expected || identity.Inode != expected.Inode {
			t.Fatal("UUID replacement fixture did not change only filesystem identity")
		}
		if _, err := root.Prepare(context.Background(), expected, Request{nativeNamespace(12), 4212, 8 << 20, 128}); !errors.Is(err, ErrUnsafe) {
			t.Fatal("replaced filesystem prepared with old identity", err)
		}
		if _, err := root.Inspect(context.Background(), expected, nativeNamespace(1), 4201); !errors.Is(err, ErrUnsafe) {
			t.Fatal("replaced filesystem inspected as old identity", err)
		}
		if _, err := os.Lstat(filepath.Join(rootPath, nativeNamespace(12))); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("identity refusal created namespace")
		}
		return
	}
	if identity != expected {
		t.Fatalf("independent root identity mismatch: %+v != %+v", identity, expected)
	}
	for _, wrong := range []RootIdentity{{}, {FilesystemUUID: expected.FilesystemUUID, Inode: expected.Inode + 1}, {FilesystemUUID: "00000000-0000-0000-0000-000000000001", Inode: expected.Inode}} {
		if _, err := root.Prepare(context.Background(), wrong, Request{nativeNamespace(12), 4212, 8 << 20, 128}); !errors.Is(err, ErrUnsafe) {
			t.Fatal("wrong root prepared", err)
		}
		if _, err := os.Lstat(filepath.Join(rootPath, nativeNamespace(12))); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("root mismatch created namespace")
		}
		limit, err := readLimits(root.file, 4212)
		if err != nil || limit != (limits{}) {
			t.Fatal("root mismatch changed quota", limit, err)
		}
		if _, err := root.Inspect(context.Background(), wrong, nativeNamespace(1), 4201); !errors.Is(err, ErrUnsafe) {
			t.Fatal("wrong root inspection", err)
		}
	}
	canceledIdentity, cancelIdentity := context.WithCancel(context.Background())
	cancelIdentity()
	if _, err := root.Identity(canceledIdentity); !errors.Is(err, context.Canceled) {
		t.Fatal("canceled identity observation", err)
	}
	if other, err := Open(input); err == nil {
		other.Close()
		t.Fatal("another provider acquired root")
	}
	if os.Getenv("APP_DATA_NATIVE_PHASE") == "inspect" {
		for i := 1; i <= 4; i++ {
			observed, err := root.Inspect(context.Background(), expected, nativeNamespace(i), uint32(4200+i))
			if err != nil || observed.ByteLimit != 8<<20 || observed.InodeLimit != 128 || !observed.Inherited {
				t.Fatalf("reopen observation: %+v %v", observed, err)
			}
		}
		return
	}
	for i := 1; i <= 4; i++ {
		request := Request{nativeNamespace(i), uint32(4200 + i), 8 << 20, 128}
		observed, err := root.Prepare(context.Background(), expected, request)
		if err != nil || observed.ProjectID != request.ProjectID || observed.NamespaceID != request.NamespaceID || observed.ByteLimit != request.Bytes || observed.InodeLimit != request.Inodes || observed.Inode == 0 || !observed.Inherited {
			t.Fatalf("native prepare: %+v %v", observed, err)
		}
		if _, err := root.Prepare(context.Background(), expected, request); err == nil {
			t.Fatal("existing namespace replayed mutation")
		}
		request.NamespaceID = nativeNamespace(100 + i)
		if _, err := root.Prepare(context.Background(), expected, request); !errors.Is(err, ErrConflict) {
			t.Fatal("existing project reused", err)
		}
		if _, err := os.Lstat(filepath.Join(rootPath, request.NamespaceID)); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("project conflict created namespace")
		}
	}

	// Replacing the configured path does not retarget an already pinned root.
	renamed := rootPath + "-pinned"
	if err := os.Rename(rootPath, renamed); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if renamed != rootPath {
			os.Remove(rootPath)
			os.Rename(renamed, rootPath)
		}
	})
	if err := os.Mkdir(rootPath, 0700); err != nil {
		t.Fatal(err)
	}
	replacementInput, err := os.Open(rootPath)
	if err != nil {
		t.Fatal(err)
	}
	defer replacementInput.Close()
	replacement, err := Open(replacementInput)
	if err != nil {
		t.Fatal(err)
	}
	defer replacement.Close()
	replacementIdentity, err := replacement.Identity(context.Background())
	if err != nil || replacementIdentity == expected || replacementIdentity.FilesystemUUID != expected.FilesystemUUID {
		t.Fatal("directory replacement fixture", replacementIdentity, err)
	}
	if _, err := replacement.Prepare(context.Background(), expected, Request{nativeNamespace(13), 4213, 8 << 20, 128}); !errors.Is(err, ErrUnsafe) {
		t.Fatal("replacement directory used old identity", err)
	}
	if _, err := root.Inspect(context.Background(), expected, nativeNamespace(1), 4201); err != nil {
		t.Fatal("pinned descriptor followed replaced pathname", err)
	}
	if err := os.Remove(rootPath); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(renamed, rootPath); err != nil {
		t.Fatal(err)
	}
	// The cleanup only restores on a premature test failure.
	renamed = rootPath
	if err := os.Chmod(rootPath, 0755); err != nil {
		t.Fatal(err)
	}
	_, unsafeErr := root.Prepare(context.Background(), expected, Request{nativeNamespace(13), 4213, 8 << 20, 128})
	if err := os.Chmod(rootPath, 0700); err != nil {
		t.Fatal(err)
	}
	if !errors.Is(unsafeErr, ErrUnsafe) {
		t.Fatal("opened root permissions not rechecked", unsafeErr)
	}
	if _, err := os.Lstat(filepath.Join(rootPath, nativeNamespace(13))); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("invalid root mutated namespace")
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := root.Prepare(canceled, expected, Request{nativeNamespace(10), 4210, 8 << 20, 128}); !errors.Is(err, context.Canceled) {
		t.Fatal("canceled mutation", err)
	}
	if _, err := os.Lstat(filepath.Join(rootPath, nativeNamespace(10))); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("canceled request created folder")
	}
	collision := filepath.Join(rootPath, nativeNamespace(11))
	if err := os.Symlink(filepath.Join(rootPath, nativeNamespace(1)), collision); err != nil {
		t.Fatal(err)
	}
	if _, err := root.Prepare(context.Background(), expected, Request{nativeNamespace(11), 4211, 8 << 20, 128}); err == nil {
		t.Fatal("symlink adopted")
	}
	if _, err := root.Inspect(context.Background(), expected, nativeNamespace(11), 4201); err == nil {
		t.Fatal("inspection followed namespace symlink")
	}
	limit, err := readLimits(root.file, 4211)
	if err != nil || limit != (limits{}) {
		t.Fatal("namespace collision changed quota", limit, err)
	}
	if err := os.Remove(collision); err != nil {
		t.Fatal(err)
	}
	if err := root.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := root.Inspect(context.Background(), expected, nativeNamespace(1), 4201); err == nil {
		t.Fatal("closed root usable")
	}
	if _, err := root.Identity(context.Background()); err == nil {
		t.Fatal("closed root identity usable")
	}
	// An ordinary directory on the worker's host filesystem is never a quota root.
	invalid := t.TempDir()
	if err := os.Chmod(invalid, 0700); err != nil {
		t.Fatal(err)
	}
	bad, err := os.Open(invalid)
	if err != nil {
		t.Fatal(err)
	}
	defer bad.Close()
	if other, err := Open(bad); err == nil {
		other.Close()
		t.Fatal("non-XFS root accepted")
	}
	var stat unix.Statfs_t
	if err := unix.Statfs(rootPath, &stat); err != nil || stat.Type != unix.XFS_SUPER_MAGIC {
		t.Fatal("fixture lost native filesystem")
	}
}
