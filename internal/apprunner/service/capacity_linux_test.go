//go:build linux

package service

import (
	"testing"

	"golang.org/x/sys/unix"
)

func TestVolumeBytesReadsARealFilesystemAndFailsClosed(t *testing.T) {
	if volumeBytes(t.TempDir()) <= 0 {
		t.Fatal("the size of a real filesystem was not read")
	}
	if volumeBytes("/nonexistent/path/for/statfs") != 0 {
		t.Fatal("an unreadable volume reported a size")
	}
}

func TestVolumeBytesUsesTheFragmentSize(t *testing.T) {
	dir := t.TempDir()
	var st unix.Statfs_t
	if err := unix.Statfs(dir, &st); err != nil {
		t.Fatal(err)
	}
	if got, want := volumeBytes(dir), int64(st.Blocks)*int64(st.Frsize); got != want {
		t.Fatalf("volume = %d, want blocks*frsize = %d", got, want)
	}
}

func TestSameFilesystem(t *testing.T) {
	dir := t.TempDir()
	if !sameFilesystem(dir, dir) {
		t.Fatal("a path is on its own filesystem")
	}
	if sameFilesystem(dir, "/nonexistent/for/stat") {
		t.Fatal("an unreadable path was treated as the same filesystem")
	}
}
