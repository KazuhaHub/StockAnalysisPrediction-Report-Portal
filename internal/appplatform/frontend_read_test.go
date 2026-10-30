package appplatform

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"reflect"
	"testing"
)

func TestReadFrontendFileExactVerifiedBytes(t *testing.T) {
	raw := frontendTestArchive(t, frontendTestEntry{name: "index.html", body: "<!doctype html>"}, frontendTestEntry{name: "assets/main.js", body: "console.log('fixture')", method: zip.Deflate}, frontendTestEntry{name: "empty.css", body: ""})
	m := frontendManifest(t, raw)
	before := bytes.Clone(raw)
	for _, item := range []struct{ name, body, contentType string }{{"index.html", "<!doctype html>", "text/html; charset=utf-8"}, {"assets/main.js", "console.log('fixture')", "text/javascript; charset=utf-8"}, {"empty.css", "", "text/css; charset=utf-8"}} {
		result, err := ReadFrontendFile(m, raw, item.name)
		sum := sha256.Sum256([]byte(item.body))
		if err != nil || string(result.Content) != item.body || result.Metadata.Path != item.name || result.Metadata.ContentType != item.contentType || result.Metadata.Bytes != int64(len(item.body)) || result.Metadata.SHA256 != hex.EncodeToString(sum[:]) {
			t.Fatalf("incorrect file: %+v %v", result, err)
		}
		if len(result.Content) > 0 {
			result.Content[0] ^= 1
		}
	}
	if !bytes.Equal(raw, before) {
		t.Fatal("returned content aliases archive input")
	}
}
func TestReadFrontendFileRefusesUnsafeOrUnverifiedInput(t *testing.T) {
	raw := frontendTestArchive(t, frontendTestEntry{name: "index.html", body: "<!doctype html>"})
	m := frontendManifest(t, raw)
	for _, name := range []string{"", ".", "..", "/index.html", "./index.html", "x/../index.html", "index.html/", "INDEX.HTML", "missing.js", "index.html?x=1", "index.html#x", "%69ndex.html", "x//index.html", "x\\index.html", "index.html\x00"} {
		if result, err := ReadFrontendFile(m, raw, name); err == nil || !reflect.DeepEqual(result, FrontendFile{}) {
			t.Fatalf("unsafe/missing name %q returned content", name)
		}
	}
	raw[len(raw)-1] ^= 1
	if result, err := ReadFrontendFile(m, raw, "index.html"); err == nil || !reflect.DeepEqual(result, FrontendFile{}) {
		t.Fatal("changed archive returned executable bytes")
	}
}

func TestReadFrontendFileRejectsInvalidSibling(t *testing.T) {
	raw := frontendTestArchive(t, frontendTestEntry{name: "index.html", body: "<!doctype html>"}, frontendTestEntry{name: "unsafe.bin", body: "unapproved content type"})
	m := frontendManifest(t, raw)
	if result, err := ReadFrontendFile(m, raw, "index.html"); err == nil || !reflect.DeepEqual(result, FrontendFile{}) {
		t.Fatal("selecting valid entry bypassed full archive inspection")
	}
}
