package data

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/unix"
)

func deleteTree(t *testing.T, files int) (*os.File, uint64) {
	t.Helper()
	dir := t.TempDir()
	for d := 0; d < files/1000+1; d++ {
		sub := filepath.Join(dir, fmt.Sprintf("d%04d", d))
		if err := os.Mkdir(sub, 0o700); err != nil {
			t.Fatal(err)
		}
		for i := 0; i < 1000 && d*1000+i < files; i++ {
			f, err := os.Create(filepath.Join(sub, fmt.Sprintf("f%04d", i)))
			if err != nil {
				t.Fatal(err)
			}
			f.Close()
		}
	}
	folder, err := os.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { folder.Close() })
	var stat unix.Stat_t
	if err := unix.Fstat(int(folder.Fd()), &stat); err != nil {
		t.Fatal(err)
	}
	return folder, uint64(stat.Dev)
}

// A namespace may hold as many entries as its inode quota; the walk is bounded by that quota, not by 100,000.
func TestDeleteWalkIsBoundedByTheInodeQuota(t *testing.T) {
	folder, device := deleteTree(t, 100500)
	count := 0
	if err := deleteNamespaceChildren(context.Background(), folder, device, 0, &count, 200000); err != nil {
		t.Fatalf("a namespace of 100,500 files under a 200,000 inode quota: %v", err)
	}
	if left, _ := folder.ReadDir(-1); len(left) != 0 {
		t.Fatalf("%d entries left", len(left))
	}
	folder, device = deleteTree(t, 2500)
	count = 0
	if err := deleteNamespaceChildren(context.Background(), folder, device, 0, &count, 2000); !errors.Is(err, ErrDeleteUnsafe) {
		t.Fatalf("a walk beyond the quota: %v", err)
	}
}
