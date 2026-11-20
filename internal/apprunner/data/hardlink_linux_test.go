//go:build linux && (amd64 || arm64)

package data

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// allowTestOwner lets the walker, the readers and the extractor accept entries owned by the user running the
// test, so the whole archive path runs on a temporary directory without root.
func allowTestOwner(t *testing.T) (uid, gid uint32) {
	t.Helper()
	uid, gid = uint32(os.Getuid()), uint32(os.Getgid())
	extraOwnerAllowed = func(u, g uint32) bool { return u == uid && g == gid }
	t.Cleanup(func() { extraOwnerAllowed = nil })
	return uid, gid
}

func hardlinkTree(t *testing.T) string {
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
	write("data/part.bin", strings.Repeat("p", 3000))
	write("plain.txt", "plain")
	write("zlast.txt", "z")
	for _, rel := range []string{"snapshots/1/part.bin", "snapshots/2/part.bin"} {
		if err := os.MkdirAll(filepath.Join(dir, filepath.Dir(rel)), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Link(filepath.Join(dir, "data/part.bin"), filepath.Join(dir, rel)); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func openDir(t *testing.T, dir string) *os.File {
	t.Helper()
	f, err := os.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.Close() })
	return f
}

func checkpointRequestForTest() CheckpointRequest {
	return CheckpointRequest{MaxArchiveBytes: 1 << 20, MaxEntries: 100}
}

// walkToArchive runs the checkpoint walker over dir and returns the archive as the restore readers see it: the
// manifest entry first, then the walk.
func walkToArchive(t *testing.T, dir string, request CheckpointRequest) (*os.File, *checkpointWalker, error) {
	t.Helper()
	var buffer bytes.Buffer
	writer := tar.NewWriter(&buffer)
	if err := writer.WriteHeader(&tar.Header{Name: checkpointManifestEntry, Mode: 0600, Size: 2, Typeflag: tar.TypeReg}); err != nil {
		t.Fatal(err)
	}
	if _, err := writer.Write([]byte("{}")); err != nil {
		t.Fatal(err)
	}
	walker := newCheckpointWalker(context.Background(), writer, request)
	if err := walker.walk(openDir(t, dir)); err != nil {
		return nil, walker, err
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	file, err := os.Create(filepath.Join(t.TempDir(), "archive.tar"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { file.Close() })
	if _, err := file.Write(buffer.Bytes()); err != nil {
		t.Fatal(err)
	}
	return file, walker, nil
}

func archiveHeaders(t *testing.T, file *os.File) []*tar.Header {
	t.Helper()
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		t.Fatal(err)
	}
	reader := tar.NewReader(file)
	var headers []*tar.Header
	for {
		header, err := reader.Next()
		if err == io.EOF {
			return headers
		}
		if err != nil {
			t.Fatal(err)
		}
		headers = append(headers, header)
	}
}

func TestCheckpointWalkerWritesInTreeHardLinksAsLinkEntries(t *testing.T) {
	allowTestOwner(t)
	file, walker, err := walkToArchive(t, hardlinkTree(t), checkpointRequestForTest())
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]*tar.Header{}
	for _, header := range archiveHeaders(t, file) {
		got[header.Name] = header
	}
	if h := got["data/part.bin"]; h == nil || h.Typeflag != tar.TypeReg || h.Size != 3000 {
		t.Fatalf("the first path is not written as a regular file: %+v", h)
	}
	for _, name := range []string{"snapshots/1/part.bin", "snapshots/2/part.bin"} {
		h := got[name]
		if h == nil || h.Typeflag != tar.TypeLink || h.Linkname != "data/part.bin" || h.Size != 0 {
			t.Fatalf("%s is not a link to the first path: %+v", name, h)
		}
	}
	// Content bytes are charged once; inodes (directories and first paths) count apart from link entries.
	if walker.bytes != 3000+5+1 || walker.links != 2 {
		t.Fatalf("bytes=%d links=%d", walker.bytes, walker.links)
	}
	if walker.entries != 7 {
		t.Fatalf("inode entries = %d, want 7 (4 directories, 3 files)", walker.entries)
	}
}

func TestCheckpointWalkerRefusesALinkCountLargerThanTheLinksFound(t *testing.T) {
	allowTestOwner(t)
	dir := hardlinkTree(t)
	outside := filepath.Join(t.TempDir(), "elsewhere")
	if err := os.Link(filepath.Join(dir, "data/part.bin"), outside); err != nil {
		t.Fatal(err)
	}
	if _, _, err := walkToArchive(t, dir, checkpointRequestForTest()); !errors.Is(err, ErrCheckpointUnsafe) {
		t.Fatalf("a file with a link outside the namespace: %v", err)
	}
	// A lone file whose only extra link is outside is refused as well.
	lone := t.TempDir()
	if err := os.WriteFile(filepath.Join(lone, "f"), []byte("x"), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(filepath.Join(lone, "f"), filepath.Join(t.TempDir(), "g")); err != nil {
		t.Fatal(err)
	}
	if _, _, err := walkToArchive(t, lone, checkpointRequestForTest()); !errors.Is(err, ErrCheckpointUnsafe) {
		t.Fatalf("a lone file with an outside link: %v", err)
	}
}

func TestCheckpointWalkerBoundsLinkEntriesSeparatelyFromInodes(t *testing.T) {
	allowTestOwner(t)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a"), []byte("x"), 0o640); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 4; i++ {
		if err := os.Link(filepath.Join(dir, "a"), filepath.Join(dir, fmt.Sprintf("b%d", i))); err != nil {
			t.Fatal(err)
		}
	}
	// Five paths of one inode: the inode cap counts the inode once, the link bound counts the other four.
	if _, _, err := walkToArchive(t, dir, CheckpointRequest{MaxArchiveBytes: 1 << 20, MaxEntries: 4}); err != nil {
		t.Fatalf("four links under a cap of four: %v", err)
	}
	if _, _, err := walkToArchive(t, dir, CheckpointRequest{MaxArchiveBytes: 1 << 20, MaxEntries: 3}); !errors.Is(err, ErrCheckpointUnsafe) {
		t.Fatalf("four links under a cap of three: %v", err)
	}
}

type restoreFixture struct {
	namespace *os.File
	dir       string
	device    uint64
	request   RestoreRequest
}

func newRestoreFixture(t *testing.T, entries int64) restoreFixture {
	t.Helper()
	dir := t.TempDir()
	var stat unix.Stat_t
	if err := unix.Stat(dir, &stat); err != nil {
		t.Fatal(err)
	}
	return restoreFixture{
		namespace: openDir(t, dir), dir: dir, device: uint64(stat.Dev),
		request: RestoreRequest{ArchiveBytes: 1 << 20, MaxArchiveBytes: 1 << 20, MaxEntries: entries},
	}
}

func linkCounts(t *testing.T, dir string, names ...string) (counts []uint64, inodes []uint64) {
	t.Helper()
	for _, name := range names {
		var stat unix.Stat_t
		if err := unix.Stat(filepath.Join(dir, name), &stat); err != nil {
			t.Fatal(err)
		}
		counts = append(counts, uint64(stat.Nlink))
		inodes = append(inodes, stat.Ino)
	}
	return counts, inodes
}

func TestHardLinksSurviveCheckpointAndRestoreWithEqualLinkCounts(t *testing.T) {
	allowTestOwner(t)
	source := hardlinkTree(t)
	archive, _, err := walkToArchive(t, source, checkpointRequestForTest())
	if err != nil {
		t.Fatal(err)
	}
	fixture := newRestoreFixture(t, 100)
	entries, err := restoreArchiveEntries(context.Background(), archive, fixture.request)
	if err != nil {
		t.Fatal(err)
	}
	links := 0
	for _, entry := range entries {
		if entry.link != "" {
			links++
			if entry.link != "data/part.bin" || entry.size != 0 || entry.digest != "" {
				t.Fatalf("link entry: %+v", entry)
			}
		}
	}
	if links != 2 {
		t.Fatalf("%d link entries", links)
	}
	// Restore over a namespace that already holds old content, as a real restore does.
	if err := os.WriteFile(filepath.Join(fixture.dir, "stale"), []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	count := 0
	ctx := context.Background()
	if err := restoreClearNamespace(ctx, fixture.namespace, fixture.device, 1, &count, fixture.request.pathLimit()); err != nil {
		t.Fatal(err)
	}
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
	names := []string{"data/part.bin", "snapshots/1/part.bin", "snapshots/2/part.bin"}
	want, _ := linkCounts(t, source, names...)
	got, gotInodes := linkCounts(t, fixture.dir, names...)
	for i := range names {
		if want[i] != 3 || got[i] != want[i] || gotInodes[i] != gotInodes[0] {
			t.Fatalf("%s: links %d (source %d), inode %d (first %d)", names[i], got[i], want[i], gotInodes[i], gotInodes[0])
		}
	}
	// The read-back refuses a restored file that gained a link outside the namespace.
	outside := filepath.Join(t.TempDir(), "extra")
	if err := os.Link(filepath.Join(fixture.dir, "data/part.bin"), outside); err != nil {
		t.Fatal(err)
	}
	if _, err := restoreReadNamespace(ctx, fixture.namespace, fixture.device, fixture.request); !errors.Is(err, ErrRestoreUnsafe) {
		t.Fatalf("read-back with an outside link: %v", err)
	}
}

func TestReadBackDiffersWhereTheArchiveRecordedSeparateFiles(t *testing.T) {
	allowTestOwner(t)
	plain := t.TempDir()
	for _, name := range []string{"a", "b"} {
		if err := os.WriteFile(filepath.Join(plain, name), []byte("same"), 0o640); err != nil {
			t.Fatal(err)
		}
	}
	archive, _, err := walkToArchive(t, plain, checkpointRequestForTest())
	if err != nil {
		t.Fatal(err)
	}
	fixture := newRestoreFixture(t, 100)
	entries, err := restoreArchiveEntries(context.Background(), archive, fixture.request)
	if err != nil {
		t.Fatal(err)
	}
	// The namespace holds b as a link of a: same bytes, different shape.
	if err := os.WriteFile(filepath.Join(fixture.dir, "a"), []byte("same"), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(filepath.Join(fixture.dir, "a"), filepath.Join(fixture.dir, "b")); err != nil {
		t.Fatal(err)
	}
	restored, err := restoreReadNamespace(context.Background(), fixture.namespace, fixture.device, fixture.request)
	if err != nil {
		t.Fatal(err)
	}
	if sameRestoreEntries(entries, restored) {
		t.Fatal("a link was accepted where the archive recorded two files")
	}
}

func TestArchiveWithoutLinksReadsAsBefore(t *testing.T) {
	allowTestOwner(t)
	plain := t.TempDir()
	if err := os.MkdirAll(filepath.Join(plain, "d"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(plain, "d", "f"), []byte("x"), 0o640); err != nil {
		t.Fatal(err)
	}
	archive, walker, err := walkToArchive(t, plain, checkpointRequestForTest())
	if err != nil {
		t.Fatal(err)
	}
	if walker.links != 0 {
		t.Fatalf("links = %d", walker.links)
	}
	entries, err := restoreArchiveEntries(context.Background(), archive, RestoreRequest{ArchiveBytes: 1 << 20, MaxEntries: 2})
	if err != nil || len(entries) != 2 || entries[0].link != "" || entries[1].link != "" {
		t.Fatalf("entries %+v %v", entries, err)
	}
}

// linkArchive builds an archive of the given headers (regular files carry the body "x").
func linkArchive(t *testing.T, headers []tar.Header) *os.File {
	t.Helper()
	var buffer bytes.Buffer
	writer := tar.NewWriter(&buffer)
	if err := writer.WriteHeader(&tar.Header{Name: checkpointManifestEntry, Mode: 0600, Size: 2, Typeflag: tar.TypeReg}); err != nil {
		t.Fatal(err)
	}
	_, _ = writer.Write([]byte("{}"))
	for _, h := range headers {
		h.Format, h.ModTime = tar.FormatPAX, time.Unix(0, 0)
		if h.Typeflag == tar.TypeReg {
			h.Size = 1
		}
		if err := writer.WriteHeader(&h); err != nil {
			t.Fatal(err)
		}
		if h.Typeflag == tar.TypeReg {
			_, _ = writer.Write([]byte("x"))
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	file, err := os.Create(filepath.Join(t.TempDir(), "bad.tar"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { file.Close() })
	if _, err := file.Write(buffer.Bytes()); err != nil {
		t.Fatal(err)
	}
	return file
}

func TestArchiveReaderRefusesBadLinkTargets(t *testing.T) {
	uid, gid := allowTestOwner(t)
	base := tar.Header{Mode: 0640, Uid: int(uid), Gid: int(gid)}
	with := func(mutate func(*tar.Header)) tar.Header {
		h := base
		mutate(&h)
		return h
	}
	reg := func(name string) tar.Header {
		return with(func(h *tar.Header) { h.Name, h.Typeflag = name, tar.TypeReg })
	}
	dir := func(name string) tar.Header {
		return with(func(h *tar.Header) { h.Name, h.Typeflag, h.Mode = name, tar.TypeDir, 0750 })
	}
	link := func(name, target string) tar.Header {
		return with(func(h *tar.Header) { h.Name, h.Typeflag, h.Linkname = name, tar.TypeLink, target })
	}
	cases := map[string][]tar.Header{
		"link to a directory":      {dir("d/"), link("l", "d/")},
		"link to a directory name": {dir("d/"), link("l", "d")},
		"link to a later entry":    {link("a", "b"), reg("b")},
		"link to an unknown name":  {reg("a"), link("b", "nope")},
		"link to itself":           {reg("a"), link("b", "b")},
		"link to a link":           {reg("a"), link("b", "a"), link("c", "b")},
		"absolute target":          {reg("a"), link("b", "/a")},
		"traversal target":         {reg("a"), link("b", "../a")},
		"dot-dot inside target":    {dir("d/"), reg("d/x"), link("e", "d/../d/x")},
		"backslash target":         {reg("a"), link("b", "a\\x")},
		"empty target":             {reg("a"), link("b", "")},
		"the manifest as target":   {reg("a"), link("b", checkpointManifestEntry)},
		"link with content":        {reg("a"), with(func(h *tar.Header) { h.Name, h.Typeflag, h.Linkname, h.Size = "b", tar.TypeLink, "a", 1 })},
		"link with other mode":     {reg("a"), with(func(h *tar.Header) { h.Name, h.Typeflag, h.Linkname, h.Mode = "b", tar.TypeLink, "a", 0600 })},
		"link named as directory":  {reg("a"), link("b/", "a")},
		"duplicate link name":      {reg("a"), link("b", "a"), link("b", "a")},
		"symlink entry":            {reg("a"), with(func(h *tar.Header) { h.Name, h.Typeflag, h.Linkname = "b", tar.TypeSymlink, "a" })},
	}
	for name, headers := range cases {
		t.Run(name, func(t *testing.T) {
			archive := linkArchive(t, headers)
			if _, err := restoreArchiveEntries(context.Background(), archive, RestoreRequest{ArchiveBytes: 1 << 20, MaxEntries: 100}); !errors.Is(err, ErrRestoreUnsafe) {
				t.Fatalf("accepted: %v", err)
			}
		})
	}
	good := linkArchive(t, []tar.Header{reg("a"), link("b", "a")})
	if entries, err := restoreArchiveEntries(context.Background(), good, RestoreRequest{ArchiveBytes: 1 << 20, MaxEntries: 100}); err != nil || len(entries) != 2 {
		t.Fatalf("the well-formed pair: %v", err)
	}
}

func TestArchiveReaderBoundsInodesAndLinksSeparately(t *testing.T) {
	uid, gid := allowTestOwner(t)
	base := tar.Header{Mode: 0640, Uid: int(uid), Gid: int(gid)}
	mk := func(typeflag byte, name, target string) tar.Header {
		h := base
		h.Name, h.Typeflag, h.Linkname = name, typeflag, target
		return h
	}
	read := func(limit int64, headers ...tar.Header) error {
		_, err := restoreArchiveEntries(context.Background(), linkArchive(t, headers), RestoreRequest{ArchiveBytes: 1 << 20, MaxEntries: limit})
		return err
	}
	two := []tar.Header{mk(tar.TypeReg, "a", ""), mk(tar.TypeReg, "b", ""), mk(tar.TypeLink, "c", "a"), mk(tar.TypeLink, "d", "b")}
	if err := read(2, two...); err != nil {
		t.Fatalf("two inodes and two links under a cap of two: %v", err)
	}
	if err := read(2, append(two, mk(tar.TypeLink, "e", "a"))...); !errors.Is(err, ErrRestoreUnsafe) {
		t.Fatalf("a third link under a cap of two: %v", err)
	}
	if err := read(2, mk(tar.TypeReg, "a", ""), mk(tar.TypeReg, "b", ""), mk(tar.TypeReg, "c", "")); !errors.Is(err, ErrRestoreUnsafe) {
		t.Fatalf("a third inode under a cap of two: %v", err)
	}
}

func TestLinkEntriesCostNoInodeAndNoBytes(t *testing.T) {
	withLinks := importFootprintOf([]restoreEntry{
		{name: "a", size: 1000},
		{name: "b", link: "a"},
		{name: "c", link: "a"},
		{name: "d/", directory: true},
	}, 512)
	plain := importFootprintOf([]restoreEntry{{name: "a", size: 1000}, {name: "d/", directory: true}}, 512)
	if *withLinks != *plain {
		t.Fatalf("links change the footprint: %+v vs %+v", withLinks, plain)
	}
	if withLinks.inodes != 3 { // the root, the file and the directory
		t.Fatalf("inodes = %d", withLinks.inodes)
	}
}

func TestPathLimitIsTwiceTheInodeCap(t *testing.T) {
	if got := (RestoreRequest{MaxEntries: 65536}).pathLimit(); got != 2*65536 {
		t.Fatalf("pathLimit = %d", got)
	}
	if got := (RestoreRequest{}).pathLimit(); got != 2*legacyMaxCheckpointEntries {
		t.Fatalf("legacy pathLimit = %d", got)
	}
}
