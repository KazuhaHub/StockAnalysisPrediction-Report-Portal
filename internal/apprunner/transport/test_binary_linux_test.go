//go:build linux

package transport

import (
	"io"
	"os"
	"path/filepath"
	"testing"
)

func transportAcceptanceChildBinary(
	t *testing.T, prefix string,
) string {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	source, err := os.Open(executable)
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	// /run may be noexec on a disposable worker. Keep endpoint paths on the
	// reviewed runtime mount, but execute the child from a private /tmp copy.
	dir, err := os.MkdirTemp("/tmp", prefix)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(dir); err != nil {
			t.Error(err)
		}
	})
	if err := os.Chmod(dir, 0755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "child.test")
	target, err := os.OpenFile(
		path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0555)
	if err != nil {
		t.Fatal(err)
	}
	if err := target.Chmod(0555); err != nil {
		target.Close()
		t.Fatal(err)
	}
	_, copyErr := io.Copy(target, source)
	closeErr := target.Close()
	if copyErr != nil || closeErr != nil {
		t.Fatal("copy fixture child binary")
	}
	return path
}
