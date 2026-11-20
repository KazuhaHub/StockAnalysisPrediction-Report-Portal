package data

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/unix"
)

// Extra hard-link paths add no inodes, so the delete walk may visit twice the inode quota in paths.
func TestDeleteWalkVisitsLinkPathsBeyondTheInodeQuota(t *testing.T) {
	if deletePathLimit(1000) != 2000 || deletePathLimit(0) != 2*maxCheckpointEntryCap || deletePathLimit(1<<40) != 2*maxCheckpointEntryCap {
		t.Fatalf("limits: %d %d %d", deletePathLimit(1000), deletePathLimit(0), deletePathLimit(1<<40))
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "f"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 12; i++ {
		if err := os.Link(filepath.Join(dir, "f"), filepath.Join(dir, fmt.Sprintf("l%02d", i))); err != nil {
			t.Fatal(err)
		}
	}
	folder, err := os.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer folder.Close()
	var stat unix.Stat_t
	if err := unix.Fstat(int(folder.Fd()), &stat); err != nil {
		t.Fatal(err)
	}
	count := 0
	if err := deleteNamespaceChildren(context.Background(), folder, uint64(stat.Dev), 0, &count, deletePathLimit(10)); err != nil {
		t.Fatalf("13 paths of one inode under an inode quota of 10: %v", err)
	}
	if left, _ := folder.ReadDir(-1); len(left) != 0 {
		t.Fatalf("%d entries left", len(left))
	}
}
