//go:build linux && (amd64 || arm64)

package data

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"testing"
)

// siblingOrderTree holds directories next to files whose names sort between "name" and "name/": "a" beside "a.txt"
// and "a-b", "snapshot" beside "snapshot.py", and the same one level down. "a.txt" and "a/x" share an inode, so the
// content-carrying first path and its link are on either side of a directory boundary.
func siblingOrderTree(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	write := func(rel, body string) {
		full := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(full), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(body), 0o640); err != nil {
			t.Fatal(err)
		}
	}
	write("a/x", "shared")
	write("a/x-z/q", "q")
	write("a/x.py", "py")
	write("a-b", "ab")
	write("a0", "zero")
	write("snapshot/c", "c")
	write("snapshot.py", "py")
	write("deep/snapshot/c", "c")
	write("deep/snapshot.py", "py")
	if err := os.Link(filepath.Join(dir, "a/x"), filepath.Join(dir, "a.txt")); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestCheckpointWritesSiblingsInAscendingArchiveNameAndRestores(t *testing.T) {
	allowTestOwner(t)
	source := siblingOrderTree(t)
	archive, _, err := walkToArchive(t, source, checkpointRequestForTest())
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, header := range archiveHeaders(t, archive)[1:] {
		names = append(names, header.Name)
	}
	if !sort.StringsAreSorted(names) {
		t.Fatalf("archive order is not ascending: %q", names)
	}
	// The link target precedes the link: "a.txt" is written first, "a/x" links to it.
	for _, header := range archiveHeaders(t, archive)[1:] {
		if header.Name == "a/x" && header.Linkname != "a.txt" {
			t.Fatalf("a/x links to %q", header.Linkname)
		}
	}

	fixture := newRestoreFixture(t, 100)
	entries, err := restoreArchiveEntries(context.Background(), archive, fixture.request)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := restoreExtract(ctx, archive, fixture.namespace, fixture.device, fixture.request); err != nil {
		t.Fatal(err)
	}
	if err := restoreApplyDirectoryMetadata(ctx, fixture.namespace, fixture.device, entries); err != nil {
		t.Fatal(err)
	}
	restored, err := restoreReadNamespace(ctx, fixture.namespace, fixture.device, fixture.request)
	if err != nil {
		t.Fatal(err)
	}
	if !sameRestoreEntries(entries, restored) {
		t.Fatal(restoreEntryDifference(entries, restored))
	}
	for _, rel := range []string{"snapshot.py", "snapshot/c", "deep/snapshot.py", "a.txt", "a/x"} {
		if _, err := os.Stat(filepath.Join(fixture.dir, rel)); err != nil {
			t.Fatalf("%s not restored: %v", rel, err)
		}
	}
	counts, inodes := linkCounts(t, fixture.dir, "a.txt", "a/x")
	if counts[0] != 2 || counts[1] != 2 || inodes[0] != inodes[1] {
		t.Fatalf("hard link across the siblings lost: counts %v inodes %v", counts, inodes)
	}
}
