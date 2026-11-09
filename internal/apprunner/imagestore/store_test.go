package imagestore

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
)

const operation = "40000000-0000-4000-8000-000000000004"

func sum(raw []byte) string {
	s := sha256.Sum256(raw)
	return hex.EncodeToString(s[:])
}

func newStore(t *testing.T) *Store {
	t.Helper()
	root := filepath.Join(canonicalTemp(t), "images")
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	s, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func canonicalTemp(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "imagestore-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	real, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	return real
}

type testLayer struct {
	blob       appplatform.ImageBlob
	compressed []byte
}

func gzipTar(t *testing.T, body string) (compressed, plain []byte) {
	t.Helper()
	var tarball bytes.Buffer
	tw := tar.NewWriter(&tarball)
	_ = tw.WriteHeader(&tar.Header{Name: "file", Mode: 0o644, Size: int64(len(body)), Typeflag: tar.TypeReg})
	_, _ = tw.Write([]byte(body))
	_ = tw.Close()
	var gz bytes.Buffer
	zw := gzip.NewWriter(&gz)
	_, _ = zw.Write(tarball.Bytes())
	_ = zw.Close()
	return gz.Bytes(), tarball.Bytes()
}

func newLayer(t *testing.T, body string) testLayer {
	t.Helper()
	compressed, plain := gzipTar(t, body)
	return testLayer{compressed: compressed, blob: appplatform.ImageBlob{
		Digest: sum(compressed), Kind: appplatform.ImageBlobLayer, MediaType: "application/vnd.oci.image.layer.v1.tar+gzip",
		Size: int64(len(compressed)), DiffID: sum(plain), UncompressedBytes: int64(len(plain)),
	}}
}

func upload(t *testing.T, s *Store, blob appplatform.ImageBlob, body []byte, chunk int) Refusal {
	t.Helper()
	u, _, done, refusal, err := s.Begin(operation, blob, 0)
	if err != nil || refusal != RefusalNone {
		t.Fatalf("begin: %v %v", refusal, err)
	}
	if done {
		return RefusalNone
	}
	for len(body) > 0 {
		n := min(chunk, len(body))
		if r := u.Write(body[:n]); r != RefusalNone {
			return r
		}
		body = body[n:]
	}
	return u.Commit(blob.Digest, blob.Size)
}

func TestStoresAVerifiedLayerAndServesItBack(t *testing.T) {
	s := newStore(t)
	layer := newLayer(t, "hello")
	if r := upload(t, s, layer.blob, layer.compressed, 7); r != RefusalNone {
		t.Fatal(r)
	}
	if !s.Has(operation, layer.blob.Digest) || s.Used() != layer.blob.Size {
		t.Fatalf("stored=%v used=%d", s.Has(operation, layer.blob.Digest), s.Used())
	}
	file, err := s.Source(operation).Open(layer.blob.Digest)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	var got bytes.Buffer
	_, _ = got.ReadFrom(file)
	if !bytes.Equal(got.Bytes(), layer.compressed) {
		t.Fatal("served bytes differ")
	}
	if _, err := os.Stat(filepath.Join(s.root, operation, layer.blob.Digest+".partial")); err == nil {
		t.Fatal("partial left behind")
	}
}

// Each case names how an upload is refused. A refused upload leaves no stored blob and no partial.
func TestRefusesBlobsThatBreakTheirDeclaration(t *testing.T) {
	layer := newLayer(t, "hello")
	other := newLayer(t, "other")
	bomb := bytes.Repeat([]byte{0}, 1<<20)
	var bombGz bytes.Buffer
	zw := gzip.NewWriter(&bombGz)
	_, _ = zw.Write(bomb)
	_ = zw.Close()
	bombBlob := appplatform.ImageBlob{Digest: sum(bombGz.Bytes()), Kind: appplatform.ImageBlobLayer, MediaType: "application/vnd.oci.image.layer.v1.tar+gzip", Size: int64(bombGz.Len()), DiffID: sum(bomb), UncompressedBytes: 1000}
	cases := map[string]struct {
		blob    appplatform.ImageBlob
		body    []byte
		want    Refusal
		atBegin bool
	}{
		"digest differs from the bytes":                    {layer.blob, other.compressed[:len(layer.compressed)], RefusalDigestMismatch, false},
		"diff id differs from the tar":                     {func() appplatform.ImageBlob { b := layer.blob; b.DiffID = strings.Repeat("a", 64); return b }(), layer.compressed, RefusalDiffIDMismatch, false},
		"uncompressed size differs":                        {func() appplatform.ImageBlob { b := layer.blob; b.UncompressedBytes++; return b }(), layer.compressed, RefusalDiffIDMismatch, false},
		"a decompression bomb stops at the declared bound": {bombBlob, bombGz.Bytes(), RefusalUncompressedBound, false},
		"more bytes than declared":                         {func() appplatform.ImageBlob { b := layer.blob; b.Size--; return b }(), layer.compressed, RefusalSizeBound, false},
		"layer media type zstd": {func() appplatform.ImageBlob {
			b := layer.blob
			b.MediaType = "application/vnd.oci.image.layer.v1.tar+zstd"
			return b
		}(), layer.compressed, RefusalMediaType, true},
		"layer over 256 MiB": {func() appplatform.ImageBlob { b := layer.blob; b.Size = appplatform.MaxImageBlobBytes + 1; return b }(), nil, RefusalSizeBound, true},
		"config over 1 MiB":  {appplatform.ImageBlob{Digest: strings.Repeat("c", 64), Kind: appplatform.ImageBlobConfig, MediaType: appplatform.MediaTypeOCIConfig, Size: appplatform.MaxImageMetadataBytes + 1}, nil, RefusalSizeBound, true},
		"unknown kind":       {appplatform.ImageBlob{Digest: strings.Repeat("c", 64), Kind: "index", MediaType: appplatform.MediaTypeOCIConfig, Size: 10}, nil, RefusalMediaType, true},
		"uncompressed bound above 1 GiB": {func() appplatform.ImageBlob {
			b := layer.blob
			b.UncompressedBytes = appplatform.MaxImageLayerUncompressedBytes + 1
			return b
		}(), nil, RefusalUncompressedBound, true},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			s := newStore(t)
			u, _, _, refusal, err := s.Begin(operation, tc.blob, 0)
			if err != nil {
				t.Fatal(err)
			}
			if tc.atBegin {
				if refusal != tc.want || u != nil {
					t.Fatalf("begin: %v", refusal)
				}
				return
			}
			if refusal != RefusalNone {
				t.Fatalf("begin refused: %v", refusal)
			}
			got := RefusalNone
			for rest := tc.body; len(rest) > 0 && got == RefusalNone; {
				n := min(1024, len(rest))
				got = u.Write(rest[:n])
				rest = rest[n:]
			}
			if got == RefusalNone {
				got = u.Commit(tc.blob.Digest, tc.blob.Size)
			}
			if got != tc.want {
				t.Fatalf("refusal %q, want %q", got, tc.want)
			}
			if s.Has(operation, tc.blob.Digest) {
				t.Fatal("a refused blob was stored")
			}
			if _, err := os.Stat(filepath.Join(s.root, operation, tc.blob.Digest+".partial")); err == nil {
				t.Fatal("a refused blob left a partial")
			}
			if s.Used() != 0 {
				t.Fatalf("a refused blob is still accounted: %d", s.Used())
			}
		})
	}
}

func TestUncompressedLayerIsHashedAsItsOwnDiffID(t *testing.T) {
	_, plain := gzipTar(t, "plain")
	blob := appplatform.ImageBlob{Digest: sum(plain), Kind: appplatform.ImageBlobLayer, MediaType: "application/vnd.oci.image.layer.v1.tar", Size: int64(len(plain)), DiffID: sum(plain), UncompressedBytes: int64(len(plain))}
	s := newStore(t)
	if r := upload(t, s, blob, plain, 100); r != RefusalNone {
		t.Fatal(r)
	}
	bad := blob
	bad.DiffID = strings.Repeat("a", 64)
	if r := upload(t, newStore(t), bad, plain, 100); r != RefusalDiffIDMismatch {
		t.Fatalf("uncompressed diff id mismatch: %q", r)
	}
}

func TestResumeContinuesFromTheStoredPrefixAndRebuildsTheHashes(t *testing.T) {
	s := newStore(t)
	layer := newLayer(t, strings.Repeat("resume me ", 40))
	half := len(layer.compressed) / 2
	u, _, _, _, err := s.Begin(operation, layer.blob, 0)
	if err != nil {
		t.Fatal(err)
	}
	if r := u.Write(layer.compressed[:half]); r != RefusalNone {
		t.Fatal(r)
	}
	u.Abort() // a lost connection keeps the partial
	// Resuming at the wrong offset names the right one and changes nothing.
	if u2, at, _, r, err := s.Begin(operation, layer.blob, int64(half)+1); u2 != nil || at != int64(half) || r != RefusalNone || err != nil {
		t.Fatalf("wrong resume: at=%d refusal=%q err=%v", at, r, err)
	}
	u, at, _, _, err := s.Begin(operation, layer.blob, int64(half))
	if err != nil || at != int64(half) {
		t.Fatalf("resume: at=%d %v", at, err)
	}
	if r := u.Write(layer.compressed[half:]); r != RefusalNone {
		t.Fatal(r)
	}
	if r := u.Commit(layer.blob.Digest, layer.blob.Size); r != RefusalNone {
		t.Fatalf("resumed upload refused: %q", r)
	}
	if !s.Has(operation, layer.blob.Digest) {
		t.Fatal("resumed blob not stored")
	}
}

func TestResumeFromZeroRestartsAndAFinishedPartialCommitsWithNoBytes(t *testing.T) {
	s := newStore(t)
	layer := newLayer(t, "restart")
	u, _, _, _, _ := s.Begin(operation, layer.blob, 0)
	_ = u.Write(layer.compressed[:3])
	u.Abort()
	u, at, _, _, err := s.Begin(operation, layer.blob, 0)
	if err != nil || at != 0 {
		t.Fatalf("restart: %d %v", at, err)
	}
	_ = u.Write(layer.compressed)
	u.Abort() // all bytes arrived but the end frame was lost
	u, at, _, _, err = s.Begin(operation, layer.blob, layer.blob.Size)
	if err != nil || at != layer.blob.Size {
		t.Fatalf("resume at end: %d %v", at, err)
	}
	if r := u.Commit(layer.blob.Digest, layer.blob.Size); r != RefusalNone {
		t.Fatalf("commit with no new bytes: %q", r)
	}
}

// A stored prefix that is not the start of a valid layer is found out and discarded, so a resume can never
// complete a blob around corrupt bytes, and the same refusal does not repeat for ever.
func TestACorruptStoredPrefixIsRefusedAndDiscarded(t *testing.T) {
	s := newStore(t)
	layer := newLayer(t, "prefix")
	dir := filepath.Join(s.root, operation)
	_ = os.Mkdir(dir, 0700)
	prefix := []byte("not a gzip stream at all......")
	partial := filepath.Join(dir, layer.blob.Digest+".partial")
	_ = os.WriteFile(partial, prefix, 0600)
	s.used = int64(len(prefix))
	u, _, _, refusal, err := s.Begin(operation, layer.blob, int64(len(prefix)))
	if err != nil {
		t.Fatal(err)
	}
	if u != nil {
		got := u.Write(layer.compressed[:len(layer.compressed)-len(prefix)])
		if got == RefusalNone {
			got = u.Commit(layer.blob.Digest, layer.blob.Size)
		}
		refusal = got
	}
	if refusal == RefusalNone {
		t.Fatal("a corrupt prefix was completed into a stored blob")
	}
	if _, err := os.Stat(partial); err == nil || s.Used() != 0 || s.Has(operation, layer.blob.Digest) {
		t.Fatalf("corrupt prefix kept (used=%d)", s.Used())
	}
}

func TestBoundsConcurrentSessionsAndDuplicateBlobs(t *testing.T) {
	s := newStore(t)
	a, b, c := newLayer(t, "a"), newLayer(t, "b"), newLayer(t, "c")
	ua, _, _, _, _ := s.Begin(operation, a.blob, 0)
	if again, _, _, refusal, _ := s.Begin(operation, a.blob, 0); again != nil || refusal != RefusalBusy {
		t.Fatalf("the same blob uploaded twice at once: %q", refusal)
	}
	ub, _, _, _, _ := s.Begin(operation, b.blob, 0)
	if third, _, _, refusal, _ := s.Begin(operation, c.blob, 0); third != nil || refusal != RefusalBusy {
		t.Fatalf("a third session was admitted: %q", refusal)
	}
	ua.Abort()
	if third, _, _, refusal, _ := s.Begin(operation, c.blob, 0); third == nil || refusal != RefusalNone {
		t.Fatalf("a freed slot was not reusable: %q", refusal)
	} else {
		third.Abort()
	}
	ub.Abort()
}

func TestScratchCapRefusesBeforeAnyByteIsWritten(t *testing.T) {
	s := newStore(t)
	s.limit = 100
	layer := newLayer(t, strings.Repeat("big ", 100))
	if layer.blob.Size <= 100 {
		t.Skip("fixture smaller than the cap")
	}
	if u, _, _, refusal, err := s.Begin(operation, layer.blob, 0); u != nil || refusal != RefusalScratchFull || err != nil {
		t.Fatalf("cap: %q %v", refusal, err)
	}
	if s.Used() != 0 {
		t.Fatal("a refused reservation stayed accounted")
	}
}

func TestOpenRemovesOrphansAndRefusesAnUnsafeRoot(t *testing.T) {
	dir := canonicalTemp(t)
	root := filepath.Join(dir, "images")
	_ = os.Mkdir(root, 0700)
	_ = os.MkdirAll(filepath.Join(root, operation), 0700)
	_ = os.WriteFile(filepath.Join(root, operation, strings.Repeat("a", 64)), []byte("orphan"), 0600)
	_ = os.WriteFile(filepath.Join(root, "stray"), []byte("x"), 0600)
	s, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	if entries, _ := os.ReadDir(root); len(entries) != 0 || s.Used() != 0 {
		t.Fatalf("orphans left: %v", entries)
	}
	for name, prepare := range map[string]func() string{
		"relative":       func() string { return "images" },
		"world readable": func() string { p := filepath.Join(dir, "open"); _ = os.Mkdir(p, 0755); _ = os.Chmod(p, 0755); return p },
		"missing":        func() string { return filepath.Join(dir, "missing") },
		"symlink": func() string {
			p := filepath.Join(dir, "link")
			_ = os.Symlink(root, p)
			return p
		},
		"a file": func() string { p := filepath.Join(dir, "file"); _ = os.WriteFile(p, nil, 0700); return p },
	} {
		if _, err := Open(prepare()); err == nil {
			t.Fatalf("%s root accepted", name)
		}
	}
}

func TestNeverFollowsALinkInScratchOrAcceptsAnotherName(t *testing.T) {
	s := newStore(t)
	dir := filepath.Join(s.root, operation)
	_ = os.Mkdir(dir, 0700)
	secret := filepath.Join(canonicalTemp(t), "secret")
	_ = os.WriteFile(secret, []byte("secret"), 0600)
	digest := strings.Repeat("a", 64)
	_ = os.Symlink(secret, filepath.Join(dir, digest))
	if s.Has(operation, digest) {
		t.Fatal("a symlink counts as a stored blob")
	}
	if _, err := s.Open(operation, digest); err == nil {
		t.Fatal("a symlink was followed")
	}
	for _, name := range []string{"../etc", "ABC", strings.Repeat("a", 63), digest + ".partial"} {
		if s.Has(operation, name) {
			t.Fatalf("name %q accepted", name)
		}
		if _, err := s.Open(operation, name); err == nil {
			t.Fatalf("name %q opened", name)
		}
	}
	if _, err := s.Open("not-an-operation", digest); err == nil || s.Has("../x", digest) {
		t.Fatal("a non-uuid operation was accepted")
	}
}

func TestMissingListsManifestAndConfigThenLayers(t *testing.T) {
	s := newStore(t)
	f := newImageFixture(t)
	if got := s.Missing(operation, f.entry); len(got) != 2 {
		t.Fatalf("empty scratch: %v", got)
	}
	upload(t, s, f.config, f.configBytes, 1<<20)
	if got := s.Missing(operation, f.entry); len(got) != 1 || got[0] != f.entry.ManifestDigest {
		t.Fatalf("manifest missing: %v", got)
	}
	upload(t, s, f.manifest, f.manifestBytes, 1<<20)
	got := s.Missing(operation, f.entry)
	if len(got) != 2 {
		t.Fatalf("layers missing: %v", got)
	}
	for _, l := range f.layers {
		if r := upload(t, s, l.blob, l.compressed, 1<<20); r != RefusalNone {
			t.Fatal(r)
		}
	}
	if got := s.Missing(operation, f.entry); len(got) != 0 {
		t.Fatalf("complete scratch lists %v", got)
	}
	// A stored manifest that does not match the entry is as good as absent.
	_ = os.WriteFile(filepath.Join(s.root, operation, f.entry.ManifestDigest), []byte("tampered"), 0600)
	if got := s.Missing(operation, f.entry); len(got) != 2 {
		t.Fatalf("tampered manifest: %v", got)
	}
}

func TestRemoveOperationReturnsItsBytes(t *testing.T) {
	s := newStore(t)
	layer := newLayer(t, "gone")
	upload(t, s, layer.blob, layer.compressed, 100)
	if err := s.RemoveOperation(operation); err != nil || s.Used() != 0 || s.Has(operation, layer.blob.Digest) {
		t.Fatalf("remove: used=%d %v", s.Used(), err)
	}
	if err := s.RemoveOperation(operation); err != nil {
		t.Fatal("removing nothing failed:", err)
	}
}
