//go:build linux

package service

import "testing"

func TestVolumeBytesReadsARealFilesystemAndFailsClosed(t *testing.T) {
	if volumeBytes(t.TempDir()) <= 0 {
		t.Fatal("the size of a real filesystem was not read")
	}
	if volumeBytes("/nonexistent/path/for/statfs") != 0 {
		t.Fatal("an unreadable volume reported a size")
	}
}
