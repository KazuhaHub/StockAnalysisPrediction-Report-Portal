//go:build linux

package driver

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/unix"
)

func TestCandidateSocketDirectoryProvisioning(t *testing.T) {
	root := t.TempDir()
	parent, err := unix.Open(root, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer unix.Close(parent)
	uid, gid := os.Geteuid(), os.Getegid()
	fd, err := ensureCandidateSocketDirectory(context.Background(), parent, "owned", uid, gid, 0700)
	if err != nil {
		t.Fatal(err)
	}
	var first unix.Stat_t
	if err := unix.Fstat(fd, &first); err != nil {
		t.Fatal(err)
	}
	unix.Close(fd)
	fd, err = ensureCandidateSocketDirectory(context.Background(), parent, "owned", uid, gid, 0700)
	if err != nil {
		t.Fatal(err)
	}
	var second unix.Stat_t
	if err := unix.Fstat(fd, &second); err != nil {
		t.Fatal(err)
	}
	unix.Close(fd)
	if !mountStatEqual(first, second) {
		t.Fatal("existing directory identity replaced")
	}
	if err := os.Chmod(filepath.Join(root, "owned"), 0755); err != nil {
		t.Fatal(err)
	}
	if _, err := ensureCandidateSocketDirectory(context.Background(), parent, "owned", uid, gid, 0700); err == nil {
		t.Fatal("unexpected directory permissions repaired")
	}
	if err := os.Symlink("owned", filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"link", "..", "../outside", "/absolute", ""} {
		if _, err := ensureCandidateSocketDirectory(context.Background(), parent, name, uid, gid, 0700); err == nil {
			t.Fatal("unsafe directory accepted", name)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := ensureCandidateSocketDirectory(ctx, parent, "cancelled", uid, gid, 0700); err == nil {
		t.Fatal("cancelled creation accepted")
	}
	if _, err := os.Stat(filepath.Join(root, "cancelled")); !os.IsNotExist(err) {
		t.Fatal("cancelled creation mutated filesystem")
	}
}
