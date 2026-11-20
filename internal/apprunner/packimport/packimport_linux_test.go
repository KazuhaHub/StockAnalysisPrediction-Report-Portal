//go:build linux

package packimport

import (
	"archive/tar"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/data"
)

// fixture is a source tree and an empty checkpoint root, both private to the test.
type fixture struct{ from, root string }

func newFixture(t *testing.T) fixture {
	t.Helper()
	base := t.TempDir()
	f := fixture{from: filepath.Join(base, "tree"), root: filepath.Join(base, "checkpoints")}
	for _, dir := range []string{f.from, f.root} {
		if err := os.Mkdir(dir, 0755); err != nil {
			t.Fatal(err)
		}
	}
	return f
}

func (f fixture) write(t *testing.T, name, body string, mode os.FileMode) {
	t.Helper()
	path := filepath.Join(f.from, name)
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
}

func (f fixture) out(name string) string {
	return filepath.Join(f.root, data.ImportInboxDirectory, name)
}

func (f fixture) pack(t *testing.T, name string) (Result, error) {
	t.Helper()
	return Pack(context.Background(), Options{From: f.from, Out: f.out(name), Packer: "app-runner/test"})
}

func wantRefusal(t *testing.T, err error, contains ...string) {
	t.Helper()
	if err == nil {
		t.Fatal("pack accepted a tree that must be refused")
	}
	var refusal *TreeError
	if !errors.As(err, &refusal) {
		t.Fatalf("not a typed tree refusal: %v", err)
	}
	for _, text := range contains {
		if !strings.Contains(err.Error(), text) {
			t.Fatalf("error %q lacks %q", err, text)
		}
	}
}

func TestPackRefusesUnsafeTrees(t *testing.T) {
	cases := []struct {
		name  string
		build func(t *testing.T, f fixture)
		want  []string
	}{
		{"symlink", func(t *testing.T, f fixture) {
			f.write(t, "ok.txt", "x", 0644)
			if err := os.Symlink("ok.txt", filepath.Join(f.from, "link")); err != nil {
				t.Fatal(err)
			}
		}, []string{"link", "symbolic link"}},
		{"fifo", func(t *testing.T, f fixture) {
			if err := syscall.Mkfifo(filepath.Join(f.from, "pipe"), 0644); err != nil {
				t.Fatal(err)
			}
		}, []string{"pipe", "regular files and directories"}},
		{"setuid", func(t *testing.T, f fixture) { f.write(t, "suid", "x", 0755|os.ModeSetuid) }, []string{"suid", "setuid"}},
		{"setgid file", func(t *testing.T, f fixture) { f.write(t, "sgid", "x", 0755|os.ModeSetgid) }, []string{"sgid", "setgid"}},
		{"sticky", func(t *testing.T, f fixture) {
			f.write(t, "d/x", "x", 0644)
			if err := os.Chmod(filepath.Join(f.from, "d"), os.ModeSticky|0777); err != nil {
				t.Fatal(err)
			}
		}, []string{"d", "sticky"}},
		{"reserved name", func(t *testing.T, f fixture) { f.write(t, data.ArchiveManifestEntry, "{}", 0644) }, []string{"reserved"}},
		{"too deep", func(t *testing.T, f fixture) {
			f.write(t, strings.Repeat("d/", 65)+"x", "x", 0644)
		}, []string{"deeper than 64"}},
		{"invalid utf-8", func(t *testing.T, f fixture) { f.write(t, "bad\xff\xfename", "x", 0644) }, []string{"UTF-8"}},
		{"hard link outside the tree", func(t *testing.T, f fixture) {
			f.write(t, "a", "x", 0644)
			if err := os.Link(filepath.Join(f.from, "a"), filepath.Join(f.root, "elsewhere")); err != nil {
				t.Fatal(err)
			}
		}, []string{"a", "hard link", "outside"}},
		{"one of two hard links outside the tree", func(t *testing.T, f fixture) {
			f.write(t, "a", "x", 0644)
			if err := os.Link(filepath.Join(f.from, "a"), filepath.Join(f.from, "b")); err != nil {
				t.Fatal(err)
			}
			if err := os.Link(filepath.Join(f.from, "a"), filepath.Join(f.root, "elsewhere")); err != nil {
				t.Fatal(err)
			}
		}, []string{"hard link", "outside"}},
		{"name order", func(t *testing.T, f fixture) {
			f.write(t, "a/inside", "x", 0644)
			f.write(t, "a.txt", "x", 0644)
		}, []string{"a.txt", "sorts"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t)
			tc.build(t, f)
			_, err := f.pack(t, "seed.tar")
			wantRefusal(t, err, tc.want...)
			if _, err := os.Stat(f.out("seed.tar")); !os.IsNotExist(err) {
				t.Fatalf("a refused pack left an archive: %v", err)
			}
			entries, _ := os.ReadDir(filepath.Join(f.root, data.ImportInboxDirectory))
			if len(entries) != 0 {
				t.Fatalf("a refused pack left %d files in the inbox", len(entries))
			}
		})
	}
}

func TestPackWritesCanonicalArchive(t *testing.T) {
	f := newFixture(t)
	f.write(t, ".portal-data-schema", "3\n", 0600)
	f.write(t, "bars/2026/part.bin", strings.Repeat("b", 5000), 0600)
	f.write(t, "readme.txt", "hello", 0755)
	if err := os.Mkdir(filepath.Join(f.from, "empty"), 0700); err != nil {
		t.Fatal(err)
	}
	result, err := f.pack(t, "seed.tar")
	if err != nil {
		t.Fatal(err)
	}
	if result.Entries != 6 || result.ContentBytes != 2+5000+5 {
		t.Fatalf("counts: %+v", result)
	}
	path := f.out("seed.tar")
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0400 || info.Size() != result.Bytes {
		t.Fatalf("archive profile: %v %v", info, err)
	}
	dir, _ := os.Stat(filepath.Dir(path))
	if dir.Mode().Perm() != 0700 {
		t.Fatalf("inbox mode %v", dir.Mode().Perm())
	}

	// The manifest is the first entry and the printed digest is the digest of everything after it.
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	reader := tar.NewReader(file)
	first, err := reader.Next()
	if err != nil || first.Name != data.ArchiveManifestEntry {
		t.Fatalf("first entry %v %v", first, err)
	}
	raw, _ := io.ReadAll(reader)
	manifest, err := data.ParseImportManifest(raw)
	if err != nil || manifest.Entries != result.Entries || manifest.Bytes != result.ContentBytes ||
		manifest.BodySHA256 != result.SHA256 || manifest.Packer != "app-runner/test" {
		t.Fatalf("manifest %+v %v", manifest, err)
	}
	if _, err := file.Seek(data.ImportManifestBlockBytes, io.SeekStart); err != nil {
		t.Fatal(err)
	}
	sum := sha256.New()
	if _, err := io.Copy(sum, file); err != nil || hex.EncodeToString(sum.Sum(nil)) != result.SHA256 {
		t.Fatalf("body digest differs from the printed digest: %v", err)
	}

	// Owners and modes are normalised whatever the source tree carried.
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		t.Fatal(err)
	}
	reader = tar.NewReader(file)
	_, _ = reader.Next()
	names := []string{}
	for {
		header, err := reader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		names = append(names, header.Name)
		want := int64(data.ImportFileMode)
		if header.Typeflag == tar.TypeDir {
			want = data.ImportDirectoryMode
		}
		if header.Uid != 65532 || header.Gid != 65532 || header.Mode != want || header.ModTime.Unix() != 0 {
			t.Fatalf("%s not normalised: uid=%d gid=%d mode=%o", header.Name, header.Uid, header.Gid, header.Mode)
		}
	}
	want := []string{".portal-data-schema", "bars/", "bars/2026/", "bars/2026/part.bin", "empty/", "readme.txt"}
	if strings.Join(names, ",") != strings.Join(want, ",") {
		t.Fatalf("entry order %v", names)
	}

	// The runner reader accepts it and reports the same facts.
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		t.Fatal(err)
	}
	inspected, err := data.InspectImportArchive(context.Background(), file, 1000)
	if err != nil || !inspected.Found || inspected.Manifest != manifest || inspected.SchemaMarker != 3 || inspected.Bytes != result.Bytes {
		t.Fatalf("inspect %+v %v", inspected, err)
	}
}

func TestPackNeverOverwrites(t *testing.T) {
	f := newFixture(t)
	f.write(t, "a", "one", 0600)
	first, err := f.pack(t, "seed.tar")
	if err != nil {
		t.Fatal(err)
	}
	f.write(t, "a", "two", 0600)
	if _, err := f.pack(t, "seed.tar"); !errors.Is(err, ErrExists) {
		t.Fatalf("overwrite not refused: %v", err)
	}
	again, err := os.ReadFile(f.out("seed.tar"))
	if err != nil || int64(len(again)) != first.Bytes {
		t.Fatalf("the existing archive changed: %v", err)
	}
	entries, _ := os.ReadDir(filepath.Dir(f.out("x")))
	if len(entries) != 1 {
		t.Fatalf("a refused overwrite left files behind: %d", len(entries))
	}
}

func TestPackRefusesAnUnsafeInboxOrName(t *testing.T) {
	f := newFixture(t)
	f.write(t, "a", "x", 0600)
	inbox := filepath.Join(f.root, data.ImportInboxDirectory)
	if err := os.Mkdir(inbox, 0755); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pack(t, "seed.tar"); err == nil || !strings.Contains(err.Error(), "0700") {
		t.Fatalf("a shared inbox was accepted: %v", err)
	}
	if err := os.Chmod(inbox, 0700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"../x.tar", "seed", "a b.tar", ".hidden.tar"} {
		if _, err := Pack(context.Background(), Options{From: f.from, Out: filepath.Join(inbox, name), Packer: "t"}); err == nil {
			t.Fatalf("name %q accepted", name)
		}
	}
	if _, err := Pack(context.Background(), Options{From: f.from, Out: filepath.Join(f.root, "elsewhere", "seed.tar"), Packer: "t"}); err == nil {
		t.Fatal("an output outside the inbox directory was accepted")
	}
	// The source may not contain the inbox.
	if _, err := Pack(context.Background(), Options{From: f.root, Out: filepath.Join(inbox, "self.tar"), Packer: "t"}); err == nil {
		t.Fatal("a source containing the inbox was accepted")
	}
}

func TestPackWritesInTreeHardLinksAsLinkEntries(t *testing.T) {
	f := newFixture(t)
	f.write(t, "data/part.bin", strings.Repeat("p", 3000), 0600)
	f.write(t, "plain.txt", "plain", 0600)
	for _, rel := range []string{"snapshots/1/part.bin", "snapshots/2/part.bin"} {
		if err := os.MkdirAll(filepath.Join(f.from, filepath.Dir(rel)), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.Link(filepath.Join(f.from, "data/part.bin"), filepath.Join(f.from, rel)); err != nil {
			t.Fatal(err)
		}
	}
	result, err := f.pack(t, "seed.tar")
	if err != nil {
		t.Fatal(err)
	}
	// 4 directories, 2 files and 2 link entries; content bytes are counted once.
	if result.Entries != 8 || result.ContentBytes != 3000+5 {
		t.Fatalf("counts: %+v", result)
	}
	file, err := os.Open(f.out("seed.tar"))
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	reader := tar.NewReader(file)
	_, _ = reader.Next()
	links := map[string]string{}
	for {
		header, err := reader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if header.Typeflag == tar.TypeLink {
			links[header.Name] = header.Linkname
			if header.Size != 0 || header.Uid != 65532 || header.Gid != 65532 || header.Mode != data.ImportFileMode {
				t.Fatalf("link entry %s: size=%d uid=%d gid=%d mode=%o", header.Name, header.Size, header.Uid, header.Gid, header.Mode)
			}
		}
	}
	if len(links) != 2 || links["snapshots/1/part.bin"] != "data/part.bin" || links["snapshots/2/part.bin"] != "data/part.bin" {
		t.Fatalf("links: %v", links)
	}
	// The runner reader accepts the archive, including its link entries, and agrees with the manifest.
	inspected, err := data.InspectImportArchive(context.Background(), file, 1000)
	if err != nil || !inspectedMatches(inspected, result) {
		t.Fatalf("inspect %+v %v", inspected, err)
	}
}

func inspectedMatches(inspected data.ImportInboxArchive, result Result) bool {
	return inspected.Found && inspected.Manifest.Entries == result.Entries && inspected.Manifest.Bytes == result.ContentBytes &&
		inspected.Manifest.BodySHA256 == result.SHA256
}
