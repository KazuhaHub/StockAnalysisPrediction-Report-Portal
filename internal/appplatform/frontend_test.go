package appplatform

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"io"
	"io/fs"
	"os"
	"strings"
	"testing"
)

type frontendTestEntry struct {
	name, body string
	mode       fs.FileMode
	method     uint16
}

func frontendTestArchive(t testing.TB, entries ...frontendTestEntry) []byte {
	t.Helper()
	var b bytes.Buffer
	w := zip.NewWriter(&b)
	for _, item := range entries {
		h := &zip.FileHeader{Name: item.name, Method: item.method}
		if item.mode != 0 {
			h.SetMode(item.mode)
		}
		f, err := w.CreateHeader(h)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = io.WriteString(f, item.body); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func frontendManifest(t testing.TB, raw []byte) Manifest {
	t.Helper()
	rawDescriptor, err := os.ReadFile("testdata/descriptors/static.json")
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := ParseDescriptor(rawDescriptor)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(raw)
	parsed.Manifest.Frontend.SHA256 = hex.EncodeToString(sum[:])
	return parsed.Manifest
}

func TestFrontendInspectionBindsBytesAndUsesDeterministicAssetMetadata(t *testing.T) {
	raw := frontendTestArchive(t,
		frontendTestEntry{name: "assets/", mode: fs.ModeDir | 0755},
		frontendTestEntry{name: "assets/main.js", body: "console.log('sample')", method: zip.Deflate},
		frontendTestEntry{name: "index.html", body: "<!doctype html><script src='assets/main.js'></script>"},
		frontendTestEntry{name: "style.css", body: "body { color: black }"},
	)
	m := frontendManifest(t, raw)
	result, err := InspectFrontend(m, raw)
	if err != nil {
		t.Fatal(err)
	}
	if result.SHA256 != m.Frontend.SHA256 || result.Entry != "index.html" || result.ArchiveBytes != int64(len(raw)) || len(result.Files) != 3 {
		t.Fatalf("unexpected inspection: %+v", result)
	}
	if result.Files[0].Path != "assets/main.js" || result.Files[0].ContentType != "text/javascript; charset=utf-8" || result.Files[1].ContentType != "text/html; charset=utf-8" {
		t.Fatalf("wrong metadata: %+v", result.Files)
	}
	var size int64
	for _, file := range result.Files {
		size += file.Bytes
		if len(file.SHA256) != 64 {
			t.Fatal("missing file digest")
		}
	}
	if result.UncompressedBytes != size {
		t.Fatal("incorrect actual size accounting")
	}
	changed := append([]byte(nil), raw...)
	changed[0] ^= 1
	failed, err := InspectFrontend(m, changed)
	if !errors.Is(err, ErrFrontendDigestMismatch) || failed.SHA256 != "" || len(failed.Files) != 0 {
		t.Fatalf("changed artifact accepted or partial result: %+v %v", failed, err)
	}
}

func TestFrontendInspectionRejectsUnsafeArchiveMembers(t *testing.T) {
	cases := map[string][]frontendTestEntry{
		"traversal":                     {{name: "../escape.js", body: "bad"}},
		"absolute":                      {{name: "/escape.js", body: "bad"}},
		"backslash":                     {{name: `assets\escape.js`, body: "bad"}},
		"encoded separator":             {{name: "assets%2fescape.js", body: "bad"}},
		"dot normalization":             {{name: "assets/./main.js", body: "bad"}},
		"duplicate":                     {{name: "main.js", body: "one"}, {name: "main.js", body: "two"}},
		"case collision":                {{name: "main.js", body: "one"}, {name: "MAIN.js", body: "two"}},
		"directory case collision":      {{name: "assets/a.js", body: "one"}, {name: "Assets/b.js", body: "two"}},
		"file ancestor":                 {{name: "assets", body: "one"}, {name: "assets/b.js", body: "two"}},
		"file after implicit directory": {{name: "assets/b.js", body: "two"}, {name: "assets", body: "one"}},
		"duplicate directory":           {{name: "assets/", mode: fs.ModeDir | 0755}, {name: "assets/", mode: fs.ModeDir | 0755}},
		"symlink directory":             {{name: "linked/", mode: fs.ModeSymlink | 0777}},
		"device directory":              {{name: "device/", mode: fs.ModeDevice | 0600}},
		"setuid directory":              {{name: "assets/", mode: fs.ModeDir | fs.ModeSetuid | 0755}},
		"symlink":                       {{name: "linked.js", body: "../outside", mode: fs.ModeSymlink | 0777}},
		"device":                        {{name: "device.js", mode: fs.ModeDevice | 0600}},
		"executable":                    {{name: "main.js", body: "code", mode: 0755}},
		"server program":                {{name: "install.sh", body: "do not execute"}},
		"unknown media":                 {{name: "payload.unknown", body: "unknown"}},
	}
	for name, entries := range cases {
		t.Run(name, func(t *testing.T) {
			entries = append(entries, frontendTestEntry{name: "index.html", body: "<!doctype html>"})
			raw := frontendTestArchive(t, entries...)
			result, err := InspectFrontend(frontendManifest(t, raw), raw)
			if !errors.Is(err, ErrInvalidFrontend) || result.SHA256 != "" || len(result.Files) != 0 {
				t.Fatalf("unsafe archive accepted or partial result: %+v %v", result, err)
			}
		})
	}
}

func TestFrontendInspectionRequiresHtmlEntryAndValidDescriptor(t *testing.T) {
	for name, entries := range map[string][]frontendTestEntry{
		"empty archive": {},
		"missing entry": {{name: "other.html", body: "<!doctype html>"}},
		"empty entry":   {{name: "index.html"}},
	} {
		t.Run(name, func(t *testing.T) {
			raw := frontendTestArchive(t, entries...)
			if _, err := InspectFrontend(frontendManifest(t, raw), raw); !errors.Is(err, ErrInvalidFrontend) {
				t.Fatalf("accepted: %v", err)
			}
		})
	}
	raw := frontendTestArchive(t, frontendTestEntry{name: "index.js", body: "console.log('not an HTML entry')"})
	m := frontendManifest(t, raw)
	m.Presentation.Entry = "index.js"
	if _, err := InspectFrontend(m, raw); !errors.Is(err, ErrInvalidFrontend) {
		t.Fatalf("non-HTML entry accepted: %v", err)
	}
	m.Format = "unsupported"
	if _, err := InspectFrontend(m, raw); !errors.Is(err, ErrInvalidDescriptor) {
		t.Fatalf("invalid descriptor accepted: %v", err)
	}
	raw = []byte("not a ZIP")
	if _, err := InspectFrontend(frontendManifest(t, raw), raw); !errors.Is(err, ErrInvalidFrontend) {
		t.Fatalf("non-ZIP accepted: %v", err)
	}
}

func TestFrontendInspectionEnforcesIndependentLimits(t *testing.T) {
	raw := frontendTestArchive(t, frontendTestEntry{name: "index.html", body: strings.Repeat("a", 17), method: zip.Deflate}, frontendTestEntry{name: "main.js", body: strings.Repeat("b", 17), method: zip.Deflate})
	for name, limits := range map[string]frontendLimits{
		"archive": {archiveBytes: 1, fileBytes: 32, totalBytes: 64, entries: 3},
		"file":    {archiveBytes: 1024, fileBytes: 16, totalBytes: 64, entries: 3},
		"total":   {archiveBytes: 1024, fileBytes: 32, totalBytes: 33, entries: 3},
		"entries": {archiveBytes: 1024, fileBytes: 32, totalBytes: 64, entries: 1},
	} {
		t.Run(name, func(t *testing.T) {
			result, err := inspectFrontend(frontendManifest(t, raw), raw, limits)
			if !errors.Is(err, ErrInvalidFrontend) || len(result.Files) != 0 {
				t.Fatalf("limit bypassed: %+v %v", result, err)
			}
		})
	}
}

func TestFrontendInspectionReadsAndChecksCrc(t *testing.T) {
	raw := frontendTestArchive(t, frontendTestEntry{name: "index.html", body: "<!doctype html>UNIQUE_CONTENT", method: zip.Store})
	offset := bytes.Index(raw, []byte("UNIQUE_CONTENT"))
	if offset < 0 {
		t.Fatal("fixture missing body")
	}
	raw[offset] ^= 1
	m := frontendManifest(t, raw)
	result, err := InspectFrontend(m, raw)
	if !errors.Is(err, ErrInvalidFrontend) || len(result.Files) != 0 {
		t.Fatalf("CRC failure accepted: %+v %v", result, err)
	}
}

func TestFrontendInspectionRejectsForgedZipMetadata(t *testing.T) {
	base := frontendTestArchive(t, frontendTestEntry{name: "index.html", body: "<!doctype html>"})
	for name, change := range map[string]func([]byte){
		"encrypted":                func(h []byte) { binary.LittleEndian.PutUint16(h[8:10], 1) },
		"unsupported flags":        func(h []byte) { binary.LittleEndian.PutUint16(h[8:10], 0x40) },
		"unsupported compression":  func(h []byte) { binary.LittleEndian.PutUint16(h[10:12], 99) },
		"advertised oversize":      func(h []byte) { binary.LittleEndian.PutUint32(h[24:28], MaxFrontendFileBytes+1) },
		"advertised undersize":     func(h []byte) { binary.LittleEndian.PutUint32(h[24:28], 1) },
		"unreachable local record": func(h []byte) { binary.LittleEndian.PutUint32(h[42:46], 0xffffff00) },
	} {
		t.Run(name, func(t *testing.T) {
			raw := append([]byte(nil), base...)
			offset := bytes.Index(raw, []byte{'P', 'K', 1, 2})
			if offset < 0 {
				t.Fatal("missing central record")
			}
			change(raw[offset:])
			result, err := InspectFrontend(frontendManifest(t, raw), raw)
			if !errors.Is(err, ErrInvalidFrontend) || len(result.Files) != 0 {
				t.Fatalf("forged archive accepted: %+v %v", result, err)
			}
		})
	}
}

func TestFrontendInspectionChecksRawDirectoryPayload(t *testing.T) {
	for _, body := range []string{"", "hidden payload"} {
		raw := frontendTestArchive(t, frontendTestEntry{name: "assetsX", body: body, method: zip.Deflate, mode: 0755}, frontendTestEntry{name: "index.html", body: "<!doctype html>"})
		raw = bytes.ReplaceAll(raw, []byte("assetsX"), []byte("assets/"))
		offset := bytes.Index(raw, []byte{'P', 'K', 1, 2})
		if offset < 0 {
			t.Fatal("missing central record")
		}
		binary.LittleEndian.PutUint32(raw[offset+16:offset+20], 0)
		binary.LittleEndian.PutUint32(raw[offset+24:offset+28], 0)
		result, err := InspectFrontend(frontendManifest(t, raw), raw)
		if body == "" {
			if err != nil || len(result.Files) != 1 {
				t.Fatalf("valid compressed directory refused: %+v %v", result, err)
			}
		} else if !errors.Is(err, ErrInvalidFrontend) || len(result.Files) != 0 {
			t.Fatalf("hidden directory payload accepted: %+v %v", result, err)
		}
	}
}

func TestFrontendInspectionMediaPolicy(t *testing.T) {
	types := map[string]string{
		".html": "text/html; charset=utf-8", ".htm": "text/html; charset=utf-8",
		".js": "text/javascript; charset=utf-8", ".css": "text/css; charset=utf-8",
		".json": "application/json", ".map": "application/json",
		".txt": "text/plain; charset=utf-8", ".md": "text/plain; charset=utf-8", ".csv": "text/plain; charset=utf-8",
		".svg": "image/svg+xml", ".png": "image/png", ".jpg": "image/jpeg", ".jpeg": "image/jpeg",
		".webp": "image/webp", ".gif": "image/gif", ".ico": "image/x-icon",
		".woff": "font/woff", ".woff2": "font/woff2", ".ttf": "font/ttf", ".otf": "font/otf", ".wasm": "application/wasm",
	}
	entries := []frontendTestEntry{{name: "index.html", body: "<!doctype html>"}}
	for ext := range types {
		entries = append(entries, frontendTestEntry{name: "asset" + ext, body: "sample"})
	}
	raw := frontendTestArchive(t, entries...)
	result, err := InspectFrontend(frontendManifest(t, raw), raw)
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range result.Files {
		ext := file.Path[strings.LastIndex(file.Path, "."):]
		if file.ContentType != types[ext] {
			t.Fatalf("unexpected type: %+v", file)
		}
	}
}

func FuzzFrontendInspection(f *testing.F) {
	f.Add([]byte("not a zip"))
	f.Add(frontendTestArchive(f, frontendTestEntry{name: "index.html", body: "<!doctype html>"}))
	f.Fuzz(func(t *testing.T, raw []byte) {
		result, err := InspectFrontend(frontendManifest(t, raw), raw)
		if err != nil && (result.SHA256 != "" || len(result.Files) != 0) {
			t.Fatal("error returned partial metadata")
		}
		if err == nil && (len(result.Files) == 0 || result.Entry == "" || result.SHA256 == "") {
			t.Fatal("success missing required metadata")
		}
	})
}

func TestFrontendInspectionPreflightsCentralDirectory(t *testing.T) {
	base := frontendTestArchive(t, frontendTestEntry{name: "index.html", body: "<!doctype html>"})
	for name, change := range map[string]func([]byte) []byte{
		"trailing bytes": func(raw []byte) []byte { return append(raw, 'x') },
		"archive ZIP64": func(raw []byte) []byte {
			binary.LittleEndian.PutUint16(raw[len(raw)-12:len(raw)-10], 0xffff)
			return raw
		},
		"spanned archive": func(raw []byte) []byte { binary.LittleEndian.PutUint16(raw[len(raw)-18:len(raw)-16], 1); return raw },
		"count mismatch":  func(raw []byte) []byte { binary.LittleEndian.PutUint16(raw[len(raw)-12:len(raw)-10], 2); return raw },
		"central offset": func(raw []byte) []byte {
			binary.LittleEndian.PutUint32(raw[len(raw)-6:len(raw)-2], 0xffffff00)
			return raw
		},
		"central signature": func(raw []byte) []byte {
			offset := bytes.Index(raw, []byte{'P', 'K', 1, 2})
			raw[offset] ^= 1
			return raw
		},
		"entry allocation attack": func(raw []byte) []byte {
			binary.LittleEndian.PutUint16(raw[len(raw)-12:len(raw)-10], MaxFrontendEntries+1)
			return raw
		},
		"compressed byte budget": func(raw []byte) []byte {
			offset := bytes.Index(raw, []byte{'P', 'K', 1, 2})
			binary.LittleEndian.PutUint32(raw[offset+20:offset+24], uint32(len(raw)+1))
			return raw
		},
	} {
		t.Run(name, func(t *testing.T) {
			raw := change(append([]byte(nil), base...))
			result, err := InspectFrontend(frontendManifest(t, raw), raw)
			if !errors.Is(err, ErrInvalidFrontend) || len(result.Files) != 0 {
				t.Fatalf("malformed envelope accepted: %+v %v", result, err)
			}
		})
	}
	raw := append([]byte(nil), base...)
	binary.LittleEndian.PutUint16(raw[len(raw)-2:], 4)
	raw = append(raw, []byte("note")...)
	if !boundedFrontendDirectory(raw, MaxFrontendEntries) {
		t.Fatal("bounded ZIP comment refused")
	}
}

func TestFrontendInspectionPublicArchiveLimits(t *testing.T) {
	for _, raw := range [][]byte{nil, make([]byte, MaxFrontendArchiveBytes+1)} {
		result, err := InspectFrontend(frontendManifest(t, raw), raw)
		if !errors.Is(err, ErrInvalidFrontend) || len(result.Files) != 0 {
			t.Fatalf("public bound bypassed: %+v %v", result, err)
		}
	}
}
