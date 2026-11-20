//go:build linux

package rootfs

import "testing"

func TestInspectReadsAnExistingDirectoryAndRefusesAMissingOne(t *testing.T) {
	info, err := Inspect(t.TempDir())
	if err != nil || info.Kind == "" {
		t.Fatalf("inspect of a real directory: %+v %v", info, err)
	}
	if _, err := Inspect(t.TempDir() + "/missing"); err == nil {
		t.Fatal("a missing path was inspected")
	}
}
