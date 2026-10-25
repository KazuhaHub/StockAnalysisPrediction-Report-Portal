package appplatform

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func descriptorFixture(t *testing.T, managed bool) []byte {
	t.Helper()
	name := "static.json"
	if managed {
		name = "managed.json"
	}
	raw, err := os.ReadFile(filepath.Join("testdata", "descriptors", name))
	if err != nil {
		t.Fatal(err)
	}
	// Normalize only fixture whitespace so mutation cases have stable targets.
	var compact bytes.Buffer
	if err := json.Compact(&compact, raw); err != nil {
		t.Fatal(err)
	}
	return compact.Bytes()
}

func mutateDescriptor(t *testing.T, fn func(map[string]any)) []byte {
	t.Helper()
	var d map[string]any
	if err := json.Unmarshal(descriptorFixture(t, true), &d); err != nil {
		t.Fatal(err)
	}
	fn(d)
	raw, err := json.Marshal(d)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestDescriptorAcceptsStaticAndManagedAndBindsExactBytes(t *testing.T) {
	for _, managed := range []bool{false, true} {
		raw := descriptorFixture(t, managed)
		d, err := ParseDescriptor(raw)
		if err != nil {
			t.Fatal(err)
		}
		if d.Manifest.ID != "notes-sample" || (d.Manifest.Backend != nil) != managed {
			t.Fatalf("unexpected manifest: %+v", d.Manifest)
		}
		sum := sha256.Sum256(raw)
		if d.SHA256 != hex.EncodeToString(sum[:]) {
			t.Fatal("descriptor digest is not bound to exact input bytes")
		}
		other, err := ParseDescriptor(append([]byte("\n"), raw...))
		if err != nil || other.SHA256 == d.SHA256 {
			t.Fatal("distinct descriptor bytes reused the same review identity")
		}
	}
}

func TestDescriptorRejectsAmbiguousAndUnboundedJSON(t *testing.T) {
	raw := descriptorFixture(t, true)
	cases := map[string][]byte{
		"empty":             nil,
		"array":             []byte("[]"),
		"duplicate":         bytes.Replace(raw, []byte(`"id":"notes-sample"`), []byte(`"id":"notes-sample","id":"other-app"`), 1),
		"escaped duplicate": bytes.Replace(raw, []byte(`"id":"notes-sample"`), []byte(`"id":"notes-sample","\u0069d":"other-app"`), 1),
		"nested duplicate":  bytes.Replace(raw, []byte(`"kind":"oci"`), []byte(`"kind":"oci","kind":"other"`), 1),
		"case alias":        bytes.Replace(raw, []byte(`"format":`), []byte(`"Format":`), 1),
		"unknown nested":    bytes.Replace(raw, []byte(`"kind":"oci"`), []byte(`"kind":"oci","command":"shell"`), 1),
		"trailing object":   append(append([]byte{}, raw...), []byte(" {}")...),
		"too large":         bytes.Repeat([]byte(" "), MaxDescriptorBytes+1),
		"too deep":          []byte(strings.Repeat(`{"x":`, 40) + "0" + strings.Repeat("}", 40)),
		"invalid utf8":      append([]byte{0xff}, raw...),
	}
	for name, input := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := ParseDescriptor(input); !errors.Is(err, ErrInvalidDescriptor) {
				t.Fatalf("expected bounded invalid-descriptor error, got %v", err)
			}
		})
	}
}

func TestDescriptorRejectsUnsafeDeclarations(t *testing.T) {
	cases := map[string]func(map[string]any){
		"legacy format":         func(d map[string]any) { d["format"] = "portal.app/v1" },
		"invalid identity":      func(d map[string]any) { d["id"] = "../another" },
		"nonversion":            func(d map[string]any) { d["version"] = "latest" },
		"leading zero":          func(d map[string]any) { d["version"] = "01.0.0" },
		"missing compatibility": func(d map[string]any) { delete(d, "compatibility") },
		"bad hash":              func(d map[string]any) { d["frontend"].(map[string]any)["sha256"] = "not-a-digest" },
		"path escape":           func(d map[string]any) { d["presentation"].(map[string]any)["entry"] = "../index.html" },
		"encoded path":          func(d map[string]any) { d["frontend"].(map[string]any)["artifact"] = "%2e%2e/ui.zip" },
		"missing locale":        func(d map[string]any) { d["presentation"].(map[string]any)["locales"] = []string{"en-US"} },
		"route escape": func(d map[string]any) {
			d["presentation"].(map[string]any)["routes"] = []any{map[string]any{"id": "home", "path": "//outside", "layout": "document"}}
		},
		"duplicate route": func(d map[string]any) {
			p := d["presentation"].(map[string]any)
			p["routes"] = []any{map[string]any{"id": "home", "path": "/", "layout": "document"}, map[string]any{"id": "home", "path": "/edit", "layout": "viewport"}}
		},
		"unknown layout": func(d map[string]any) {
			d["presentation"].(map[string]any)["routes"] = []any{map[string]any{"id": "home", "path": "/", "layout": "host-dom"}}
		},
		"mutable image":              func(d map[string]any) { d["backend"].(map[string]any)["image"] = "ghcr.io/example/digest:beta" },
		"unknown backend":            func(d map[string]any) { d["backend"].(map[string]any)["kind"] = "native-shell" },
		"unsupported platform":       func(d map[string]any) { d["backend"].(map[string]any)["platforms"] = []string{"linux/unknown"} },
		"unbounded memory":           func(d map[string]any) { d["backend"].(map[string]any)["resources"].(map[string]any)["memory_mib"] = 0 },
		"unknown capability":         func(d map[string]any) { d["capabilities"].(map[string]any)["required"] = []string{"host.exec"} },
		"duplicate capability":       func(d map[string]any) { d["capabilities"].(map[string]any)["optional"] = []string{"storage.files"} },
		"background without backend": func(d map[string]any) { delete(d, "backend") },
		"required job optional background": func(d map[string]any) {
			d["capabilities"] = map[string]any{"required": []string{"tasks.schedule"}, "optional": []string{"background.run"}}
		},
		"startup migration":     func(d map[string]any) { d["data"].(map[string]any)["writes_on_startup"] = true },
		"unreadable own schema": func(d map[string]any) { d["data"].(map[string]any)["readable_schemas"] = []int{2} },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := ParseDescriptor(mutateDescriptor(t, mutate))
			if !errors.Is(err, ErrInvalidDescriptor) {
				t.Fatalf("expected refusal, got %v", err)
			}
		})
	}
}

func FuzzDescriptorParser(f *testing.F) {
	for _, name := range []string{"static.json", "managed.json"} {
		raw, err := os.ReadFile(filepath.Join("testdata", "descriptors", name))
		if err != nil {
			f.Fatal(err)
		}
		f.Add(raw)
	}
	f.Add([]byte(`{"format":"portal.app/v2"}`))
	f.Add([]byte(`{"id":"first","\u0069d":"second"}`))
	f.Add([]byte(`{"backend":{"resources":[[[null]]]}}`))
	f.Fuzz(func(t *testing.T, raw []byte) {
		parsed, err := ParseDescriptor(raw)
		if err != nil {
			if !errors.Is(err, ErrInvalidDescriptor) || parsed.Manifest.ID != "" || parsed.SHA256 != "" {
				t.Fatal("refusal exposed a partial descriptor or unstable error")
			}
			return
		}
		sum := sha256.Sum256(raw)
		if parsed.SHA256 != hex.EncodeToString(sum[:]) {
			t.Fatal("accepted descriptor identity diverged")
		}
	})
}
